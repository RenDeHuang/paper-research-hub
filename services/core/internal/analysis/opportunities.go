package analysis

import (
	"bytes"
	"sort"

	"github.com/google/uuid"
)

const (
	OpportunityFormulaVersion = "biomedical-opportunities/v1"
	OpportunityRuleSetVersion = "biomedical-opportunity-rules/v1"
)

const (
	opportunityMinimumSupportingWorks = 5
	opportunityMinimumCoverage        = 0.8
)

type OpportunityRule string

const (
	OpportunityRuleRapidGrowthLowRCTShare                OpportunityRule = "rapid_growth_low_rct_share"
	OpportunityRuleSingleCenterExternalValidationGap     OpportunityRule = "single_center_external_validation_gap"
	OpportunityRuleHighCitationLowOpenData               OpportunityRule = "high_citation_low_open_data"
	OpportunityRuleObservationalDominance                OpportunityRule = "observational_dominance"
	OpportunityRuleEmergingMethodLowIndependentTeamCount OpportunityRule = "emerging_method_low_independent_team_count"
)

type OpportunityMetric string

const (
	OpportunityMetricTrendRateRatio          OpportunityMetric = "trend_rate_ratio"
	OpportunityMetricRCTShare                OpportunityMetric = "rct_share"
	OpportunityMetricSingleCenterShare       OpportunityMetric = "single_center_share"
	OpportunityMetricExternalValidationShare OpportunityMetric = "external_validation_share"
	OpportunityMetricCitationPercentile      OpportunityMetric = "citation_percentile"
	OpportunityMetricOpenDataShare           OpportunityMetric = "open_data_share"
	OpportunityMetricObservationalShare      OpportunityMetric = "observational_share"
	OpportunityMetricIndependentTeamCount    OpportunityMetric = "independent_team_count"
)

type OpportunityStatus string

const (
	OpportunityStatusTriggered            OpportunityStatus = "triggered"
	OpportunityStatusNotTriggered         OpportunityStatus = "not_triggered"
	OpportunityStatusInsufficientEvidence OpportunityStatus = "insufficient_evidence"
)

type OpportunityEstimate struct {
	Metric             OpportunityMetric
	Value              float64
	ConfidenceInterval ConfidenceInterval
}

type OpportunityInput struct {
	Rule               OpportunityRule
	EntityID           string
	SupportingWorkIDs  []uuid.UUID
	Estimates          []OpportunityEstimate
	CoveredPaperCount  int
	EligiblePaperCount int
	Limitations        []string
}

type ResearchOpportunity struct {
	Rule                 OpportunityRule
	RuleVersion          string
	EntityID             string
	SupportingWorkIDs    []uuid.UUID
	Estimates            []OpportunityEstimate
	Coverage             Coverage
	Limitations          []string
	Status               OpportunityStatus
	InsufficientEvidence *InsufficientEvidence
}

type opportunityRuleDefinition struct {
	requiredMetrics []OpportunityMetric
	trigger         func(map[OpportunityMetric]OpportunityEstimate) bool
}

var opportunityRuleDefinitions = map[OpportunityRule]opportunityRuleDefinition{
	OpportunityRuleRapidGrowthLowRCTShare: {
		requiredMetrics: []OpportunityMetric{
			OpportunityMetricTrendRateRatio,
			OpportunityMetricRCTShare,
		},
		trigger: func(estimates map[OpportunityMetric]OpportunityEstimate) bool {
			return estimates[OpportunityMetricTrendRateRatio].
				ConfidenceInterval.Lower > 1 &&
				estimates[OpportunityMetricRCTShare].
					ConfidenceInterval.Upper <= 0.2
		},
	},
	OpportunityRuleSingleCenterExternalValidationGap: {
		requiredMetrics: []OpportunityMetric{
			OpportunityMetricSingleCenterShare,
			OpportunityMetricExternalValidationShare,
		},
		trigger: func(estimates map[OpportunityMetric]OpportunityEstimate) bool {
			return estimates[OpportunityMetricSingleCenterShare].
				ConfidenceInterval.Lower >= 0.6 &&
				estimates[OpportunityMetricExternalValidationShare].
					ConfidenceInterval.Upper <= 0.2
		},
	},
	OpportunityRuleHighCitationLowOpenData: {
		requiredMetrics: []OpportunityMetric{
			OpportunityMetricCitationPercentile,
			OpportunityMetricOpenDataShare,
		},
		trigger: func(estimates map[OpportunityMetric]OpportunityEstimate) bool {
			return estimates[OpportunityMetricCitationPercentile].
				ConfidenceInterval.Lower >= 0.75 &&
				estimates[OpportunityMetricOpenDataShare].
					ConfidenceInterval.Upper <= 0.25
		},
	},
	OpportunityRuleObservationalDominance: {
		requiredMetrics: []OpportunityMetric{
			OpportunityMetricObservationalShare,
		},
		trigger: func(estimates map[OpportunityMetric]OpportunityEstimate) bool {
			return estimates[OpportunityMetricObservationalShare].
				ConfidenceInterval.Lower >= 0.7
		},
	},
	OpportunityRuleEmergingMethodLowIndependentTeamCount: {
		requiredMetrics: []OpportunityMetric{
			OpportunityMetricTrendRateRatio,
			OpportunityMetricIndependentTeamCount,
		},
		trigger: func(estimates map[OpportunityMetric]OpportunityEstimate) bool {
			return estimates[OpportunityMetricTrendRateRatio].
				ConfidenceInterval.Lower > 1 &&
				estimates[OpportunityMetricIndependentTeamCount].
					ConfidenceInterval.Upper < 3
		},
	},
}

func PredefinedOpportunityRules() []OpportunityRule {
	return []OpportunityRule{
		OpportunityRuleRapidGrowthLowRCTShare,
		OpportunityRuleSingleCenterExternalValidationGap,
		OpportunityRuleHighCitationLowOpenData,
		OpportunityRuleObservationalDominance,
		OpportunityRuleEmergingMethodLowIndependentTeamCount,
	}
}

func EvaluateOpportunity(
	input OpportunityInput,
	ruleVersion string,
) (ResearchOpportunity, error) {
	if ruleVersion != OpportunityRuleSetVersion {
		return ResearchOpportunity{}, &InvalidInputError{
			Field:  "rule_version",
			Reason: "must equal " + OpportunityRuleSetVersion,
		}
	}
	definition, exists := opportunityRuleDefinitions[input.Rule]
	if !exists {
		return ResearchOpportunity{}, &InvalidInputError{
			Field:  "rule",
			Reason: "is not one of the five predefined rules",
		}
	}
	if err := validateTrimmed("entity_id", input.EntityID); err != nil {
		return ResearchOpportunity{}, err
	}
	coverage, err := calculateCoverage(
		input.CoveredPaperCount,
		input.EligiblePaperCount,
	)
	if err != nil {
		return ResearchOpportunity{}, err
	}
	workIDs, err := normalizedWorkIDs(input.SupportingWorkIDs)
	if err != nil {
		return ResearchOpportunity{}, err
	}
	limitations := normalizedStrings(input.Limitations)
	if len(limitations) == 0 {
		return ResearchOpportunity{}, &InvalidInputError{
			Field:  "limitations",
			Reason: "must contain at least one explicit limitation",
		}
	}
	estimates, byMetric, err := validatedOpportunityEstimates(
		input.Estimates,
		definition.requiredMetrics,
	)
	if err != nil {
		return ResearchOpportunity{}, err
	}

	result := ResearchOpportunity{
		Rule:              input.Rule,
		RuleVersion:       ruleVersion,
		EntityID:          input.EntityID,
		SupportingWorkIDs: workIDs,
		Estimates:         estimates,
		Coverage:          coverage,
		Limitations:       limitations,
		Status:            OpportunityStatusNotTriggered,
	}
	reasons := make([]string, 0, 2)
	if len(workIDs) < opportunityMinimumSupportingWorks {
		reasons = append(reasons, "supporting_work_count")
	}
	if coverage.Proportion < opportunityMinimumCoverage {
		reasons = append(reasons, "coverage")
	}
	if len(reasons) != 0 {
		evidence, _ := newInsufficientEvidence("research_opportunity", reasons)
		result.Status = OpportunityStatusInsufficientEvidence
		result.InsufficientEvidence = evidence
		return result, nil
	}
	if definition.trigger(byMetric) {
		result.Status = OpportunityStatusTriggered
	}
	return result, nil
}

func normalizedWorkIDs(values []uuid.UUID) ([]uuid.UUID, error) {
	seen := make(map[uuid.UUID]struct{}, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			return nil, &InvalidInputError{
				Field:  "supporting_work_ids",
				Reason: "cannot contain nil UUID",
			}
		}
		seen[value] = struct{}{}
	}
	result := make([]uuid.UUID, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool {
		return bytes.Compare(result[left][:], result[right][:]) < 0
	})
	return result, nil
}

func validatedOpportunityEstimates(
	values []OpportunityEstimate,
	required []OpportunityMetric,
) (
	[]OpportunityEstimate,
	map[OpportunityMetric]OpportunityEstimate,
	error,
) {
	allowed := make(map[OpportunityMetric]struct{}, len(required))
	for _, metric := range required {
		allowed[metric] = struct{}{}
	}
	byMetric := make(map[OpportunityMetric]OpportunityEstimate, len(values))
	for _, estimate := range values {
		if _, exists := allowed[estimate.Metric]; !exists {
			return nil, nil, &InvalidInputError{
				Field:  "estimates.metric",
				Reason: "is not declared for this predefined rule",
			}
		}
		if _, exists := byMetric[estimate.Metric]; exists {
			return nil, nil, &InvalidInputError{
				Field:  "estimates.metric",
				Reason: "must be unique",
			}
		}
		if err := validateOpportunityEstimate(estimate); err != nil {
			return nil, nil, err
		}
		byMetric[estimate.Metric] = estimate
	}
	for _, metric := range required {
		if _, exists := byMetric[metric]; !exists {
			return nil, nil, &InvalidInputError{
				Field:  "estimates",
				Reason: "is missing required metric " + string(metric),
			}
		}
	}

	result := append([]OpportunityEstimate(nil), values...)
	sort.Slice(result, func(left, right int) bool {
		return result[left].Metric < result[right].Metric
	})
	return result, byMetric, nil
}

func validateOpportunityEstimate(estimate OpportunityEstimate) error {
	interval := estimate.ConfidenceInterval
	if interval.Level != confidenceLevel95 {
		return &InvalidInputError{
			Field:  "estimates.confidence_interval.level",
			Reason: "must equal 0.95",
		}
	}
	if !nonNegativeFinite(estimate.Value) ||
		!nonNegativeFinite(interval.Lower) ||
		!nonNegativeFinite(interval.Upper) ||
		interval.Lower > estimate.Value ||
		estimate.Value > interval.Upper {
		return &InvalidInputError{
			Field:  "estimates.confidence_interval",
			Reason: "must be finite, ordered, and contain the estimate",
		}
	}
	switch estimate.Metric {
	case OpportunityMetricRCTShare,
		OpportunityMetricSingleCenterShare,
		OpportunityMetricExternalValidationShare,
		OpportunityMetricCitationPercentile,
		OpportunityMetricOpenDataShare,
		OpportunityMetricObservationalShare:
		if interval.Upper > 1 {
			return &InvalidInputError{
				Field:  "estimates",
				Reason: "share and percentile intervals must be within zero and one",
			}
		}
	case OpportunityMetricTrendRateRatio:
		if !positiveFinite(interval.Lower) {
			return &InvalidInputError{
				Field:  "estimates",
				Reason: "rate-ratio interval must be positive",
			}
		}
	case OpportunityMetricIndependentTeamCount:
	default:
		return &InvalidInputError{
			Field:  "estimates.metric",
			Reason: "is not a predefined opportunity metric",
		}
	}
	return nil
}
