package analysis

import (
	"errors"
	"math"
	"sort"
)

const JournalPatternFormulaVersion = "biomedical-journal-patterns/v1"

type EditorialPatternKindType string

const EditorialPatternKind EditorialPatternKindType = "editorial_pattern"

type JournalPatternMeasure string

const (
	JournalPatternOddsRatio JournalPatternMeasure = "odds_ratio"
	JournalPatternRateRatio JournalPatternMeasure = "rate_ratio"
)

type JournalPatternPolicy struct {
	FormulaVersion            string
	ConfidenceLevel           float64
	MinimumSupportPaperCount  int
	MinimumFieldBaselineCount int
	MinimumCoverage           float64
}

type JournalPatternInput struct {
	ID                       string
	JournalID                string
	FieldID                  string
	FeatureType              string
	FeatureValue             string
	Measure                  JournalPatternMeasure
	JournalFeaturePaperCount int
	JournalPaperCount        int
	FieldFeaturePaperCount   int
	FieldBaselinePaperCount  int
	JournalExposure          float64
	FieldBaselineExposure    float64
	CoveredPaperCount        int
	EligiblePaperCount       int
}

type JournalPattern struct {
	ID                      string
	Kind                    EditorialPatternKindType
	FormulaVersion          string
	JournalID               string
	FieldID                 string
	FeatureType             string
	FeatureValue            string
	Measure                 JournalPatternMeasure
	Effect                  StatisticalEstimate
	SupportPaperCount       int
	FieldBaselinePaperCount int
	Coverage                Coverage
}

func AnalyzeJournalPattern(
	input JournalPatternInput,
	policy JournalPatternPolicy,
) (JournalPattern, error) {
	if err := validateJournalPatternPolicy(policy); err != nil {
		return JournalPattern{}, err
	}
	if err := validateJournalPatternInput(input); err != nil {
		return JournalPattern{}, err
	}
	coverage, err := calculateCoverage(
		input.CoveredPaperCount,
		input.EligiblePaperCount,
	)
	if err != nil {
		return JournalPattern{}, err
	}

	reasons := make([]string, 0, 3)
	if input.JournalFeaturePaperCount < policy.MinimumSupportPaperCount {
		reasons = append(reasons, "support_paper_count")
	}
	if input.FieldBaselinePaperCount < policy.MinimumFieldBaselineCount {
		reasons = append(reasons, "field_baseline_paper_count")
	}
	if coverage.Proportion < policy.MinimumCoverage {
		reasons = append(reasons, "coverage")
	}
	if len(reasons) != 0 {
		_, insufficient := newInsufficientEvidence("journal_editorial_pattern", reasons)
		return JournalPattern{}, insufficient
	}

	result := JournalPattern{
		ID:                      input.ID,
		Kind:                    EditorialPatternKind,
		FormulaVersion:          policy.FormulaVersion,
		JournalID:               input.JournalID,
		FieldID:                 input.FieldID,
		FeatureType:             input.FeatureType,
		FeatureValue:            input.FeatureValue,
		Measure:                 input.Measure,
		SupportPaperCount:       input.JournalFeaturePaperCount,
		FieldBaselinePaperCount: input.FieldBaselinePaperCount,
		Coverage:                coverage,
	}
	switch input.Measure {
	case JournalPatternOddsRatio:
		journalWithoutFeature := input.JournalPaperCount -
			input.JournalFeaturePaperCount
		fieldWithoutFeature := input.FieldBaselinePaperCount -
			input.FieldFeaturePaperCount
		if journalWithoutFeature == 0 || input.FieldFeaturePaperCount == 0 ||
			fieldWithoutFeature == 0 {
			_, insufficient := newInsufficientEvidence(
				"journal_editorial_pattern",
				[]string{"nonzero_contingency_cells"},
			)
			return JournalPattern{}, insufficient
		}
		ratio := (float64(input.JournalFeaturePaperCount) *
			float64(fieldWithoutFeature)) /
			(float64(journalWithoutFeature) *
				float64(input.FieldFeaturePaperCount))
		standardError := math.Sqrt(
			1/float64(input.JournalFeaturePaperCount) +
				1/float64(journalWithoutFeature) +
				1/float64(input.FieldFeaturePaperCount) +
				1/float64(fieldWithoutFeature),
		)
		result.Effect, err = ratioEstimate("odds_ratio", ratio, standardError)
	case JournalPatternRateRatio:
		if input.FieldFeaturePaperCount == 0 {
			_, insufficient := newInsufficientEvidence(
				"journal_editorial_pattern",
				[]string{"nonzero_feature_counts"},
			)
			return JournalPattern{}, insufficient
		}
		journalRate := float64(input.JournalFeaturePaperCount) / input.JournalExposure
		fieldRate := float64(input.FieldFeaturePaperCount) / input.FieldBaselineExposure
		ratio := journalRate / fieldRate
		standardError := math.Sqrt(
			1/float64(input.JournalFeaturePaperCount) +
				1/float64(input.FieldFeaturePaperCount),
		)
		result.Effect, err = ratioEstimate("rate_ratio", ratio, standardError)
	}
	if err != nil {
		return JournalPattern{}, err
	}
	return result, nil
}

func AnalyzeJournalPatterns(
	inputs []JournalPatternInput,
	policy JournalPatternPolicy,
) ([]JournalPattern, error) {
	if err := validateJournalPatternPolicy(policy); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(inputs))
	results := make([]JournalPattern, 0, len(inputs))
	pValues := make(map[string]float64, len(inputs))
	for _, input := range inputs {
		if _, exists := seen[input.ID]; exists {
			return nil, &InvalidInputError{
				Field:  "id",
				Reason: "journal pattern IDs must be unique",
			}
		}
		seen[input.ID] = struct{}{}
		result, err := AnalyzeJournalPattern(input, policy)
		if err != nil {
			var insufficient *InsufficientEvidenceError
			if errors.As(err, &insufficient) {
				continue
			}
			return nil, err
		}
		results = append(results, result)
		pValues[result.ID] = result.Effect.PValue
	}

	adjusted := benjaminiHochberg(pValues)
	for index := range results {
		results[index].Effect.AdjustedPValue = adjusted[results[index].ID]
	}
	sort.Slice(results, func(left, right int) bool {
		return results[left].ID < results[right].ID
	})
	return results, nil
}

func validateJournalPatternPolicy(policy JournalPatternPolicy) error {
	if policy.FormulaVersion != JournalPatternFormulaVersion {
		return &InvalidInputError{
			Field:  "formula_version",
			Reason: "must equal " + JournalPatternFormulaVersion,
		}
	}
	if policy.ConfidenceLevel != confidenceLevel95 {
		return &InvalidInputError{
			Field:  "confidence_level",
			Reason: "must equal 0.95",
		}
	}
	if policy.MinimumSupportPaperCount <= 0 {
		return &InvalidInputError{
			Field:  "minimum_support_paper_count",
			Reason: "must be positive",
		}
	}
	if policy.MinimumFieldBaselineCount <= 0 {
		return &InvalidInputError{
			Field:  "minimum_field_baseline_count",
			Reason: "must be positive",
		}
	}
	if policy.MinimumCoverage < 0 || policy.MinimumCoverage > 1 ||
		!nonNegativeFinite(policy.MinimumCoverage) {
		return &InvalidInputError{
			Field:  "minimum_coverage",
			Reason: "must be finite and between zero and one",
		}
	}
	return nil
}

func validateJournalPatternInput(input JournalPatternInput) error {
	identifiers := []struct {
		field string
		value string
	}{
		{field: "id", value: input.ID},
		{field: "journal_id", value: input.JournalID},
		{field: "field_id", value: input.FieldID},
		{field: "feature_type", value: input.FeatureType},
		{field: "feature_value", value: input.FeatureValue},
	}
	for _, identifier := range identifiers {
		if err := validateTrimmed(identifier.field, identifier.value); err != nil {
			return err
		}
	}
	if input.JournalPaperCount <= 0 {
		return &InvalidInputError{
			Field:  "journal_paper_count",
			Reason: "must be positive",
		}
	}
	if input.FieldBaselinePaperCount <= 0 {
		return &InvalidInputError{
			Field:  "field_baseline_paper_count",
			Reason: "must be positive",
		}
	}
	if input.JournalFeaturePaperCount < 0 ||
		input.JournalFeaturePaperCount > input.JournalPaperCount {
		return &InvalidInputError{
			Field:  "journal_feature_paper_count",
			Reason: "must be between zero and journal paper count",
		}
	}
	if input.FieldFeaturePaperCount < 0 ||
		input.FieldFeaturePaperCount > input.FieldBaselinePaperCount {
		return &InvalidInputError{
			Field:  "field_feature_paper_count",
			Reason: "must be between zero and field baseline paper count",
		}
	}
	switch input.Measure {
	case JournalPatternOddsRatio:
	case JournalPatternRateRatio:
		if !positiveFinite(input.JournalExposure) {
			return &InvalidInputError{
				Field:  "journal_exposure",
				Reason: "must be positive and finite for rate ratio",
			}
		}
		if !positiveFinite(input.FieldBaselineExposure) {
			return &InvalidInputError{
				Field:  "field_baseline_exposure",
				Reason: "must be positive and finite for rate ratio",
			}
		}
	default:
		return &InvalidInputError{
			Field:  "measure",
			Reason: "must be odds_ratio or rate_ratio",
		}
	}
	return nil
}
