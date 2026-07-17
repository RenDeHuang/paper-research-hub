package analysis

import (
	"errors"
	"reflect"
	"testing"
)

func TestJournalEditorialPatternComputesOddsRatioAndEvidencePayload(t *testing.T) {
	t.Parallel()

	result, err := AnalyzeJournalPattern(
		JournalPatternInput{
			ID:                       "journal-a|mesh|neoplasm",
			JournalID:                "journal-a",
			FieldID:                  "oncology",
			FeatureType:              "mesh",
			FeatureValue:             "Neoplasms",
			Measure:                  JournalPatternOddsRatio,
			JournalFeaturePaperCount: 30,
			JournalPaperCount:        100,
			FieldFeaturePaperCount:   40,
			FieldBaselinePaperCount:  200,
			CoveredPaperCount:        270,
			EligiblePaperCount:       300,
		},
		validJournalPatternPolicy(),
	)
	if err != nil {
		t.Fatalf("AnalyzeJournalPattern() error = %v", err)
	}

	if result.Kind != EditorialPatternKind {
		t.Fatalf("Kind = %q, want %q", result.Kind, EditorialPatternKind)
	}
	if string(result.Kind) != "editorial_pattern" {
		t.Fatalf("Kind payload = %q", result.Kind)
	}
	if result.Measure != JournalPatternOddsRatio {
		t.Fatalf("Measure = %q", result.Measure)
	}
	assertClose(t, "odds ratio", result.Effect.Value, 1.7142857142857142, 1e-12)
	assertClose(t, "CI lower", result.Effect.ConfidenceInterval.Lower, 0.9886325004901108, 1e-12)
	assertClose(t, "CI upper", result.Effect.ConfidenceInterval.Upper, 2.972566154508571, 1e-12)
	assertClose(t, "p value", result.Effect.PValue, 0.0549520886219641, 1e-15)
	if result.SupportPaperCount != 30 || result.FieldBaselinePaperCount != 200 {
		t.Fatalf(
			"support/baseline = %d/%d, want 30/200",
			result.SupportPaperCount,
			result.FieldBaselinePaperCount,
		)
	}
	if result.Coverage.Covered != 270 ||
		result.Coverage.Eligible != 300 {
		t.Fatalf("Coverage counts = %#v", result.Coverage)
	}
	assertClose(t, "coverage", result.Coverage.Proportion, 0.9, 1e-15)
}

func TestJournalEditorialPatternSupportsPredeclaredRateRatioWithExactExposure(t *testing.T) {
	t.Parallel()

	result, err := AnalyzeJournalPattern(
		JournalPatternInput{
			ID:                       "journal-b|method|single-cell",
			JournalID:                "journal-b",
			FieldID:                  "cell-biology",
			FeatureType:              "method",
			FeatureValue:             "single-cell RNA sequencing",
			Measure:                  JournalPatternRateRatio,
			JournalFeaturePaperCount: 25,
			JournalPaperCount:        100,
			FieldFeaturePaperCount:   25,
			FieldBaselinePaperCount:  200,
			JournalExposure:          100,
			FieldBaselineExposure:    200,
			CoveredPaperCount:        285,
			EligiblePaperCount:       300,
		},
		validJournalPatternPolicy(),
	)
	if err != nil {
		t.Fatalf("AnalyzeJournalPattern() error = %v", err)
	}
	if result.Measure != JournalPatternRateRatio {
		t.Fatalf("Measure = %q", result.Measure)
	}
	assertClose(t, "rate ratio", result.Effect.Value, 2, 1e-12)
	assertClose(t, "coverage", result.Coverage.Proportion, 0.95, 1e-15)
}

func TestJournalEditorialPatternsExcludeLowSupportAndApplyBHCorrection(t *testing.T) {
	t.Parallel()

	inputs := []JournalPatternInput{
		{
			ID:                       "gamma",
			JournalID:                "journal-c",
			FieldID:                  "oncology",
			FeatureType:              "mesh",
			FeatureValue:             "gamma",
			Measure:                  JournalPatternOddsRatio,
			JournalFeaturePaperCount: 4,
			JournalPaperCount:        100,
			FieldFeaturePaperCount:   30,
			FieldBaselinePaperCount:  200,
			CoveredPaperCount:        285,
			EligiblePaperCount:       300,
		},
		{
			ID:                       "beta",
			JournalID:                "journal-b",
			FieldID:                  "oncology",
			FeatureType:              "mesh",
			FeatureValue:             "beta",
			Measure:                  JournalPatternOddsRatio,
			JournalFeaturePaperCount: 18,
			JournalPaperCount:        80,
			FieldFeaturePaperCount:   20,
			FieldBaselinePaperCount:  160,
			CoveredPaperCount:        228,
			EligiblePaperCount:       240,
		},
		{
			ID:                       "alpha",
			JournalID:                "journal-a",
			FieldID:                  "oncology",
			FeatureType:              "mesh",
			FeatureValue:             "alpha",
			Measure:                  JournalPatternOddsRatio,
			JournalFeaturePaperCount: 25,
			JournalPaperCount:        100,
			FieldFeaturePaperCount:   25,
			FieldBaselinePaperCount:  200,
			CoveredPaperCount:        285,
			EligiblePaperCount:       300,
		},
	}

	results, err := AnalyzeJournalPatterns(inputs, validJournalPatternPolicy())
	if err != nil {
		t.Fatalf("AnalyzeJournalPatterns() error = %v", err)
	}
	if got := []string{results[0].ID, results[1].ID}; !reflect.DeepEqual(
		got,
		[]string{"alpha", "beta"},
	) {
		t.Fatalf("result IDs = %v, want low-support gamma excluded", got)
	}
	assertClose(t, "alpha adjusted", results[0].Effect.AdjustedPValue, 0.014194686477234172, 1e-15)
	assertClose(t, "beta adjusted", results[1].Effect.AdjustedPValue, 0.04818289154720172, 1e-15)

	_, err = AnalyzeJournalPattern(inputs[0], validJournalPatternPolicy())
	var insufficient *InsufficientEvidenceError
	if !errors.As(err, &insufficient) {
		t.Fatalf("low-support error = %T %v, want *InsufficientEvidenceError", err, err)
	}
	if !reflect.DeepEqual(insufficient.Reasons, []string{"support_paper_count"}) {
		t.Fatalf("low-support reasons = %v", insufficient.Reasons)
	}
}

func TestJournalEditorialPatternAvoidsIntegerOverflowInOddsRatio(t *testing.T) {
	t.Parallel()

	result, err := AnalyzeJournalPattern(
		JournalPatternInput{
			ID:                       "large-valid-cohort",
			JournalID:                "journal-large",
			FieldID:                  "field-large",
			FeatureType:              "mesh",
			FeatureValue:             "large cohort feature",
			Measure:                  JournalPatternOddsRatio,
			JournalFeaturePaperCount: 4_000_000_000,
			JournalPaperCount:        10_000_000_000,
			FieldFeaturePaperCount:   3_000_000_000,
			FieldBaselinePaperCount:  10_000_000_000,
			CoveredPaperCount:        19_000_000_000,
			EligiblePaperCount:       20_000_000_000,
		},
		validJournalPatternPolicy(),
	)
	if err != nil {
		t.Fatalf("AnalyzeJournalPattern() error = %v", err)
	}
	assertClose(t, "large-cohort odds ratio", result.Effect.Value, 28.0/18.0, 1e-12)
}

func TestJournalEditorialPatternValidationHasDeterministicFieldOrder(t *testing.T) {
	t.Parallel()

	for iteration := 0; iteration < 128; iteration++ {
		_, err := AnalyzeJournalPattern(
			JournalPatternInput{},
			validJournalPatternPolicy(),
		)
		var invalid *InvalidInputError
		if !errors.As(err, &invalid) {
			t.Fatalf("iteration %d error = %T %v", iteration, err, err)
		}
		if invalid.Field != "id" {
			t.Fatalf(
				"iteration %d first invalid field = %q, want deterministic id",
				iteration,
				invalid.Field,
			)
		}
	}
}

func validJournalPatternPolicy() JournalPatternPolicy {
	return JournalPatternPolicy{
		FormulaVersion:            JournalPatternFormulaVersion,
		ConfidenceLevel:           0.95,
		MinimumSupportPaperCount:  5,
		MinimumFieldBaselineCount: 20,
		MinimumCoverage:           0.8,
	}
}
