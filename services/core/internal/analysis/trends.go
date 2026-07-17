package analysis

import (
	"errors"
	"math"
	"sort"
	"time"
)

const PublicationTrendFormulaVersion = "biomedical-trends/v1"

type TrendModel string

const (
	TrendModelPoisson          TrendModel = "poisson"
	TrendModelNegativeBinomial TrendModel = "negative_binomial"
)

type TrendModelSelectionRule string

const (
	TrendModelSelectionFixedPoisson          TrendModelSelectionRule = "fixed_poisson"
	TrendModelSelectionFixedNegativeBinomial TrendModelSelectionRule = "fixed_negative_binomial"
	TrendModelSelectionDispersionThreshold   TrendModelSelectionRule = "predeclared_dispersion_threshold"
)

type TrendModelSelection struct {
	Rule                TrendModelSelectionRule
	DispersionThreshold float64
}

type PublicationTrendPolicy struct {
	FormulaVersion             string
	ConfidenceLevel            float64
	MinimumPapersPerWindow     int
	MinimumIndependentJournals int
	MinimumIndependentTeams    int
	ModelSelection             TrendModelSelection
}

type TrendWindow struct {
	Start      time.Time
	End        time.Time
	PaperCount int
}

type PublicationTrendInput struct {
	ID                         string
	Recent                     TrendWindow
	Baseline                   TrendWindow
	IndependentJournalCount    int
	IndependentTeamCount       int
	PredeclaredDispersionAlpha float64
}

type TrendWindowEvidence struct {
	Start      time.Time
	End        time.Time
	Exposure   time.Duration
	PaperCount int
	RatePerDay float64
}

type PublicationTrend struct {
	ID                         string
	FormulaVersion             string
	Status                     AnalysisStatus
	Model                      TrendModel
	ModelSelection             TrendModelSelection
	PredeclaredDispersionAlpha float64
	Recent                     TrendWindowEvidence
	Baseline                   TrendWindowEvidence
	IndependentJournalCount    int
	IndependentTeamCount       int
	RateRatio                  StatisticalEstimate
	InsufficientEvidence       *InsufficientEvidence
}

func AnalyzePublicationTrend(
	input PublicationTrendInput,
	policy PublicationTrendPolicy,
) (PublicationTrend, error) {
	if err := validatePublicationTrendPolicy(policy); err != nil {
		return PublicationTrend{}, err
	}
	if err := validatePublicationTrendInput(input); err != nil {
		return PublicationTrend{}, err
	}

	model, err := selectTrendModel(input.PredeclaredDispersionAlpha, policy.ModelSelection)
	if err != nil {
		return PublicationTrend{}, err
	}
	result := PublicationTrend{
		ID:                         input.ID,
		FormulaVersion:             policy.FormulaVersion,
		Status:                     AnalysisStatusSufficientEvidence,
		Model:                      model,
		ModelSelection:             policy.ModelSelection,
		PredeclaredDispersionAlpha: input.PredeclaredDispersionAlpha,
		Recent:                     trendWindowEvidence(input.Recent),
		Baseline:                   trendWindowEvidence(input.Baseline),
		IndependentJournalCount:    input.IndependentJournalCount,
		IndependentTeamCount:       input.IndependentTeamCount,
	}

	reasons := make([]string, 0, 4)
	if input.Recent.PaperCount < policy.MinimumPapersPerWindow {
		reasons = append(reasons, "recent_paper_count")
	}
	if input.Baseline.PaperCount < policy.MinimumPapersPerWindow {
		reasons = append(reasons, "baseline_paper_count")
	}
	if input.IndependentJournalCount < policy.MinimumIndependentJournals {
		reasons = append(reasons, "independent_journal_count")
	}
	if input.IndependentTeamCount < policy.MinimumIndependentTeams {
		reasons = append(reasons, "independent_team_count")
	}
	if len(reasons) != 0 {
		evidence, insufficient := newInsufficientEvidence("publication_trend", reasons)
		result.Status = AnalysisStatusInsufficientEvidence
		result.InsufficientEvidence = evidence
		return result, insufficient
	}

	ratio := result.Recent.RatePerDay / result.Baseline.RatePerDay
	variance := 1/float64(input.Recent.PaperCount) +
		1/float64(input.Baseline.PaperCount)
	if model == TrendModelNegativeBinomial {
		variance += 2 * input.PredeclaredDispersionAlpha
	}
	result.RateRatio, err = ratioEstimate("rate_ratio", ratio, math.Sqrt(variance))
	if err != nil {
		return PublicationTrend{}, err
	}
	return result, nil
}

func AnalyzePublicationTrends(
	inputs []PublicationTrendInput,
	policy PublicationTrendPolicy,
) ([]PublicationTrend, error) {
	if err := validatePublicationTrendPolicy(policy); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(inputs))
	results := make([]PublicationTrend, 0, len(inputs))
	pValues := make(map[string]float64, len(inputs))
	for _, input := range inputs {
		if _, exists := seen[input.ID]; exists {
			return nil, &InvalidInputError{
				Field:  "id",
				Reason: "publication trend IDs must be unique",
			}
		}
		seen[input.ID] = struct{}{}

		result, err := AnalyzePublicationTrend(input, policy)
		if err != nil {
			var insufficient *InsufficientEvidenceError
			if !errors.As(err, &insufficient) {
				return nil, err
			}
		} else {
			pValues[result.ID] = result.RateRatio.PValue
		}
		results = append(results, result)
	}

	adjusted := benjaminiHochberg(pValues)
	for index := range results {
		if results[index].Status == AnalysisStatusSufficientEvidence {
			results[index].RateRatio.AdjustedPValue = adjusted[results[index].ID]
		}
	}
	sort.Slice(results, func(left, right int) bool {
		return results[left].ID < results[right].ID
	})
	return results, nil
}

func validatePublicationTrendPolicy(policy PublicationTrendPolicy) error {
	if policy.FormulaVersion != PublicationTrendFormulaVersion {
		return &InvalidInputError{
			Field:  "formula_version",
			Reason: "must equal " + PublicationTrendFormulaVersion,
		}
	}
	if policy.ConfidenceLevel != confidenceLevel95 {
		return &InvalidInputError{
			Field:  "confidence_level",
			Reason: "must equal 0.95",
		}
	}
	if policy.MinimumPapersPerWindow <= 0 {
		return &InvalidInputError{
			Field:  "minimum_papers_per_window",
			Reason: "must be positive",
		}
	}
	if policy.MinimumIndependentJournals <= 0 {
		return &InvalidInputError{
			Field:  "minimum_independent_journals",
			Reason: "must be positive",
		}
	}
	if policy.MinimumIndependentTeams <= 0 {
		return &InvalidInputError{
			Field:  "minimum_independent_teams",
			Reason: "must be positive",
		}
	}
	switch policy.ModelSelection.Rule {
	case TrendModelSelectionFixedPoisson,
		TrendModelSelectionFixedNegativeBinomial:
		if policy.ModelSelection.DispersionThreshold != 0 {
			return &InvalidInputError{
				Field:  "model_selection.dispersion_threshold",
				Reason: "must be zero for a fixed model",
			}
		}
	case TrendModelSelectionDispersionThreshold:
		if !positiveFinite(policy.ModelSelection.DispersionThreshold) {
			return &InvalidInputError{
				Field:  "model_selection.dispersion_threshold",
				Reason: "must be positive and finite",
			}
		}
	default:
		return &InvalidInputError{
			Field:  "model_selection.rule",
			Reason: "is not a declared model-selection rule",
		}
	}
	return nil
}

func validatePublicationTrendInput(input PublicationTrendInput) error {
	if err := validateTrimmed("id", input.ID); err != nil {
		return err
	}
	if err := validateTrendWindow("recent", input.Recent); err != nil {
		return err
	}
	if err := validateTrendWindow("baseline", input.Baseline); err != nil {
		return err
	}
	if input.Baseline.End.After(input.Recent.Start) {
		return &InvalidInputError{
			Field:  "windows",
			Reason: "baseline exposure must not overlap recent exposure",
		}
	}
	if input.IndependentJournalCount < 0 {
		return &InvalidInputError{
			Field:  "independent_journal_count",
			Reason: "must be non-negative",
		}
	}
	if input.IndependentTeamCount < 0 {
		return &InvalidInputError{
			Field:  "independent_team_count",
			Reason: "must be non-negative",
		}
	}
	if !nonNegativeFinite(input.PredeclaredDispersionAlpha) {
		return &InvalidInputError{
			Field:  "predeclared_dispersion_alpha",
			Reason: "must be non-negative and finite",
		}
	}
	return nil
}

func validateTrendWindow(field string, window TrendWindow) error {
	if window.Start.IsZero() || window.End.IsZero() || !window.Start.Before(window.End) {
		return &InvalidInputError{
			Field:  field + "_window",
			Reason: "must have ordered nonzero boundaries",
		}
	}
	if window.PaperCount < 0 {
		return &InvalidInputError{
			Field:  field + "_paper_count",
			Reason: "must be non-negative",
		}
	}
	return nil
}

func selectTrendModel(
	predeclaredDispersionAlpha float64,
	selection TrendModelSelection,
) (TrendModel, error) {
	switch selection.Rule {
	case TrendModelSelectionFixedPoisson:
		return TrendModelPoisson, nil
	case TrendModelSelectionFixedNegativeBinomial:
		if !positiveFinite(predeclaredDispersionAlpha) {
			return "", &InvalidInputError{
				Field:  "predeclared_dispersion_alpha",
				Reason: "must be positive for fixed negative binomial analysis",
			}
		}
		return TrendModelNegativeBinomial, nil
	case TrendModelSelectionDispersionThreshold:
		if predeclaredDispersionAlpha >= selection.DispersionThreshold {
			return TrendModelNegativeBinomial, nil
		}
		return TrendModelPoisson, nil
	default:
		return "", &InvalidInputError{
			Field:  "model_selection.rule",
			Reason: "is not a declared model-selection rule",
		}
	}
}

func trendWindowEvidence(window TrendWindow) TrendWindowEvidence {
	exposure := window.End.Sub(window.Start)
	exposureDays := exposure.Hours() / 24
	return TrendWindowEvidence{
		Start:      window.Start.UTC(),
		End:        window.End.UTC(),
		Exposure:   exposure,
		PaperCount: window.PaperCount,
		RatePerDay: float64(window.PaperCount) / exposureDays,
	}
}
