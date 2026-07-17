package citation

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/ranking"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type AnalysisSignal string

const (
	SignalCitationVelocity   AnalysisSignal = "citation_velocity"
	SignalCitationPercentile AnalysisSignal = "citation_percentile"
)

type CitationVelocityInput struct {
	Source    string
	AsOf      time.Time
	Window    time.Duration
	Snapshots []Snapshot
}

type CitationVelocityResult struct {
	Source   string
	AsOf     time.Time
	Window   time.Duration
	Start    Snapshot
	End      Snapshot
	Elapsed  time.Duration
	Velocity decimal.Decimal
}

type CitationCohortIdentity struct {
	SubjectVersionID  uuid.UUID
	SubjectID         uuid.UUID
	PublicationYear   int
	PublicationTypeID uuid.UUID
}

type CitationPercentileObservation struct {
	WorkID uuid.UUID
	Cohort CitationCohortIdentity
	Count  int64
}

type CitationPercentileInput struct {
	Cohort            CitationCohortIdentity
	Target            CitationPercentileObservation
	Observations      []CitationPercentileObservation
	MinimumCohortSize int
}

type CitationPercentileResult struct {
	Cohort            CitationCohortIdentity
	CohortSize        int
	Rank              decimal.Decimal
	Percentile        decimal.Decimal
	SupportingWorkIDs []uuid.UUID
}

type InvalidInputError struct {
	Field  string
	Reason string
}

func (err *InvalidInputError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("invalid citation analysis input %q: %s", err.Field, err.Reason)
}

type MixedSourceError struct {
	Expected string
	Actual   string
	Index    int
}

func (err *MixedSourceError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"citation snapshot %d source %q does not match requested source %q",
		err.Index,
		err.Actual,
		err.Expected,
	)
}

type TimeOrderError struct {
	Index    int
	Previous time.Time
	Current  time.Time
}

func (err *TimeOrderError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"citation snapshot %d observed at %s before previous snapshot at %s",
		err.Index,
		err.Current.Format(time.RFC3339Nano),
		err.Previous.Format(time.RFC3339Nano),
	)
}

type NegativeCountError struct {
	WorkID uuid.UUID
	Count  int64
	Index  int
}

func (err *NegativeCountError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"citation observation %d for Work %s has negative count %d",
		err.Index,
		err.WorkID,
		err.Count,
	)
}

type MixedCohortError struct {
	Expected CitationCohortIdentity
	Actual   CitationCohortIdentity
	WorkID   uuid.UUID
	Index    int
}

func (err *MixedCohortError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"citation percentile Work %s at index %d belongs to a different cohort",
		err.WorkID,
		err.Index,
	)
}

type DuplicateWorkError struct {
	WorkID         uuid.UUID
	FirstIndex     int
	DuplicateIndex int
}

func (err *DuplicateWorkError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf(
		"citation percentile Work %s is duplicated at indexes %d and %d",
		err.WorkID,
		err.FirstIndex,
		err.DuplicateIndex,
	)
}

type InsufficientEvidenceError struct {
	Signal            AnalysisSignal
	Missing           []string
	CohortSize        int
	MinimumCohortSize int
}

func (err *InsufficientEvidenceError) Error() string {
	if err == nil {
		return "<nil>"
	}
	if len(err.Missing) != 0 {
		return fmt.Sprintf(
			"insufficient evidence for %s: missing %s",
			err.Signal,
			strings.Join(err.Missing, ", "),
		)
	}
	return fmt.Sprintf(
		"insufficient evidence for %s: cohort size %d is below minimum %d",
		err.Signal,
		err.CohortSize,
		err.MinimumCohortSize,
	)
}

func AnalyzeCitationVelocity(
	input CitationVelocityInput,
) (CitationVelocityResult, error) {
	if input.Source == "" || input.Source != strings.TrimSpace(input.Source) {
		return CitationVelocityResult{}, &InvalidInputError{
			Field:  "source",
			Reason: "must be non-empty and trimmed",
		}
	}
	if input.AsOf.IsZero() {
		return CitationVelocityResult{}, &InvalidInputError{
			Field:  "as_of",
			Reason: "must be set",
		}
	}
	if input.Window <= 0 {
		return CitationVelocityResult{}, &InvalidInputError{
			Field:  "window",
			Reason: "must be positive",
		}
	}

	startBoundary := input.AsOf.Add(-input.Window)
	var start, end *Snapshot
	previousObservedAt := time.Time{}
	for index := range input.Snapshots {
		snapshot := input.Snapshots[index]
		if snapshot.Source != input.Source {
			return CitationVelocityResult{}, &MixedSourceError{
				Expected: input.Source,
				Actual:   snapshot.Source,
				Index:    index,
			}
		}
		if snapshot.ObservedAt.IsZero() {
			return CitationVelocityResult{}, &InvalidInputError{
				Field:  fmt.Sprintf("snapshots[%d].observed_at", index),
				Reason: "must be set",
			}
		}
		if snapshot.Count < 0 {
			return CitationVelocityResult{}, &NegativeCountError{
				WorkID: snapshot.WorkID,
				Count:  snapshot.Count,
				Index:  index,
			}
		}
		if index > 0 && snapshot.ObservedAt.Before(previousObservedAt) {
			return CitationVelocityResult{}, &TimeOrderError{
				Index:    index,
				Previous: previousObservedAt,
				Current:  snapshot.ObservedAt,
			}
		}
		previousObservedAt = snapshot.ObservedAt

		if !snapshot.ObservedAt.After(startBoundary) {
			candidate := snapshot
			start = &candidate
		}
		if !snapshot.ObservedAt.After(input.AsOf) {
			candidate := snapshot
			end = &candidate
		}
	}

	missing := make([]string, 0, 2)
	if start == nil {
		missing = append(missing, "start_snapshot")
	}
	if end == nil {
		missing = append(missing, "end_snapshot")
	}
	if len(missing) != 0 {
		return CitationVelocityResult{}, &InsufficientEvidenceError{
			Signal:  SignalCitationVelocity,
			Missing: missing,
		}
	}
	if !end.ObservedAt.After(start.ObservedAt) {
		return CitationVelocityResult{}, &InsufficientEvidenceError{
			Signal:  SignalCitationVelocity,
			Missing: []string{"distinct_end_snapshot"},
		}
	}

	velocity, err := ranking.SourceSpecificCitationVelocity(
		ranking.SourceSpecificCitationVelocityInput{
			Source: input.Source,
			Start: &ranking.SourceMetricSnapshot{
				Source:     start.Source,
				ObservedAt: start.ObservedAt,
				Value:      decimal.NewFromInt(start.Count),
			},
			End: &ranking.SourceMetricSnapshot{
				Source:     end.Source,
				ObservedAt: end.ObservedAt,
				Value:      decimal.NewFromInt(end.Count),
			},
		},
	)
	if err != nil {
		return CitationVelocityResult{}, err
	}

	return CitationVelocityResult{
		Source:   input.Source,
		AsOf:     input.AsOf.UTC(),
		Window:   input.Window,
		Start:    *start,
		End:      *end,
		Elapsed:  end.ObservedAt.Sub(start.ObservedAt),
		Velocity: velocity,
	}, nil
}

func AnalyzeCitationPercentile(
	input CitationPercentileInput,
) (CitationPercentileResult, error) {
	if err := validateCitationCohortIdentity(input.Cohort); err != nil {
		return CitationPercentileResult{}, err
	}
	if input.MinimumCohortSize <= 0 {
		return CitationPercentileResult{}, &InvalidInputError{
			Field:  "minimum_cohort_size",
			Reason: "must be positive",
		}
	}
	if input.Target.WorkID == uuid.Nil {
		return CitationPercentileResult{}, &InvalidInputError{
			Field:  "target.work_id",
			Reason: "must be set",
		}
	}
	if input.Target.Cohort != input.Cohort {
		return CitationPercentileResult{}, &MixedCohortError{
			Expected: input.Cohort,
			Actual:   input.Target.Cohort,
			WorkID:   input.Target.WorkID,
			Index:    -1,
		}
	}
	if input.Target.Count < 0 {
		return CitationPercentileResult{}, &NegativeCountError{
			WorkID: input.Target.WorkID,
			Count:  input.Target.Count,
			Index:  -1,
		}
	}

	seen := map[uuid.UUID]int{input.Target.WorkID: -1}
	supportingWorkIDs := make([]uuid.UUID, 0, len(input.Observations)+1)
	supportingWorkIDs = append(supportingWorkIDs, input.Target.WorkID)
	less := int64(0)
	equal := int64(1)
	for index, observation := range input.Observations {
		if observation.WorkID == uuid.Nil {
			return CitationPercentileResult{}, &InvalidInputError{
				Field:  fmt.Sprintf("observations[%d].work_id", index),
				Reason: "must be set",
			}
		}
		if observation.Cohort != input.Cohort {
			return CitationPercentileResult{}, &MixedCohortError{
				Expected: input.Cohort,
				Actual:   observation.Cohort,
				WorkID:   observation.WorkID,
				Index:    index,
			}
		}
		if observation.Count < 0 {
			return CitationPercentileResult{}, &NegativeCountError{
				WorkID: observation.WorkID,
				Count:  observation.Count,
				Index:  index,
			}
		}
		if firstIndex, exists := seen[observation.WorkID]; exists {
			return CitationPercentileResult{}, &DuplicateWorkError{
				WorkID:         observation.WorkID,
				FirstIndex:     firstIndex,
				DuplicateIndex: index,
			}
		}
		seen[observation.WorkID] = index
		supportingWorkIDs = append(supportingWorkIDs, observation.WorkID)

		switch {
		case observation.Count < input.Target.Count:
			less++
		case observation.Count == input.Target.Count:
			equal++
		}
	}

	cohortSize := len(supportingWorkIDs)
	if cohortSize < input.MinimumCohortSize {
		return CitationPercentileResult{}, &InsufficientEvidenceError{
			Signal:            SignalCitationPercentile,
			CohortSize:        cohortSize,
			MinimumCohortSize: input.MinimumCohortSize,
		}
	}

	rank := decimal.NewFromInt(less).
		Add(decimal.NewFromInt(equal + 1).Div(decimal.NewFromInt(2)))
	percentile := rank.
		Mul(decimal.NewFromInt(100)).
		DivRound(decimal.NewFromInt(int64(cohortSize)), ranking.DivisionScale)
	sort.Slice(supportingWorkIDs, func(left, right int) bool {
		return bytes.Compare(
			supportingWorkIDs[left][:],
			supportingWorkIDs[right][:],
		) < 0
	})

	return CitationPercentileResult{
		Cohort:            input.Cohort,
		CohortSize:        cohortSize,
		Rank:              rank,
		Percentile:        percentile,
		SupportingWorkIDs: supportingWorkIDs,
	}, nil
}

func validateCitationCohortIdentity(cohort CitationCohortIdentity) error {
	if cohort.SubjectVersionID == uuid.Nil {
		return &InvalidInputError{
			Field:  "cohort.subject_version_id",
			Reason: "must be set",
		}
	}
	if cohort.SubjectID == uuid.Nil {
		return &InvalidInputError{
			Field:  "cohort.subject_id",
			Reason: "must be set",
		}
	}
	if cohort.PublicationYear <= 0 {
		return &InvalidInputError{
			Field:  "cohort.publication_year",
			Reason: "must be positive",
		}
	}
	if cohort.PublicationTypeID == uuid.Nil {
		return &InvalidInputError{
			Field:  "cohort.publication_type_id",
			Reason: "must be set",
		}
	}
	return nil
}
