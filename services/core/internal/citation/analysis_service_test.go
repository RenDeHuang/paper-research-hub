package citation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/biomed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCitationAnalysisInputRequiresExactDeclaredScope(t *testing.T) {
	t.Parallel()

	valid := validCitationAnalysisInput()
	tests := []struct {
		name   string
		mutate func(*AnalysisInput)
		want   string
	}{
		{
			name: "zero as of",
			mutate: func(input *AnalysisInput) {
				input.AsOf = time.Time{}
			},
			want: "as_of",
		},
		{
			name: "untrimmed source",
			mutate: func(input *AnalysisInput) {
				input.Source = " openalex"
			},
			want: "source",
		},
		{
			name: "invalid velocity window",
			mutate: func(input *AnalysisInput) {
				input.VelocityWindowDays = 0
			},
			want: "velocity_window_days",
		},
		{
			name: "small cohort minimum",
			mutate: func(input *AnalysisInput) {
				input.MinimumCohortSize = 1
			},
			want: "minimum_cohort_size",
		},
		{
			name: "unsupported formula",
			mutate: func(input *AnalysisInput) {
				input.FormulaVersion = "citation-intelligence/v2"
			},
			want: CitationIntelligenceFormulaVersion,
		},
		{
			name: "invalid subject version",
			mutate: func(input *AnalysisInput) {
				input.SubjectVersion = " biomedical-jcr-subjects/v1"
			},
			want: "subject_version",
		},
		{
			name: "unsupported eligibility policy",
			mutate: func(input *AnalysisInput) {
				input.EligibilityPolicyVersion =
					"biomedical-public-eligibility/v2"
			},
			want: "biomedical-public-eligibility/v1",
		},
		{
			name: "invalid metric year",
			mutate: func(input *AnalysisInput) {
				input.JCRMetricYear = 1899
			},
			want: "jcr_metric_year",
		},
		{
			name: "missing JCR receipt",
			mutate: func(input *AnalysisInput) {
				input.JCRImportReceipt = uuid.Nil
			},
			want: "jcr_import_receipt",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			input := valid
			test.mutate(&input)
			err := input.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"AnalysisInput.Validate() error = %v, want containing %q",
					err,
					test.want,
				)
			}
		})
	}
}

func TestNewPostgresAnalysisServiceRejectsNilPoolAndClock(t *testing.T) {
	t.Parallel()

	if _, err := NewPostgresAnalysisService(nil, time.Now); err == nil {
		t.Fatal("NewPostgresAnalysisService(nil, clock) error = nil")
	}
	fixture := openCitationPostgresFixture(t)
	if _, err := NewPostgresAnalysisService(fixture.Pool, nil); err == nil {
		t.Fatal("NewPostgresAnalysisService(pool, nil) error = nil")
	}
}

func TestPostgresAnalysisServicePersistsOneImmutableSourceSpecificRun(
	t *testing.T,
) {
	fixture := openCitationPostgresFixture(t)
	input := validCitationAnalysisInput()
	input.MinimumCohortSize = 2
	prepareCitationAnalysisScope(t, fixture.Pool, fixture.WorkID, input)
	insertCurrentPubMedPublicationType(
		t,
		fixture.Pool,
		fixture.WorkID,
	)
	appendAnalysisCitationEvidence(
		t,
		fixture,
		input.Source,
		input.AsOf.Add(-45*24*time.Hour),
		10,
		"baseline",
	)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	if _, err := store.AppendSnapshot(context.Background(), Snapshot{
		WorkID:            fixture.WorkID,
		Source:            input.Source,
		ObservedAt:        fixture.ObservedAt,
		Count:             42,
		SourceRecordID:    fixture.SourceRecordID,
		IngestionJobID:    fixture.IngestionJobID,
		RetrievedAt:       fixture.RetrievedAt,
		Coverage:          1,
		DefinitionVersion: "openalex-cited-by-count/v1",
		DatasetVersion:    "openalex-source-record/current",
	}); err != nil {
		t.Fatalf("AppendSnapshot(current) error = %v", err)
	}

	generatedAt := input.AsOf.Add(time.Hour)
	service, err := NewPostgresAnalysisService(
		fixture.Pool,
		func() time.Time { return generatedAt },
	)
	if err != nil {
		t.Fatalf("NewPostgresAnalysisService() error = %v", err)
	}
	summary, err := service.Analyze(context.Background(), input)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if summary.RunID == uuid.Nil ||
		summary.TotalWorks != 1 ||
		summary.KnownCitationCounts != 1 ||
		summary.KnownVelocities != 1 ||
		summary.PercentileRows != 1 ||
		summary.KnownPercentiles != 0 ||
		summary.InsufficientPercentile != 1 {
		t.Fatalf("Analyze() summary = %#v", summary)
	}

	var (
		runStatus, runType, runSource string
		completedAt                   time.Time
	)
	if err := fixture.Pool.QueryRow(context.Background(), `
		SELECT
			status,
			analysis_type,
			input_payload ->> 'source',
			completed_at
		FROM analysis_runs
		WHERE id = $1
	`, summary.RunID).Scan(
		&runStatus,
		&runType,
		&runSource,
		&completedAt,
	); err != nil {
		t.Fatalf("query citation analysis run: %v", err)
	}
	if runStatus != "succeeded" ||
		runType != CitationIntelligenceAnalysisType ||
		runSource != input.Source ||
		!completedAt.Equal(generatedAt) {
		t.Fatalf(
			"persisted run = status %q type %q source %q completed %s",
			runStatus,
			runType,
			runSource,
			completedAt,
		)
	}

	var (
		countState, velocityState, source, formula string
		countValue                                 int64
		velocityValue                              string
	)
	if err := fixture.Pool.QueryRow(context.Background(), `
		SELECT
			citation_count_state,
			citation_count,
			citation_velocity_state,
			citation_velocity::text,
			source,
			formula_version
		FROM citation_analysis_work_snapshots
		WHERE analysis_run_id = $1
		  AND work_id = $2
	`, summary.RunID, fixture.WorkID).Scan(
		&countState,
		&countValue,
		&velocityState,
		&velocityValue,
		&source,
		&formula,
	); err != nil {
		t.Fatalf("query citation analysis Work snapshot: %v", err)
	}
	if countState != "known" ||
		countValue != 42 ||
		velocityState != "known" ||
		velocityValue == "" ||
		source != input.Source ||
		formula != input.FormulaVersion {
		t.Fatalf(
			"persisted Work analysis = count %q/%d velocity %q/%q source %q formula %q",
			countState,
			countValue,
			velocityState,
			velocityValue,
			source,
			formula,
		)
	}

	var percentileState string
	var cohortSize, minimumSize int
	if err := fixture.Pool.QueryRow(context.Background(), `
		SELECT
			percentile_state,
			cohort_size,
			minimum_cohort_size
		FROM citation_analysis_percentiles
		WHERE analysis_run_id = $1
		  AND work_id = $2
	`, summary.RunID, fixture.WorkID).Scan(
		&percentileState,
		&cohortSize,
		&minimumSize,
	); err != nil {
		t.Fatalf("query citation percentile snapshot: %v", err)
	}
	if percentileState != "insufficient_evidence" ||
		cohortSize != 1 ||
		minimumSize != 2 {
		t.Fatalf(
			"persisted percentile = state %q cohort %d minimum %d",
			percentileState,
			cohortSize,
			minimumSize,
		)
	}

	for _, query := range []string{
		`UPDATE citation_analysis_work_snapshots
		 SET generated_at = generated_at + interval '1 second'
		 WHERE analysis_run_id = $1`,
		`DELETE FROM citation_analysis_percentiles
		 WHERE analysis_run_id = $1`,
	} {
		if _, err := fixture.Pool.Exec(
			context.Background(),
			query,
			summary.RunID,
		); err == nil {
			t.Fatalf("immutable citation analysis mutation succeeded: %s", query)
		}
	}
}

func validCitationAnalysisInput() AnalysisInput {
	return AnalysisInput{
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
		Source:                   "openalex",
		VelocityWindowDays:       30,
		MinimumCohortSize:        20,
		FormulaVersion:           CitationIntelligenceFormulaVersion,
		SubjectVersion:           "biomedical-jcr-subjects/v1",
		EligibilityPolicyVersion: "biomedical-public-eligibility/v1",
		JCRMetricYear:            2025,
		JCRImportReceipt: uuid.MustParse(
			"00000000-0000-0000-0000-000000000501",
		),
	}
}

func prepareCitationAnalysisScope(
	t *testing.T,
	pool *pgxpool.Pool,
	workID uuid.UUID,
	input AnalysisInput,
) {
	t.Helper()
	ctx := context.Background()
	venueID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO venues (
			id,
			venue_type,
			display_title,
			issn_l
		) VALUES ($1, 'journal', 'Citation Analysis Journal', '1234-5679')
	`, venueID); err != nil {
		t.Fatalf("insert analysis Venue: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE works
		SET venue_id = $2,
		    published_at = '2026-06-01T00:00:00Z'
		WHERE id = $1
	`, workID, venueID); err != nil {
		t.Fatalf("bind analysis Work Venue/year: %v", err)
	}

	subjectReceiptID := uuid.New()
	subjectVersionID := uuid.New()
	subjectID := uuid.New()
	subjectRuleID := uuid.New()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin analysis Subject fixture: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `
		INSERT INTO subject_import_receipts (
			id,
			source,
			registry_version,
			file_sha256,
			subject_count,
			rule_count,
			imported_at
		) VALUES (
			$1,
			'medpaperhub-reviewed-jcr-category-allowlist',
			$2,
			$3,
			1,
			1,
			'2026-07-17T08:00:00Z'
		)
	`,
		subjectReceiptID,
		input.SubjectVersion,
		strings.Repeat("a", 64),
	); err != nil {
		t.Fatalf("insert analysis Subject receipt: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO subject_versions (
			id,
			subject_import_receipt_id,
			version_key
		) VALUES ($1, $2, $3)
	`, subjectVersionID, subjectReceiptID, input.SubjectVersion); err != nil {
		t.Fatalf("insert analysis Subject version: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO subjects (
			id,
			subject_version_id,
			slug,
			display_label
		) VALUES ($1, $2, 'oncology', 'Oncology')
	`, subjectID, subjectVersionID); err != nil {
		t.Fatalf("insert analysis Subject: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO biomedical_subject_rules (
			id,
			subject_version_id,
			subject_id,
			jcr_category
		) VALUES ($1, $2, $3, 'Oncology')
	`, subjectRuleID, subjectVersionID, subjectID); err != nil {
		t.Fatalf("insert analysis Subject rule: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit analysis Subject fixture: %v", err)
	}

	metricID := uuid.New()
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin analysis JCR fixture: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipts (
			id,
			file_sha256,
			source,
			imported_at,
			input_rows,
			inserted_rows,
			unchanged_rows
		) VALUES (
			$1,
			$2,
			'authorized-jcr',
			'2026-07-17T08:30:00Z',
			1,
			1,
			0
		)
	`, input.JCRImportReceipt, strings.Repeat("b", 64)); err != nil {
		t.Fatalf("insert analysis JCR receipt: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO venue_metric_snapshots (
			id,
			venue_id,
			metric_year,
			category,
			registry_version,
			edition_year,
			jif,
			jif_rank,
			category_journal_count,
			jif_percentile,
			quartile,
			metric_status,
			source_name,
			source_license,
			captured_at,
			jcr_import_receipt_id
		) VALUES (
			$1,
			$2,
			$3,
			'Oncology',
			'jcr-registry/v2',
			2026,
			12,
			1,
			100,
			99,
			'Q1',
			'known',
			'authorized-jcr',
			'institution-authorized',
			'2026-07-17T08:30:00Z',
			$4
		)
	`, metricID, venueID, input.JCRMetricYear, input.JCRImportReceipt); err != nil {
		t.Fatalf("insert analysis JCR metric: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipt_metrics (
			import_receipt_id,
			metric_snapshot_id
		) VALUES ($1, $2)
	`, input.JCRImportReceipt, metricID); err != nil {
		t.Fatalf("link analysis JCR receipt metric: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO journal_subject_metrics (
			venue_metric_snapshot_id,
			subject_rule_id,
			jcr_category
		) VALUES ($1, $2, 'Oncology')
	`, metricID, subjectRuleID); err != nil {
		t.Fatalf("link analysis Subject metric: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit analysis JCR fixture: %v", err)
	}

	assessmentStore, err := venue.NewPostgresAssessmentStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresAssessmentStore() error = %v", err)
	}
	assessmentService, err := venue.NewAssessmentService(assessmentStore)
	if err != nil {
		t.Fatalf("NewAssessmentService() error = %v", err)
	}
	if _, err := assessmentService.Assess(ctx, venue.AssessmentInput{
		JCRImportReceiptID: input.JCRImportReceipt.String(),
		MetricYear:         input.JCRMetricYear,
		PolicyVersion:      venue.JournalAllQ1PolicyVersion,
		AssessedAt:         input.AsOf.Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("assess analysis Venue policy: %v", err)
	}

	eligibilityStore, err := biomed.NewPostgresPublicEligibilityStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresPublicEligibilityStore() error = %v", err)
	}
	eligibilityService, err := biomed.NewPublicEligibilityService(
		eligibilityStore,
	)
	if err != nil {
		t.Fatalf("NewPublicEligibilityService() error = %v", err)
	}
	assessment, err := eligibilityService.Assess(
		ctx,
		biomed.PublicEligibilityInput{
			WorkID:            workID.String(),
			PolicyVersion:     input.EligibilityPolicyVersion,
			MetricYear:        input.JCRMetricYear,
			SubjectVersionKey: input.SubjectVersion,
			AssessedAt:        input.AsOf.Add(-time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("assess analysis biomedical eligibility: %v", err)
	}
	if assessment.Decision != biomed.PublicEligibilityDecisionAccepted {
		t.Fatalf(
			"analysis eligibility = %q, want accepted",
			assessment.Decision,
		)
	}
}

func insertCurrentPubMedPublicationType(
	t *testing.T,
	pool *pgxpool.Pool,
	workID uuid.UUID,
) {
	t.Helper()
	ctx := context.Background()
	sourceTime := time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC)
	jobID := uuid.New()
	sourceRecordID := uuid.New()
	rawEventID := uuid.New()
	normalizedID := uuid.New()
	projectionID := uuid.New()
	publicationTypeID := uuid.New()
	hash := fmt.Sprintf(
		"%x",
		sha256.Sum256([]byte("pubmed-"+workID.String())),
	)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin PubMed projection fixture: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_jobs (
			id,
			source,
			job_type,
			idempotency_key,
			status,
			payload,
			max_attempts,
			batch_key,
			stage
		) VALUES (
			$1,
			'pubmed',
			'citation-analysis-pubmed',
			$2,
			'succeeded',
			'{}',
			1,
			$2,
			'project'
		)
	`, jobID, "pubmed-"+workID.String()); err != nil {
		t.Fatalf("insert PubMed job: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_records (
			id,
			source,
			source_record_id,
			source_identity,
			source_time,
			content_hash,
			raw_payload,
			retrieved_at
		) VALUES (
			$1,
			'pubmed',
			$2,
			'{"pmid":"900001"}',
			$3,
			$4,
			'{"pmid":"900001"}',
			$3
		)
	`, sourceRecordID, "pmid-"+workID.String(), sourceTime, hash); err != nil {
		t.Fatalf("insert PubMed SourceRecord: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_record_works (source_record_id, work_id)
		VALUES ($1, $2)
	`, sourceRecordID, workID); err != nil {
		t.Fatalf("link PubMed SourceRecord: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_raw_events (
			id,
			job_id,
			logical_source,
			event_key,
			event_kind,
			source_record_id,
			source_time,
			tie_break_key,
			position,
			content_hash,
			raw_format,
			raw_payload
		) VALUES (
			$1,
			$2,
			'pubmed',
			$3,
			'upsert',
			$4,
			$5,
			$4,
			0,
			$6,
			'xml',
			convert_to('<PubmedArticle/>', 'UTF8')
		)
	`,
		rawEventID,
		jobID,
		"pubmed:"+workID.String(),
		"pmid-"+workID.String(),
		sourceTime,
		hash,
	); err != nil {
		t.Fatalf("insert PubMed raw event: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_normalized_records (
			id,
			raw_event_id,
			source_record_uuid,
			normalization_policy_version,
			payload_schema_version,
			normalized_payload
		) VALUES (
			$1,
			$2,
			$3,
			'pubmed-normalization/v1',
			'normalized-record/v2',
			'{"pmid":"900001"}'
		)
	`, normalizedID, rawEventID, sourceRecordID); err != nil {
		t.Fatalf("insert PubMed normalized assertion: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_projection_assertions (
			id,
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			work_id,
			job_id,
			scope_policy_version,
			projection_policy_version,
			record_payload
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			$5,
			$6,
			'citation-analysis-scope/v1',
			'citation-analysis-projection/v1',
			'{"fixture":"pubmed"}'
		)
	`, projectionID, rawEventID, normalizedID, sourceRecordID, workID, jobID); err != nil {
		t.Fatalf("insert PubMed projection assertion: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_projection_states (
			work_id,
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			source_time,
			tie_break_key,
			position,
			scope_policy_version,
			projection_policy_version
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			$5,
			$6,
			0,
			'citation-analysis-scope/v1',
			'citation-analysis-projection/v1'
		)
	`,
		workID,
		rawEventID,
		normalizedID,
		sourceRecordID,
		sourceTime,
		"pmid-"+workID.String(),
	); err != nil {
		t.Fatalf("insert PubMed Work projection state: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO publication_types (
			id,
			publication_type_ui
		) VALUES ($1, 'D016449')
	`, publicationTypeID); err != nil {
		t.Fatalf("insert Publication Type: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_publication_types (
			projection_assertion_id,
			source_record_id,
			work_id,
			publication_type_id,
			source_path,
			publication_type_label
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			'/PubmedArticle/PublicationType[1]',
			'Randomized Controlled Trial'
		)
	`, projectionID, sourceRecordID, workID, publicationTypeID); err != nil {
		t.Fatalf("insert Work Publication Type: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit PubMed projection fixture: %v", err)
	}
}

func appendAnalysisCitationEvidence(
	t *testing.T,
	fixture citationPostgresFixture,
	source string,
	observedAt time.Time,
	count int64,
	label string,
) {
	t.Helper()
	ctx := context.Background()
	jobID := uuid.New()
	sourceRecordID := uuid.New()
	rawEventID := uuid.New()
	normalizedID := uuid.New()
	hash := fmt.Sprintf(
		"%x",
		sha256.Sum256(
			[]byte(label+fixture.WorkID.String()),
		),
	)
	tx, err := fixture.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin citation evidence fixture: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_jobs (
			id,
			source,
			job_type,
			idempotency_key,
			status,
			payload,
			max_attempts,
			batch_key,
			stage
		) VALUES (
			$1,
			$2,
			'citation-analysis-evidence',
			$3,
			'succeeded',
			'{}',
			1,
			$3,
			'project'
		)
	`, jobID, source, label+"-"+fixture.WorkID.String()); err != nil {
		t.Fatalf("insert citation evidence job: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_records (
			id,
			source,
			source_record_id,
			source_identity,
			source_time,
			content_hash,
			raw_payload,
			retrieved_at
		) VALUES (
			$1,
			$2,
			$3,
			'{"id":"analysis-evidence"}',
			$4,
			$5,
			'{"id":"analysis-evidence"}',
			$4
		)
	`,
		sourceRecordID,
		source,
		label+"-"+fixture.WorkID.String(),
		observedAt.UTC(),
		hash,
	); err != nil {
		t.Fatalf("insert citation evidence SourceRecord: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_record_works (source_record_id, work_id)
		VALUES ($1, $2)
	`, sourceRecordID, fixture.WorkID); err != nil {
		t.Fatalf("link citation evidence SourceRecord: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_raw_events (
			id,
			job_id,
			logical_source,
			event_key,
			event_kind,
			source_record_id,
			source_time,
			tie_break_key,
			position,
			content_hash,
			raw_format,
			raw_payload
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			'upsert',
			$5,
			$6,
			$5,
			0,
			$7,
			'json',
			convert_to('{"id":"analysis-evidence"}', 'UTF8')
		)
	`,
		rawEventID,
		jobID,
		source,
		source+":"+label+":"+fixture.WorkID.String(),
		label+"-"+fixture.WorkID.String(),
		observedAt.UTC(),
		hash,
	); err != nil {
		t.Fatalf("insert citation evidence raw event: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_normalized_records (
			id,
			raw_event_id,
			source_record_uuid,
			normalization_policy_version,
			payload_schema_version,
			normalized_payload
		) VALUES (
			$1,
			$2,
			$3,
			'citation-analysis-normalization/v1',
			'normalized-record/v2',
			'{"id":"analysis-evidence"}'
		)
	`, normalizedID, rawEventID, sourceRecordID); err != nil {
		t.Fatalf("insert citation evidence normalized assertion: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_projection_assertions (
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			work_id,
			job_id,
			scope_policy_version,
			projection_policy_version,
			record_payload
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			$5,
			'citation-analysis-scope/v1',
			'citation-analysis-projection/v1',
			'{"fixture":"citation-analysis"}'
		)
	`, rawEventID, normalizedID, sourceRecordID, fixture.WorkID, jobID); err != nil {
		t.Fatalf("insert citation evidence projection: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit citation evidence fixture: %v", err)
	}

	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	if _, err := store.AppendSnapshot(ctx, Snapshot{
		WorkID:            fixture.WorkID,
		Source:            source,
		ObservedAt:        observedAt.UTC(),
		Count:             count,
		SourceRecordID:    sourceRecordID,
		IngestionJobID:    jobID,
		RetrievedAt:       observedAt.UTC(),
		Coverage:          1,
		DefinitionVersion: "openalex-cited-by-count/v1",
		DatasetVersion:    "openalex-source-record/" + label,
	}); err != nil {
		t.Fatalf("AppendSnapshot(%s) error = %v", label, err)
	}
}
