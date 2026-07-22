package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/analysis"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var cohortRevisionPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type persistedBiomedicalAnalysisRun struct {
	ID                  uuid.UUID
	AnalysisType        string
	FormulaVersion      string
	AsOf                time.Time
	GeneratedAt         time.Time
	CohortRevision      string
	RecentWindowDays    int
	BaselineWindowDays  int
	WindowDays          int
	MinimumSupportCount int
}

type validatedBiomedicalAnalysisRuns struct {
	Trend       persistedBiomedicalAnalysisRun
	Journal     persistedBiomedicalAnalysisRun
	Opportunity persistedBiomedicalAnalysisRun
}

type biomedicalAnalysisScopeInput struct {
	AsOf                     string    `json:"as_of"`
	FormulaVersion           string    `json:"formula_version"`
	SubjectVersion           string    `json:"subject_version"`
	EligibilityPolicyVersion string    `json:"eligibility_policy_version"`
	JCRMetricYear            int       `json:"jcr_metric_year"`
	JCRImportReceipt         uuid.UUID `json:"jcr_import_receipt"`
	VenuePolicyName          string    `json:"venue_policy_name"`
	VenuePolicyVersion       int       `json:"venue_policy_version"`
	CohortRevision           string    `json:"cohort_revision"`
}

type trendAnalysisRunInput struct {
	biomedicalAnalysisScopeInput
	RecentWindowDays               int     `json:"recent_window_days"`
	BaselineWindowDays             int     `json:"baseline_window_days"`
	MinimumPaperCount              int     `json:"minimum_paper_count"`
	MinimumIndependentJournalCount int     `json:"minimum_independent_journal_count"`
	MinimumIndependentTeamCount    int     `json:"minimum_independent_team_count"`
	ModelSelectionRule             string  `json:"model_selection_rule"`
	DispersionThreshold            float64 `json:"dispersion_threshold"`
}

type journalAnalysisRunInput struct {
	biomedicalAnalysisScopeInput
	WindowDays                int `json:"window_days"`
	MinimumSupportCount       int `json:"minimum_support_count"`
	MinimumFieldBaselineCount int `json:"minimum_field_baseline_count"`
}

type opportunityAnalysisRunInput struct {
	biomedicalAnalysisScopeInput
	RuleSetVersion        string    `json:"rule_set_version"`
	CitationAnalysisRunID uuid.UUID `json:"citation_analysis_run_id"`
	TrendAnalysisRunID    uuid.UUID `json:"trend_analysis_run_id"`
	JournalAnalysisRunID  uuid.UUID `json:"journal_analysis_run_id"`
}

type rawBiomedicalAnalysisRun struct {
	ID            uuid.UUID
	AnalysisType  string
	ModelProvider string
	ModelName     string
	PromptVersion string
	Status        string
	InputPayload  []byte
	OutputPayload []byte
	StartedAt     time.Time
	CompletedAt   time.Time
}

func validateBiomedicalAnalysisRuns(
	ctx context.Context,
	tx pgx.Tx,
	input PublishInput,
	cohortRevision string,
) (validatedBiomedicalAnalysisRuns, error) {
	if !cohortRevisionPattern.MatchString(cohortRevision) {
		return validatedBiomedicalAnalysisRuns{}, fmt.Errorf(
			"%w: current biomedical cohort revision is invalid",
			ErrCatalogNotReady,
		)
	}

	trendRaw, err := loadRawBiomedicalAnalysisRun(
		ctx,
		tx,
		input.TrendAnalysisRunID,
	)
	if err != nil {
		return validatedBiomedicalAnalysisRuns{}, err
	}
	var trendInput trendAnalysisRunInput
	if err := decodeStrictJSONObject(trendRaw.InputPayload, &trendInput); err != nil {
		return validatedBiomedicalAnalysisRuns{}, fmt.Errorf(
			"%w: decode publication trend analysis run %s input: %v",
			ErrCatalogNotReady,
			trendRaw.ID,
			err,
		)
	}
	trend, err := validateTrendAnalysisRun(
		input,
		cohortRevision,
		trendRaw,
		trendInput,
	)
	if err != nil {
		return validatedBiomedicalAnalysisRuns{}, err
	}

	journalRaw, err := loadRawBiomedicalAnalysisRun(
		ctx,
		tx,
		input.JournalAnalysisRunID,
	)
	if err != nil {
		return validatedBiomedicalAnalysisRuns{}, err
	}
	var journalInput journalAnalysisRunInput
	if err := decodeStrictJSONObject(journalRaw.InputPayload, &journalInput); err != nil {
		return validatedBiomedicalAnalysisRuns{}, fmt.Errorf(
			"%w: decode journal editorial-pattern analysis run %s input: %v",
			ErrCatalogNotReady,
			journalRaw.ID,
			err,
		)
	}
	journal, err := validateJournalAnalysisRun(
		input,
		cohortRevision,
		journalRaw,
		journalInput,
	)
	if err != nil {
		return validatedBiomedicalAnalysisRuns{}, err
	}

	opportunityRaw, err := loadRawBiomedicalAnalysisRun(
		ctx,
		tx,
		input.OpportunityAnalysisRunID,
	)
	if err != nil {
		return validatedBiomedicalAnalysisRuns{}, err
	}
	var opportunityInput opportunityAnalysisRunInput
	if err := decodeStrictJSONObject(
		opportunityRaw.InputPayload,
		&opportunityInput,
	); err != nil {
		return validatedBiomedicalAnalysisRuns{}, fmt.Errorf(
			"%w: decode research opportunity analysis run %s input: %v",
			ErrCatalogNotReady,
			opportunityRaw.ID,
			err,
		)
	}
	opportunity, err := validateOpportunityAnalysisRun(
		input,
		cohortRevision,
		opportunityRaw,
		opportunityInput,
	)
	if err != nil {
		return validatedBiomedicalAnalysisRuns{}, err
	}

	return validatedBiomedicalAnalysisRuns{
		Trend:       trend,
		Journal:     journal,
		Opportunity: opportunity,
	}, nil
}

func loadRawBiomedicalAnalysisRun(
	ctx context.Context,
	tx pgx.Tx,
	runID uuid.UUID,
) (rawBiomedicalAnalysisRun, error) {
	var run rawBiomedicalAnalysisRun
	err := tx.QueryRow(ctx, `
		SELECT
			id,
			analysis_type,
			model_provider,
			model_name,
			prompt_version,
			status,
			input_payload,
			output_payload,
			started_at,
			completed_at
		FROM analysis_runs
		WHERE id = $1
	`, runID).Scan(
		&run.ID,
		&run.AnalysisType,
		&run.ModelProvider,
		&run.ModelName,
		&run.PromptVersion,
		&run.Status,
		&run.InputPayload,
		&run.OutputPayload,
		&run.StartedAt,
		&run.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return rawBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: biomedical analysis run %s does not exist",
			ErrCatalogNotReady,
			runID,
		)
	}
	if err != nil {
		return rawBiomedicalAnalysisRun{}, fmt.Errorf(
			"query biomedical analysis run %s: %w",
			runID,
			err,
		)
	}
	return run, nil
}

func validateTrendAnalysisRun(
	input PublishInput,
	cohortRevision string,
	raw rawBiomedicalAnalysisRun,
	payload trendAnalysisRunInput,
) (persistedBiomedicalAnalysisRun, error) {
	if payload.RecentWindowDays != biomedicalHomeWindowDays {
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: publication trend analysis run %s recent window = %d days, want %d",
			ErrCatalogNotReady,
			raw.ID,
			payload.RecentWindowDays,
			biomedicalHomeWindowDays,
		)
	}
	if raw.AnalysisType != "publication_trends" ||
		raw.PromptVersion != analysis.PublicationTrendFormulaVersion ||
		payload.FormulaVersion != analysis.PublicationTrendFormulaVersion ||
		payload.BaselineWindowDays <= payload.RecentWindowDays ||
		payload.BaselineWindowDays > 3650 ||
		payload.MinimumPaperCount < 1 ||
		payload.MinimumIndependentJournalCount < 1 ||
		payload.MinimumIndependentTeamCount < 1 {
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: publication trend analysis run %s has invalid deterministic boundaries",
			ErrCatalogNotReady,
			raw.ID,
		)
	}
	switch analysis.TrendModelSelectionRule(payload.ModelSelectionRule) {
	case analysis.TrendModelSelectionFixedPoisson,
		analysis.TrendModelSelectionFixedNegativeBinomial:
		if payload.DispersionThreshold != 0 {
			return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
				"%w: publication trend analysis run %s has invalid fixed-model dispersion threshold",
				ErrCatalogNotReady,
				raw.ID,
			)
		}
	case analysis.TrendModelSelectionDispersionThreshold:
		if payload.DispersionThreshold <= 0 {
			return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
				"%w: publication trend analysis run %s has invalid predeclared dispersion threshold",
				ErrCatalogNotReady,
				raw.ID,
			)
		}
	default:
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: publication trend analysis run %s has an unsupported model-selection rule",
			ErrCatalogNotReady,
			raw.ID,
		)
	}
	run, err := validateBiomedicalAnalysisRunScope(
		input,
		cohortRevision,
		raw,
		payload.biomedicalAnalysisScopeInput,
	)
	if err != nil {
		return persistedBiomedicalAnalysisRun{}, err
	}
	runAsOfDate := time.Date(
		run.AsOf.UTC().Year(),
		run.AsOf.UTC().Month(),
		run.AsOf.UTC().Day(),
		0,
		0,
		0,
		0,
		time.UTC,
	)
	homeDate := time.Date(
		input.GeneratedAt.UTC().Year(),
		input.GeneratedAt.UTC().Month(),
		input.GeneratedAt.UTC().Day(),
		0,
		0,
		0,
		0,
		time.UTC,
	)
	if !runAsOfDate.Equal(homeDate) {
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: publication trend analysis run %s as_of UTC calendar date %s does not match Home date %s",
			ErrCatalogNotReady,
			raw.ID,
			runAsOfDate.Format("2006-01-02"),
			homeDate.Format("2006-01-02"),
		)
	}
	run.RecentWindowDays = payload.RecentWindowDays
	run.BaselineWindowDays = payload.BaselineWindowDays
	return run, nil
}

func validateJournalAnalysisRun(
	input PublishInput,
	cohortRevision string,
	raw rawBiomedicalAnalysisRun,
	payload journalAnalysisRunInput,
) (persistedBiomedicalAnalysisRun, error) {
	if raw.AnalysisType != "journal_editorial_patterns" ||
		raw.PromptVersion != analysis.JournalPatternFormulaVersion ||
		payload.FormulaVersion != analysis.JournalPatternFormulaVersion ||
		payload.WindowDays < 1 ||
		payload.WindowDays > 3650 ||
		payload.MinimumSupportCount < 1 ||
		payload.MinimumFieldBaselineCount < payload.MinimumSupportCount {
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: journal editorial-pattern analysis run %s has invalid deterministic boundaries",
			ErrCatalogNotReady,
			raw.ID,
		)
	}
	run, err := validateBiomedicalAnalysisRunScope(
		input,
		cohortRevision,
		raw,
		payload.biomedicalAnalysisScopeInput,
	)
	if err != nil {
		return persistedBiomedicalAnalysisRun{}, err
	}
	run.WindowDays = payload.WindowDays
	run.MinimumSupportCount = payload.MinimumSupportCount
	return run, nil
}

func validateOpportunityAnalysisRun(
	input PublishInput,
	cohortRevision string,
	raw rawBiomedicalAnalysisRun,
	payload opportunityAnalysisRunInput,
) (persistedBiomedicalAnalysisRun, error) {
	if raw.AnalysisType != "research_opportunities" ||
		raw.PromptVersion != analysis.OpportunityFormulaVersion ||
		payload.FormulaVersion != analysis.OpportunityFormulaVersion ||
		payload.RuleSetVersion != analysis.OpportunityRuleSetVersion ||
		payload.CitationAnalysisRunID != input.CitationAnalysisRunID ||
		payload.TrendAnalysisRunID != input.TrendAnalysisRunID ||
		payload.JournalAnalysisRunID != input.JournalAnalysisRunID {
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: research opportunity analysis run %s does not bind the selected upstream analysis runs",
			ErrCatalogNotReady,
			raw.ID,
		)
	}
	return validateBiomedicalAnalysisRunScope(
		input,
		cohortRevision,
		raw,
		payload.biomedicalAnalysisScopeInput,
	)
}

func validateBiomedicalAnalysisRunScope(
	input PublishInput,
	cohortRevision string,
	raw rawBiomedicalAnalysisRun,
	payload biomedicalAnalysisScopeInput,
) (persistedBiomedicalAnalysisRun, error) {
	if raw.ModelProvider != "internal" ||
		raw.ModelName != "deterministic" ||
		raw.Status != "succeeded" ||
		len(raw.OutputPayload) == 0 ||
		raw.StartedAt.IsZero() ||
		raw.CompletedAt.IsZero() ||
		raw.StartedAt.After(raw.CompletedAt) ||
		raw.CompletedAt.After(input.GeneratedAt) ||
		raw.PromptVersion != payload.FormulaVersion {
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: biomedical analysis run %s is not an exact completed deterministic run available at generated_at",
			ErrCatalogNotReady,
			raw.ID,
		)
	}
	asOf, err := time.Parse(time.RFC3339Nano, payload.AsOf)
	if err != nil || asOf.After(input.AnalysisCutoff) {
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: biomedical analysis run %s has invalid as_of for the analysis cutoff",
			ErrCatalogNotReady,
			raw.ID,
		)
	}
	if payload.SubjectVersion != input.SubjectVersion ||
		payload.EligibilityPolicyVersion != input.EligibilityPolicyVersion ||
		payload.JCRMetricYear != input.JCRMetricYear ||
		payload.JCRImportReceipt != input.JCRImportReceipt ||
		payload.VenuePolicyName != input.VenuePolicyName ||
		payload.VenuePolicyVersion != input.VenuePolicyVersion ||
		payload.CohortRevision != cohortRevision ||
		!cohortRevisionPattern.MatchString(payload.CohortRevision) {
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: biomedical analysis run %s scope or cohort revision does not match this Catalog publication",
			ErrCatalogNotReady,
			raw.ID,
		)
	}
	var output map[string]json.RawMessage
	if err := decodeStrictJSONObject(raw.OutputPayload, &output); err != nil {
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: decode biomedical analysis run %s output: %v",
			ErrCatalogNotReady,
			raw.ID,
			err,
		)
	}
	rawOutputRevision, exists := output["cohort_revision"]
	if !exists {
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: biomedical analysis run %s output lacks cohort revision",
			ErrCatalogNotReady,
			raw.ID,
		)
	}
	var outputRevision string
	if err := json.Unmarshal(rawOutputRevision, &outputRevision); err != nil ||
		outputRevision != cohortRevision {
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: biomedical analysis run %s output cohort revision does not match",
			ErrCatalogNotReady,
			raw.ID,
		)
	}
	if raw.AnalysisType == "" ||
		raw.AnalysisType != strings.TrimSpace(raw.AnalysisType) {
		return persistedBiomedicalAnalysisRun{}, fmt.Errorf(
			"%w: biomedical analysis run %s has invalid analysis type",
			ErrCatalogNotReady,
			raw.ID,
		)
	}
	return persistedBiomedicalAnalysisRun{
		ID:             raw.ID,
		AnalysisType:   raw.AnalysisType,
		FormulaVersion: payload.FormulaVersion,
		AsOf:           asOf.UTC(),
		GeneratedAt:    raw.CompletedAt.UTC(),
		CohortRevision: payload.CohortRevision,
	}, nil
}
