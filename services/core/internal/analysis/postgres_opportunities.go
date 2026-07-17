package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type OpportunityAnalysisInput struct {
	AsOf                     time.Time
	FormulaVersion           string
	RuleSetVersion           string
	SubjectVersion           string
	EligibilityPolicyVersion string
	JCRMetricYear            int
	JCRImportReceipt         uuid.UUID
	VenuePolicyName          string
	VenuePolicyVersion       int
	CitationAnalysisRunID    uuid.UUID
	TrendAnalysisRunID       uuid.UUID
	JournalAnalysisRunID     uuid.UUID
}

type opportunityRunInputPayload struct {
	AsOf                     string    `json:"as_of"`
	FormulaVersion           string    `json:"formula_version"`
	SubjectVersion           string    `json:"subject_version"`
	EligibilityPolicyVersion string    `json:"eligibility_policy_version"`
	JCRMetricYear            int       `json:"jcr_metric_year"`
	JCRImportReceipt         uuid.UUID `json:"jcr_import_receipt"`
	VenuePolicyName          string    `json:"venue_policy_name"`
	VenuePolicyVersion       int       `json:"venue_policy_version"`
	CohortRevision           string    `json:"cohort_revision"`
	RuleSetVersion           string    `json:"rule_set_version"`
	CitationAnalysisRunID    uuid.UUID `json:"citation_analysis_run_id"`
	TrendAnalysisRunID       uuid.UUID `json:"trend_analysis_run_id"`
	JournalAnalysisRunID     uuid.UUID `json:"journal_analysis_run_id"`
}

type opportunityRunOutputPayload struct {
	CohortRevision string `json:"cohort_revision"`
	SnapshotCount  int    `json:"snapshot_count"`
}

type biomedicalUpstreamRun struct {
	ID            uuid.UUID
	AnalysisType  string
	ModelProvider string
	ModelName     string
	PromptVersion string
	Status        string
	InputPayload  []byte
	CompletedAt   time.Time
}

type opportunityUpstreamRuns struct {
	Citation biomedicalUpstreamRun
	Trend    biomedicalUpstreamRun
	Journal  biomedicalUpstreamRun
}

type citationUpstreamInputPayload struct {
	Source                   string    `json:"source"`
	AsOf                     string    `json:"as_of"`
	VelocityWindowDays       int       `json:"velocity_window_days"`
	MinimumCohortSize        int       `json:"minimum_cohort_size"`
	FormulaVersion           string    `json:"formula_version"`
	SubjectVersion           string    `json:"subject_version"`
	EligibilityPolicyVersion string    `json:"eligibility_policy_version"`
	JCRMetricYear            int       `json:"jcr_metric_year"`
	JCRImportReceipt         uuid.UUID `json:"jcr_import_receipt"`
	VenuePolicyName          string    `json:"venue_policy_name"`
	VenuePolicyVersion       int       `json:"venue_policy_version"`
}

type opportunityTrendEvidence struct {
	EntityType             string
	EntityID               string
	RateRatio              float64
	ConfidenceLevel        float64
	ConfidenceIntervalLow  float64
	ConfidenceIntervalHigh float64
}

type subjectOpportunityEvidence struct {
	SubjectID            uuid.UUID
	SubjectSlug          string
	EligibleWorkIDs      map[uuid.UUID]struct{}
	CoveredWorkIDs       map[uuid.UUID]struct{}
	RCTWorkIDs           map[uuid.UUID]struct{}
	ObservationalWorkIDs map[uuid.UUID]struct{}
}

func (input OpportunityAnalysisInput) validate() error {
	if input.AsOf.IsZero() {
		return errors.New("research opportunities as_of is required")
	}
	if input.FormulaVersion != OpportunityFormulaVersion {
		return fmt.Errorf(
			"research opportunities formula_version must equal %s",
			OpportunityFormulaVersion,
		)
	}
	if input.RuleSetVersion != OpportunityRuleSetVersion {
		return fmt.Errorf(
			"research opportunities rule_set_version must equal %s",
			OpportunityRuleSetVersion,
		)
	}
	if err := validateTrimmed(
		"subject_version",
		input.SubjectVersion,
	); err != nil {
		return err
	}
	if input.EligibilityPolicyVersion !=
		"biomedical-public-eligibility/v1" {
		return &InvalidInputError{
			Field:  "eligibility_policy_version",
			Reason: "must equal biomedical-public-eligibility/v1",
		}
	}
	if input.JCRMetricYear < 1900 || input.JCRMetricYear > 3000 {
		return &InvalidInputError{
			Field:  "jcr_metric_year",
			Reason: "must be between 1900 and 3000",
		}
	}
	if input.JCRImportReceipt == uuid.Nil {
		return &InvalidInputError{
			Field:  "jcr_import_receipt",
			Reason: "is required",
		}
	}
	if err := validateTrimmed(
		"venue_policy_name",
		input.VenuePolicyName,
	); err != nil {
		return err
	}
	if input.VenuePolicyVersion < 1 {
		return &InvalidInputError{
			Field:  "venue_policy_version",
			Reason: "must be positive",
		}
	}
	if input.CitationAnalysisRunID == uuid.Nil {
		return &InvalidInputError{
			Field:  "citation_analysis_run_id",
			Reason: "is required",
		}
	}
	if input.TrendAnalysisRunID == uuid.Nil {
		return &InvalidInputError{
			Field:  "trend_analysis_run_id",
			Reason: "is required",
		}
	}
	if input.JournalAnalysisRunID == uuid.Nil {
		return &InvalidInputError{
			Field:  "journal_analysis_run_id",
			Reason: "is required",
		}
	}
	return nil
}

func validateOpportunityUpstreamRuns(
	input OpportunityAnalysisInput,
	cohortRevision string,
	generatedAt time.Time,
	runs opportunityUpstreamRuns,
) error {
	if runs.Citation.ID != input.CitationAnalysisRunID ||
		runs.Trend.ID != input.TrendAnalysisRunID ||
		runs.Journal.ID != input.JournalAnalysisRunID {
		return fmt.Errorf(
			"%w: opportunity upstream run IDs do not match the explicit input",
			errAnalysisReferenceConflict,
		)
	}
	for _, run := range []biomedicalUpstreamRun{
		runs.Citation,
		runs.Trend,
		runs.Journal,
	} {
		if run.ModelProvider != postgresAnalysisModelProvider ||
			run.ModelName != postgresAnalysisModelName ||
			run.Status != "succeeded" ||
			run.CompletedAt.IsZero() ||
			run.CompletedAt.After(generatedAt) {
			return fmt.Errorf(
				"%w: upstream analysis run %s is not an available completed deterministic run",
				errAnalysisReferenceConflict,
				run.ID,
			)
		}
	}

	var citation citationUpstreamInputPayload
	if err := decodeStrictAnalysisPayload(
		runs.Citation.InputPayload,
		&citation,
	); err != nil {
		return fmt.Errorf(
			"decode citation upstream run %s: %w",
			runs.Citation.ID,
			err,
		)
	}
	if runs.Citation.AnalysisType != "citation_intelligence" ||
		runs.Citation.PromptVersion != "citation-intelligence/v1" ||
		citation.FormulaVersion != "citation-intelligence/v1" ||
		citation.Source == "" ||
		citation.VelocityWindowDays < 1 ||
		citation.MinimumCohortSize < 1 {
		return fmt.Errorf(
			"%w: citation upstream run %s has invalid boundaries",
			errAnalysisReferenceConflict,
			runs.Citation.ID,
		)
	}
	if err := validateOpportunityUpstreamScope(
		input,
		citation.AsOf,
		citation.SubjectVersion,
		citation.EligibilityPolicyVersion,
		citation.JCRMetricYear,
		citation.JCRImportReceipt,
		citation.VenuePolicyName,
		citation.VenuePolicyVersion,
	); err != nil {
		return fmt.Errorf(
			"citation upstream run %s: %w",
			runs.Citation.ID,
			err,
		)
	}

	var trend trendRunInputPayload
	if err := decodeStrictAnalysisPayload(
		runs.Trend.InputPayload,
		&trend,
	); err != nil {
		return fmt.Errorf(
			"decode trend upstream run %s: %w",
			runs.Trend.ID,
			err,
		)
	}
	if runs.Trend.AnalysisType != "publication_trends" ||
		runs.Trend.PromptVersion != PublicationTrendFormulaVersion ||
		trend.FormulaVersion != PublicationTrendFormulaVersion ||
		trend.CohortRevision != cohortRevision {
		return fmt.Errorf(
			"%w: trend upstream run %s does not match the current cohort",
			errAnalysisReferenceConflict,
			runs.Trend.ID,
		)
	}
	if err := validateOpportunityUpstreamScope(
		input,
		trend.AsOf,
		trend.SubjectVersion,
		trend.EligibilityPolicyVersion,
		trend.JCRMetricYear,
		trend.JCRImportReceipt,
		trend.VenuePolicyName,
		trend.VenuePolicyVersion,
	); err != nil {
		return fmt.Errorf(
			"trend upstream run %s: %w",
			runs.Trend.ID,
			err,
		)
	}

	var journal journalRunInputPayload
	if err := decodeStrictAnalysisPayload(
		runs.Journal.InputPayload,
		&journal,
	); err != nil {
		return fmt.Errorf(
			"decode journal upstream run %s: %w",
			runs.Journal.ID,
			err,
		)
	}
	if runs.Journal.AnalysisType != "journal_editorial_patterns" ||
		runs.Journal.PromptVersion != JournalPatternFormulaVersion ||
		journal.FormulaVersion != JournalPatternFormulaVersion ||
		journal.CohortRevision != cohortRevision {
		return fmt.Errorf(
			"%w: journal upstream run %s does not match the current cohort",
			errAnalysisReferenceConflict,
			runs.Journal.ID,
		)
	}
	if err := validateOpportunityUpstreamScope(
		input,
		journal.AsOf,
		journal.SubjectVersion,
		journal.EligibilityPolicyVersion,
		journal.JCRMetricYear,
		journal.JCRImportReceipt,
		journal.VenuePolicyName,
		journal.VenuePolicyVersion,
	); err != nil {
		return fmt.Errorf(
			"journal upstream run %s: %w",
			runs.Journal.ID,
			err,
		)
	}
	return nil
}

func validateOpportunityUpstreamScope(
	input OpportunityAnalysisInput,
	asOf string,
	subjectVersion string,
	eligibilityPolicyVersion string,
	jcrMetricYear int,
	jcrImportReceipt uuid.UUID,
	venuePolicyName string,
	venuePolicyVersion int,
) error {
	expectedAsOf := input.AsOf.UTC().Format(time.RFC3339Nano)
	if asOf != expectedAsOf ||
		subjectVersion != input.SubjectVersion ||
		eligibilityPolicyVersion != input.EligibilityPolicyVersion ||
		jcrMetricYear != input.JCRMetricYear ||
		jcrImportReceipt != input.JCRImportReceipt ||
		venuePolicyName != input.VenuePolicyName ||
		venuePolicyVersion != input.VenuePolicyVersion {
		return fmt.Errorf(
			"%w: upstream analysis scope does not match the opportunity input",
			errAnalysisReferenceConflict,
		)
	}
	return nil
}

func decodeStrictAnalysisPayload(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("analysis payload contains trailing JSON values")
		}
		return fmt.Errorf("analysis payload contains trailing JSON: %w", err)
	}
	return nil
}

func (service *PostgresAnalysisService) AnalyzeResearchOpportunities(
	ctx context.Context,
	input OpportunityAnalysisInput,
) (AnalysisRunSummary, error) {
	if ctx == nil {
		return AnalysisRunSummary{}, errors.New(
			"analysis context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return AnalysisRunSummary{}, err
	}
	if err := service.ensureInitialized(); err != nil {
		return AnalysisRunSummary{}, err
	}
	if err := input.validate(); err != nil {
		return AnalysisRunSummary{}, err
	}

	tx, err := beginSerializableTx(ctx, service.pool)
	if err != nil {
		return AnalysisRunSummary{}, fmt.Errorf(
			"begin research opportunities transaction: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	subjectVersionID, err := loadSubjectVersionID(
		ctx,
		tx,
		input.SubjectVersion,
	)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	if err := validateJCRReceiptScope(
		ctx,
		tx,
		input.JCRImportReceipt,
		input.JCRMetricYear,
	); err != nil {
		return AnalysisRunSummary{}, err
	}
	works, err := loadAcceptedCohortWorks(
		ctx,
		tx,
		subjectVersionID,
		input.EligibilityPolicyVersion,
		input.JCRMetricYear,
	)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	if len(works) == 0 {
		return AnalysisRunSummary{}, errors.New(
			"research opportunities require at least one accepted Work",
		)
	}
	workIDs := make([]uuid.UUID, 0, len(works))
	for _, work := range works {
		workIDs = append(workIDs, work.WorkID)
	}
	facts, err := loadCohortRevisionFacts(
		ctx,
		tx,
		workIDs,
		cohortRevisionLoader{
			SubjectVersionID: subjectVersionID,
			MetricYear:       input.JCRMetricYear,
			PolicyVersion:    input.EligibilityPolicyVersion,
		},
	)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	cohortRevision, err := ComputeCohortRevision(facts)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	generatedAt := service.currentTimeUTC()
	upstreamRuns, err := loadOpportunityUpstreamRuns(
		ctx,
		tx,
		input,
	)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	if err := validateOpportunityUpstreamRuns(
		input,
		cohortRevision,
		generatedAt,
		upstreamRuns,
	); err != nil {
		return AnalysisRunSummary{}, err
	}

	runID, err := insertRunningAnalysisRun(
		ctx,
		tx,
		"research_opportunities",
		input.FormulaVersion,
		opportunityRunInputPayload{
			AsOf: input.AsOf.UTC().Format(
				time.RFC3339Nano,
			),
			FormulaVersion:           input.FormulaVersion,
			SubjectVersion:           input.SubjectVersion,
			EligibilityPolicyVersion: input.EligibilityPolicyVersion,
			JCRMetricYear:            input.JCRMetricYear,
			JCRImportReceipt:         input.JCRImportReceipt,
			VenuePolicyName:          input.VenuePolicyName,
			VenuePolicyVersion:       input.VenuePolicyVersion,
			CohortRevision:           cohortRevision,
			RuleSetVersion:           input.RuleSetVersion,
			CitationAnalysisRunID:    input.CitationAnalysisRunID,
			TrendAnalysisRunID:       input.TrendAnalysisRunID,
			JournalAnalysisRunID:     input.JournalAnalysisRunID,
		},
		generatedAt,
	)
	if err != nil {
		return AnalysisRunSummary{}, err
	}

	subjectsByWork, err := loadAcceptedCohortSubjects(
		ctx,
		tx,
		workIDs,
		subjectVersionID,
		input.EligibilityPolicyVersion,
		input.JCRMetricYear,
	)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	publicationTypesByWork, err := loadAcceptedCohortPublicationTypes(
		ctx,
		tx,
		workIDs,
	)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	trends, err := loadOpportunityTrendEvidence(
		ctx,
		tx,
		input.TrendAnalysisRunID,
	)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	evidence := buildSubjectOpportunityEvidence(
		works,
		subjectsByWork,
		publicationTypesByWork,
		input.AsOf,
	)
	opportunities, err := evaluateSubjectOpportunities(
		evidence,
		trends,
		input.RuleSetVersion,
	)
	if err != nil {
		return AnalysisRunSummary{}, err
	}
	if len(opportunities) == 0 {
		return AnalysisRunSummary{}, errors.New(
			"research opportunities require at least one evidence-backed subject candidate",
		)
	}
	for _, opportunity := range opportunities {
		if err := insertResearchOpportunitySnapshot(
			ctx,
			tx,
			runID,
			input,
			cohortRevision,
			generatedAt,
			opportunity,
		); err != nil {
			return AnalysisRunSummary{}, err
		}
	}
	if err := completeAnalysisRun(
		ctx,
		tx,
		runID,
		opportunityRunOutputPayload{
			CohortRevision: cohortRevision,
			SnapshotCount:  len(opportunities),
		},
		generatedAt,
	); err != nil {
		return AnalysisRunSummary{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AnalysisRunSummary{}, fmt.Errorf(
			"commit research opportunities run %s: %w",
			runID,
			err,
		)
	}
	return AnalysisRunSummary{
		RunID:           runID,
		SnapshotCount:   len(opportunities),
		SourceRevisions: []string{cohortRevision},
	}, nil
}

func loadOpportunityUpstreamRuns(
	ctx context.Context,
	tx pgx.Tx,
	input OpportunityAnalysisInput,
) (opportunityUpstreamRuns, error) {
	ids := []uuid.UUID{
		input.CitationAnalysisRunID,
		input.TrendAnalysisRunID,
		input.JournalAnalysisRunID,
	}
	rows, err := tx.Query(ctx, `
		SELECT
			id,
			analysis_type,
			model_provider,
			model_name,
			prompt_version,
			status,
			input_payload,
			completed_at
		FROM analysis_runs
		WHERE id = ANY($1::uuid[])
		ORDER BY id
	`, ids)
	if err != nil {
		return opportunityUpstreamRuns{}, fmt.Errorf(
			"query opportunity upstream analysis runs: %w",
			err,
		)
	}
	defer rows.Close()

	byID := make(map[uuid.UUID]biomedicalUpstreamRun, len(ids))
	for rows.Next() {
		var run biomedicalUpstreamRun
		if err := rows.Scan(
			&run.ID,
			&run.AnalysisType,
			&run.ModelProvider,
			&run.ModelName,
			&run.PromptVersion,
			&run.Status,
			&run.InputPayload,
			&run.CompletedAt,
		); err != nil {
			return opportunityUpstreamRuns{}, fmt.Errorf(
				"scan opportunity upstream analysis run: %w",
				err,
			)
		}
		byID[run.ID] = run
	}
	if err := rows.Err(); err != nil {
		return opportunityUpstreamRuns{}, fmt.Errorf(
			"iterate opportunity upstream analysis runs: %w",
			err,
		)
	}
	if len(byID) != len(ids) {
		return opportunityUpstreamRuns{}, fmt.Errorf(
			"%w: one or more explicit opportunity upstream runs do not exist",
			errAnalysisReferenceConflict,
		)
	}
	return opportunityUpstreamRuns{
		Citation: byID[input.CitationAnalysisRunID],
		Trend:    byID[input.TrendAnalysisRunID],
		Journal:  byID[input.JournalAnalysisRunID],
	}, nil
}

func loadOpportunityTrendEvidence(
	ctx context.Context,
	tx pgx.Tx,
	runID uuid.UUID,
) (map[string]opportunityTrendEvidence, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			entity_type,
			entity_id,
			rate_ratio,
			confidence_level,
			confidence_interval_lower,
			confidence_interval_upper
		FROM publication_trend_snapshots
		WHERE analysis_run_id = $1
		  AND state = 'sufficient_evidence'
		  AND entity_type = 'subject'
		ORDER BY entity_id
	`, runID)
	if err != nil {
		return nil, fmt.Errorf(
			"query opportunity trend evidence: %w",
			err,
		)
	}
	defer rows.Close()

	result := make(map[string]opportunityTrendEvidence)
	for rows.Next() {
		var evidence opportunityTrendEvidence
		if err := rows.Scan(
			&evidence.EntityType,
			&evidence.EntityID,
			&evidence.RateRatio,
			&evidence.ConfidenceLevel,
			&evidence.ConfidenceIntervalLow,
			&evidence.ConfidenceIntervalHigh,
		); err != nil {
			return nil, fmt.Errorf(
				"scan opportunity trend evidence: %w",
				err,
			)
		}
		result[evidence.EntityID] = evidence
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate opportunity trend evidence: %w",
			err,
		)
	}
	return result, nil
}

func buildSubjectOpportunityEvidence(
	works []cohortWork,
	subjectsByWork map[uuid.UUID][]cohortSubject,
	publicationTypesByWork map[uuid.UUID][]cohortPublicationType,
	asOf time.Time,
) []subjectOpportunityEvidence {
	bySubject := make(map[uuid.UUID]*subjectOpportunityEvidence)
	for _, work := range works {
		if work.PublishedAt.After(asOf) {
			continue
		}
		publicationTypes := publicationTypesByWork[work.WorkID]
		for _, subject := range subjectsByWork[work.WorkID] {
			current, exists := bySubject[subject.SubjectID]
			if !exists {
				current = &subjectOpportunityEvidence{
					SubjectID:            subject.SubjectID,
					SubjectSlug:          subject.SubjectSlug,
					EligibleWorkIDs:      make(map[uuid.UUID]struct{}),
					CoveredWorkIDs:       make(map[uuid.UUID]struct{}),
					RCTWorkIDs:           make(map[uuid.UUID]struct{}),
					ObservationalWorkIDs: make(map[uuid.UUID]struct{}),
				}
				bySubject[subject.SubjectID] = current
			}
			current.EligibleWorkIDs[work.WorkID] = struct{}{}
			if len(publicationTypes) == 0 {
				continue
			}
			current.CoveredWorkIDs[work.WorkID] = struct{}{}
			for _, publicationType := range publicationTypes {
				if isRCTPublicationType(
					publicationType.PublicationTypeLbl,
				) {
					current.RCTWorkIDs[work.WorkID] = struct{}{}
				}
				if isObservationalPublicationType(
					publicationType.PublicationTypeLbl,
				) {
					current.ObservationalWorkIDs[work.WorkID] = struct{}{}
				}
			}
		}
	}
	result := make([]subjectOpportunityEvidence, 0, len(bySubject))
	for _, evidence := range bySubject {
		result = append(result, *evidence)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].SubjectSlug < result[right].SubjectSlug
	})
	return result
}

func evaluateSubjectOpportunities(
	evidence []subjectOpportunityEvidence,
	trends map[string]opportunityTrendEvidence,
	ruleSetVersion string,
) ([]ResearchOpportunity, error) {
	result := make([]ResearchOpportunity, 0, len(evidence)*2)
	for _, subject := range evidence {
		coveredCount := len(subject.CoveredWorkIDs)
		eligibleCount := len(subject.EligibleWorkIDs)
		if coveredCount == 0 || eligibleCount == 0 {
			continue
		}
		supportingWorkIDs := uuidSetValues(subject.CoveredWorkIDs)
		observational, err := proportionEstimate(
			string(OpportunityMetricObservationalShare),
			len(subject.ObservationalWorkIDs),
			coveredCount,
		)
		if err != nil {
			return nil, err
		}
		observationalOpportunity, err := EvaluateOpportunity(
			OpportunityInput{
				Rule:              OpportunityRuleObservationalDominance,
				EntityID:          subject.SubjectSlug,
				SupportingWorkIDs: supportingWorkIDs,
				Estimates: []OpportunityEstimate{
					opportunityEstimateFromStatistical(
						OpportunityMetricObservationalShare,
						observational,
					),
				},
				CoveredPaperCount:  coveredCount,
				EligiblePaperCount: eligibleCount,
				Limitations: []string{
					"Publication Type coverage may lag source indexing.",
				},
			},
			ruleSetVersion,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, observationalOpportunity)

		trend, exists := trends[subject.SubjectSlug]
		if !exists {
			continue
		}
		rctShare, err := proportionEstimate(
			string(OpportunityMetricRCTShare),
			len(subject.RCTWorkIDs),
			coveredCount,
		)
		if err != nil {
			return nil, err
		}
		rapidGrowth, err := EvaluateOpportunity(
			OpportunityInput{
				Rule:              OpportunityRuleRapidGrowthLowRCTShare,
				EntityID:          subject.SubjectSlug,
				SupportingWorkIDs: supportingWorkIDs,
				Estimates: []OpportunityEstimate{
					{
						Metric: OpportunityMetricTrendRateRatio,
						Value:  trend.RateRatio,
						ConfidenceInterval: ConfidenceInterval{
							Level: trend.ConfidenceLevel,
							Lower: trend.ConfidenceIntervalLow,
							Upper: trend.ConfidenceIntervalHigh,
						},
					},
					opportunityEstimateFromStatistical(
						OpportunityMetricRCTShare,
						rctShare,
					),
				},
				CoveredPaperCount:  coveredCount,
				EligiblePaperCount: eligibleCount,
				Limitations: []string{
					"Publication Type coverage may lag source indexing.",
					"Publication growth is associative and does not establish unmet clinical need.",
				},
			},
			ruleSetVersion,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, rapidGrowth)
	}
	return result, nil
}

func opportunityEstimateFromStatistical(
	metric OpportunityMetric,
	estimate StatisticalEstimate,
) OpportunityEstimate {
	return OpportunityEstimate{
		Metric: metric,
		Value:  estimate.Value,
		ConfidenceInterval: ConfidenceInterval{
			Level: estimate.ConfidenceInterval.Level,
			Lower: estimate.ConfidenceInterval.Lower,
			Upper: estimate.ConfidenceInterval.Upper,
		},
	}
}

func uuidSetValues(values map[uuid.UUID]struct{}) []uuid.UUID {
	result := make([]uuid.UUID, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].String() < result[right].String()
	})
	return result
}

func insertResearchOpportunitySnapshot(
	ctx context.Context,
	tx pgx.Tx,
	runID uuid.UUID,
	input OpportunityAnalysisInput,
	cohortRevision string,
	generatedAt time.Time,
	opportunity ResearchOpportunity,
) error {
	primaryMetric, secondaryMetric, err := opportunityMetrics(
		opportunity.Rule,
	)
	if err != nil {
		return err
	}
	estimates := make(map[OpportunityMetric]OpportunityEstimate)
	for _, estimate := range opportunity.Estimates {
		estimates[estimate.Metric] = estimate
	}
	primary, exists := estimates[primaryMetric]
	if !exists {
		return fmt.Errorf(
			"research opportunity %s is missing primary metric %s",
			opportunity.EntityID,
			primaryMetric,
		)
	}
	var (
		secondaryName  *string
		secondaryValue *float64
		secondaryLow   *float64
		secondaryHigh  *float64
	)
	if secondaryMetric != "" {
		secondary, found := estimates[secondaryMetric]
		if !found {
			return fmt.Errorf(
				"research opportunity %s is missing secondary metric %s",
				opportunity.EntityID,
				secondaryMetric,
			)
		}
		name := string(secondaryMetric)
		secondaryName = &name
		secondaryValue = &secondary.Value
		secondaryLow = &secondary.ConfidenceInterval.Lower
		secondaryHigh = &secondary.ConfidenceInterval.Upper
	}
	payload, err := marshalJSONObject(map[string]any{
		"rule":            opportunity.Rule,
		"rule_version":    opportunity.RuleVersion,
		"entity_type":     "subject",
		"entity_id":       opportunity.EntityID,
		"status":          opportunity.Status,
		"estimates":       opportunity.Estimates,
		"coverage":        opportunity.Coverage,
		"limitations":     opportunity.Limitations,
		"cohort_revision": cohortRevision,
	})
	if err != nil {
		return fmt.Errorf(
			"encode research opportunity payload: %w",
			err,
		)
	}
	evidence, err := marshalJSONObject(map[string]any{
		"citation_analysis_run_id": input.CitationAnalysisRunID,
		"trend_analysis_run_id":    input.TrendAnalysisRunID,
		"journal_analysis_run_id":  input.JournalAnalysisRunID,
		"supporting_work_count":    len(opportunity.SupportingWorkIDs),
		"cohort_revision":          cohortRevision,
	})
	if err != nil {
		return fmt.Errorf(
			"encode research opportunity evidence: %w",
			err,
		)
	}

	var snapshotID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO research_opportunity_snapshots (
			analysis_run_id,
			rule,
			rule_version,
			entity_type,
			entity_id,
			state,
			supporting_work_count,
			covered_paper_count,
			eligible_paper_count,
			coverage,
			confidence_level,
			primary_metric,
			primary_estimate,
			primary_confidence_interval_lower,
			primary_confidence_interval_upper,
			secondary_metric,
			secondary_estimate,
			secondary_confidence_interval_lower,
			secondary_confidence_interval_upper,
			limitations,
			cohort_revision,
			payload,
			evidence,
			generated_at
		) VALUES (
			$1, $2, $3, 'subject', $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15, $16, $17, $18,
			$19, $20, $21::jsonb, $22::jsonb, $23
		)
		RETURNING id
	`,
		runID,
		string(opportunity.Rule),
		opportunity.RuleVersion,
		opportunity.EntityID,
		string(opportunity.Status),
		len(opportunity.SupportingWorkIDs),
		opportunity.Coverage.Covered,
		opportunity.Coverage.Eligible,
		opportunity.Coverage.Proportion,
		primary.ConfidenceInterval.Level,
		string(primaryMetric),
		primary.Value,
		primary.ConfidenceInterval.Lower,
		primary.ConfidenceInterval.Upper,
		secondaryName,
		secondaryValue,
		secondaryLow,
		secondaryHigh,
		opportunity.Limitations,
		cohortRevision,
		payload,
		evidence,
		generatedAt,
	).Scan(&snapshotID); err != nil {
		return fmt.Errorf(
			"insert research opportunity snapshot %s/%s: %w",
			opportunity.Rule,
			opportunity.EntityID,
			err,
		)
	}
	for index, workID := range opportunity.SupportingWorkIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO research_opportunity_supporting_works (
				analysis_run_id,
				research_opportunity_snapshot_id,
				work_id,
				ordinal
			) VALUES ($1, $2, $3, $4)
		`, runID, snapshotID, workID, index+1); err != nil {
			return fmt.Errorf(
				"insert research opportunity supporting Work %s: %w",
				workID,
				err,
			)
		}
	}
	return nil
}

func opportunityMetrics(
	rule OpportunityRule,
) (OpportunityMetric, OpportunityMetric, error) {
	switch rule {
	case OpportunityRuleRapidGrowthLowRCTShare:
		return OpportunityMetricTrendRateRatio, OpportunityMetricRCTShare, nil
	case OpportunityRuleSingleCenterExternalValidationGap:
		return OpportunityMetricSingleCenterShare,
			OpportunityMetricExternalValidationShare,
			nil
	case OpportunityRuleHighCitationLowOpenData:
		return OpportunityMetricCitationPercentile,
			OpportunityMetricOpenDataShare,
			nil
	case OpportunityRuleObservationalDominance:
		return OpportunityMetricObservationalShare, "", nil
	case OpportunityRuleEmergingMethodLowIndependentTeamCount:
		return OpportunityMetricTrendRateRatio,
			OpportunityMetricIndependentTeamCount,
			nil
	default:
		return "", "", &InvalidInputError{
			Field:  "rule",
			Reason: "is not one of the five predefined rules",
		}
	}
}
