package analysis

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestPublicationTrendUsesExactExposureForPredeclaredPoissonRateRatio(t *testing.T) {
	t.Parallel()

	result, err := AnalyzePublicationTrend(
		PublicationTrendInput{
			ID: "mesh:cancer",
			Recent: TrendWindow{
				Start:      time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC),
				End:        time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
				PaperCount: 20,
			},
			Baseline: TrendWindow{
				Start:      time.Date(2025, 5, 7, 12, 0, 0, 0, time.UTC),
				End:        time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC),
				PaperCount: 52,
			},
			IndependentJournalCount: 4,
			IndependentTeamCount:    8,
		},
		validTrendPolicy(TrendModelSelection{
			Rule: TrendModelSelectionFixedPoisson,
		}),
	)
	if err != nil {
		t.Fatalf("AnalyzePublicationTrend() error = %v", err)
	}

	if result.Status != AnalysisStatusSufficientEvidence {
		t.Fatalf("Status = %q, want %q", result.Status, AnalysisStatusSufficientEvidence)
	}
	if result.Model != TrendModelPoisson {
		t.Fatalf("Model = %q, want %q", result.Model, TrendModelPoisson)
	}
	if result.Recent.Exposure != 56*24*time.Hour {
		t.Fatalf("recent exposure = %s, want 56 days", result.Recent.Exposure)
	}
	if result.Baseline.Exposure != 364*24*time.Hour {
		t.Fatalf("baseline exposure = %s, want 364 days", result.Baseline.Exposure)
	}
	assertClose(t, "recent rate", result.Recent.RatePerDay, 20.0/56.0, 1e-12)
	assertClose(t, "baseline rate", result.Baseline.RatePerDay, 52.0/364.0, 1e-12)
	assertClose(t, "rate ratio", result.RateRatio.Value, 2.5, 1e-12)
	assertClose(t, "CI lower", result.RateRatio.ConfidenceInterval.Lower, 1.4927052724629826, 1e-12)
	assertClose(t, "CI upper", result.RateRatio.ConfidenceInterval.Upper, 4.187028822968798, 1e-12)
	assertClose(t, "p value", result.RateRatio.PValue, 0.0004968654711776554, 1e-15)
	if result.FormulaVersion != PublicationTrendFormulaVersion {
		t.Fatalf("FormulaVersion = %q", result.FormulaVersion)
	}
}

func TestPublicationTrendSelectsNegativeBinomialOnlyFromPredeclaredDispersion(t *testing.T) {
	t.Parallel()

	policy := validTrendPolicy(TrendModelSelection{
		Rule:                TrendModelSelectionDispersionThreshold,
		DispersionThreshold: 0.1,
	})
	base := PublicationTrendInput{
		ID: "subject:oncology",
		Recent: TrendWindow{
			Start:      time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
			End:        time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			PaperCount: 20,
		},
		Baseline: TrendWindow{
			Start:      time.Date(2025, 5, 7, 0, 0, 0, 0, time.UTC),
			End:        time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
			PaperCount: 52,
		},
		IndependentJournalCount:    4,
		IndependentTeamCount:       8,
		PredeclaredDispersionAlpha: 0.2,
	}

	negativeBinomial, err := AnalyzePublicationTrend(base, policy)
	if err != nil {
		t.Fatalf("AnalyzePublicationTrend(NB) error = %v", err)
	}
	if negativeBinomial.Model != TrendModelNegativeBinomial {
		t.Fatalf("Model = %q, want %q", negativeBinomial.Model, TrendModelNegativeBinomial)
	}
	assertClose(t, "NB CI lower", negativeBinomial.RateRatio.ConfidenceInterval.Lower, 0.652925104253098, 1e-12)
	assertClose(t, "NB CI upper", negativeBinomial.RateRatio.ConfidenceInterval.Upper, 9.572307695458543, 1e-12)
	assertClose(t, "NB p value", negativeBinomial.RateRatio.PValue, 0.18101300966545947, 1e-15)

	belowThreshold := base
	belowThreshold.ID = "subject:immunology"
	belowThreshold.PredeclaredDispersionAlpha = 0.099999
	poisson, err := AnalyzePublicationTrend(belowThreshold, policy)
	if err != nil {
		t.Fatalf("AnalyzePublicationTrend(Poisson) error = %v", err)
	}
	if poisson.Model != TrendModelPoisson {
		t.Fatalf("Model below threshold = %q, want %q", poisson.Model, TrendModelPoisson)
	}

	nonSignificant := base
	nonSignificant.ID = "subject:neurology"
	nonSignificant.Recent.PaperCount = 10
	nonSignificant.Baseline.PaperCount = 65
	stillNegativeBinomial, err := AnalyzePublicationTrend(nonSignificant, policy)
	if err != nil {
		t.Fatalf("AnalyzePublicationTrend(non-significant) error = %v", err)
	}
	if stillNegativeBinomial.Model != TrendModelNegativeBinomial {
		t.Fatalf(
			"non-significant Model = %q, want predeclared %q",
			stillNegativeBinomial.Model,
			TrendModelNegativeBinomial,
		)
	}
}

func TestPublicationTrendReturnsTypedInsufficientEvidenceForSparseCohort(t *testing.T) {
	t.Parallel()

	result, err := AnalyzePublicationTrend(
		PublicationTrendInput{
			ID: "method:rare",
			Recent: TrendWindow{
				Start:      time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
				End:        time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
				PaperCount: 2,
			},
			Baseline: TrendWindow{
				Start:      time.Date(2025, 5, 7, 0, 0, 0, 0, time.UTC),
				End:        time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
				PaperCount: 3,
			},
			IndependentJournalCount: 1,
			IndependentTeamCount:    1,
		},
		validTrendPolicy(TrendModelSelection{
			Rule: TrendModelSelectionFixedPoisson,
		}),
	)

	var insufficient *InsufficientEvidenceError
	if !errors.As(err, &insufficient) {
		t.Fatalf("error = %T %v, want *InsufficientEvidenceError", err, err)
	}
	if insufficient.Status != AnalysisStatusInsufficientEvidence ||
		insufficient.Analysis != "publication_trend" {
		t.Fatalf("InsufficientEvidenceError = %#v", insufficient)
	}
	wantReasons := []string{
		"baseline_paper_count",
		"independent_journal_count",
		"independent_team_count",
		"recent_paper_count",
	}
	if !reflect.DeepEqual(insufficient.Reasons, wantReasons) {
		t.Fatalf("Reasons = %v, want %v", insufficient.Reasons, wantReasons)
	}
	if result.Status != AnalysisStatusInsufficientEvidence ||
		result.InsufficientEvidence == nil {
		t.Fatalf("result = %#v, want typed insufficient evidence payload", result)
	}
}

func TestPublicationTrendsApplyDeterministicBenjaminiHochbergCorrection(t *testing.T) {
	t.Parallel()

	window := func(recent, baseline int) PublicationTrendInput {
		return PublicationTrendInput{
			Recent: TrendWindow{
				Start:      time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
				End:        time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
				PaperCount: recent,
			},
			Baseline: TrendWindow{
				Start:      time.Date(2025, 5, 7, 0, 0, 0, 0, time.UTC),
				End:        time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC),
				PaperCount: baseline,
			},
			IndependentJournalCount: 5,
			IndependentTeamCount:    10,
		}
	}
	alpha := window(35, 70)
	alpha.ID = "alpha"
	beta := window(10, 65)
	beta.ID = "beta"
	gamma := window(18, 90)
	gamma.ID = "gamma"

	results, err := AnalyzePublicationTrends(
		[]PublicationTrendInput{gamma, beta, alpha},
		validTrendPolicy(TrendModelSelection{
			Rule: TrendModelSelectionFixedPoisson,
		}),
	)
	if err != nil {
		t.Fatalf("AnalyzePublicationTrends() error = %v", err)
	}
	if got := []string{results[0].ID, results[1].ID, results[2].ID}; !reflect.DeepEqual(
		got,
		[]string{"alpha", "beta", "gamma"},
	) {
		t.Fatalf("result IDs = %v, want stable ID order", got)
	}
	assertClose(t, "alpha adjusted", results[0].RateRatio.AdjustedPValue, 3.735050196650368e-08, 1e-19)
	assertClose(t, "beta adjusted", results[1].RateRatio.AdjustedPValue, 1, 1e-15)
	assertClose(t, "gamma adjusted", results[2].RateRatio.AdjustedPValue, 0.46434949382663385, 1e-15)
}

func validTrendPolicy(selection TrendModelSelection) PublicationTrendPolicy {
	return PublicationTrendPolicy{
		FormulaVersion:             PublicationTrendFormulaVersion,
		ConfidenceLevel:            0.95,
		MinimumPapersPerWindow:     5,
		MinimumIndependentJournals: 2,
		MinimumIndependentTeams:    2,
		ModelSelection:             selection,
	}
}

func assertClose(t *testing.T, label string, got, want, tolerance float64) {
	t.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > tolerance {
		t.Fatalf("%s = %.17g, want %.17g ± %.3g", label, got, want, tolerance)
	}
}
