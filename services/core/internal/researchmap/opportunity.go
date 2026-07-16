package researchmap

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

type Status string

const (
	StatusWorthPursuing        Status = "worth_pursuing"
	StatusProceedWithCaution   Status = "proceed_with_caution"
	StatusNotRecommendedNow    Status = "not_recommended_now"
	StatusInsufficientEvidence Status = "insufficient_evidence"
)

func (status Status) Valid() bool {
	switch status {
	case StatusWorthPursuing,
		StatusProceedWithCaution,
		StatusNotRecommendedNow,
		StatusInsufficientEvidence:
		return true
	default:
		return false
	}
}

type Signal string

const (
	SignalEvidencePaperIDs   Signal = "evidence_paper_ids"
	SignalGrowthWindow       Signal = "growth_window"
	SignalGrowthScore        Signal = "growth_score"
	SignalCompetitionDensity Signal = "competition_density"
	SignalDataAvailability   Signal = "data_availability"
	SignalReproducibility    Signal = "reproducibility"
	SignalConfidence         Signal = "confidence"
)

func (signal Signal) Valid() bool {
	switch signal {
	case SignalEvidencePaperIDs,
		SignalGrowthWindow,
		SignalGrowthScore,
		SignalCompetitionDensity,
		SignalDataAvailability,
		SignalReproducibility,
		SignalConfidence:
		return true
	default:
		return false
	}
}

var (
	ErrInvalidPolicy     = errors.New("research opportunity policy is invalid")
	ErrInvalidAssessment = errors.New("research opportunity assessment is invalid")
)

type TopicSummary struct {
	ID   string
	Name string
}

type Window struct {
	Start time.Time
	End   time.Time
}

type Opportunity struct {
	Status             Status
	Topic              TopicSummary
	EvidencePaperIDs   []uuid.UUID
	GrowthWindow       Window
	GrowthScore        decimal.Decimal
	CompetitionDensity decimal.Decimal
	DataAvailability   decimal.Decimal
	Reproducibility    decimal.Decimal
	Limitations        []string
	FormulaVersion     string
	GeneratedAt        time.Time
	Coverage           decimal.Decimal
	MissingSignals     []Signal
	Confidence         decimal.Decimal
}

type AssessmentInput struct {
	Topic              TopicSummary
	EvidencePaperIDs   []uuid.UUID
	GrowthWindow       Window
	GrowthScore        decimal.Decimal
	CompetitionDensity decimal.Decimal
	DataAvailability   decimal.Decimal
	Reproducibility    decimal.Decimal
	Limitations        []string
	GeneratedAt        time.Time
	Coverage           decimal.Decimal
	MissingSignals     []Signal
	Confidence         decimal.Decimal
}

type WorthPursuingThresholds struct {
	MinimumGrowthScore        decimal.Decimal
	MaximumCompetitionDensity decimal.Decimal
	MinimumDataAvailability   decimal.Decimal
	MinimumReproducibility    decimal.Decimal
	MinimumConfidence         decimal.Decimal
}

type NotRecommendedNowThresholds struct {
	MaximumGrowthScore        decimal.Decimal
	MinimumCompetitionDensity decimal.Decimal
	MaximumDataAvailability   decimal.Decimal
	MaximumReproducibility    decimal.Decimal
	MinimumConfidence         decimal.Decimal
}

type Policy struct {
	Version           string
	FormulaVersion    string
	MinimumCoverage   decimal.Decimal
	RequiredSignals   []Signal
	WorthPursuing     WorthPursuingThresholds
	NotRecommendedNow NotRecommendedNowThresholds
}

func Evaluate(input AssessmentInput, policy Policy) (Opportunity, error) {
	if err := policy.Validate(); err != nil {
		return Opportunity{}, err
	}
	if err := validateAssessment(input); err != nil {
		return Opportunity{}, err
	}

	evidenceIDs := normalizedEvidenceIDs(input.EvidencePaperIDs)
	missingSignals := append([]Signal(nil), input.MissingSignals...)
	if len(evidenceIDs) == 0 {
		missingSignals = append(missingSignals, SignalEvidencePaperIDs)
	}
	missingSignals = normalizedSignals(missingSignals)

	status := StatusProceedWithCaution
	if input.Coverage.LessThan(policy.MinimumCoverage) ||
		hasMissingRequiredSignal(missingSignals, policy.RequiredSignals) {
		status = StatusInsufficientEvidence
	} else if meetsWorthPursuing(input, policy.WorthPursuing) {
		status = StatusWorthPursuing
	} else if meetsNotRecommendedNow(input, policy.NotRecommendedNow) {
		status = StatusNotRecommendedNow
	}

	return Opportunity{
		Status:             status,
		Topic:              input.Topic,
		EvidencePaperIDs:   evidenceIDs,
		GrowthWindow:       input.GrowthWindow,
		GrowthScore:        input.GrowthScore,
		CompetitionDensity: input.CompetitionDensity,
		DataAvailability:   input.DataAvailability,
		Reproducibility:    input.Reproducibility,
		Limitations:        normalizedStrings(input.Limitations),
		FormulaVersion:     policy.FormulaVersion,
		GeneratedAt:        input.GeneratedAt.UTC(),
		Coverage:           input.Coverage,
		MissingSignals:     missingSignals,
		Confidence:         input.Confidence,
	}, nil
}

func (policy Policy) Validate() error {
	if strings.TrimSpace(policy.Version) == "" ||
		policy.Version != strings.TrimSpace(policy.Version) {
		return fmt.Errorf("%w: version is required and must be trimmed", ErrInvalidPolicy)
	}
	if strings.TrimSpace(policy.FormulaVersion) == "" ||
		policy.FormulaVersion != strings.TrimSpace(policy.FormulaVersion) {
		return fmt.Errorf("%w: formula version is required and must be trimmed", ErrInvalidPolicy)
	}
	if !inUnitInterval(policy.MinimumCoverage) {
		return fmt.Errorf("%w: minimum coverage must be between zero and one", ErrInvalidPolicy)
	}
	if err := validateRequiredSignals(policy.RequiredSignals); err != nil {
		return err
	}
	if !inUnitInterval(policy.WorthPursuing.MaximumCompetitionDensity) ||
		!inUnitInterval(policy.WorthPursuing.MinimumDataAvailability) ||
		!inUnitInterval(policy.WorthPursuing.MinimumReproducibility) ||
		!inUnitInterval(policy.WorthPursuing.MinimumConfidence) {
		return fmt.Errorf("%w: worth-pursuing normalized thresholds must be between zero and one", ErrInvalidPolicy)
	}
	if !inUnitInterval(policy.NotRecommendedNow.MinimumCompetitionDensity) ||
		!inUnitInterval(policy.NotRecommendedNow.MaximumDataAvailability) ||
		!inUnitInterval(policy.NotRecommendedNow.MaximumReproducibility) ||
		!inUnitInterval(policy.NotRecommendedNow.MinimumConfidence) {
		return fmt.Errorf("%w: not-recommended normalized thresholds must be between zero and one", ErrInvalidPolicy)
	}
	if !conclusiveRegionsSeparated(policy) {
		return fmt.Errorf("%w: positive and negative policy regions overlap", ErrInvalidPolicy)
	}
	return nil
}

func validateAssessment(input AssessmentInput) error {
	if strings.TrimSpace(input.Topic.ID) == "" ||
		input.Topic.ID != strings.TrimSpace(input.Topic.ID) {
		return fmt.Errorf("%w: topic ID is required and must be trimmed", ErrInvalidAssessment)
	}
	if strings.TrimSpace(input.Topic.Name) == "" ||
		input.Topic.Name != strings.TrimSpace(input.Topic.Name) {
		return fmt.Errorf("%w: topic name is required and must be trimmed", ErrInvalidAssessment)
	}
	if input.GeneratedAt.IsZero() {
		return fmt.Errorf("%w: generated time is required", ErrInvalidAssessment)
	}
	if !inUnitInterval(input.Coverage) ||
		!inUnitInterval(input.CompetitionDensity) ||
		!inUnitInterval(input.DataAvailability) ||
		!inUnitInterval(input.Reproducibility) ||
		!inUnitInterval(input.Confidence) {
		return fmt.Errorf("%w: normalized assessment values must be between zero and one", ErrInvalidAssessment)
	}
	for _, id := range input.EvidencePaperIDs {
		if id == uuid.Nil {
			return fmt.Errorf("%w: evidence paper IDs cannot contain nil UUID", ErrInvalidAssessment)
		}
	}
	for _, signal := range input.MissingSignals {
		if !signal.Valid() {
			return fmt.Errorf("%w: unknown missing signal %q", ErrInvalidAssessment, signal)
		}
	}
	if !containsSignal(input.MissingSignals, SignalGrowthWindow) {
		if input.GrowthWindow.Start.IsZero() ||
			input.GrowthWindow.End.IsZero() ||
			!input.GrowthWindow.Start.Before(input.GrowthWindow.End) {
			return fmt.Errorf("%w: growth window must have ordered nonzero boundaries", ErrInvalidAssessment)
		}
	}
	return nil
}

func validateRequiredSignals(signals []Signal) error {
	required := map[Signal]struct{}{
		SignalEvidencePaperIDs:   {},
		SignalGrowthWindow:       {},
		SignalGrowthScore:        {},
		SignalCompetitionDensity: {},
		SignalDataAvailability:   {},
		SignalReproducibility:    {},
		SignalConfidence:         {},
	}
	seen := make(map[Signal]struct{}, len(signals))
	for _, signal := range signals {
		if !signal.Valid() {
			return fmt.Errorf("%w: unknown required signal %q", ErrInvalidPolicy, signal)
		}
		if _, exists := seen[signal]; exists {
			return fmt.Errorf("%w: duplicate required signal %q", ErrInvalidPolicy, signal)
		}
		seen[signal] = struct{}{}
	}
	if len(seen) != len(required) {
		return fmt.Errorf("%w: policy must declare every formula input as required", ErrInvalidPolicy)
	}
	for signal := range required {
		if _, exists := seen[signal]; !exists {
			return fmt.Errorf("%w: missing required signal %q", ErrInvalidPolicy, signal)
		}
	}
	return nil
}

func conclusiveRegionsSeparated(policy Policy) bool {
	return policy.WorthPursuing.MinimumGrowthScore.GreaterThan(
		policy.NotRecommendedNow.MaximumGrowthScore,
	) ||
		policy.WorthPursuing.MaximumCompetitionDensity.LessThan(
			policy.NotRecommendedNow.MinimumCompetitionDensity,
		) ||
		policy.WorthPursuing.MinimumDataAvailability.GreaterThan(
			policy.NotRecommendedNow.MaximumDataAvailability,
		) ||
		policy.WorthPursuing.MinimumReproducibility.GreaterThan(
			policy.NotRecommendedNow.MaximumReproducibility,
		)
}

func inUnitInterval(value decimal.Decimal) bool {
	return !value.IsNegative() && value.LessThanOrEqual(decimal.NewFromInt(1))
}

func containsSignal(signals []Signal, want Signal) bool {
	for _, signal := range signals {
		if signal == want {
			return true
		}
	}
	return false
}

func meetsWorthPursuing(input AssessmentInput, thresholds WorthPursuingThresholds) bool {
	return input.GrowthScore.GreaterThanOrEqual(thresholds.MinimumGrowthScore) &&
		input.CompetitionDensity.LessThanOrEqual(thresholds.MaximumCompetitionDensity) &&
		input.DataAvailability.GreaterThanOrEqual(thresholds.MinimumDataAvailability) &&
		input.Reproducibility.GreaterThanOrEqual(thresholds.MinimumReproducibility) &&
		input.Confidence.GreaterThanOrEqual(thresholds.MinimumConfidence)
}

func meetsNotRecommendedNow(
	input AssessmentInput,
	thresholds NotRecommendedNowThresholds,
) bool {
	return input.GrowthScore.LessThanOrEqual(thresholds.MaximumGrowthScore) &&
		input.CompetitionDensity.GreaterThanOrEqual(thresholds.MinimumCompetitionDensity) &&
		input.DataAvailability.LessThanOrEqual(thresholds.MaximumDataAvailability) &&
		input.Reproducibility.LessThanOrEqual(thresholds.MaximumReproducibility) &&
		input.Confidence.GreaterThanOrEqual(thresholds.MinimumConfidence)
}

func normalizedEvidenceIDs(values []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].String() < result[j].String()
	})
	return result
}

func normalizedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result
}

func normalizedSignals(values []Signal) []Signal {
	seen := make(map[Signal]struct{}, len(values))
	result := make([]Signal, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})
	if len(result) == 0 {
		return nil
	}
	return result
}

func hasMissingRequiredSignal(missing []Signal, required []Signal) bool {
	missingSet := make(map[Signal]struct{}, len(missing))
	for _, signal := range missing {
		missingSet[signal] = struct{}{}
	}
	for _, signal := range required {
		if _, exists := missingSet[signal]; exists {
			return true
		}
	}
	return false
}
