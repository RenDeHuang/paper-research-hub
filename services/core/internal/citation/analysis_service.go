package citation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/biomed"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

const (
	CitationIntelligenceAnalysisType   = "citation_intelligence"
	CitationIntelligenceFormulaVersion = "citation-intelligence/v1"
	citationAnalysisModelProvider      = "internal"
	citationAnalysisModelName          = "deterministic"
	citationVenuePolicyName            = "journal-jif-or-q1"
	citationVenuePolicyVersion         = 1
)

type AnalysisInput struct {
	AsOf                     time.Time
	Source                   string
	VelocityWindowDays       int
	MinimumCohortSize        int
	FormulaVersion           string
	SubjectVersion           string
	EligibilityPolicyVersion string
	JCRMetricYear            int
	JCRImportReceipt         uuid.UUID
}

func (input AnalysisInput) Validate() error {
	if input.AsOf.IsZero() {
		return errors.New("citation analysis as_of is required")
	}
	if input.Source == "" || input.Source != strings.TrimSpace(input.Source) {
		return errors.New(
			"citation analysis source must be non-empty and trimmed",
		)
	}
	if input.VelocityWindowDays < 1 ||
		input.VelocityWindowDays > 3650 {
		return errors.New(
			"citation analysis velocity_window_days must be between 1 and 3650",
		)
	}
	if input.MinimumCohortSize < 2 ||
		input.MinimumCohortSize > 1_000_000 {
		return errors.New(
			"citation analysis minimum_cohort_size must be between 2 and 1000000",
		)
	}
	if input.FormulaVersion != CitationIntelligenceFormulaVersion {
		return fmt.Errorf(
			"citation analysis formula_version must equal %s",
			CitationIntelligenceFormulaVersion,
		)
	}
	if input.SubjectVersion == "" ||
		input.SubjectVersion != strings.TrimSpace(input.SubjectVersion) {
		return errors.New(
			"citation analysis subject_version must be non-empty and trimmed",
		)
	}
	if input.EligibilityPolicyVersion !=
		biomed.BiomedicalPublicEligibilityPolicyVersion {
		return fmt.Errorf(
			"citation analysis eligibility policy must equal %s",
			biomed.BiomedicalPublicEligibilityPolicyVersion,
		)
	}
	if input.JCRMetricYear < 1900 || input.JCRMetricYear > 3000 {
		return errors.New(
			"citation analysis jcr_metric_year must be between 1900 and 3000",
		)
	}
	if input.JCRImportReceipt == uuid.Nil {
		return errors.New("citation analysis jcr_import_receipt is required")
	}
	return nil
}

type AnalysisSummary struct {
	RunID                  uuid.UUID
	TotalWorks             int
	KnownCitationCounts    int
	KnownVelocities        int
	PercentileRows         int
	KnownPercentiles       int
	InsufficientVelocity   int
	InsufficientPercentile int
}

type PostgresAnalysisService struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

func NewPostgresAnalysisService(
	pool *pgxpool.Pool,
	now func() time.Time,
) (*PostgresAnalysisService, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	if now == nil {
		return nil, errors.New("citation analysis clock is required")
	}
	return &PostgresAnalysisService{pool: pool, now: now}, nil
}

type storedCitationSnapshot struct {
	ID uuid.UUID
	Snapshot
}

type analysisWork struct {
	ID                 uuid.UUID
	PublicationYear    int
	HasPublicationYear bool
	SubjectIDs         []uuid.UUID
	PublicationTypeIDs []uuid.UUID
	Snapshots          []storedCitationSnapshot
	Current            *storedCitationSnapshot
}

type citationWorkEvidence struct {
	Source                string               `json:"source"`
	AsOf                  string               `json:"as_of"`
	VelocityWindowDays    int                  `json:"velocity_window_days"`
	CurrentSnapshotID     *uuid.UUID           `json:"current_snapshot_id,omitempty"`
	BaselineSnapshotID    *uuid.UUID           `json:"baseline_snapshot_id,omitempty"`
	ElapsedHours          *string              `json:"elapsed_hours,omitempty"`
	MissingSignals        []string             `json:"missing_signals"`
	SupportingSnapshotIDs []uuid.UUID          `json:"supporting_snapshot_ids"`
	Scope                 citationScopePayload `json:"scope"`
}

type citationScopePayload struct {
	JCRMetricYear            int       `json:"jcr_metric_year"`
	JCRImportReceipt         uuid.UUID `json:"jcr_import_receipt"`
	VenuePolicyName          string    `json:"venue_policy_name"`
	VenuePolicyVersion       int       `json:"venue_policy_version"`
	EligibilityPolicyVersion string    `json:"eligibility_policy_version"`
	SubjectVersion           string    `json:"subject_version"`
	SubjectVersionID         uuid.UUID `json:"subject_version_id"`
}

type citationPercentileEvidence struct {
	Cohort             CitationCohortIdentity `json:"cohort"`
	CohortKey          string                 `json:"cohort_key"`
	MinimumCohortSize  int                    `json:"minimum_cohort_size"`
	SupportingWorkIDs  []uuid.UUID            `json:"supporting_work_ids"`
	CitationSnapshotID uuid.UUID              `json:"citation_snapshot_id"`
	Scope              citationScopePayload   `json:"scope"`
}

type citationAnalysisRunInput struct {
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

type citationAnalysisRunOutput struct {
	TotalWorks             int      `json:"total_works"`
	KnownCitationCounts    int      `json:"known_citation_counts"`
	KnownVelocities        int      `json:"known_velocities"`
	InsufficientVelocity   int      `json:"insufficient_velocity"`
	PercentileRows         int      `json:"percentile_rows"`
	KnownPercentiles       int      `json:"known_percentiles"`
	InsufficientPercentile int      `json:"insufficient_percentile"`
	SourceRevisions        []string `json:"source_revisions"`
}

func (service *PostgresAnalysisService) Analyze(
	ctx context.Context,
	input AnalysisInput,
) (summary AnalysisSummary, returnErr error) {
	if ctx == nil {
		return AnalysisSummary{}, errors.New(
			"citation analysis context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return AnalysisSummary{}, err
	}
	if service == nil || service.pool == nil || service.now == nil {
		return AnalysisSummary{}, errors.New(
			"PostgresAnalysisService is not initialized",
		)
	}
	if err := input.Validate(); err != nil {
		return AnalysisSummary{}, err
	}
	generatedAt := service.now().UTC()
	if generatedAt.IsZero() {
		return AnalysisSummary{}, errors.New(
			"citation analysis clock returned zero time",
		)
	}

	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.Serializable,
		AccessMode: pgx.ReadWrite,
	})
	if err != nil {
		return AnalysisSummary{}, fmt.Errorf(
			"begin citation analysis transaction: %w",
			err,
		)
	}
	defer func() {
		if returnErr != nil {
			_ = tx.Rollback(context.Background())
		}
	}()

	subjectVersionID, err := validateAnalysisReferences(ctx, tx, input)
	if err != nil {
		return AnalysisSummary{}, err
	}
	runID, err := insertRunningAnalysis(
		ctx,
		tx,
		input,
		generatedAt,
	)
	if err != nil {
		return AnalysisSummary{}, err
	}
	summary.RunID = runID

	works, err := loadAnalysisWorks(
		ctx,
		tx,
		input,
		subjectVersionID,
	)
	if err != nil {
		return AnalysisSummary{}, err
	}
	scope := citationScopePayload{
		JCRMetricYear:            input.JCRMetricYear,
		JCRImportReceipt:         input.JCRImportReceipt,
		VenuePolicyName:          citationVenuePolicyName,
		VenuePolicyVersion:       citationVenuePolicyVersion,
		EligibilityPolicyVersion: input.EligibilityPolicyVersion,
		SubjectVersion:           input.SubjectVersion,
		SubjectVersionID:         subjectVersionID,
	}
	revisions := make([]string, 0, len(works))
	for index := range works {
		revision, persistErr := analyzeAndPersistWork(
			ctx,
			tx,
			runID,
			input,
			scope,
			&works[index],
			generatedAt,
			&summary,
		)
		if persistErr != nil {
			return AnalysisSummary{}, persistErr
		}
		revisions = append(revisions, revision)
	}
	percentileRevisions, err := analyzeAndPersistPercentiles(
		ctx,
		tx,
		runID,
		input,
		scope,
		works,
		generatedAt,
		&summary,
	)
	if err != nil {
		return AnalysisSummary{}, err
	}
	revisions = append(revisions, percentileRevisions...)
	sort.Strings(revisions)
	summary.TotalWorks = len(works)

	output := citationAnalysisRunOutput{
		TotalWorks:             summary.TotalWorks,
		KnownCitationCounts:    summary.KnownCitationCounts,
		KnownVelocities:        summary.KnownVelocities,
		InsufficientVelocity:   summary.InsufficientVelocity,
		PercentileRows:         summary.PercentileRows,
		KnownPercentiles:       summary.KnownPercentiles,
		InsufficientPercentile: summary.InsufficientPercentile,
		SourceRevisions:        revisions,
	}
	rawOutput, err := json.Marshal(output)
	if err != nil {
		return AnalysisSummary{}, fmt.Errorf(
			"encode citation analysis output: %w",
			err,
		)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE analysis_runs
		SET status = 'succeeded',
		    output_payload = $2::jsonb,
		    completed_at = $3
		WHERE id = $1
		  AND status = 'running'
	`, runID, rawOutput, generatedAt); err != nil {
		return AnalysisSummary{}, fmt.Errorf(
			"complete citation analysis run %s: %w",
			runID,
			err,
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return AnalysisSummary{}, fmt.Errorf(
			"commit citation analysis run %s: %w",
			runID,
			err,
		)
	}
	return summary, nil
}

func validateAnalysisReferences(
	ctx context.Context,
	tx pgx.Tx,
	input AnalysisInput,
) (uuid.UUID, error) {
	var subjectVersionID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id
		FROM subject_versions
		WHERE version_key = $1
	`, input.SubjectVersion).Scan(&subjectVersionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, fmt.Errorf(
				"citation analysis Subject version %q does not exist",
				input.SubjectVersion,
			)
		}
		return uuid.Nil, fmt.Errorf(
			"load citation analysis Subject version: %w",
			err,
		)
	}

	rows, err := tx.Query(ctx, `
		SELECT DISTINCT metric.metric_year
		FROM jcr_import_receipt_metrics AS receipt_metric
		JOIN venue_metric_snapshots AS metric
		  ON metric.id = receipt_metric.metric_snapshot_id
		WHERE receipt_metric.import_receipt_id = $1
		ORDER BY metric.metric_year
	`, input.JCRImportReceipt)
	if err != nil {
		return uuid.Nil, fmt.Errorf(
			"query citation analysis JCR receipt years: %w",
			err,
		)
	}
	defer rows.Close()
	years := make([]int, 0, 1)
	for rows.Next() {
		var year int
		if err := rows.Scan(&year); err != nil {
			return uuid.Nil, fmt.Errorf(
				"scan citation analysis JCR receipt year: %w",
				err,
			)
		}
		years = append(years, year)
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, fmt.Errorf(
			"iterate citation analysis JCR receipt years: %w",
			err,
		)
	}
	if len(years) != 1 || years[0] != input.JCRMetricYear {
		return uuid.Nil, fmt.Errorf(
			"citation analysis JCR receipt %s has metric years %v, expected exactly %d",
			input.JCRImportReceipt,
			years,
			input.JCRMetricYear,
		)
	}
	return subjectVersionID, nil
}

func insertRunningAnalysis(
	ctx context.Context,
	tx pgx.Tx,
	input AnalysisInput,
	startedAt time.Time,
) (uuid.UUID, error) {
	payload := citationAnalysisRunInput{
		Source:                   input.Source,
		AsOf:                     input.AsOf.UTC().Format(time.RFC3339Nano),
		VelocityWindowDays:       input.VelocityWindowDays,
		MinimumCohortSize:        input.MinimumCohortSize,
		FormulaVersion:           input.FormulaVersion,
		SubjectVersion:           input.SubjectVersion,
		EligibilityPolicyVersion: input.EligibilityPolicyVersion,
		JCRMetricYear:            input.JCRMetricYear,
		JCRImportReceipt:         input.JCRImportReceipt,
		VenuePolicyName:          citationVenuePolicyName,
		VenuePolicyVersion:       citationVenuePolicyVersion,
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return uuid.Nil, fmt.Errorf(
			"encode citation analysis input: %w",
			err,
		)
	}
	var runID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO analysis_runs (
			analysis_type,
			model_provider,
			model_name,
			prompt_version,
			status,
			input_payload,
			started_at
		) VALUES (
			$1, $2, $3, $4, 'running', $5::jsonb, $6
		)
		RETURNING id
	`,
		CitationIntelligenceAnalysisType,
		citationAnalysisModelProvider,
		citationAnalysisModelName,
		input.FormulaVersion,
		rawPayload,
		startedAt,
	).Scan(&runID); err != nil {
		return uuid.Nil, fmt.Errorf(
			"create running citation analysis: %w",
			err,
		)
	}
	return runID, nil
}

func loadAnalysisWorks(
	ctx context.Context,
	tx pgx.Tx,
	input AnalysisInput,
	subjectVersionID uuid.UUID,
) ([]analysisWork, error) {
	rows, err := tx.Query(ctx, `
		SELECT
			work.id,
			EXTRACT(YEAR FROM work.published_at)::integer
		FROM works AS work
		JOIN venues AS venue
		  ON venue.id = work.venue_id
		JOIN subject_versions AS subject_version
		  ON subject_version.id = $2
		 AND subject_version.version_key = $3
		JOIN biomedical_publication_eligibility_decisions AS eligibility
		  ON eligibility.work_id = work.id
		 AND eligibility.policy_version = $4
		 AND eligibility.metric_year = $5
		 AND eligibility.subject_version_id = subject_version.id
		 AND eligibility.decision = 'accepted'
		 AND eligibility.venue_id = venue.id
		JOIN venue_policy_versions AS policy
		  ON policy.policy_name = $6
		 AND policy.version_number = $7
		JOIN venue_policy_assessments AS assessment
		  ON assessment.venue_id = venue.id
		 AND assessment.policy_version_id = policy.id
		 AND assessment.metric_year = $5
		 AND assessment.decision = 'accepted'
		 AND assessment.evidence ->> 'jcr_import_receipt_id' =
		     ($1::uuid)::text
		WHERE work.status = 'active'
		  AND venue.venue_type = 'journal'
		  AND COALESCE(venue.issn_l, venue.issn, venue.eissn) IS NOT NULL
		  AND EXISTS (
				SELECT 1
				FROM jcr_import_receipt_metrics AS receipt_metric
				JOIN venue_metric_snapshots AS metric
				  ON metric.id = receipt_metric.metric_snapshot_id
				WHERE receipt_metric.import_receipt_id = $1
				  AND metric.venue_id = venue.id
				  AND metric.metric_year = $5
				  AND metric.metric_status = 'known'
				  AND (
						metric.quartile = 'Q1'
						OR metric.jif >= 10
				  )
		  )
		  AND EXISTS (
				SELECT 1
				FROM jcr_import_receipt_metrics AS receipt_metric
				JOIN venue_metric_snapshots AS metric
				  ON metric.id = receipt_metric.metric_snapshot_id
				JOIN journal_subject_metrics AS subject_metric
				  ON subject_metric.venue_metric_snapshot_id = metric.id
				JOIN biomedical_subject_rules AS subject_rule
				  ON subject_rule.id = subject_metric.subject_rule_id
				WHERE receipt_metric.import_receipt_id = $1
				  AND subject_metric.id =
				      eligibility.journal_subject_metric_id
				  AND metric.venue_id = venue.id
				  AND metric.metric_year = $5
				  AND subject_rule.subject_version_id = $2
				  AND subject_metric.jcr_category = metric.category
				  AND subject_rule.jcr_category = metric.category
		  )
		ORDER BY work.id
	`,
		input.JCRImportReceipt,
		subjectVersionID,
		input.SubjectVersion,
		input.EligibilityPolicyVersion,
		input.JCRMetricYear,
		citationVenuePolicyName,
		citationVenuePolicyVersion,
	)
	if err != nil {
		return nil, fmt.Errorf("query citation analysis Works: %w", err)
	}
	defer rows.Close()

	works := make([]analysisWork, 0)
	for rows.Next() {
		var (
			work analysisWork
			year pgtype.Int4
		)
		if err := rows.Scan(&work.ID, &year); err != nil {
			return nil, fmt.Errorf(
				"scan citation analysis Work: %w",
				err,
			)
		}
		if year.Valid {
			work.PublicationYear = int(year.Int32)
			work.HasPublicationYear = true
		}
		works = append(works, work)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate citation analysis Works: %w",
			err,
		)
	}
	if len(works) == 0 {
		return works, nil
	}

	workIndexes := make(map[uuid.UUID]int, len(works))
	workIDs := make([]uuid.UUID, len(works))
	for index := range works {
		workIndexes[works[index].ID] = index
		workIDs[index] = works[index].ID
	}
	if err := loadAnalysisSubjects(
		ctx,
		tx,
		input,
		subjectVersionID,
		workIDs,
		workIndexes,
		works,
	); err != nil {
		return nil, err
	}
	if err := loadAnalysisPublicationTypes(
		ctx,
		tx,
		workIDs,
		workIndexes,
		works,
	); err != nil {
		return nil, err
	}
	if err := loadAnalysisCitationSnapshots(
		ctx,
		tx,
		input,
		workIDs,
		workIndexes,
		works,
	); err != nil {
		return nil, err
	}
	return works, nil
}

func loadAnalysisSubjects(
	ctx context.Context,
	tx pgx.Tx,
	input AnalysisInput,
	subjectVersionID uuid.UUID,
	workIDs []uuid.UUID,
	workIndexes map[uuid.UUID]int,
	works []analysisWork,
) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT
			eligibility.work_id,
			subject.id
		FROM biomedical_publication_eligibility_decisions AS eligibility
		JOIN works AS work
		  ON work.id = eligibility.work_id
		JOIN jcr_import_receipt_metrics AS receipt_metric
		  ON receipt_metric.import_receipt_id = $1
		JOIN venue_metric_snapshots AS metric
		  ON metric.id = receipt_metric.metric_snapshot_id
		 AND metric.venue_id = work.venue_id
		 AND metric.metric_year = $2
		JOIN journal_subject_metrics AS subject_metric
		  ON subject_metric.venue_metric_snapshot_id = metric.id
		 AND subject_metric.jcr_category = metric.category
		JOIN biomedical_subject_rules AS subject_rule
		  ON subject_rule.id = subject_metric.subject_rule_id
		 AND subject_rule.subject_version_id = $3
		 AND subject_rule.jcr_category = metric.category
		JOIN subjects AS subject
		  ON subject.id = subject_rule.subject_id
		 AND subject.subject_version_id = subject_rule.subject_version_id
		WHERE eligibility.work_id = ANY($4::uuid[])
		  AND eligibility.policy_version = $5
		  AND eligibility.metric_year = $2
		  AND eligibility.subject_version_id = $3
		  AND eligibility.decision = 'accepted'
		ORDER BY eligibility.work_id, subject.id
	`,
		input.JCRImportReceipt,
		input.JCRMetricYear,
		subjectVersionID,
		workIDs,
		input.EligibilityPolicyVersion,
	)
	if err != nil {
		return fmt.Errorf(
			"query citation analysis Subjects: %w",
			err,
		)
	}
	defer rows.Close()
	for rows.Next() {
		var workID, subjectID uuid.UUID
		if err := rows.Scan(&workID, &subjectID); err != nil {
			return fmt.Errorf(
				"scan citation analysis Subject: %w",
				err,
			)
		}
		index, exists := workIndexes[workID]
		if !exists {
			return fmt.Errorf(
				"citation analysis Subject references out-of-scope Work %s",
				workID,
			)
		}
		works[index].SubjectIDs = append(
			works[index].SubjectIDs,
			subjectID,
		)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf(
			"iterate citation analysis Subjects: %w",
			err,
		)
	}
	return nil
}

func loadAnalysisPublicationTypes(
	ctx context.Context,
	tx pgx.Tx,
	workIDs []uuid.UUID,
	workIndexes map[uuid.UUID]int,
	works []analysisWork,
) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT
			assertion.work_id,
			publication_type.id
		FROM work_projection_states AS state
		JOIN ingestion_projection_assertions AS projection
		  ON projection.work_id = state.work_id
		 AND projection.raw_event_id = state.raw_event_id
		 AND projection.normalized_assertion_id =
		     state.normalized_assertion_id
		 AND projection.source_record_uuid = state.source_record_uuid
		 AND projection.scope_policy_version =
		     state.scope_policy_version
		 AND projection.projection_policy_version =
		     state.projection_policy_version
		JOIN source_records AS source_record
		  ON source_record.id = state.source_record_uuid
		 AND source_record.source = 'pubmed'
		JOIN work_publication_types AS assertion
		  ON assertion.work_id = state.work_id
		 AND assertion.projection_assertion_id = projection.id
		 AND assertion.source_record_id = source_record.id
		JOIN publication_types AS publication_type
		  ON publication_type.id = assertion.publication_type_id
		WHERE assertion.work_id = ANY($1::uuid[])
		ORDER BY assertion.work_id, publication_type.id
	`, workIDs)
	if err != nil {
		return fmt.Errorf(
			"query citation analysis Publication Types: %w",
			err,
		)
	}
	defer rows.Close()
	for rows.Next() {
		var workID, publicationTypeID uuid.UUID
		if err := rows.Scan(&workID, &publicationTypeID); err != nil {
			return fmt.Errorf(
				"scan citation analysis Publication Type: %w",
				err,
			)
		}
		index, exists := workIndexes[workID]
		if !exists {
			return fmt.Errorf(
				"citation analysis Publication Type references out-of-scope Work %s",
				workID,
			)
		}
		works[index].PublicationTypeIDs = append(
			works[index].PublicationTypeIDs,
			publicationTypeID,
		)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf(
			"iterate citation analysis Publication Types: %w",
			err,
		)
	}
	return nil
}

func loadAnalysisCitationSnapshots(
	ctx context.Context,
	tx pgx.Tx,
	input AnalysisInput,
	workIDs []uuid.UUID,
	workIndexes map[uuid.UUID]int,
	works []analysisWork,
) error {
	rows, err := tx.Query(ctx, `
		SELECT
			id,
			work_id,
			source,
			observed_at,
			count,
			source_record_id,
			ingestion_job_id,
			retrieved_at,
			coverage::double precision,
			definition_version,
			dataset_version
		FROM citation_snapshots
		WHERE work_id = ANY($1::uuid[])
		  AND source = $2
		  AND observed_at <= $3
		ORDER BY work_id, observed_at, id
	`, workIDs, input.Source, input.AsOf.UTC())
	if err != nil {
		return fmt.Errorf(
			"query citation analysis snapshots from %s: %w",
			input.Source,
			err,
		)
	}
	defer rows.Close()
	for rows.Next() {
		var stored storedCitationSnapshot
		if err := rows.Scan(
			&stored.ID,
			&stored.WorkID,
			&stored.Source,
			&stored.ObservedAt,
			&stored.Count,
			&stored.SourceRecordID,
			&stored.IngestionJobID,
			&stored.RetrievedAt,
			&stored.Coverage,
			&stored.DefinitionVersion,
			&stored.DatasetVersion,
		); err != nil {
			return fmt.Errorf(
				"scan citation analysis snapshot: %w",
				err,
			)
		}
		index, exists := workIndexes[stored.WorkID]
		if !exists {
			return fmt.Errorf(
				"citation snapshot references out-of-scope Work %s",
				stored.WorkID,
			)
		}
		works[index].Snapshots = append(
			works[index].Snapshots,
			stored,
		)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf(
			"iterate citation analysis snapshots: %w",
			err,
		)
	}
	return nil
}

func analyzeAndPersistWork(
	ctx context.Context,
	tx pgx.Tx,
	runID uuid.UUID,
	input AnalysisInput,
	scope citationScopePayload,
	work *analysisWork,
	generatedAt time.Time,
	summary *AnalysisSummary,
) (string, error) {
	snapshots := make([]Snapshot, len(work.Snapshots))
	snapshotIDs := make([]uuid.UUID, len(work.Snapshots))
	for index := range work.Snapshots {
		snapshots[index] = work.Snapshots[index].Snapshot
		snapshotIDs[index] = work.Snapshots[index].ID
	}

	var (
		currentSnapshotID  *uuid.UUID
		baselineSnapshotID *uuid.UUID
		citationCount      *int64
		citationVelocity   *string
		elapsedHours       *string
		countState         = "missing"
		velocityState      = "insufficient_evidence"
		missingSignals     = make([]string, 0, 4)
	)
	if len(work.Snapshots) == 0 {
		missingSignals = append(
			missingSignals,
			"current_snapshot",
			"baseline_snapshot",
		)
	} else {
		current := work.Snapshots[len(work.Snapshots)-1]
		work.Current = &current
		currentSnapshotID = &current.ID
		count := current.Count
		citationCount = &count
		countState = "known"
		summary.KnownCitationCounts++

		result, err := AnalyzeCitationVelocity(CitationVelocityInput{
			Source:    input.Source,
			AsOf:      input.AsOf.UTC(),
			Window:    time.Duration(input.VelocityWindowDays) * 24 * time.Hour,
			Snapshots: snapshots,
		})
		if err == nil {
			baseline, found := storedSnapshotForBoundary(
				work.Snapshots,
				result.Start,
			)
			if !found {
				return "", fmt.Errorf(
					"citation analysis Work %s baseline boundary has no stored snapshot identity",
					work.ID,
				)
			}
			end, found := storedSnapshotForBoundary(
				work.Snapshots,
				result.End,
			)
			if !found || end.ID != current.ID {
				return "", fmt.Errorf(
					"citation analysis Work %s current boundary changed during analysis",
					work.ID,
				)
			}
			baselineSnapshotID = &baseline.ID
			value := result.Velocity.String()
			citationVelocity = &value
			hours := decimal.NewFromInt(
				result.Elapsed.Milliseconds(),
			).Div(decimal.NewFromInt(int64(time.Hour / time.Millisecond)))
			hoursText := hours.String()
			elapsedHours = &hoursText
			velocityState = "known"
			summary.KnownVelocities++
		} else {
			var insufficient *InsufficientEvidenceError
			if !errors.As(err, &insufficient) {
				return "", fmt.Errorf(
					"analyze citation velocity for Work %s: %w",
					work.ID,
					err,
				)
			}
			missingSignals = append(
				missingSignals,
				insufficient.Missing...,
			)
		}
	}
	if !work.HasPublicationYear {
		missingSignals = append(missingSignals, "publication_year")
	}
	if len(work.SubjectIDs) == 0 {
		missingSignals = append(missingSignals, "subject")
	}
	if len(work.PublicationTypeIDs) == 0 {
		missingSignals = append(missingSignals, "publication_type")
	}
	sort.Strings(missingSignals)
	evidence := citationWorkEvidence{
		Source:                input.Source,
		AsOf:                  input.AsOf.UTC().Format(time.RFC3339Nano),
		VelocityWindowDays:    input.VelocityWindowDays,
		CurrentSnapshotID:     currentSnapshotID,
		BaselineSnapshotID:    baselineSnapshotID,
		ElapsedHours:          elapsedHours,
		MissingSignals:        missingSignals,
		SupportingSnapshotIDs: snapshotIDs,
		Scope:                 scope,
	}
	revision, rawEvidence, err := evidenceRevision(evidence)
	if err != nil {
		return "", fmt.Errorf(
			"encode citation Work %s evidence: %w",
			work.ID,
			err,
		)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO citation_analysis_work_snapshots (
			analysis_run_id,
			work_id,
			source,
			as_of,
			velocity_window_days,
			citation_count_state,
			current_snapshot_id,
			citation_count,
			citation_velocity_state,
			baseline_snapshot_id,
			citation_velocity,
			source_revision,
			formula_version,
			evidence,
			generated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11::numeric, $12, $13, $14::jsonb, $15
		)
	`,
		runID,
		work.ID,
		input.Source,
		input.AsOf.UTC(),
		input.VelocityWindowDays,
		countState,
		currentSnapshotID,
		citationCount,
		velocityState,
		baselineSnapshotID,
		citationVelocity,
		revision,
		input.FormulaVersion,
		rawEvidence,
		generatedAt,
	); err != nil {
		return "", fmt.Errorf(
			"persist citation analysis Work %s: %w",
			work.ID,
			err,
		)
	}
	if velocityState == "insufficient_evidence" {
		summary.InsufficientVelocity++
	}
	return revision, nil
}

func analyzeAndPersistPercentiles(
	ctx context.Context,
	tx pgx.Tx,
	runID uuid.UUID,
	input AnalysisInput,
	scope citationScopePayload,
	works []analysisWork,
	generatedAt time.Time,
	summary *AnalysisSummary,
) ([]string, error) {
	cohorts := make(map[CitationCohortIdentity][]CitationPercentileObservation)
	currentSnapshots := make(map[uuid.UUID]storedCitationSnapshot)
	for _, work := range works {
		if work.Current == nil ||
			!work.HasPublicationYear ||
			len(work.SubjectIDs) == 0 ||
			len(work.PublicationTypeIDs) == 0 {
			continue
		}
		currentSnapshots[work.ID] = *work.Current
		for _, subjectID := range work.SubjectIDs {
			for _, publicationTypeID := range work.PublicationTypeIDs {
				cohort := CitationCohortIdentity{
					SubjectVersionID:  scope.SubjectVersionID,
					SubjectID:         subjectID,
					PublicationYear:   work.PublicationYear,
					PublicationTypeID: publicationTypeID,
				}
				cohorts[cohort] = append(
					cohorts[cohort],
					CitationPercentileObservation{
						WorkID: work.ID,
						Cohort: cohort,
						Count:  work.Current.Count,
					},
				)
			}
		}
	}

	keys := make([]CitationCohortIdentity, 0, len(cohorts))
	for cohort := range cohorts {
		keys = append(keys, cohort)
	}
	sort.Slice(keys, func(left, right int) bool {
		return cohortKey(keys[left]) < cohortKey(keys[right])
	})
	revisions := make([]string, 0)
	for _, cohort := range keys {
		members := cohorts[cohort]
		sort.Slice(members, func(left, right int) bool {
			return members[left].WorkID.String() <
				members[right].WorkID.String()
		})
		supportingIDs := make([]uuid.UUID, len(members))
		for index := range members {
			supportingIDs[index] = members[index].WorkID
		}
		key := cohortKey(cohort)
		for targetIndex := range members {
			target := members[targetIndex]
			observations := make(
				[]CitationPercentileObservation,
				0,
				len(members)-1,
			)
			observations = append(
				observations,
				members[:targetIndex]...,
			)
			observations = append(
				observations,
				members[targetIndex+1:]...,
			)
			result, err := AnalyzeCitationPercentile(
				CitationPercentileInput{
					Cohort:            cohort,
					Target:            target,
					Observations:      observations,
					MinimumCohortSize: input.MinimumCohortSize,
				},
			)
			state := "known"
			var midrank, percentile *string
			if err == nil {
				rankText := result.Rank.String()
				percentileText := result.Percentile.String()
				midrank = &rankText
				percentile = &percentileText
				summary.KnownPercentiles++
			} else {
				var insufficient *InsufficientEvidenceError
				if !errors.As(err, &insufficient) {
					return nil, fmt.Errorf(
						"analyze citation percentile for Work %s cohort %s: %w",
						target.WorkID,
						key,
						err,
					)
				}
				state = "insufficient_evidence"
				summary.InsufficientPercentile++
			}
			current := currentSnapshots[target.WorkID]
			evidence := citationPercentileEvidence{
				Cohort:             cohort,
				CohortKey:          key,
				MinimumCohortSize:  input.MinimumCohortSize,
				SupportingWorkIDs:  supportingIDs,
				CitationSnapshotID: current.ID,
				Scope:              scope,
			}
			revision, rawEvidence, err := evidenceRevision(evidence)
			if err != nil {
				return nil, fmt.Errorf(
					"encode citation percentile Work %s evidence: %w",
					target.WorkID,
					err,
				)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO citation_analysis_percentiles (
					analysis_run_id,
					work_id,
					subject_version_id,
					subject_id,
					publication_year,
					publication_type_id,
					source,
					citation_snapshot_id,
					citation_count,
					cohort_key,
					cohort_size,
					minimum_cohort_size,
					percentile_state,
					midrank,
					citation_percentile,
					source_revision,
					formula_version,
					evidence,
					generated_at
				) VALUES (
					$1, $2, $3, $4, $5, $6, $7, $8, $9,
					$10, $11, $12, $13, $14::numeric,
					$15::numeric, $16, $17, $18::jsonb, $19
				)
			`,
				runID,
				target.WorkID,
				cohort.SubjectVersionID,
				cohort.SubjectID,
				cohort.PublicationYear,
				cohort.PublicationTypeID,
				input.Source,
				current.ID,
				current.Count,
				key,
				len(members),
				input.MinimumCohortSize,
				state,
				midrank,
				percentile,
				revision,
				input.FormulaVersion,
				rawEvidence,
				generatedAt,
			); err != nil {
				return nil, fmt.Errorf(
					"persist citation percentile Work %s cohort %s: %w",
					target.WorkID,
					key,
					err,
				)
			}
			summary.PercentileRows++
			revisions = append(revisions, revision)
		}
	}
	return revisions, nil
}

func storedSnapshotForBoundary(
	snapshots []storedCitationSnapshot,
	boundary Snapshot,
) (storedCitationSnapshot, bool) {
	for _, snapshot := range snapshots {
		if snapshot.WorkID == boundary.WorkID &&
			snapshot.Source == boundary.Source &&
			snapshot.ObservedAt.Equal(boundary.ObservedAt) &&
			snapshot.Count == boundary.Count {
			return snapshot, true
		}
	}
	return storedCitationSnapshot{}, false
}

func cohortKey(cohort CitationCohortIdentity) string {
	return fmt.Sprintf(
		"subject-version:%s:subject:%s:year:%d:publication-type:%s",
		cohort.SubjectVersionID,
		cohort.SubjectID,
		cohort.PublicationYear,
		cohort.PublicationTypeID,
	)
}

func evidenceRevision(value any) (string, []byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), raw, nil
}
