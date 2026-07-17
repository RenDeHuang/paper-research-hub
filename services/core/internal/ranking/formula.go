package ranking

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const (
	FormulaVersion       = "ranking-formulas/v1"
	DivisionScale  int32 = 18
)

type Signal string

const (
	SignalCitationVelocity   Signal = "citation_velocity"
	SignalCodeGrowth         Signal = "code_growth"
	SignalTopicGrowth        Signal = "topic_growth"
	SignalMethodAdoption     Signal = "method_adoption"
	SignalCitationPercentile Signal = "citation_percentile"
)

var (
	ErrTimeReversed        = errors.New("ranking time boundary is reversed")
	ErrWindowMismatch      = errors.New("ranking windows do not match")
	ErrInvalidMetric       = errors.New("ranking metric is invalid")
	ErrInvalidCohort       = errors.New("citation cohort key is invalid")
	ErrMixedMetricSource   = errors.New("citation metric snapshots mix sources")
	ErrInvalidMetricSource = errors.New("citation metric source is invalid")
)

var taxonomySlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type MetricSnapshot struct {
	ObservedAt time.Time
	Value      decimal.Decimal
}

type CitationVelocityInput struct {
	Start *MetricSnapshot
	End   *MetricSnapshot
}

type SourceMetricSnapshot struct {
	Source     string
	ObservedAt time.Time
	Value      decimal.Decimal
}

type SourceSpecificCitationVelocityInput struct {
	Source string
	Start  *SourceMetricSnapshot
	End    *SourceMetricSnapshot
}

type CodeGrowthInput struct {
	Start *MetricSnapshot
	End   *MetricSnapshot
}

type Window struct {
	Start time.Time
	End   time.Time
}

type WindowMetric struct {
	Window Window
	Value  decimal.Decimal
}

type TopicGrowthInput struct {
	Baseline *WindowMetric
	Current  *WindowMetric
}

type AdoptionWindow struct {
	Window  Window
	Adopted decimal.Decimal
	Total   decimal.Decimal
}

type MethodAdoptionInput struct {
	Baseline *AdoptionWindow
	Current  *AdoptionWindow
}

type CitationCohortKey struct {
	Topic     string
	Month     time.Time
	PaperType paper.PaperType
}

type CohortObservation struct {
	PaperID uuid.UUID
	Value   decimal.Decimal
}

type CitationPercentileInput struct {
	Cohort       CitationCohortKey
	Target       CohortObservation
	Observations []CohortObservation
}

type RankabilityInput struct {
	Status   paper.WorkStatus
	Excluded bool
}

type MissingSignalError struct {
	Signal Signal
	Fields []string
}

func (err *MissingSignalError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("missing %s signal fields: %s", err.Signal, strings.Join(err.Fields, ", "))
}

type ZeroDenominatorError struct {
	Signal Signal
	Field  string
}

func (err *ZeroDenominatorError) Error() string {
	if err == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s denominator %q is zero", err.Signal, err.Field)
}

type UnrankableWorkError struct {
	Status   paper.WorkStatus
	Excluded bool
}

func (err *UnrankableWorkError) Error() string {
	if err == nil {
		return "<nil>"
	}
	if err.Excluded {
		return fmt.Sprintf("work with status %q is currently excluded from rankings", err.Status)
	}
	return fmt.Sprintf("work status %q is not rankable", err.Status)
}

func CitationVelocity(input CitationVelocityInput) (decimal.Decimal, error) {
	missing := make([]string, 0, 2)
	if input.Start == nil {
		missing = append(missing, "start_snapshot")
	}
	if input.End == nil {
		missing = append(missing, "end_snapshot")
	}
	if len(missing) != 0 {
		return decimal.Zero, &MissingSignalError{
			Signal: SignalCitationVelocity,
			Fields: missing,
		}
	}
	if input.Start.ObservedAt.IsZero() {
		missing = append(missing, "start_observed_at")
	}
	if input.End.ObservedAt.IsZero() {
		missing = append(missing, "end_observed_at")
	}
	if len(missing) != 0 {
		return decimal.Zero, &MissingSignalError{
			Signal: SignalCitationVelocity,
			Fields: missing,
		}
	}

	if input.End.ObservedAt.Before(input.Start.ObservedAt) {
		return decimal.Zero, ErrTimeReversed
	}

	elapsed := input.End.ObservedAt.Sub(input.Start.ObservedAt)
	if elapsed == 0 {
		return decimal.Zero, &ZeroDenominatorError{
			Signal: SignalCitationVelocity,
			Field:  "elapsed_time",
		}
	}
	if err := validateMetricSnapshot("start", *input.Start); err != nil {
		return decimal.Zero, err
	}
	if err := validateMetricSnapshot("end", *input.End); err != nil {
		return decimal.Zero, err
	}

	change := input.End.Value.Sub(input.Start.Value)
	nanosecondsPerDay := decimal.NewFromInt(int64((24 * time.Hour).Nanoseconds()))
	elapsedNanoseconds := decimal.NewFromInt(elapsed.Nanoseconds())
	return change.Mul(nanosecondsPerDay).DivRound(elapsedNanoseconds, DivisionScale), nil
}

func SourceSpecificCitationVelocity(
	input SourceSpecificCitationVelocityInput,
) (decimal.Decimal, error) {
	if input.Source == "" || input.Source != strings.TrimSpace(input.Source) {
		return decimal.Zero, ErrInvalidMetricSource
	}
	if input.Start != nil && input.Start.Source != input.Source {
		return decimal.Zero, ErrMixedMetricSource
	}
	if input.End != nil && input.End.Source != input.Source {
		return decimal.Zero, ErrMixedMetricSource
	}

	var start, end *MetricSnapshot
	if input.Start != nil {
		start = &MetricSnapshot{
			ObservedAt: input.Start.ObservedAt,
			Value:      input.Start.Value,
		}
	}
	if input.End != nil {
		end = &MetricSnapshot{
			ObservedAt: input.End.ObservedAt,
			Value:      input.End.Value,
		}
	}
	return CitationVelocity(CitationVelocityInput{
		Start: start,
		End:   end,
	})
}

func CodeGrowth(input CodeGrowthInput) (decimal.Decimal, error) {
	missing := make([]string, 0, 2)
	if input.Start == nil {
		missing = append(missing, "start_snapshot")
	}
	if input.End == nil {
		missing = append(missing, "end_snapshot")
	}
	if len(missing) != 0 {
		return decimal.Zero, &MissingSignalError{
			Signal: SignalCodeGrowth,
			Fields: missing,
		}
	}
	if input.Start.ObservedAt.IsZero() {
		missing = append(missing, "start_observed_at")
	}
	if input.End.ObservedAt.IsZero() {
		missing = append(missing, "end_observed_at")
	}
	if len(missing) != 0 {
		return decimal.Zero, &MissingSignalError{
			Signal: SignalCodeGrowth,
			Fields: missing,
		}
	}

	if input.End.ObservedAt.Before(input.Start.ObservedAt) {
		return decimal.Zero, ErrTimeReversed
	}
	if input.End.ObservedAt.Equal(input.Start.ObservedAt) {
		return decimal.Zero, ErrWindowMismatch
	}
	if err := validateMetricSnapshot("start", *input.Start); err != nil {
		return decimal.Zero, err
	}
	if err := validateMetricSnapshot("end", *input.End); err != nil {
		return decimal.Zero, err
	}
	if input.Start.Value.IsZero() {
		return decimal.Zero, &ZeroDenominatorError{
			Signal: SignalCodeGrowth,
			Field:  "start_value",
		}
	}

	change := input.End.Value.Sub(input.Start.Value)
	return change.DivRound(input.Start.Value, DivisionScale), nil
}

func TopicGrowth(input TopicGrowthInput) (decimal.Decimal, error) {
	missing := make([]string, 0, 2)
	if input.Baseline == nil {
		missing = append(missing, "baseline_window")
	}
	if input.Current == nil {
		missing = append(missing, "current_window")
	}
	if len(missing) != 0 {
		return decimal.Zero, &MissingSignalError{
			Signal: SignalTopicGrowth,
			Fields: missing,
		}
	}
	missing = append(missing, missingWindowBoundaries("baseline_window", input.Baseline.Window)...)
	missing = append(missing, missingWindowBoundaries("current_window", input.Current.Window)...)
	if len(missing) != 0 {
		return decimal.Zero, &MissingSignalError{
			Signal: SignalTopicGrowth,
			Fields: missing,
		}
	}

	if err := validateComparableWindows(
		SignalTopicGrowth,
		input.Baseline.Window,
		input.Current.Window,
	); err != nil {
		return decimal.Zero, err
	}
	if input.Baseline.Value.IsNegative() || input.Current.Value.IsNegative() {
		return decimal.Zero, fmt.Errorf("%w: topic window values must be nonnegative", ErrInvalidMetric)
	}
	if input.Baseline.Value.IsZero() {
		return decimal.Zero, &ZeroDenominatorError{
			Signal: SignalTopicGrowth,
			Field:  "baseline_value",
		}
	}

	change := input.Current.Value.Sub(input.Baseline.Value)
	return change.DivRound(input.Baseline.Value, DivisionScale), nil
}

func MethodAdoption(input MethodAdoptionInput) (decimal.Decimal, error) {
	missing := make([]string, 0, 2)
	if input.Baseline == nil {
		missing = append(missing, "baseline_window")
	}
	if input.Current == nil {
		missing = append(missing, "current_window")
	}
	if len(missing) != 0 {
		return decimal.Zero, &MissingSignalError{
			Signal: SignalMethodAdoption,
			Fields: missing,
		}
	}
	missing = append(missing, missingWindowBoundaries("baseline_window", input.Baseline.Window)...)
	missing = append(missing, missingWindowBoundaries("current_window", input.Current.Window)...)
	if len(missing) != 0 {
		return decimal.Zero, &MissingSignalError{
			Signal: SignalMethodAdoption,
			Fields: missing,
		}
	}

	if err := validateComparableWindows(
		SignalMethodAdoption,
		input.Baseline.Window,
		input.Current.Window,
	); err != nil {
		return decimal.Zero, err
	}
	if input.Baseline.Total.IsZero() {
		return decimal.Zero, &ZeroDenominatorError{
			Signal: SignalMethodAdoption,
			Field:  "baseline_total",
		}
	}
	if input.Current.Total.IsZero() {
		return decimal.Zero, &ZeroDenominatorError{
			Signal: SignalMethodAdoption,
			Field:  "current_total",
		}
	}
	if err := validateAdoptionWindow("baseline", *input.Baseline); err != nil {
		return decimal.Zero, err
	}
	if err := validateAdoptionWindow("current", *input.Current); err != nil {
		return decimal.Zero, err
	}

	numerator := input.Current.Adopted.Mul(input.Baseline.Total).
		Sub(input.Baseline.Adopted.Mul(input.Current.Total))
	denominator := input.Current.Total.Mul(input.Baseline.Total)
	return numerator.DivRound(denominator, DivisionScale), nil
}

func NewCitationCohortKey(
	topic string,
	month time.Time,
	paperType paper.PaperType,
) (CitationCohortKey, error) {
	key := CitationCohortKey{
		Topic:     topic,
		Month:     time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC),
		PaperType: paperType,
	}
	if month.IsZero() || !key.Valid() {
		return CitationCohortKey{}, ErrInvalidCohort
	}
	return key, nil
}

func (key CitationCohortKey) Valid() bool {
	if !validCanonicalTopic(key.Topic) || !key.PaperType.Valid() {
		return false
	}
	wantMonth := time.Date(key.Month.Year(), key.Month.Month(), 1, 0, 0, 0, 0, time.UTC)
	return key.Month.Location() == time.UTC && key.Month.Equal(wantMonth)
}

func (key CitationCohortKey) String() string {
	if !key.Valid() {
		return ""
	}
	return encodeCohortComponents(
		key.Topic,
		key.Month.Format("2006-01"),
		key.PaperType.String(),
	)
}

func CitationPercentile(input CitationPercentileInput) (decimal.Decimal, error) {
	if !input.Cohort.Valid() {
		return decimal.Zero, ErrInvalidCohort
	}
	if input.Target.PaperID == uuid.Nil {
		return decimal.Zero, fmt.Errorf("%w: target paper ID is nil", ErrInvalidCohort)
	}
	if input.Target.Value.IsNegative() {
		return decimal.Zero, fmt.Errorf("%w: target citation value must be nonnegative", ErrInvalidMetric)
	}
	if len(input.Observations) == 0 {
		return decimal.Zero, &MissingSignalError{
			Signal: SignalCitationPercentile,
			Fields: []string{"cohort_observations"},
		}
	}

	seenPaperIDs := map[uuid.UUID]struct{}{
		input.Target.PaperID: {},
	}
	atOrBelow := int64(0)
	for _, observation := range input.Observations {
		if observation.PaperID == uuid.Nil {
			return decimal.Zero, fmt.Errorf("%w: cohort paper ID is nil", ErrInvalidCohort)
		}
		if _, exists := seenPaperIDs[observation.PaperID]; exists {
			return decimal.Zero, fmt.Errorf(
				"%w: duplicate paper ID %s",
				ErrInvalidCohort,
				observation.PaperID,
			)
		}
		seenPaperIDs[observation.PaperID] = struct{}{}
		if observation.Value.IsNegative() {
			return decimal.Zero, fmt.Errorf(
				"%w: cohort citation value for %s must be nonnegative",
				ErrInvalidMetric,
				observation.PaperID,
			)
		}
		if observation.Value.LessThanOrEqual(input.Target.Value) {
			atOrBelow++
		}
	}
	numerator := decimal.NewFromInt(atOrBelow).Mul(decimal.NewFromInt(100))
	denominator := decimal.NewFromInt(int64(len(input.Observations)))
	return numerator.DivRound(denominator, DivisionScale), nil
}

func ValidateRankable(input RankabilityInput) error {
	if input.Excluded || !input.Status.Rankable() {
		return &UnrankableWorkError{
			Status:   input.Status,
			Excluded: input.Excluded,
		}
	}
	return nil
}

func validateComparableWindows(signal Signal, baseline Window, current Window) error {
	if baseline.End.Before(baseline.Start) || current.End.Before(current.Start) {
		return ErrTimeReversed
	}
	if baseline.End.Equal(baseline.Start) || current.End.Equal(current.Start) {
		return &ZeroDenominatorError{
			Signal: signal,
			Field:  "window_duration",
		}
	}
	if current.Start.Before(baseline.Start) {
		return ErrTimeReversed
	}
	if current.Start.Before(baseline.End) {
		return ErrWindowMismatch
	}
	if baseline.End.Sub(baseline.Start) != current.End.Sub(current.Start) {
		return ErrWindowMismatch
	}
	return nil
}

func validateAdoptionWindow(label string, window AdoptionWindow) error {
	if window.Adopted.IsNegative() || window.Total.IsNegative() {
		return fmt.Errorf("%w: %s adoption counts must be nonnegative", ErrInvalidMetric, label)
	}
	if window.Adopted.GreaterThan(window.Total) {
		return fmt.Errorf("%w: %s adopted count exceeds total", ErrInvalidMetric, label)
	}
	return nil
}

func validateMetricSnapshot(label string, snapshot MetricSnapshot) error {
	if snapshot.Value.IsNegative() {
		return fmt.Errorf("%w: %s snapshot value must be nonnegative", ErrInvalidMetric, label)
	}
	return nil
}

func missingWindowBoundaries(prefix string, window Window) []string {
	missing := make([]string, 0, 2)
	if window.Start.IsZero() {
		missing = append(missing, prefix+"_start")
	}
	if window.End.IsZero() {
		missing = append(missing, prefix+"_end")
	}
	return missing
}

func validCanonicalTopic(value string) bool {
	if len(value) == 36 {
		if id, err := uuid.Parse(value); err == nil {
			return id != uuid.Nil && id.String() == value
		}
	}
	return len(value) <= 120 && taxonomySlugPattern.MatchString(value)
}

func encodeCohortComponents(components ...string) string {
	labels := [...]byte{'t', 'm', 'p'}
	var encoded strings.Builder
	for index, component := range components {
		if index < len(labels) {
			encoded.WriteByte(labels[index])
		}
		encoded.WriteString(strconv.Itoa(len(component)))
		encoded.WriteByte(':')
		encoded.WriteString(component)
	}
	return encoded.String()
}
