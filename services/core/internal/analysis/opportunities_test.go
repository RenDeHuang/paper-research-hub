package analysis

import (
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestOpportunityAllowsExactlyFivePredefinedRules(t *testing.T) {
	t.Parallel()

	want := []OpportunityRule{
		OpportunityRuleRapidGrowthLowRCTShare,
		OpportunityRuleSingleCenterExternalValidationGap,
		OpportunityRuleHighCitationLowOpenData,
		OpportunityRuleObservationalDominance,
		OpportunityRuleEmergingMethodLowIndependentTeamCount,
	}
	if got := PredefinedOpportunityRules(); !reflect.DeepEqual(got, want) {
		t.Fatalf("PredefinedOpportunityRules() = %v, want %v", got, want)
	}

	_, err := EvaluateOpportunity(OpportunityInput{
		Rule: "custom_rule",
	}, OpportunityRuleSetVersion)
	var invalid *InvalidInputError
	if !errors.As(err, &invalid) || invalid.Field != "rule" {
		t.Fatalf("unknown rule error = %T %v, want rule InvalidInputError", err, err)
	}
}

func TestOpportunityEvaluatesEachPredefinedRuleDeterministically(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rule      OpportunityRule
		estimates []OpportunityEstimate
	}{
		{
			name: "rapid growth and low randomized-trial share",
			rule: OpportunityRuleRapidGrowthLowRCTShare,
			estimates: []OpportunityEstimate{
				opportunityEstimate(OpportunityMetricTrendRateRatio, 1.8, 1.3, 2.4),
				opportunityEstimate(OpportunityMetricRCTShare, 0.1, 0.06, 0.15),
			},
		},
		{
			name: "single-center evidence and external-validation gap",
			rule: OpportunityRuleSingleCenterExternalValidationGap,
			estimates: []OpportunityEstimate{
				opportunityEstimate(OpportunityMetricSingleCenterShare, 0.75, 0.65, 0.82),
				opportunityEstimate(OpportunityMetricExternalValidationShare, 0.08, 0.04, 0.14),
			},
		},
		{
			name: "high citation and low open-data share",
			rule: OpportunityRuleHighCitationLowOpenData,
			estimates: []OpportunityEstimate{
				opportunityEstimate(OpportunityMetricCitationPercentile, 0.9, 0.82, 0.95),
				opportunityEstimate(OpportunityMetricOpenDataShare, 0.1, 0.05, 0.2),
			},
		},
		{
			name: "observational dominance",
			rule: OpportunityRuleObservationalDominance,
			estimates: []OpportunityEstimate{
				opportunityEstimate(OpportunityMetricObservationalShare, 0.84, 0.76, 0.9),
			},
		},
		{
			name: "emerging method with few independent teams",
			rule: OpportunityRuleEmergingMethodLowIndependentTeamCount,
			estimates: []OpportunityEstimate{
				opportunityEstimate(OpportunityMetricTrendRateRatio, 1.7, 1.2, 2.3),
				opportunityEstimate(OpportunityMetricIndependentTeamCount, 2, 1, 2.8),
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := EvaluateOpportunity(
				validOpportunityInput(tt.rule, tt.estimates),
				OpportunityRuleSetVersion,
			)
			if err != nil {
				t.Fatalf("EvaluateOpportunity() error = %v", err)
			}
			if result.Status != OpportunityStatusTriggered {
				t.Fatalf("Status = %q, want %q", result.Status, OpportunityStatusTriggered)
			}
			if result.Rule != tt.rule || result.RuleVersion != OpportunityRuleSetVersion {
				t.Fatalf("rule/version = %q/%q", result.Rule, result.RuleVersion)
			}
			if len(result.Estimates) != len(tt.estimates) {
				t.Fatalf("Estimates = %#v", result.Estimates)
			}
			for _, estimate := range result.Estimates {
				if estimate.ConfidenceInterval.Level != 0.95 {
					t.Fatalf("estimate uncertainty = %#v", estimate.ConfidenceInterval)
				}
			}
		})
	}
}

func TestOpportunityPayloadNormalizesEvidenceAndCarriesCoverageAndLimitations(t *testing.T) {
	t.Parallel()

	first := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	second := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	third := uuid.MustParse("00000000-0000-0000-0000-000000000003")
	fourth := uuid.MustParse("00000000-0000-0000-0000-000000000004")
	fifth := uuid.MustParse("00000000-0000-0000-0000-000000000005")

	input := validOpportunityInput(
		OpportunityRuleObservationalDominance,
		[]OpportunityEstimate{
			opportunityEstimate(OpportunityMetricObservationalShare, 0.84, 0.76, 0.9),
		},
	)
	input.SupportingWorkIDs = []uuid.UUID{fifth, second, first, third, fourth, second}
	input.Limitations = []string{
		" publication-type indexing lag ",
		"residual field heterogeneity",
		"residual field heterogeneity",
	}

	result, err := EvaluateOpportunity(input, OpportunityRuleSetVersion)
	if err != nil {
		t.Fatalf("EvaluateOpportunity() error = %v", err)
	}
	if !reflect.DeepEqual(
		result.SupportingWorkIDs,
		[]uuid.UUID{first, second, third, fourth, fifth},
	) {
		t.Fatalf("SupportingWorkIDs = %v", result.SupportingWorkIDs)
	}
	if !reflect.DeepEqual(
		result.Limitations,
		[]string{"publication-type indexing lag", "residual field heterogeneity"},
	) {
		t.Fatalf("Limitations = %v", result.Limitations)
	}
	if result.Coverage.Covered != 90 || result.Coverage.Eligible != 100 {
		t.Fatalf("Coverage = %#v", result.Coverage)
	}
	assertClose(t, "coverage", result.Coverage.Proportion, 0.9, 1e-15)
}

func TestOpportunityUsesTypedInsufficientEvidenceAndDoesNotTriggerFromPointEstimateAlone(t *testing.T) {
	t.Parallel()

	sparse := validOpportunityInput(
		OpportunityRuleObservationalDominance,
		[]OpportunityEstimate{
			opportunityEstimate(OpportunityMetricObservationalShare, 0.84, 0.76, 0.9),
		},
	)
	sparse.SupportingWorkIDs = sparse.SupportingWorkIDs[:4]
	sparse.CoveredPaperCount = 79

	result, err := EvaluateOpportunity(sparse, OpportunityRuleSetVersion)
	if err != nil {
		t.Fatalf("EvaluateOpportunity(sparse) error = %v", err)
	}
	if result.Status != OpportunityStatusInsufficientEvidence {
		t.Fatalf("sparse Status = %q", result.Status)
	}
	if !reflect.DeepEqual(
		result.InsufficientEvidence.Reasons,
		[]string{"coverage", "supporting_work_count"},
	) {
		t.Fatalf("sparse reasons = %v", result.InsufficientEvidence.Reasons)
	}

	uncertain := validOpportunityInput(
		OpportunityRuleRapidGrowthLowRCTShare,
		[]OpportunityEstimate{
			opportunityEstimate(OpportunityMetricTrendRateRatio, 1.8, 0.9, 2.8),
			opportunityEstimate(OpportunityMetricRCTShare, 0.1, 0.04, 0.3),
		},
	)
	notTriggered, err := EvaluateOpportunity(uncertain, OpportunityRuleSetVersion)
	if err != nil {
		t.Fatalf("EvaluateOpportunity(uncertain) error = %v", err)
	}
	if notTriggered.Status != OpportunityStatusNotTriggered {
		t.Fatalf("uncertain Status = %q, want %q", notTriggered.Status, OpportunityStatusNotTriggered)
	}
}

func validOpportunityInput(
	rule OpportunityRule,
	estimates []OpportunityEstimate,
) OpportunityInput {
	workIDs := make([]uuid.UUID, 5)
	for index := range workIDs {
		workIDs[index] = uuid.MustParse(
			"00000000-0000-0000-0000-00000000000" + string(rune('1'+index)),
		)
	}
	return OpportunityInput{
		Rule:               rule,
		EntityID:           "entity:example",
		SupportingWorkIDs:  workIDs,
		Estimates:          estimates,
		CoveredPaperCount:  90,
		EligiblePaperCount: 100,
		Limitations:        []string{"observational source classification"},
	}
}

func opportunityEstimate(
	metric OpportunityMetric,
	value, lower, upper float64,
) OpportunityEstimate {
	return OpportunityEstimate{
		Metric: metric,
		Value:  value,
		ConfidenceInterval: ConfidenceInterval{
			Level: 0.95,
			Lower: lower,
			Upper: upper,
		},
	}
}
