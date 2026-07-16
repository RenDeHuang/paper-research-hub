package researchmap

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestEvaluateWorthPursuingAtExactPolicyThresholds(t *testing.T) {
	t.Parallel()

	policy := validPolicy()
	firstID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	secondID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	generatedAt := time.Date(
		2026,
		time.July,
		16,
		16,
		0,
		0,
		0,
		time.FixedZone("CST", 8*60*60),
	)
	window := Window{
		Start: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC),
	}

	got, err := Evaluate(AssessmentInput{
		Topic: TopicSummary{
			ID:   "topic:ml-agents",
			Name: "ML Agents",
		},
		EvidencePaperIDs:   []uuid.UUID{secondID, firstID, secondID},
		GrowthWindow:       window,
		GrowthScore:        decimal.RequireFromString("0.25"),
		CompetitionDensity: decimal.RequireFromString("0.4"),
		DataAvailability:   decimal.RequireFromString("0.7"),
		Reproducibility:    decimal.RequireFromString("0.6"),
		Limitations:        []string{" single region ", "limited cohort", "limited cohort"},
		GeneratedAt:        generatedAt,
		Coverage:           decimal.RequireFromString("0.8"),
		Confidence:         decimal.RequireFromString("0.75"),
	}, policy)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}

	if got.Status != StatusWorthPursuing || !got.Status.Valid() {
		t.Fatalf("Opportunity.Status = %q, want %q", got.Status, StatusWorthPursuing)
	}
	if got.Topic != (TopicSummary{ID: "topic:ml-agents", Name: "ML Agents"}) {
		t.Fatalf("Opportunity.Topic = %#v", got.Topic)
	}
	if !reflect.DeepEqual(got.EvidencePaperIDs, []uuid.UUID{firstID, secondID}) {
		t.Fatalf("Opportunity.EvidencePaperIDs = %v, want stable sorted unique IDs", got.EvidencePaperIDs)
	}
	if got.GrowthWindow != window {
		t.Fatalf("Opportunity.GrowthWindow = %#v, want %#v", got.GrowthWindow, window)
	}
	assertDecimalEqual(t, "GrowthScore", got.GrowthScore, "0.25")
	assertDecimalEqual(t, "CompetitionDensity", got.CompetitionDensity, "0.4")
	assertDecimalEqual(t, "DataAvailability", got.DataAvailability, "0.7")
	assertDecimalEqual(t, "Reproducibility", got.Reproducibility, "0.6")
	if !reflect.DeepEqual(got.Limitations, []string{"limited cohort", "single region"}) {
		t.Fatalf("Opportunity.Limitations = %v, want sorted unique limitations", got.Limitations)
	}
	if got.FormulaVersion != "opportunity-formula/v1" {
		t.Fatalf("Opportunity.FormulaVersion = %q", got.FormulaVersion)
	}
	if !got.GeneratedAt.Equal(generatedAt.UTC()) || got.GeneratedAt.Location() != time.UTC {
		t.Fatalf("Opportunity.GeneratedAt = %v, want canonical UTC %v", got.GeneratedAt, generatedAt.UTC())
	}
	assertDecimalEqual(t, "Coverage", got.Coverage, "0.8")
	if len(got.MissingSignals) != 0 {
		t.Fatalf("Opportunity.MissingSignals = %v, want none", got.MissingSignals)
	}
	assertDecimalEqual(t, "Confidence", got.Confidence, "0.75")
}

func TestEvaluateInsufficientEvidencePrecedesConclusiveClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       AssessmentInput
		wantMissing []Signal
	}{
		{
			name: "positive values with missing required signal",
			input: func() AssessmentInput {
				input := worthPursuingInput()
				input.MissingSignals = []Signal{
					SignalCompetitionDensity,
					SignalCompetitionDensity,
				}
				return input
			}(),
			wantMissing: []Signal{SignalCompetitionDensity},
		},
		{
			name: "negative values with missing required signal",
			input: func() AssessmentInput {
				input := notRecommendedInput()
				input.MissingSignals = []Signal{SignalDataAvailability}
				return input
			}(),
			wantMissing: []Signal{SignalDataAvailability},
		},
		{
			name: "coverage below policy minimum",
			input: func() AssessmentInput {
				input := worthPursuingInput()
				input.Coverage = decimal.RequireFromString("0.799999")
				return input
			}(),
		},
		{
			name: "empty evidence IDs",
			input: func() AssessmentInput {
				input := worthPursuingInput()
				input.EvidencePaperIDs = nil
				return input
			}(),
			wantMissing: []Signal{SignalEvidencePaperIDs},
		},
		{
			name: "missing growth window",
			input: func() AssessmentInput {
				input := worthPursuingInput()
				input.GrowthWindow = Window{}
				input.MissingSignals = []Signal{SignalGrowthWindow}
				return input
			}(),
			wantMissing: []Signal{SignalGrowthWindow},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Evaluate(tt.input, validPolicy())
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if got.Status != StatusInsufficientEvidence {
				t.Fatalf(
					"Opportunity.Status = %q, want %q",
					got.Status,
					StatusInsufficientEvidence,
				)
			}
			if !reflect.DeepEqual(got.MissingSignals, tt.wantMissing) {
				t.Fatalf(
					"Opportunity.MissingSignals = %v, want %v",
					got.MissingSignals,
					tt.wantMissing,
				)
			}
		})
	}
}

func TestEvaluateClassifiesNegativeBoundaryAndPolicyGap(t *testing.T) {
	t.Parallel()

	negative, err := Evaluate(notRecommendedInput(), validPolicy())
	if err != nil {
		t.Fatalf("Evaluate(not recommended) error = %v", err)
	}
	if negative.Status != StatusNotRecommendedNow {
		t.Fatalf(
			"negative boundary status = %q, want %q",
			negative.Status,
			StatusNotRecommendedNow,
		)
	}

	cautionInput := worthPursuingInput()
	cautionInput.GrowthScore = decimal.RequireFromString("0.1")
	cautionInput.CompetitionDensity = decimal.RequireFromString("0.6")
	cautionInput.DataAvailability = decimal.RequireFromString("0.5")
	cautionInput.Reproducibility = decimal.RequireFromString("0.4")
	cautionInput.Confidence = decimal.RequireFromString("0.72")
	caution, err := Evaluate(cautionInput, validPolicy())
	if err != nil {
		t.Fatalf("Evaluate(caution) error = %v", err)
	}
	if caution.Status != StatusProceedWithCaution {
		t.Fatalf(
			"policy-gap status = %q, want %q",
			caution.Status,
			StatusProceedWithCaution,
		)
	}
}

func TestStatusAllowsExactlyFourValues(t *testing.T) {
	t.Parallel()

	valid := []Status{
		StatusWorthPursuing,
		StatusProceedWithCaution,
		StatusNotRecommendedNow,
		StatusInsufficientEvidence,
	}
	for _, status := range valid {
		if !status.Valid() {
			t.Errorf("Status(%q).Valid() = false", status)
		}
	}
	for _, status := range []Status{"", "recommended", "insufficient"} {
		if status.Valid() {
			t.Errorf("Status(%q).Valid() = true", status)
		}
	}
}

func TestEvaluateRejectsInvalidPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Policy)
	}{
		{
			name: "missing policy version",
			mutate: func(policy *Policy) {
				policy.Version = ""
			},
		},
		{
			name: "missing formula version",
			mutate: func(policy *Policy) {
				policy.FormulaVersion = ""
			},
		},
		{
			name: "minimum coverage below zero",
			mutate: func(policy *Policy) {
				policy.MinimumCoverage = decimal.RequireFromString("-0.01")
			},
		},
		{
			name: "minimum coverage above one",
			mutate: func(policy *Policy) {
				policy.MinimumCoverage = decimal.RequireFromString("1.01")
			},
		},
		{
			name: "missing required signal",
			mutate: func(policy *Policy) {
				policy.RequiredSignals = policy.RequiredSignals[:len(policy.RequiredSignals)-1]
			},
		},
		{
			name: "duplicate required signal",
			mutate: func(policy *Policy) {
				policy.RequiredSignals = append(
					policy.RequiredSignals,
					SignalGrowthScore,
				)
			},
		},
		{
			name: "unknown required signal",
			mutate: func(policy *Policy) {
				policy.RequiredSignals[0] = Signal("unknown")
			},
		},
		{
			name: "positive normalized threshold outside unit interval",
			mutate: func(policy *Policy) {
				policy.WorthPursuing.MaximumCompetitionDensity = decimal.RequireFromString("1.1")
			},
		},
		{
			name: "negative normalized threshold outside unit interval",
			mutate: func(policy *Policy) {
				policy.NotRecommendedNow.MaximumDataAvailability = decimal.RequireFromString("-0.1")
			},
		},
		{
			name: "positive and negative regions overlap",
			mutate: func(policy *Policy) {
				policy.WorthPursuing.MinimumGrowthScore = decimal.RequireFromString("-0.2")
				policy.WorthPursuing.MaximumCompetitionDensity = decimal.RequireFromString("0.9")
				policy.WorthPursuing.MinimumDataAvailability = decimal.RequireFromString("0.2")
				policy.WorthPursuing.MinimumReproducibility = decimal.RequireFromString("0.1")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			policy := validPolicy()
			tt.mutate(&policy)
			if _, err := Evaluate(worthPursuingInput(), policy); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("Evaluate() error = %v, want ErrInvalidPolicy", err)
			}
		})
	}
}

func TestEvaluateRejectsInvalidAssessment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*AssessmentInput)
	}{
		{
			name: "blank topic ID",
			mutate: func(input *AssessmentInput) {
				input.Topic.ID = " "
			},
		},
		{
			name: "blank topic name",
			mutate: func(input *AssessmentInput) {
				input.Topic.Name = ""
			},
		},
		{
			name: "zero generated time",
			mutate: func(input *AssessmentInput) {
				input.GeneratedAt = time.Time{}
			},
		},
		{
			name: "reversed growth window",
			mutate: func(input *AssessmentInput) {
				input.GrowthWindow.Start, input.GrowthWindow.End =
					input.GrowthWindow.End, input.GrowthWindow.Start
			},
		},
		{
			name: "coverage above one",
			mutate: func(input *AssessmentInput) {
				input.Coverage = decimal.RequireFromString("1.01")
			},
		},
		{
			name: "competition below zero",
			mutate: func(input *AssessmentInput) {
				input.CompetitionDensity = decimal.RequireFromString("-0.01")
			},
		},
		{
			name: "data availability above one",
			mutate: func(input *AssessmentInput) {
				input.DataAvailability = decimal.RequireFromString("1.01")
			},
		},
		{
			name: "reproducibility below zero",
			mutate: func(input *AssessmentInput) {
				input.Reproducibility = decimal.RequireFromString("-0.01")
			},
		},
		{
			name: "confidence above one",
			mutate: func(input *AssessmentInput) {
				input.Confidence = decimal.RequireFromString("1.01")
			},
		},
		{
			name: "nil evidence UUID",
			mutate: func(input *AssessmentInput) {
				input.EvidencePaperIDs = []uuid.UUID{uuid.Nil}
			},
		},
		{
			name: "unknown missing signal",
			mutate: func(input *AssessmentInput) {
				input.MissingSignals = []Signal{"unknown"}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			input := worthPursuingInput()
			tt.mutate(&input)
			if _, err := Evaluate(input, validPolicy()); !errors.Is(err, ErrInvalidAssessment) {
				t.Fatalf("Evaluate() error = %v, want ErrInvalidAssessment", err)
			}
		})
	}
}

func validPolicy() Policy {
	return Policy{
		Version:         "research-opportunity-policy/v1",
		FormulaVersion:  "opportunity-formula/v1",
		MinimumCoverage: decimal.RequireFromString("0.8"),
		RequiredSignals: []Signal{
			SignalEvidencePaperIDs,
			SignalGrowthWindow,
			SignalGrowthScore,
			SignalCompetitionDensity,
			SignalDataAvailability,
			SignalReproducibility,
			SignalConfidence,
		},
		WorthPursuing: WorthPursuingThresholds{
			MinimumGrowthScore:        decimal.RequireFromString("0.25"),
			MaximumCompetitionDensity: decimal.RequireFromString("0.4"),
			MinimumDataAvailability:   decimal.RequireFromString("0.7"),
			MinimumReproducibility:    decimal.RequireFromString("0.6"),
			MinimumConfidence:         decimal.RequireFromString("0.75"),
		},
		NotRecommendedNow: NotRecommendedNowThresholds{
			MaximumGrowthScore:        decimal.RequireFromString("-0.1"),
			MinimumCompetitionDensity: decimal.RequireFromString("0.8"),
			MaximumDataAvailability:   decimal.RequireFromString("0.3"),
			MaximumReproducibility:    decimal.RequireFromString("0.2"),
			MinimumConfidence:         decimal.RequireFromString("0.7"),
		},
	}
}

func worthPursuingInput() AssessmentInput {
	return AssessmentInput{
		Topic: TopicSummary{ID: "topic:ml-agents", Name: "ML Agents"},
		EvidencePaperIDs: []uuid.UUID{
			uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		},
		GrowthWindow: Window{
			Start: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC),
		},
		GrowthScore:        decimal.RequireFromString("0.25"),
		CompetitionDensity: decimal.RequireFromString("0.4"),
		DataAvailability:   decimal.RequireFromString("0.7"),
		Reproducibility:    decimal.RequireFromString("0.6"),
		GeneratedAt:        time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC),
		Coverage:           decimal.RequireFromString("0.8"),
		Confidence:         decimal.RequireFromString("0.75"),
	}
}

func notRecommendedInput() AssessmentInput {
	input := worthPursuingInput()
	input.GrowthScore = decimal.RequireFromString("-0.1")
	input.CompetitionDensity = decimal.RequireFromString("0.8")
	input.DataAvailability = decimal.RequireFromString("0.3")
	input.Reproducibility = decimal.RequireFromString("0.2")
	input.Confidence = decimal.RequireFromString("0.7")
	return input
}

func assertDecimalEqual(t *testing.T, field string, got decimal.Decimal, want string) {
	t.Helper()

	wantDecimal := decimal.RequireFromString(want)
	if !got.Equal(wantDecimal) {
		t.Fatalf("Opportunity.%s = %s, want %s", field, got, wantDecimal)
	}
}
