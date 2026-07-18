package venue

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type MatchedRule string

const (
	MatchedRuleAnyQ1 MatchedRule = "jcr_q1"
)

type PolicyDecision string

const (
	PolicyDecisionAccepted      PolicyDecision = "accepted"
	PolicyDecisionRejected      PolicyDecision = "rejected"
	PolicyDecisionUnknown       PolicyDecision = "unknown"
	PolicyDecisionNotApplicable PolicyDecision = "not_applicable"
)

type JournalPolicy struct {
	version string
}

func NewJournalPolicy(version string) (JournalPolicy, error) {
	normalizedVersion := strings.TrimSpace(version)
	if normalizedVersion == "" {
		return JournalPolicy{}, errors.New("journal policy version is required")
	}
	if normalizedVersion != JournalAllQ1PolicyVersion {
		return JournalPolicy{}, fmt.Errorf(
			"unsupported journal policy version %q",
			normalizedVersion,
		)
	}
	return JournalPolicy{version: normalizedVersion}, nil
}

func (policy JournalPolicy) Version() string {
	return policy.version
}

type CategoryEvidence struct {
	snapshot MetricSnapshot
}

func (evidence CategoryEvidence) VenueID() string {
	return evidence.snapshot.VenueID()
}

func (evidence CategoryEvidence) MetricYear() int {
	return evidence.snapshot.MetricYear()
}

func (evidence CategoryEvidence) RegistryVersion() MetricRegistryVersion {
	return evidence.snapshot.RegistryVersion()
}

func (evidence CategoryEvidence) EditionYear() (int, bool) {
	return evidence.snapshot.EditionYear()
}

func (evidence CategoryEvidence) Category() string {
	return evidence.snapshot.Category()
}

func (evidence CategoryEvidence) HasJIF() bool {
	return evidence.snapshot.HasJIF()
}

func (evidence CategoryEvidence) JIF() Decimal {
	return evidence.snapshot.JIF()
}

func (evidence CategoryEvidence) Quartile() Quartile {
	return evidence.snapshot.Quartile()
}

func (evidence CategoryEvidence) JIFRank() (int, bool) {
	return evidence.snapshot.JIFRank()
}

func (evidence CategoryEvidence) CategoryJournalCount() (int, bool) {
	return evidence.snapshot.CategoryJournalCount()
}

func (evidence CategoryEvidence) JIFPercentile() (Decimal, bool) {
	return evidence.snapshot.JIFPercentile()
}

func (evidence CategoryEvidence) Status() MetricStatus {
	return evidence.snapshot.Status()
}

func (evidence CategoryEvidence) Source() string {
	return evidence.snapshot.Source()
}

type PolicyResult struct {
	policyVersion    string
	metricYear       int
	decision         PolicyDecision
	matchedRules     []MatchedRule
	categoryEvidence []CategoryEvidence
	evaluatedAt      time.Time
}

func (result PolicyResult) PolicyVersion() string {
	return result.policyVersion
}

func (result PolicyResult) MetricYear() int {
	return result.metricYear
}

func (result PolicyResult) Decision() PolicyDecision {
	return result.decision
}

func (result PolicyResult) MatchedRules() []MatchedRule {
	return slices.Clone(result.matchedRules)
}

func (result PolicyResult) CategoryEvidence() []CategoryEvidence {
	return slices.Clone(result.categoryEvidence)
}

func (result PolicyResult) EvaluatedAt() time.Time {
	return result.evaluatedAt
}

func (policy JournalPolicy) Evaluate(
	item Venue,
	metricYear int,
	metrics []MetricSnapshot,
	evaluatedAt time.Time,
) (PolicyResult, error) {
	if policy.version != JournalAllQ1PolicyVersion {
		return PolicyResult{}, errors.New("journal policy is invalid")
	}
	if metricYear < 1900 || metricYear > 3000 {
		return PolicyResult{}, fmt.Errorf("metric_year %d is outside 1900..3000", metricYear)
	}
	if evaluatedAt.IsZero() {
		return PolicyResult{}, errors.New("policy evaluated_at is required")
	}
	if strings.TrimSpace(item.ID()) == "" || !item.Type().Valid() {
		return PolicyResult{}, errors.New("policy requires a valid venue")
	}

	base := PolicyResult{
		policyVersion: policy.version,
		metricYear:    metricYear,
		evaluatedAt:   evaluatedAt.UTC(),
	}
	if item.Type() != VenueTypeJournal {
		base.decision = PolicyDecisionNotApplicable
		return base, nil
	}

	evidence := make([]CategoryEvidence, 0, len(metrics))
	hasUnknown := len(metrics) == 0
	matchedQ1 := false
	for index, metric := range metrics {
		if metric.VenueID() != item.ID() {
			return PolicyResult{}, fmt.Errorf(
				"metric evidence row %d belongs to venue %q, want %q",
				index,
				metric.VenueID(),
				item.ID(),
			)
		}
		if metric.MetricYear() != metricYear {
			return PolicyResult{}, fmt.Errorf(
				"metric evidence row %d has metric_year %d, want %d",
				index,
				metric.MetricYear(),
				metricYear,
			)
		}
		evidence = append(evidence, CategoryEvidence{snapshot: metric})
		if metric.Status() == MetricStatusUnknown {
			hasUnknown = true
			continue
		}
		if !metric.CompleteKnownEvidence() {
			hasUnknown = true
			continue
		}
		if metric.Quartile() == QuartileQ1 {
			matchedQ1 = true
		}
	}
	base.categoryEvidence = evidence
	if matchedQ1 {
		base.matchedRules = append(base.matchedRules, MatchedRuleAnyQ1)
	}
	switch {
	case len(base.matchedRules) > 0:
		base.decision = PolicyDecisionAccepted
	case hasUnknown:
		base.decision = PolicyDecisionUnknown
	default:
		base.decision = PolicyDecisionRejected
	}
	return base, nil
}
