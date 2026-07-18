package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBiomedicalAnalysisRunPayloadsMatchCatalogContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		payload    any
		required   []string
		prohibited []string
	}{
		{
			name:    "publication trends",
			payload: trendRunInputPayload{},
			required: []string{
				"as_of",
				"formula_version",
				"subject_version",
				"eligibility_policy_version",
				"jcr_metric_year",
				"jcr_import_receipt",
				"venue_policy_name",
				"venue_policy_version",
				"cohort_revision",
				"recent_window_days",
				"baseline_window_days",
				"minimum_paper_count",
				"minimum_independent_journal_count",
				"minimum_independent_team_count",
				"model_selection_rule",
				"dispersion_threshold",
			},
			prohibited: []string{
				"analysis_type",
				"scope",
				"source",
				"minimum_papers_per_window",
				"minimum_independent_journals",
				"minimum_independent_teams",
			},
		},
		{
			name:    "journal editorial patterns",
			payload: journalRunInputPayload{},
			required: []string{
				"as_of",
				"formula_version",
				"subject_version",
				"eligibility_policy_version",
				"jcr_metric_year",
				"jcr_import_receipt",
				"venue_policy_name",
				"venue_policy_version",
				"cohort_revision",
				"window_days",
				"minimum_support_count",
				"minimum_field_baseline_count",
			},
			prohibited: []string{
				"analysis_type",
				"scope",
				"source",
				"minimum_coverage",
				"minimum_support_paper_count",
			},
		},
		{
			name:    "research opportunities",
			payload: opportunityRunInputPayload{},
			required: []string{
				"as_of",
				"formula_version",
				"subject_version",
				"eligibility_policy_version",
				"jcr_metric_year",
				"jcr_import_receipt",
				"venue_policy_name",
				"venue_policy_version",
				"cohort_revision",
				"rule_set_version",
				"citation_analysis_run_id",
				"trend_analysis_run_id",
				"journal_analysis_run_id",
			},
			prohibited: []string{
				"analysis_type",
				"scope",
				"source",
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(test.payload)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			for _, key := range test.required {
				if _, exists := decoded[key]; !exists {
					t.Errorf("payload is missing required key %q: %s", key, raw)
				}
			}
			for _, key := range test.prohibited {
				if _, exists := decoded[key]; exists {
					t.Errorf("payload contains prohibited key %q: %s", key, raw)
				}
			}
		})
	}
}

func TestDecodeStrictAnalysisPayloadRejectsTrailingTopLevelSyntax(
	t *testing.T,
) {
	t.Parallel()

	var payload struct {
		FormulaVersion string `json:"formula_version"`
	}
	err := decodeStrictAnalysisPayload(
		[]byte(`{"formula_version":"v1"}]`),
		&payload,
	)
	if err == nil {
		t.Fatal(
			"decodeStrictAnalysisPayload() accepted trailing top-level syntax",
		)
	}
}

func TestOpportunityAnalysisInputRequiresExactRunsAndScope(t *testing.T) {
	t.Parallel()

	valid := OpportunityAnalysisInput{
		AsOf: time.Date(
			2026,
			time.July,
			17,
			12,
			0,
			0,
			0,
			time.UTC,
		),
		FormulaVersion:           OpportunityFormulaVersion,
		RuleSetVersion:           OpportunityRuleSetVersion,
		SubjectVersion:           "biomedical-jcr-subjects/v1",
		EligibilityPolicyVersion: "biomedical-public-eligibility/v1",
		JCRMetricYear:            2025,
		JCRImportReceipt: uuid.MustParse(
			"00000000-0000-0000-0000-000000000501",
		),
		VenuePolicyName:    "journal-all-q1",
		VenuePolicyVersion: 2,
		CitationAnalysisRunID: uuid.MustParse(
			"00000000-0000-0000-0000-000000000701",
		),
		TrendAnalysisRunID: uuid.MustParse(
			"00000000-0000-0000-0000-000000000702",
		),
		JournalAnalysisRunID: uuid.MustParse(
			"00000000-0000-0000-0000-000000000703",
		),
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid OpportunityAnalysisInput.validate() error = %v", err)
	}

	tests := []struct {
		name  string
		alter func(*OpportunityAnalysisInput)
	}{
		{
			name: "citation run",
			alter: func(input *OpportunityAnalysisInput) {
				input.CitationAnalysisRunID = uuid.Nil
			},
		},
		{
			name: "trend run",
			alter: func(input *OpportunityAnalysisInput) {
				input.TrendAnalysisRunID = uuid.Nil
			},
		},
		{
			name: "journal run",
			alter: func(input *OpportunityAnalysisInput) {
				input.JournalAnalysisRunID = uuid.Nil
			},
		},
		{
			name: "venue policy",
			alter: func(input *OpportunityAnalysisInput) {
				input.VenuePolicyName = ""
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := valid
			test.alter(&input)
			if err := input.validate(); err == nil {
				t.Fatal("OpportunityAnalysisInput.validate() error = nil")
			}
		})
	}
}

func TestValidateOpportunityUpstreamRunsRequiresExactScopeAndCohort(
	t *testing.T,
) {
	t.Parallel()

	input := OpportunityAnalysisInput{
		AsOf: time.Date(
			2026,
			time.July,
			17,
			12,
			0,
			0,
			0,
			time.UTC,
		),
		FormulaVersion:           OpportunityFormulaVersion,
		RuleSetVersion:           OpportunityRuleSetVersion,
		SubjectVersion:           "biomedical-jcr-subjects/v1",
		EligibilityPolicyVersion: "biomedical-public-eligibility/v1",
		JCRMetricYear:            2025,
		JCRImportReceipt: uuid.MustParse(
			"00000000-0000-0000-0000-000000000501",
		),
		VenuePolicyName:    "journal-all-q1",
		VenuePolicyVersion: 2,
		CitationAnalysisRunID: uuid.MustParse(
			"00000000-0000-0000-0000-000000000701",
		),
		TrendAnalysisRunID: uuid.MustParse(
			"00000000-0000-0000-0000-000000000702",
		),
		JournalAnalysisRunID: uuid.MustParse(
			"00000000-0000-0000-0000-000000000703",
		),
	}
	cohortRevision := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	completedAt := input.AsOf.Add(-time.Minute)
	baseScope := map[string]any{
		"as_of":                      input.AsOf.Format(time.RFC3339Nano),
		"subject_version":            input.SubjectVersion,
		"eligibility_policy_version": input.EligibilityPolicyVersion,
		"jcr_metric_year":            input.JCRMetricYear,
		"jcr_import_receipt":         input.JCRImportReceipt,
		"venue_policy_name":          input.VenuePolicyName,
		"venue_policy_version":       input.VenuePolicyVersion,
	}
	citationPayload := cloneMap(baseScope)
	citationPayload["source"] = "openalex"
	citationPayload["velocity_window_days"] = 30
	citationPayload["minimum_cohort_size"] = 5
	citationPayload["formula_version"] = "citation-intelligence/v1"
	trendPayload := cloneMap(baseScope)
	trendPayload["formula_version"] = PublicationTrendFormulaVersion
	trendPayload["cohort_revision"] = cohortRevision
	trendPayload["recent_window_days"] = 56
	trendPayload["baseline_window_days"] = 364
	trendPayload["minimum_paper_count"] = 20
	trendPayload["minimum_independent_journal_count"] = 3
	trendPayload["minimum_independent_team_count"] = 3
	trendPayload["model_selection_rule"] = string(
		TrendModelSelectionDispersionThreshold,
	)
	trendPayload["dispersion_threshold"] = 1.5
	journalPayload := cloneMap(baseScope)
	journalPayload["formula_version"] = JournalPatternFormulaVersion
	journalPayload["cohort_revision"] = cohortRevision
	journalPayload["window_days"] = 364
	journalPayload["minimum_support_count"] = 10
	journalPayload["minimum_field_baseline_count"] = 40

	runs := opportunityUpstreamRuns{
		Citation: biomedicalUpstreamRun{
			ID:            input.CitationAnalysisRunID,
			AnalysisType:  "citation_intelligence",
			ModelProvider: "internal",
			ModelName:     "deterministic",
			PromptVersion: "citation-intelligence/v1",
			Status:        "succeeded",
			InputPayload:  mustJSON(t, citationPayload),
			CompletedAt:   completedAt,
		},
		Trend: biomedicalUpstreamRun{
			ID:            input.TrendAnalysisRunID,
			AnalysisType:  "publication_trends",
			ModelProvider: "internal",
			ModelName:     "deterministic",
			PromptVersion: PublicationTrendFormulaVersion,
			Status:        "succeeded",
			InputPayload:  mustJSON(t, trendPayload),
			CompletedAt:   completedAt,
		},
		Journal: biomedicalUpstreamRun{
			ID:            input.JournalAnalysisRunID,
			AnalysisType:  "journal_editorial_patterns",
			ModelProvider: "internal",
			ModelName:     "deterministic",
			PromptVersion: JournalPatternFormulaVersion,
			Status:        "succeeded",
			InputPayload:  mustJSON(t, journalPayload),
			CompletedAt:   completedAt,
		},
	}
	if err := validateOpportunityUpstreamRuns(
		input,
		cohortRevision,
		input.AsOf,
		runs,
	); err != nil {
		t.Fatalf("validateOpportunityUpstreamRuns() error = %v", err)
	}

	mismatched := runs
	mismatched.Trend.InputPayload = mustJSON(
		t,
		mapWith(trendPayload, "cohort_revision", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
	)
	if err := validateOpportunityUpstreamRuns(
		input,
		cohortRevision,
		input.AsOf,
		mismatched,
	); err == nil {
		t.Fatal("validateOpportunityUpstreamRuns() accepted mismatched cohort")
	}
}

func TestAnalyzeResearchOpportunitiesRejectsUninitializedService(
	t *testing.T,
) {
	t.Parallel()

	var service *PostgresAnalysisService
	_, err := service.AnalyzeResearchOpportunities(
		context.Background(),
		OpportunityAnalysisInput{},
	)
	if !errors.Is(err, errAnalysisServiceNotInitialized) {
		t.Fatalf(
			"AnalyzeResearchOpportunities() error = %T %v, want %v",
			err,
			err,
			errAnalysisServiceNotInitialized,
		)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return raw
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func mapWith(source map[string]any, key string, value any) map[string]any {
	result := cloneMap(source)
	result[key] = value
	return result
}
