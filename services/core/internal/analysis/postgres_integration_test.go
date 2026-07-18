package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/biomed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
)

const (
	analysisTrendPostgresImage    = "postgres:18-alpine"
	analysisTrendPostgresUser     = "analysis_trend_test"
	analysisTrendPostgresPassword = "analysis-trend-test-password"
	analysisTrendPostgresDatabase = "analysis_trend_test"

	analysisTrendSubjectVersion = "biomedical-jcr-subjects/v1"
	analysisTrendPolicyName     = "journal-all-q1"
	analysisTrendPolicyVersion  = 2
	analysisTrendMetricYear     = 2025
)

type analysisTrendFixture struct {
	pool                *pgxpool.Pool
	asOf                time.Time
	jcrReceiptID        uuid.UUID
	acceptedWorkIDs     []uuid.UUID
	rejectedVenueWorkID uuid.UUID
	unassessedWorkID    uuid.UUID
}

type analysisTrendWorkRow struct {
	venueID     uuid.UUID
	methodID    uuid.UUID
	publishedAt time.Time
}

type analysisTrendStateCounts struct {
	runs      int
	snapshots int
}

type analysisTrendWindowDeclaration struct {
	AsOf               string `json:"as_of"`
	RecentWindowDays   int    `json:"recent_window_days"`
	BaselineWindowDays int    `json:"baseline_window_days"`
}

type analysisTrendWindowEvidence struct {
	AnalysisRunID       string `json:"analysis_run_id"`
	AsOf                string `json:"as_of"`
	RecentWindowDays    int    `json:"recent_window_days"`
	BaselineWindowDays  int    `json:"baseline_window_days"`
	RecentWindowStart   string `json:"recent_window_start"`
	RecentWindowEnd     string `json:"recent_window_end"`
	BaselineWindowStart string `json:"baseline_window_start"`
	BaselineWindowEnd   string `json:"baseline_window_end"`
}

func TestPostgresAnalysisServicePersistsAcceptedTrendCohortAtomically(
	t *testing.T,
) {
	fixture := openAnalysisTrendFixture(t)
	service, err := NewPostgresAnalysisService(
		fixture.pool,
		func() time.Time { return fixture.asOf.Add(time.Hour) },
	)
	if err != nil {
		t.Fatalf("NewPostgresAnalysisService() error = %v", err)
	}
	input := TrendAnalysisInput{
		AsOf:                           fixture.asOf,
		FormulaVersion:                 PublicationTrendFormulaVersion,
		ModelSelectionRule:             TrendModelSelectionFixedPoisson,
		SubjectVersion:                 analysisTrendSubjectVersion,
		EligibilityPolicyVersion:       biomed.BiomedicalPublicEligibilityPolicyVersion,
		JCRMetricYear:                  analysisTrendMetricYear,
		JCRImportReceipt:               fixture.jcrReceiptID,
		VenuePolicyName:                analysisTrendPolicyName,
		VenuePolicyVersion:             analysisTrendPolicyVersion,
		RecentWindowDays:               7,
		BaselineWindowDays:             14,
		MinimumPaperCount:              1,
		MinimumIndependentJournalCount: 1,
		MinimumIndependentTeamCount:    1,
	}

	summary, err := service.AnalyzePublicationTrends(t.Context(), input)
	if err != nil {
		t.Fatalf("AnalyzePublicationTrends() error = %v", err)
	}
	assertPersistedTrendRun(t, fixture, summary)
	assertTrendAcceptedCohort(t, fixture, summary.RunID)
	assertTrendWindowEvidenceBoundToRun(t, fixture, summary.RunID)
	assertFailedTrendTransactionLeavesNoPartialState(
		t,
		fixture,
		service,
		input,
	)
}

func openAnalysisTrendFixture(t *testing.T) analysisTrendFixture {
	t.Helper()
	containerCtx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(
		containerCtx,
		analysisTrendPostgresImage,
		postgres.WithDatabase(analysisTrendPostgresDatabase),
		postgres.WithUsername(analysisTrendPostgresUser),
		postgres.WithPassword(analysisTrendPostgresPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf(
			"start required %s Testcontainer: %v",
			analysisTrendPostgresImage,
			err,
		)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Errorf("terminate PostgreSQL Testcontainer: %v", err)
		}
	})

	databaseURL, err := container.ConnectionString(
		containerCtx,
		"sslmode=disable",
	)
	if err != nil {
		t.Fatalf("resolve PostgreSQL Testcontainer connection string: %v", err)
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL analysis trend pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := database.Up(t.Context(), pool); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}

	fixture := analysisTrendFixture{
		pool: pool,
		asOf: time.Date(
			2026,
			time.July,
			17,
			12,
			0,
			0,
			0,
			time.UTC,
		),
	}
	venueIDs := insertAnalysisTrendVenues(t, pool)
	importAnalysisTrendSubjectRegistry(t, pool)
	fixture.jcrReceiptID = importAnalysisTrendJCR(t, pool)
	assertAnalysisTrendVenuePolicy(
		t,
		pool,
		fixture.jcrReceiptID,
		fixture.asOf,
	)
	fixture.acceptedWorkIDs,
		fixture.rejectedVenueWorkID,
		fixture.unassessedWorkID =
		insertAnalysisTrendWorks(t, fixture, venueIDs)
	return fixture
}

func insertAnalysisTrendVenues(
	t *testing.T,
	pool *pgxpool.Pool,
) []uuid.UUID {
	t.Helper()
	rows := []struct {
		title string
		issnL string
		issn  string
		eissn string
	}{
		{
			title: "Analysis Trend Journal A",
			issnL: "1234-5679",
			issn:  "1234-5679",
			eissn: "2049-3630",
		},
		{
			title: "Analysis Trend Journal B",
			issnL: "9876-5434",
			issn:  "9876-5434",
			eissn: "1357-2466",
		},
		{
			title: "Analysis Trend Journal Q2 High JIF",
			issnL: "2468-1350",
			issn:  "2468-1350",
			eissn: "8642-9752",
		},
	}
	result := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		var venueID uuid.UUID
		if err := pool.QueryRow(t.Context(), `
			INSERT INTO venues (
				venue_type,
				display_title,
				issn_l,
				issn,
				eissn
			) VALUES ('journal', $1, $2, $3, $4)
			RETURNING id
		`, row.title, row.issnL, row.issn, row.eissn).Scan(&venueID); err != nil {
			t.Fatalf("insert analysis trend Venue %q: %v", row.title, err)
		}
		result = append(result, venueID)
	}
	return result
}

func importAnalysisTrendSubjectRegistry(
	t *testing.T,
	pool *pgxpool.Pool,
) {
	t.Helper()
	importer, err := biomed.NewPostgresSubjectImporter(
		pool,
		func() time.Time {
			return time.Date(
				2026,
				time.July,
				17,
				8,
				0,
				0,
				0,
				time.UTC,
			)
		},
	)
	if err != nil {
		t.Fatalf("NewPostgresSubjectImporter() error = %v", err)
	}
	registry := "source,registry_version,slug,display_label,jcr_category\n" +
		"analysis-trend-reviewed," +
		analysisTrendSubjectVersion +
		",oncology,Oncology,Oncology\n"
	if _, err := importer.Import(
		t.Context(),
		strings.NewReader(registry),
	); err != nil {
		t.Fatalf("Import(analysis trend Subject registry) error = %v", err)
	}
}

func importAnalysisTrendJCR(
	t *testing.T,
	pool *pgxpool.Pool,
) uuid.UUID {
	t.Helper()
	store, err := venue.NewPostgresJCRStore(
		pool,
		venue.PostgresJCRStoreConfig{
			SourceLicense: "institution-authorized-test-fixture",
		},
	)
	if err != nil {
		t.Fatalf("NewPostgresJCRStore() error = %v", err)
	}
	importer, err := venue.NewJCRImporter(
		store,
		store,
		func() time.Time {
			return time.Date(
				2026,
				time.July,
				17,
				8,
				30,
				0,
				0,
				time.UTC,
			)
		},
	)
	if err != nil {
		t.Fatalf("NewJCRImporter() error = %v", err)
	}
	jcrCSV := "title,issn_l,issn,eissn,edition_year,metric_year,category,jif,jif_rank,category_journal_count,jif_percentile,quartile,status,source\n" +
		"Analysis Trend Journal A,1234-5679,1234-5679,2049-3630,2026,2025,Oncology,12,1,100,99,Q1,known,authorized-jcr-trend-test\n" +
		"Analysis Trend Journal B,9876-5434,9876-5434,1357-2466,2026,2025,Oncology,12,1,100,99,Q1,known,authorized-jcr-trend-test\n" +
		"Analysis Trend Journal Q2 High JIF,2468-1350,2468-1350,8642-9752,2026,2025,Oncology,20,30,100,70,Q2,known,authorized-jcr-trend-test\n"
	receipt, err := importer.Import(
		t.Context(),
		strings.NewReader(jcrCSV),
	)
	if err != nil {
		t.Fatalf("Import(analysis trend JCR) error = %v", err)
	}
	var receiptID uuid.UUID
	if err := pool.QueryRow(t.Context(), `
		SELECT id
		FROM jcr_import_receipts
		WHERE file_sha256 = $1
	`, receipt.FileSHA256()).Scan(&receiptID); err != nil {
		t.Fatalf("load persisted analysis trend JCR receipt: %v", err)
	}
	return receiptID
}

func assertAnalysisTrendVenuePolicy(
	t *testing.T,
	pool *pgxpool.Pool,
	receiptID uuid.UUID,
	asOf time.Time,
) {
	t.Helper()
	store, err := venue.NewPostgresAssessmentStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresAssessmentStore() error = %v", err)
	}
	service, err := venue.NewAssessmentService(store)
	if err != nil {
		t.Fatalf("NewAssessmentService() error = %v", err)
	}
	summary, err := service.Assess(
		t.Context(),
		venue.AssessmentInput{
			JCRImportReceiptID: receiptID.String(),
			MetricYear:         analysisTrendMetricYear,
			PolicyVersion:      venue.JournalAllQ1PolicyVersion,
			AssessedAt:         asOf.Add(-2 * time.Hour),
		},
	)
	if err != nil {
		t.Fatalf("Assess(analysis trend Venues) error = %v", err)
	}
	if summary.Total != 3 || summary.Accepted != 2 ||
		summary.Rejected != 1 {
		t.Fatalf(
			"Venue policy summary = %#v, want two Q1 accepts and one Q2 rejection",
			summary,
		)
	}
}

func insertAnalysisTrendWorks(
	t *testing.T,
	fixture analysisTrendFixture,
	venueIDs []uuid.UUID,
) ([]uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	if len(venueIDs) != 3 {
		t.Fatalf("analysis trend Venue count = %d, want 3", len(venueIDs))
	}
	var cohortMethodID, caseControlMethodID, institutionID uuid.UUID
	if err := fixture.pool.QueryRow(t.Context(), `
		INSERT INTO methods (name)
		VALUES ('Prospective cohort analysis')
		RETURNING id
	`).Scan(&cohortMethodID); err != nil {
		t.Fatalf("insert cohort Method: %v", err)
	}
	if err := fixture.pool.QueryRow(t.Context(), `
		INSERT INTO methods (name)
		VALUES ('Case-control analysis')
		RETURNING id
	`).Scan(&caseControlMethodID); err != nil {
		t.Fatalf("insert case-control Method: %v", err)
	}
	if err := fixture.pool.QueryRow(t.Context(), `
		INSERT INTO institutions (display_name, country_code)
		VALUES ('Analysis Trend Medical Center', 'US')
		RETURNING id
	`).Scan(&institutionID); err != nil {
		t.Fatalf("insert analysis trend Institution: %v", err)
	}

	rows := []analysisTrendWorkRow{
		{
			venueID:     venueIDs[0],
			methodID:    cohortMethodID,
			publishedAt: fixture.asOf.Add(-24 * time.Hour),
		},
		{
			venueID:     venueIDs[0],
			methodID:    caseControlMethodID,
			publishedAt: fixture.asOf.Add(-48 * time.Hour),
		},
		{
			venueID:     venueIDs[1],
			methodID:    cohortMethodID,
			publishedAt: fixture.asOf.Add(-72 * time.Hour),
		},
		{
			venueID:     venueIDs[0],
			methodID:    caseControlMethodID,
			publishedAt: fixture.asOf.Add(-7 * 24 * time.Hour),
		},
		{
			venueID:     venueIDs[0],
			methodID:    cohortMethodID,
			publishedAt: fixture.asOf.Add(-8 * 24 * time.Hour),
		},
		{
			venueID:     venueIDs[1],
			methodID:    caseControlMethodID,
			publishedAt: fixture.asOf.Add(-9 * 24 * time.Hour),
		},
		{
			venueID:     venueIDs[1],
			methodID:    caseControlMethodID,
			publishedAt: fixture.asOf.Add(-10 * 24 * time.Hour),
		},
	}

	store, err := biomed.NewPostgresPublicEligibilityStore(fixture.pool)
	if err != nil {
		t.Fatalf("NewPostgresPublicEligibilityStore() error = %v", err)
	}
	service, err := biomed.NewPublicEligibilityService(store)
	if err != nil {
		t.Fatalf("NewPublicEligibilityService() error = %v", err)
	}
	accepted := make([]uuid.UUID, 0, len(rows))
	for index, row := range rows {
		workID := insertAnalysisTrendWork(
			t,
			fixture.pool,
			institutionID,
			index+1,
			row,
		)
		assessment, err := service.Assess(
			t.Context(),
			biomed.PublicEligibilityInput{
				WorkID: workID.String(),
				PolicyVersion: biomed.
					BiomedicalPublicEligibilityPolicyVersion,
				MetricYear:        analysisTrendMetricYear,
				SubjectVersionKey: analysisTrendSubjectVersion,
				AssessedAt: fixture.asOf.Add(
					-time.Duration(90-index) * time.Minute,
				),
			},
		)
		if err != nil {
			t.Fatalf("Assess(accepted Work %d) error = %v", index+1, err)
		}
		if assessment.Decision !=
			biomed.PublicEligibilityDecisionAccepted {
			t.Fatalf(
				"Work %d eligibility = %q, want accepted",
				index+1,
				assessment.Decision,
			)
		}
		accepted = append(accepted, workID)
	}

	rejectedVenueWorkID := insertAnalysisTrendWork(
		t,
		fixture.pool,
		institutionID,
		len(rows)+1,
		analysisTrendWorkRow{
			venueID:     venueIDs[2],
			methodID:    cohortMethodID,
			publishedAt: fixture.asOf.Add(-12 * time.Hour),
		},
	)
	rejectedVenueEligibility, err := service.Assess(
		t.Context(),
		biomed.PublicEligibilityInput{
			WorkID: rejectedVenueWorkID.String(),
			PolicyVersion: biomed.
				BiomedicalPublicEligibilityPolicyVersion,
			MetricYear:        analysisTrendMetricYear,
			SubjectVersionKey: analysisTrendSubjectVersion,
			AssessedAt:        fixture.asOf.Add(-30 * time.Minute),
		},
	)
	if err != nil {
		t.Fatalf("Assess(Q2 high-JIF Work) error = %v", err)
	}
	if rejectedVenueEligibility.Decision !=
		biomed.PublicEligibilityDecisionAccepted {
		t.Fatalf(
			"Q2 high-JIF Work eligibility = %q, want accepted Subject eligibility",
			rejectedVenueEligibility.Decision,
		)
	}
	accepted = append(accepted, rejectedVenueWorkID)

	unassessed := insertAnalysisTrendWork(
		t,
		fixture.pool,
		institutionID,
		len(rows)+2,
		analysisTrendWorkRow{
			venueID:     venueIDs[0],
			methodID:    cohortMethodID,
			publishedAt: fixture.asOf.Add(-12 * time.Hour),
		},
	)
	return accepted, rejectedVenueWorkID, unassessed
}

func insertAnalysisTrendWork(
	t *testing.T,
	pool *pgxpool.Pool,
	institutionID uuid.UUID,
	index int,
	row analysisTrendWorkRow,
) uuid.UUID {
	t.Helper()
	var workID uuid.UUID
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO works (
			canonical_key,
			status,
			title,
			published_at,
			venue_id
		) VALUES ($1, 'active', $2, $3, $4)
		RETURNING id
	`,
		fmt.Sprintf("doi:10.5555/analysis-trend-%d", index),
		fmt.Sprintf("Analysis trend biomedical Work %d", index),
		row.publishedAt,
		row.venueID,
	).Scan(&workID); err != nil {
		t.Fatalf("insert analysis trend Work %d: %v", index, err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO work_methods (work_id, method_id, confidence)
		VALUES ($1, $2, 1)
	`, workID, row.methodID); err != nil {
		t.Fatalf("insert Work %d Method assertion: %v", index, err)
	}

	var authorID uuid.UUID
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO authors (display_name)
		VALUES ($1)
		RETURNING id
	`, fmt.Sprintf("Analysis Trend Author %d", index)).Scan(
		&authorID,
	); err != nil {
		t.Fatalf("insert analysis trend Author %d: %v", index, err)
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO work_authors (
			work_id,
			author_id,
			institution_id,
			author_position,
			is_corresponding
		) VALUES ($1, $2, $3, 1, true)
	`, workID, authorID, institutionID); err != nil {
		t.Fatalf("link Work %d corresponding Institution: %v", index, err)
	}
	return workID
}

func assertPersistedTrendRun(
	t *testing.T,
	fixture analysisTrendFixture,
	summary AnalysisRunSummary,
) {
	t.Helper()
	if summary.RunID == uuid.Nil ||
		len(summary.SourceRevisions) != 1 ||
		len(summary.SourceRevisions[0]) != 64 {
		t.Fatalf("trend summary = %#v, want run ID and one cohort revision", summary)
	}
	var (
		analysisType        string
		status              string
		outputSnapshotCount int
		actualSnapshotCount int
	)
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT
			analysis_type,
			status,
			(output_payload ->> 'snapshot_count')::integer
		FROM analysis_runs
		WHERE id = $1
	`, summary.RunID).Scan(
		&analysisType,
		&status,
		&outputSnapshotCount,
	); err != nil {
		t.Fatalf("query persisted trend analysis run: %v", err)
	}
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM publication_trend_snapshots
		WHERE analysis_run_id = $1
	`, summary.RunID).Scan(&actualSnapshotCount); err != nil {
		t.Fatalf("count persisted publication trend snapshots: %v", err)
	}
	if analysisType != "publication_trends" ||
		status != "succeeded" ||
		actualSnapshotCount == 0 ||
		summary.SnapshotCount != actualSnapshotCount ||
		outputSnapshotCount != actualSnapshotCount {
		t.Fatalf(
			"persisted trend run = type %q status %q summary %d output %d rows %d",
			analysisType,
			status,
			summary.SnapshotCount,
			outputSnapshotCount,
			actualSnapshotCount,
		)
	}
}

func assertTrendAcceptedCohort(
	t *testing.T,
	fixture analysisTrendFixture,
	runID uuid.UUID,
) {
	t.Helper()
	var acceptedCount, unassessedCount int
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM biomedical_publication_eligibility_decisions
		WHERE policy_version = $1
		  AND metric_year = $2
		  AND decision = 'accepted'
	`,
		biomed.BiomedicalPublicEligibilityPolicyVersion,
		analysisTrendMetricYear,
	).Scan(&acceptedCount); err != nil {
		t.Fatalf("count accepted trend cohort: %v", err)
	}
	if acceptedCount != len(fixture.acceptedWorkIDs) {
		t.Fatalf(
			"accepted decisions = %d, want %d",
			acceptedCount,
			len(fixture.acceptedWorkIDs),
		)
	}
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM biomedical_publication_eligibility_decisions
		WHERE work_id = $1
	`, fixture.unassessedWorkID).Scan(&unassessedCount); err != nil {
		t.Fatalf("query unassessed Work eligibility: %v", err)
	}
	if unassessedCount != 0 {
		t.Fatalf(
			"unassessed Work %s has %d eligibility decisions",
			fixture.unassessedWorkID,
			unassessedCount,
		)
	}
	var rejectedVenueDecision string
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT assessment.decision
		FROM works AS work
		JOIN venue_policy_versions AS policy
		  ON policy.policy_name = $2
		 AND policy.version_number = $3
		JOIN venue_policy_assessments AS assessment
		  ON assessment.venue_id = work.venue_id
		 AND assessment.policy_version_id = policy.id
		 AND assessment.metric_year = $4
		WHERE work.id = $1
	`,
		fixture.rejectedVenueWorkID,
		venue.JournalAllQ1PolicyName,
		venue.JournalAllQ1PolicyRevision,
		analysisTrendMetricYear,
	).Scan(&rejectedVenueDecision); err != nil {
		t.Fatalf("query Q2 high-JIF Venue assessment: %v", err)
	}
	if rejectedVenueDecision != "rejected" {
		t.Fatalf(
			"Q2 high-JIF Venue assessment = %q, want rejected",
			rejectedVenueDecision,
		)
	}

	var (
		recentCount         int
		baselineCount       int
		recentWindowStart   time.Time
		recentWindowEnd     time.Time
		baselineWindowStart time.Time
		baselineWindowEnd   time.Time
	)
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT
			recent_paper_count,
			baseline_paper_count,
			recent_window_start,
			recent_window_end,
			baseline_window_start,
			baseline_window_end
		FROM publication_trend_snapshots
		WHERE analysis_run_id = $1
		  AND entity_type = 'subject'
		  AND entity_id = 'oncology'
	`, runID).Scan(
		&recentCount,
		&baselineCount,
		&recentWindowStart,
		&recentWindowEnd,
		&baselineWindowStart,
		&baselineWindowEnd,
	); err != nil {
		t.Fatalf("query Subject trend snapshot: %v", err)
	}
	if recentCount != 3 || baselineCount != 4 {
		t.Fatalf(
			"Subject trend counts = recent %d baseline %d, want 3/4; D-7 must be baseline, not recent",
			recentCount,
			baselineCount,
		)
	}
	calendarDate := time.Date(
		fixture.asOf.UTC().Year(),
		fixture.asOf.UTC().Month(),
		fixture.asOf.UTC().Day(),
		0,
		0,
		0,
		0,
		time.UTC,
	)
	wantRecentStart := calendarDate.AddDate(0, 0, -6)
	wantRecentEnd := fixture.asOf.UTC()
	wantBaselineEnd := wantRecentStart
	wantBaselineStart := wantBaselineEnd.AddDate(0, 0, -7)
	for field, gotAndWant := range map[string][2]time.Time{
		"recent_window_start":   {recentWindowStart, wantRecentStart},
		"recent_window_end":     {recentWindowEnd, wantRecentEnd},
		"baseline_window_start": {baselineWindowStart, wantBaselineStart},
		"baseline_window_end":   {baselineWindowEnd, wantBaselineEnd},
	} {
		if !gotAndWant[0].Equal(gotAndWant[1]) {
			t.Fatalf(
				"%s = %s, want %s",
				field,
				gotAndWant[0].Format(time.RFC3339Nano),
				gotAndWant[1].Format(time.RFC3339Nano),
			)
		}
	}
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT recent_paper_count, baseline_paper_count
		FROM publication_trend_snapshots
		WHERE analysis_run_id = $1
		  AND entity_type = 'method'
		  AND entity_id = 'Prospective cohort analysis'
	`, runID).Scan(&recentCount, &baselineCount); err != nil {
		t.Fatalf("query Method trend snapshot: %v", err)
	}
	if recentCount != 2 || baselineCount != 1 {
		t.Fatalf(
			"Method trend counts = recent %d baseline %d, want 2/1; unassessed Work leaked into accepted cohort",
			recentCount,
			baselineCount,
		)
	}
}

func assertTrendWindowEvidenceBoundToRun(
	t *testing.T,
	fixture analysisTrendFixture,
	runID uuid.UUID,
) {
	t.Helper()
	var (
		rawDeclaration      []byte
		rawEvidence         []byte
		recentWindowStart   time.Time
		recentWindowEnd     time.Time
		baselineWindowStart time.Time
		baselineWindowEnd   time.Time
	)
	if err := fixture.pool.QueryRow(t.Context(), `
		SELECT
			r.input_payload,
			s.evidence,
			s.recent_window_start,
			s.recent_window_end,
			s.baseline_window_start,
			s.baseline_window_end
		FROM analysis_runs AS r
		JOIN publication_trend_snapshots AS s
		  ON s.analysis_run_id = r.id
		WHERE r.id = $1
		  AND s.entity_type = 'subject'
		  AND s.entity_id = 'oncology'
	`, runID).Scan(
		&rawDeclaration,
		&rawEvidence,
		&recentWindowStart,
		&recentWindowEnd,
		&baselineWindowStart,
		&baselineWindowEnd,
	); err != nil {
		t.Fatalf("query persisted trend window evidence: %v", err)
	}

	var declaration analysisTrendWindowDeclaration
	if err := json.Unmarshal(rawDeclaration, &declaration); err != nil {
		t.Fatalf("decode persisted trend window declaration: %v", err)
	}
	var evidence analysisTrendWindowEvidence
	if err := json.Unmarshal(rawEvidence, &evidence); err != nil {
		t.Fatalf("decode persisted trend window evidence: %v", err)
	}

	wantAsOf := fixture.asOf.UTC().Format(time.RFC3339Nano)
	if declaration.AsOf != wantAsOf ||
		declaration.RecentWindowDays != 7 ||
		declaration.BaselineWindowDays != 14 {
		t.Fatalf(
			"trend run window declaration = %#v, want as_of %q and 7/14-day windows",
			declaration,
			wantAsOf,
		)
	}
	for field, gotAndWant := range map[string][2]string{
		"analysis_run_id":       {evidence.AnalysisRunID, runID.String()},
		"as_of":                 {evidence.AsOf, declaration.AsOf},
		"recent_window_start":   {evidence.RecentWindowStart, recentWindowStart.UTC().Format(time.RFC3339Nano)},
		"recent_window_end":     {evidence.RecentWindowEnd, recentWindowEnd.UTC().Format(time.RFC3339Nano)},
		"baseline_window_start": {evidence.BaselineWindowStart, baselineWindowStart.UTC().Format(time.RFC3339Nano)},
		"baseline_window_end":   {evidence.BaselineWindowEnd, baselineWindowEnd.UTC().Format(time.RFC3339Nano)},
	} {
		if gotAndWant[0] != gotAndWant[1] {
			t.Fatalf(
				"trend evidence %s = %q, want %q",
				field,
				gotAndWant[0],
				gotAndWant[1],
			)
		}
	}
	if evidence.RecentWindowDays != declaration.RecentWindowDays ||
		evidence.BaselineWindowDays != declaration.BaselineWindowDays {
		t.Fatalf(
			"trend evidence window days = %d/%d, want run declaration %d/%d",
			evidence.RecentWindowDays,
			evidence.BaselineWindowDays,
			declaration.RecentWindowDays,
			declaration.BaselineWindowDays,
		)
	}
	if evidence.RecentWindowEnd != evidence.AsOf {
		t.Fatalf(
			"trend evidence recent_window_end = %q, want as_of %q",
			evidence.RecentWindowEnd,
			evidence.AsOf,
		)
	}
}

func assertFailedTrendTransactionLeavesNoPartialState(
	t *testing.T,
	fixture analysisTrendFixture,
	service *PostgresAnalysisService,
	input TrendAnalysisInput,
) {
	t.Helper()
	before := loadAnalysisTrendStateCounts(t, fixture.pool)
	if _, err := fixture.pool.Exec(t.Context(), `
		CREATE FUNCTION fail_analysis_trend_method_snapshot()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			IF NEW.entity_type = 'method'
			   AND NEW.entity_id = 'Prospective cohort analysis' THEN
				RAISE EXCEPTION 'forced publication trend snapshot failure';
			END IF;
			RETURN NEW;
		END;
		$$;

		CREATE TRIGGER analysis_trend_snapshot_failure
		BEFORE INSERT ON publication_trend_snapshots
		FOR EACH ROW
		EXECUTE FUNCTION fail_analysis_trend_method_snapshot();
	`); err != nil {
		t.Fatalf("install deterministic trend transaction fault: %v", err)
	}

	_, err := service.AnalyzePublicationTrends(t.Context(), input)
	if err == nil ||
		!strings.Contains(
			err.Error(),
			"forced publication trend snapshot failure",
		) {
		t.Fatalf("AnalyzePublicationTrends(forced failure) error = %v", err)
	}
	after := loadAnalysisTrendStateCounts(t, fixture.pool)
	if after != before {
		t.Fatalf(
			"failed trend transaction left partial state: before %#v after %#v",
			before,
			after,
		)
	}
}

func loadAnalysisTrendStateCounts(
	t *testing.T,
	pool *pgxpool.Pool,
) analysisTrendStateCounts {
	t.Helper()
	var result analysisTrendStateCounts
	if err := pool.QueryRow(t.Context(), `
		SELECT
			(
				SELECT count(*)
				FROM analysis_runs
				WHERE analysis_type = 'publication_trends'
			),
			(SELECT count(*) FROM publication_trend_snapshots)
	`).Scan(&result.runs, &result.snapshots); err != nil {
		t.Fatalf("query publication trend transaction state: %v", err)
	}
	return result
}
