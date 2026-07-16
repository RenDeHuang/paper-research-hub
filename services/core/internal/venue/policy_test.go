package venue

import (
	"slices"
	"testing"
	"time"
)

func TestJournalPolicyAcceptsHighJIFOrAnyQ1WithVersionedEvidence(t *testing.T) {
	t.Parallel()

	item := venueForTest(
		t,
		"venue-alpha",
		"Synthetic Journal",
		"1234-5679",
		"1234-5679",
		"2049-3630",
	)
	evaluatedAt := time.Date(2026, time.July, 16, 14, 0, 0, 0, time.UTC)
	policy, err := NewJournalPolicy("journal-jif-or-q1/v1")
	if err != nil {
		t.Fatalf("NewJournalPolicy() error = %v", err)
	}

	tests := []struct {
		name    string
		metrics []MetricSnapshot
		rules   []MatchedRule
	}{
		{
			name: "high JIF",
			metrics: []MetricSnapshot{
				mustMetricSnapshot(t, item.ID(), 2025, "AI", "10", QuartileQ2, MetricStatusKnown, "synthetic-jcr"),
			},
			rules: []MatchedRule{MatchedRuleJIFAtLeast10},
		},
		{
			name: "Q1",
			metrics: []MetricSnapshot{
				mustMetricSnapshot(t, item.ID(), 2025, "Robotics", "4.2", QuartileQ1, MetricStatusKnown, "synthetic-jcr"),
			},
			rules: []MatchedRule{MatchedRuleAnyQ1},
		},
		{
			name: "both rules across categories",
			metrics: []MetricSnapshot{
				mustMetricSnapshot(t, item.ID(), 2025, "AI", "10.1", QuartileQ2, MetricStatusKnown, "synthetic-jcr"),
				mustMetricSnapshot(t, item.ID(), 2025, "Robotics", "10.1", QuartileQ1, MetricStatusKnown, "synthetic-jcr"),
			},
			rules: []MatchedRule{MatchedRuleJIFAtLeast10, MatchedRuleAnyQ1},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := policy.Evaluate(item, 2025, tt.metrics, evaluatedAt)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if result.Decision() != PolicyDecisionAccepted {
				t.Fatalf("Decision() = %q, want accepted", result.Decision())
			}
			if result.PolicyVersion() != "journal-jif-or-q1/v1" ||
				result.MetricYear() != 2025 ||
				!result.EvaluatedAt().Equal(evaluatedAt) {
				t.Fatalf("versioned result metadata = %#v", result)
			}
			if got := result.MatchedRules(); !slices.Equal(got, tt.rules) {
				t.Fatalf("MatchedRules() = %#v, want %#v", got, tt.rules)
			}
			evidence := result.CategoryEvidence()
			if len(evidence) != len(tt.metrics) {
				t.Fatalf("CategoryEvidence() = %d rows, want %d", len(evidence), len(tt.metrics))
			}
			for index, category := range evidence {
				if category.Category() != tt.metrics[index].Category() ||
					category.Source() != "synthetic-jcr" ||
					category.MetricYear() != 2025 {
					t.Fatalf("category evidence %d = %#v, want source-backed row", index, category)
				}
			}
		})
	}
}

func TestJournalPolicyUsesThreeValuedUnknownSemantics(t *testing.T) {
	t.Parallel()

	item := venueForTest(
		t,
		"venue-alpha",
		"Synthetic Journal",
		"1234-5679",
		"1234-5679",
		"2049-3630",
	)
	policy, err := NewJournalPolicy("journal-jif-or-q1/v1")
	if err != nil {
		t.Fatalf("NewJournalPolicy() error = %v", err)
	}
	evaluatedAt := time.Date(2026, time.July, 16, 14, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		metrics  []MetricSnapshot
		decision PolicyDecision
	}{
		{name: "no rows", decision: PolicyDecisionUnknown},
		{
			name: "unknown row",
			metrics: []MetricSnapshot{
				mustMetricSnapshot(t, item.ID(), 2025, "AI", "", "", MetricStatusUnknown, "synthetic-jcr"),
			},
			decision: PolicyDecisionUnknown,
		},
		{
			name: "known reject plus unknown remains unknown",
			metrics: []MetricSnapshot{
				mustMetricSnapshot(t, item.ID(), 2025, "AI", "9.9", QuartileQ2, MetricStatusKnown, "synthetic-jcr"),
				mustMetricSnapshot(t, item.ID(), 2025, "Robotics", "", "", MetricStatusUnknown, "synthetic-jcr"),
			},
			decision: PolicyDecisionUnknown,
		},
		{
			name: "all known and no match is rejected",
			metrics: []MetricSnapshot{
				mustMetricSnapshot(t, item.ID(), 2025, "AI", "9.999999999999999999", QuartileQ2, MetricStatusKnown, "synthetic-jcr"),
				mustMetricSnapshot(t, item.ID(), 2025, "Robotics", "9.9", QuartileQ3, MetricStatusKnown, "synthetic-jcr"),
			},
			decision: PolicyDecisionRejected,
		},
		{
			name: "known acceptance dominates unknown",
			metrics: []MetricSnapshot{
				mustMetricSnapshot(t, item.ID(), 2025, "AI", "10.000000000000000000", QuartileQ2, MetricStatusKnown, "synthetic-jcr"),
				mustMetricSnapshot(t, item.ID(), 2025, "Robotics", "", "", MetricStatusUnknown, "synthetic-jcr"),
			},
			decision: PolicyDecisionAccepted,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := policy.Evaluate(item, 2025, tt.metrics, evaluatedAt)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if result.Decision() != tt.decision {
				t.Fatalf("Decision() = %q, want %q", result.Decision(), tt.decision)
			}
			if tt.decision != PolicyDecisionAccepted && len(result.MatchedRules()) != 0 {
				t.Fatalf("MatchedRules() = %#v for %q decision, want none", result.MatchedRules(), tt.decision)
			}
		})
	}
}

func TestJournalPolicyReturnsNotApplicableForNonJournals(t *testing.T) {
	t.Parallel()

	policy, err := NewJournalPolicy("journal-jif-or-q1/v1")
	if err != nil {
		t.Fatalf("NewJournalPolicy() error = %v", err)
	}
	evaluatedAt := time.Date(2026, time.July, 16, 14, 0, 0, 0, time.UTC)

	for _, venueType := range []VenueType{VenueTypeConference, VenueTypePreprint} {
		venueType := venueType
		t.Run(string(venueType), func(t *testing.T) {
			t.Parallel()

			alias, aliasErr := NewAlias("Synthetic Non-Journal", "fixture")
			if aliasErr != nil {
				t.Fatalf("NewAlias() error = %v", aliasErr)
			}
			item, venueErr := NewVenue("non-journal-"+string(venueType), venueType, ISSNSet{}, []Alias{alias})
			if venueErr != nil {
				t.Fatalf("NewVenue() error = %v", venueErr)
			}
			result, evaluateErr := policy.Evaluate(item, 2025, nil, evaluatedAt)
			if evaluateErr != nil {
				t.Fatalf("Evaluate() error = %v", evaluateErr)
			}
			if result.Decision() != PolicyDecisionNotApplicable {
				t.Fatalf("Decision() = %q, want not_applicable", result.Decision())
			}
			if result.PolicyVersion() != "journal-jif-or-q1/v1" ||
				result.MetricYear() != 2025 ||
				!result.EvaluatedAt().Equal(evaluatedAt) ||
				len(result.MatchedRules()) != 0 ||
				len(result.CategoryEvidence()) != 0 {
				t.Fatalf("not-applicable result = %#v, want versioned empty journal evidence", result)
			}
		})
	}
}

func TestJournalPolicyRejectsMismatchedMetricEvidence(t *testing.T) {
	t.Parallel()

	item := venueForTest(
		t,
		"venue-alpha",
		"Synthetic Journal",
		"1234-5679",
		"1234-5679",
		"2049-3630",
	)
	policy, err := NewJournalPolicy("journal-jif-or-q1/v1")
	if err != nil {
		t.Fatalf("NewJournalPolicy() error = %v", err)
	}
	evaluatedAt := time.Date(2026, time.July, 16, 14, 0, 0, 0, time.UTC)

	tests := []MetricSnapshot{
		mustMetricSnapshot(t, "other-venue", 2025, "AI", "12", QuartileQ1, MetricStatusKnown, "synthetic-jcr"),
		mustMetricSnapshot(t, item.ID(), 2024, "AI", "12", QuartileQ1, MetricStatusKnown, "synthetic-jcr"),
	}
	for _, metric := range tests {
		if _, err := policy.Evaluate(item, 2025, []MetricSnapshot{metric}, evaluatedAt); err == nil {
			t.Fatalf("Evaluate() accepted mismatched evidence %#v", metric)
		}
	}
}
