package citation

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestAnalyzeCitationVelocitySelectsSameSourceBoundariesWithoutInterpolation(
	t *testing.T,
) {
	t.Parallel()

	workID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	asOf := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	input := CitationVelocityInput{
		Source: "openalex",
		AsOf:   asOf,
		Window: 48 * time.Hour,
		Snapshots: []Snapshot{
			analysisSnapshot(workID, "openalex", asOf.Add(-72*time.Hour), 2),
			analysisSnapshot(workID, "openalex", asOf.Add(-51*time.Hour), 5),
			analysisSnapshot(workID, "openalex", asOf.Add(-47*time.Hour), 8),
			analysisSnapshot(workID, "openalex", asOf.Add(-9*time.Hour), 26),
			analysisSnapshot(workID, "openalex", asOf.Add(time.Hour), 99),
		},
	}

	got, err := AnalyzeCitationVelocity(input)
	if err != nil {
		t.Fatalf("AnalyzeCitationVelocity() error = %v", err)
	}
	if !got.Start.ObservedAt.Equal(asOf.Add(-51 * time.Hour)) {
		t.Fatalf(
			"AnalyzeCitationVelocity().Start.ObservedAt = %v, want %v",
			got.Start.ObservedAt,
			asOf.Add(-51*time.Hour),
		)
	}
	if !got.End.ObservedAt.Equal(asOf.Add(-9 * time.Hour)) {
		t.Fatalf(
			"AnalyzeCitationVelocity().End.ObservedAt = %v, want %v",
			got.End.ObservedAt,
			asOf.Add(-9*time.Hour),
		)
	}
	if got.Elapsed != 42*time.Hour {
		t.Fatalf(
			"AnalyzeCitationVelocity().Elapsed = %v, want 42h",
			got.Elapsed,
		)
	}
	if !got.Velocity.Equal(decimal.NewFromInt(12)) {
		t.Fatalf(
			"AnalyzeCitationVelocity().Velocity = %s, want 12 citations/day",
			got.Velocity,
		)
	}
}

func TestAnalyzeCitationVelocityReturnsTypedInsufficientEvidenceForMissingBoundary(
	t *testing.T,
) {
	t.Parallel()

	workID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	asOf := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		snapshots []Snapshot
		missing   []string
	}{
		{
			name: "start",
			snapshots: []Snapshot{
				analysisSnapshot(workID, "openalex", asOf.Add(-24*time.Hour), 10),
			},
			missing: []string{"start_snapshot"},
		},
		{
			name: "end",
			snapshots: []Snapshot{
				analysisSnapshot(workID, "openalex", asOf.Add(time.Hour), 10),
			},
			missing: []string{"start_snapshot", "end_snapshot"},
		},
		{
			name: "distinct boundaries",
			snapshots: []Snapshot{
				analysisSnapshot(workID, "openalex", asOf.Add(-72*time.Hour), 10),
			},
			missing: []string{"distinct_end_snapshot"},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result, err := AnalyzeCitationVelocity(CitationVelocityInput{
				Source:    "openalex",
				AsOf:      asOf,
				Window:    48 * time.Hour,
				Snapshots: test.snapshots,
			})
			var insufficient *InsufficientEvidenceError
			if !errors.As(err, &insufficient) {
				t.Fatalf(
					"AnalyzeCitationVelocity() = %#v, %v, want InsufficientEvidenceError",
					result,
					err,
				)
			}
			if insufficient.Signal != SignalCitationVelocity ||
				!reflect.DeepEqual(insufficient.Missing, test.missing) {
				t.Fatalf(
					"InsufficientEvidenceError = %#v, want signal %q missing %v",
					insufficient,
					SignalCitationVelocity,
					test.missing,
				)
			}
		})
	}
}

func TestAnalyzeCitationVelocityRejectsMixedSourcesWithTypedError(t *testing.T) {
	t.Parallel()

	asOf := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	workID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	_, err := AnalyzeCitationVelocity(CitationVelocityInput{
		Source: "openalex",
		AsOf:   asOf,
		Window: 48 * time.Hour,
		Snapshots: []Snapshot{
			analysisSnapshot(workID, "openalex", asOf.Add(-72*time.Hour), 10),
			analysisSnapshot(workID, "crossref", asOf.Add(-time.Hour), 20),
		},
	})

	var mixed *MixedSourceError
	if !errors.As(err, &mixed) {
		t.Fatalf(
			"AnalyzeCitationVelocity() error = %v, want MixedSourceError",
			err,
		)
	}
	if mixed.Expected != "openalex" || mixed.Actual != "crossref" || mixed.Index != 1 {
		t.Fatalf("MixedSourceError = %#v", mixed)
	}
}

func TestAnalyzeCitationVelocityRejectsReversedInputWithTypedError(t *testing.T) {
	t.Parallel()

	asOf := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	workID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	later := asOf.Add(-time.Hour)
	earlier := asOf.Add(-72 * time.Hour)
	_, err := AnalyzeCitationVelocity(CitationVelocityInput{
		Source: "openalex",
		AsOf:   asOf,
		Window: 48 * time.Hour,
		Snapshots: []Snapshot{
			analysisSnapshot(workID, "openalex", later, 20),
			analysisSnapshot(workID, "openalex", earlier, 10),
		},
	})

	var reversed *TimeOrderError
	if !errors.As(err, &reversed) {
		t.Fatalf(
			"AnalyzeCitationVelocity() error = %v, want TimeOrderError",
			err,
		)
	}
	if reversed.Index != 1 ||
		!reversed.Previous.Equal(later) ||
		!reversed.Current.Equal(earlier) {
		t.Fatalf("TimeOrderError = %#v", reversed)
	}
}

func TestAnalyzeCitationVelocityRejectsNegativeCountWithTypedError(t *testing.T) {
	t.Parallel()

	asOf := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	workID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	_, err := AnalyzeCitationVelocity(CitationVelocityInput{
		Source: "openalex",
		AsOf:   asOf,
		Window: 48 * time.Hour,
		Snapshots: []Snapshot{
			analysisSnapshot(workID, "openalex", asOf.Add(-72*time.Hour), -1),
			analysisSnapshot(workID, "openalex", asOf.Add(-time.Hour), 20),
		},
	})

	var negative *NegativeCountError
	if !errors.As(err, &negative) {
		t.Fatalf(
			"AnalyzeCitationVelocity() error = %v, want NegativeCountError",
			err,
		)
	}
	if negative.WorkID != workID || negative.Count != -1 || negative.Index != 0 {
		t.Fatalf("NegativeCountError = %#v", negative)
	}
}

func TestAnalyzeCitationVelocityRequiresExplicitSourceAsOfAndPositiveWindow(
	t *testing.T,
) {
	t.Parallel()

	asOf := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		input CitationVelocityInput
		field string
	}{
		{
			name: "blank source",
			input: CitationVelocityInput{
				AsOf:   asOf,
				Window: time.Hour,
			},
			field: "source",
		},
		{
			name: "untrimmed source",
			input: CitationVelocityInput{
				Source: " openalex",
				AsOf:   asOf,
				Window: time.Hour,
			},
			field: "source",
		},
		{
			name: "zero as of",
			input: CitationVelocityInput{
				Source: "openalex",
				Window: time.Hour,
			},
			field: "as_of",
		},
		{
			name: "zero window",
			input: CitationVelocityInput{
				Source: "openalex",
				AsOf:   asOf,
			},
			field: "window",
		},
		{
			name: "negative window",
			input: CitationVelocityInput{
				Source: "openalex",
				AsOf:   asOf,
				Window: -time.Hour,
			},
			field: "window",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := AnalyzeCitationVelocity(test.input)
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) {
				t.Fatalf(
					"AnalyzeCitationVelocity() error = %v, want InvalidInputError",
					err,
				)
			}
			if invalid.Field != test.field {
				t.Fatalf(
					"InvalidInputError.Field = %q, want %q",
					invalid.Field,
					test.field,
				)
			}
		})
	}
}

func TestAnalyzeCitationPercentileUsesTargetInclusiveMidrank(t *testing.T) {
	t.Parallel()

	cohort := analysisCohort()
	target := CitationPercentileObservation{
		WorkID: uuid.MustParse("00000000-0000-0000-0000-000000000100"),
		Cohort: cohort,
		Count:  20,
	}
	observations := []CitationPercentileObservation{
		{
			WorkID: uuid.MustParse("00000000-0000-0000-0000-000000000004"),
			Cohort: cohort,
			Count:  20,
		},
		{
			WorkID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
			Cohort: cohort,
			Count:  10,
		},
		{
			WorkID: uuid.MustParse("00000000-0000-0000-0000-000000000003"),
			Cohort: cohort,
			Count:  30,
		},
		{
			WorkID: uuid.MustParse("00000000-0000-0000-0000-000000000002"),
			Cohort: cohort,
			Count:  5,
		},
	}
	before := append([]CitationPercentileObservation(nil), observations...)

	got, err := AnalyzeCitationPercentile(CitationPercentileInput{
		Cohort:            cohort,
		Target:            target,
		Observations:      observations,
		MinimumCohortSize: 5,
	})
	if err != nil {
		t.Fatalf("AnalyzeCitationPercentile() error = %v", err)
	}
	if got.Cohort != cohort {
		t.Fatalf("AnalyzeCitationPercentile().Cohort = %#v, want %#v", got.Cohort, cohort)
	}
	if got.CohortSize != 5 {
		t.Fatalf("AnalyzeCitationPercentile().CohortSize = %d, want 5", got.CohortSize)
	}
	wantRank := decimal.RequireFromString("3.5")
	if !got.Rank.Equal(wantRank) {
		t.Fatalf(
			"AnalyzeCitationPercentile().Rank = %s, want midrank %s",
			got.Rank,
			wantRank,
		)
	}
	if !got.Percentile.Equal(decimal.NewFromInt(70)) {
		t.Fatalf(
			"AnalyzeCitationPercentile().Percentile = %s, want 70",
			got.Percentile,
		)
	}
	wantSupporting := []uuid.UUID{
		uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		uuid.MustParse("00000000-0000-0000-0000-000000000003"),
		uuid.MustParse("00000000-0000-0000-0000-000000000004"),
		uuid.MustParse("00000000-0000-0000-0000-000000000100"),
	}
	if !reflect.DeepEqual(got.SupportingWorkIDs, wantSupporting) {
		t.Fatalf(
			"AnalyzeCitationPercentile().SupportingWorkIDs = %v, want %v",
			got.SupportingWorkIDs,
			wantSupporting,
		)
	}
	if !reflect.DeepEqual(observations, before) {
		t.Fatalf(
			"AnalyzeCitationPercentile() mutated observations: got %#v want %#v",
			observations,
			before,
		)
	}
}

func TestAnalyzeCitationPercentileReturnsTypedInsufficientEvidenceForSmallCohort(
	t *testing.T,
) {
	t.Parallel()

	cohort := analysisCohort()
	result, err := AnalyzeCitationPercentile(CitationPercentileInput{
		Cohort: cohort,
		Target: CitationPercentileObservation{
			WorkID: uuid.MustParse("00000000-0000-0000-0000-000000000100"),
			Cohort: cohort,
			Count:  20,
		},
		Observations: []CitationPercentileObservation{
			{
				WorkID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
				Cohort: cohort,
				Count:  10,
			},
		},
		MinimumCohortSize: 3,
	})

	var insufficient *InsufficientEvidenceError
	if !errors.As(err, &insufficient) {
		t.Fatalf(
			"AnalyzeCitationPercentile() = %#v, %v, want InsufficientEvidenceError",
			result,
			err,
		)
	}
	if insufficient.Signal != SignalCitationPercentile ||
		insufficient.CohortSize != 2 ||
		insufficient.MinimumCohortSize != 3 {
		t.Fatalf("InsufficientEvidenceError = %#v", insufficient)
	}
}

func TestAnalyzeCitationPercentileRejectsEveryMixedCohortComponent(t *testing.T) {
	t.Parallel()

	cohort := analysisCohort()
	tests := []struct {
		name   string
		mutate func(*CitationCohortIdentity)
	}{
		{
			name: "subject version",
			mutate: func(candidate *CitationCohortIdentity) {
				candidate.SubjectVersionID = uuid.New()
			},
		},
		{
			name: "subject",
			mutate: func(candidate *CitationCohortIdentity) {
				candidate.SubjectID = uuid.New()
			},
		},
		{
			name: "publication year",
			mutate: func(candidate *CitationCohortIdentity) {
				candidate.PublicationYear++
			},
		},
		{
			name: "publication type",
			mutate: func(candidate *CitationCohortIdentity) {
				candidate.PublicationTypeID = uuid.New()
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			mixedCohort := cohort
			test.mutate(&mixedCohort)
			mixedWorkID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
			_, err := AnalyzeCitationPercentile(CitationPercentileInput{
				Cohort: cohort,
				Target: CitationPercentileObservation{
					WorkID: uuid.MustParse("00000000-0000-0000-0000-000000000100"),
					Cohort: cohort,
					Count:  20,
				},
				Observations: []CitationPercentileObservation{
					{
						WorkID: mixedWorkID,
						Cohort: mixedCohort,
						Count:  10,
					},
				},
				MinimumCohortSize: 2,
			})

			var mixed *MixedCohortError
			if !errors.As(err, &mixed) {
				t.Fatalf(
					"AnalyzeCitationPercentile() error = %v, want MixedCohortError",
					err,
				)
			}
			if mixed.WorkID != mixedWorkID ||
				mixed.Expected != cohort ||
				mixed.Actual != mixedCohort {
				t.Fatalf("MixedCohortError = %#v", mixed)
			}
		})
	}
}

func TestAnalyzeCitationPercentileRejectsDuplicateWork(t *testing.T) {
	t.Parallel()

	cohort := analysisCohort()
	targetID := uuid.MustParse("00000000-0000-0000-0000-000000000100")
	peerID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tests := []struct {
		name         string
		observations []CitationPercentileObservation
		duplicateID  uuid.UUID
	}{
		{
			name: "peer repeated",
			observations: []CitationPercentileObservation{
				{WorkID: peerID, Cohort: cohort, Count: 10},
				{WorkID: peerID, Cohort: cohort, Count: 20},
			},
			duplicateID: peerID,
		},
		{
			name: "target repeated as peer",
			observations: []CitationPercentileObservation{
				{WorkID: targetID, Cohort: cohort, Count: 20},
			},
			duplicateID: targetID,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := AnalyzeCitationPercentile(CitationPercentileInput{
				Cohort: cohort,
				Target: CitationPercentileObservation{
					WorkID: targetID,
					Cohort: cohort,
					Count:  20,
				},
				Observations:      test.observations,
				MinimumCohortSize: 2,
			})
			var duplicate *DuplicateWorkError
			if !errors.As(err, &duplicate) {
				t.Fatalf(
					"AnalyzeCitationPercentile() error = %v, want DuplicateWorkError",
					err,
				)
			}
			if duplicate.WorkID != test.duplicateID {
				t.Fatalf(
					"DuplicateWorkError.WorkID = %s, want %s",
					duplicate.WorkID,
					test.duplicateID,
				)
			}
		})
	}
}

func TestAnalyzeCitationPercentileRejectsNegativeCount(t *testing.T) {
	t.Parallel()

	cohort := analysisCohort()
	targetID := uuid.MustParse("00000000-0000-0000-0000-000000000100")
	peerID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	tests := []CitationPercentileInput{
		{
			Cohort: cohort,
			Target: CitationPercentileObservation{
				WorkID: targetID,
				Cohort: cohort,
				Count:  -1,
			},
			MinimumCohortSize: 1,
		},
		{
			Cohort: cohort,
			Target: CitationPercentileObservation{
				WorkID: targetID,
				Cohort: cohort,
				Count:  20,
			},
			Observations: []CitationPercentileObservation{
				{WorkID: peerID, Cohort: cohort, Count: -1},
			},
			MinimumCohortSize: 2,
		},
	}

	for _, input := range tests {
		_, err := AnalyzeCitationPercentile(input)
		var negative *NegativeCountError
		if !errors.As(err, &negative) {
			t.Fatalf(
				"AnalyzeCitationPercentile() error = %v, want NegativeCountError",
				err,
			)
		}
		if negative.Count != -1 {
			t.Fatalf("NegativeCountError.Count = %d, want -1", negative.Count)
		}
	}
}

func TestAnalyzeCitationPercentileRequiresExplicitValidCohortAndMinimumSize(
	t *testing.T,
) {
	t.Parallel()

	validCohort := analysisCohort()
	validTarget := CitationPercentileObservation{
		WorkID: uuid.MustParse("00000000-0000-0000-0000-000000000100"),
		Cohort: validCohort,
		Count:  20,
	}
	tests := []struct {
		name  string
		input CitationPercentileInput
		field string
	}{
		{
			name: "missing subject version",
			input: CitationPercentileInput{
				Cohort: func() CitationCohortIdentity {
					candidate := validCohort
					candidate.SubjectVersionID = uuid.Nil
					return candidate
				}(),
				Target:            validTarget,
				MinimumCohortSize: 1,
			},
			field: "cohort.subject_version_id",
		},
		{
			name: "missing subject",
			input: CitationPercentileInput{
				Cohort: func() CitationCohortIdentity {
					candidate := validCohort
					candidate.SubjectID = uuid.Nil
					return candidate
				}(),
				Target:            validTarget,
				MinimumCohortSize: 1,
			},
			field: "cohort.subject_id",
		},
		{
			name: "invalid publication year",
			input: CitationPercentileInput{
				Cohort: func() CitationCohortIdentity {
					candidate := validCohort
					candidate.PublicationYear = 0
					return candidate
				}(),
				Target:            validTarget,
				MinimumCohortSize: 1,
			},
			field: "cohort.publication_year",
		},
		{
			name: "missing publication type",
			input: CitationPercentileInput{
				Cohort: func() CitationCohortIdentity {
					candidate := validCohort
					candidate.PublicationTypeID = uuid.Nil
					return candidate
				}(),
				Target:            validTarget,
				MinimumCohortSize: 1,
			},
			field: "cohort.publication_type_id",
		},
		{
			name: "zero minimum cohort size",
			input: CitationPercentileInput{
				Cohort: validCohort,
				Target: validTarget,
			},
			field: "minimum_cohort_size",
		},
		{
			name: "negative minimum cohort size",
			input: CitationPercentileInput{
				Cohort:            validCohort,
				Target:            validTarget,
				MinimumCohortSize: -1,
			},
			field: "minimum_cohort_size",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := AnalyzeCitationPercentile(test.input)
			var invalid *InvalidInputError
			if !errors.As(err, &invalid) {
				t.Fatalf(
					"AnalyzeCitationPercentile() error = %v, want InvalidInputError",
					err,
				)
			}
			if invalid.Field != test.field {
				t.Fatalf(
					"InvalidInputError.Field = %q, want %q",
					invalid.Field,
					test.field,
				)
			}
		})
	}
}

func analysisCohort() CitationCohortIdentity {
	return CitationCohortIdentity{
		SubjectVersionID:  uuid.MustParse("00000000-0000-0000-0000-000000000010"),
		SubjectID:         uuid.MustParse("00000000-0000-0000-0000-000000000020"),
		PublicationYear:   2026,
		PublicationTypeID: uuid.MustParse("00000000-0000-0000-0000-000000000030"),
	}
}

func analysisSnapshot(
	workID uuid.UUID,
	source string,
	observedAt time.Time,
	count int64,
) Snapshot {
	return Snapshot{
		WorkID:     workID,
		Source:     source,
		ObservedAt: observedAt,
		Count:      count,
	}
}
