package biomed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
)

const (
	biomedScopeTestPostgresImage    = "postgres:18-alpine"
	biomedScopeTestPostgresUser     = "biomed_scope_test"
	biomedScopeTestPostgresPassword = "biomed-scope-test-password"
	biomedScopeTestPostgresDatabase = "postgres"
)

var (
	biomedScopeTestPostgresURL string
	biomedScopeTestDatabaseID  atomic.Uint64
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(
		ctx,
		biomedScopeTestPostgresImage,
		postgres.WithDatabase(biomedScopeTestPostgresDatabase),
		postgres.WithUsername(biomedScopeTestPostgresUser),
		postgres.WithPassword(biomedScopeTestPostgresPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(
			os.Stderr,
			"start required %s Testcontainer: %v\n",
			biomedScopeTestPostgresImage,
			err,
		)
		os.Exit(1)
	}

	biomedScopeTestPostgresURL, err = container.ConnectionString(
		ctx,
		"sslmode=disable",
	)
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		fmt.Fprintf(
			os.Stderr,
			"resolve PostgreSQL Testcontainer connection string: %v\n",
			err,
		)
		os.Exit(1)
	}

	code := m.Run()
	if err := testcontainers.TerminateContainer(container); err != nil {
		fmt.Fprintf(os.Stderr, "terminate PostgreSQL Testcontainer: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func TestPostgresPublicEligibilityStorePersistsExactImmutableDecisionEvidence(t *testing.T) {
	pool := openBiomedScopeTestPool(t)
	fixture := insertBiomedScopeFixture(t, pool, "Oncology", true)
	store, err := NewPostgresPublicEligibilityStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresPublicEligibilityStore() error = %v", err)
	}
	service, err := NewPublicEligibilityService(store)
	if err != nil {
		t.Fatalf("NewPublicEligibilityService() error = %v", err)
	}
	input := PublicEligibilityInput{
		WorkID:            fixture.workID,
		PolicyVersion:     BiomedicalPublicEligibilityPolicyVersion,
		MetricYear:        2025,
		SubjectVersionKey: eligibilitySubjectVersionKey,
		AssessedAt:        time.Date(2026, time.July, 17, 10, 30, 0, 0, time.UTC),
	}

	first, err := service.Assess(context.Background(), input)
	if err != nil {
		t.Fatalf("Assess(first) error = %v", err)
	}
	second, err := service.Assess(context.Background(), input)
	if err != nil {
		t.Fatalf("Assess(idempotent) error = %v", err)
	}
	if first.Decision != PublicEligibilityDecisionAccepted ||
		second.Decision != PublicEligibilityDecisionAccepted ||
		first.DecisiveJournalSubjectMetricID() == "" ||
		first.DecisiveJournalSubjectMetricID() !=
			second.DecisiveJournalSubjectMetricID() {
		t.Fatalf("assessments = first %#v second %#v", first, second)
	}

	ctx := biomedScopeTestContext(t)
	var (
		count                  int
		decision               string
		policyVersion          string
		metricYear             int
		subjectVersionID       string
		venueID                string
		journalSubjectMetricID string
		rawEvidence            []byte
		assessedAt             time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) OVER (),
			decision,
			policy_version,
			metric_year,
			subject_version_id::text,
			venue_id::text,
			journal_subject_metric_id::text,
			evidence,
			assessed_at
		FROM biomedical_publication_eligibility_decisions
		WHERE work_id = $1
	`, fixture.workID).Scan(
		&count,
		&decision,
		&policyVersion,
		&metricYear,
		&subjectVersionID,
		&venueID,
		&journalSubjectMetricID,
		&rawEvidence,
		&assessedAt,
	); err != nil {
		t.Fatalf("query persisted eligibility: %v", err)
	}
	if count != 1 ||
		decision != "accepted" ||
		policyVersion != BiomedicalPublicEligibilityPolicyVersion ||
		metricYear != 2025 ||
		subjectVersionID != fixture.subjectVersionID ||
		venueID != fixture.venueID ||
		journalSubjectMetricID != first.DecisiveJournalSubjectMetricID() ||
		!assessedAt.Equal(input.AssessedAt) {
		t.Fatalf(
			"persisted eligibility = count %d decision %q policy %q year %d subject %q venue %q link %q time %s",
			count,
			decision,
			policyVersion,
			metricYear,
			subjectVersionID,
			venueID,
			journalSubjectMetricID,
			assessedAt,
		)
	}
	var evidence PublicEligibilityEvidence
	if err := json.Unmarshal(rawEvidence, &evidence); err != nil {
		t.Fatalf("decode persisted eligibility evidence: %v", err)
	}
	if evidence.Reason != "" ||
		len(evidence.Matches) != 1 ||
		evidence.Matches[0].JCRCategory != "Oncology" ||
		evidence.Matches[0].SubjectVersionID != fixture.subjectVersionID {
		t.Fatalf("persisted eligibility evidence = %#v", evidence)
	}
	loadedDecision, found, err := store.FindEligibility(
		ctx,
		fixture.workID,
		BiomedicalPublicEligibilityPolicyVersion,
		2025,
		eligibilitySubjectVersionKey,
	)
	if err != nil {
		t.Fatalf("FindEligibility() error = %v", err)
	}
	if !found || !reflect.DeepEqual(loadedDecision, first) {
		t.Fatalf(
			"FindEligibility() = %#v, %t, want %#v, true",
			loadedDecision,
			found,
			first,
		)
	}
	_, found, err = store.FindEligibility(
		ctx,
		fixture.workID,
		BiomedicalPublicEligibilityPolicyVersion,
		2024,
		eligibilitySubjectVersionKey,
	)
	if err != nil {
		t.Fatalf("FindEligibility(absent) error = %v", err)
	}
	if found {
		t.Fatal("FindEligibility(absent) found = true")
	}

	if _, err := pool.Exec(ctx, `
		UPDATE biomedical_publication_eligibility_decisions
		SET decision = 'rejected'
		WHERE work_id = $1
	`, fixture.workID); err == nil {
		t.Fatal("immutable eligibility UPDATE error = nil")
	}
	if _, err := pool.Exec(ctx, `
		DELETE FROM biomedical_publication_eligibility_decisions
		WHERE work_id = $1
	`, fixture.workID); err == nil {
		t.Fatal("immutable eligibility DELETE error = nil")
	}

	input.AssessedAt = input.AssessedAt.Add(time.Second)
	if _, err := service.Assess(context.Background(), input); err == nil ||
		!strings.Contains(err.Error(), "conflicting immutable") {
		t.Fatalf("Assess(conflicting replay) error = %v", err)
	}
}

func TestPostgresPublicEligibilityStoreKeepsRejectedAndMissingDistinct(t *testing.T) {
	pool := openBiomedScopeTestPool(t)
	rejected := insertBiomedScopeFixture(t, pool, "Clinical Oncology", true)
	missing := insertBiomedScopeFixture(t, pool, "", true)
	titleOnly := insertBiomedScopeFixture(t, pool, "", false)

	store, err := NewPostgresPublicEligibilityStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresPublicEligibilityStore() error = %v", err)
	}
	service, err := NewPublicEligibilityService(store)
	if err != nil {
		t.Fatalf("NewPublicEligibilityService() error = %v", err)
	}
	assessedAt := time.Date(2026, time.July, 17, 10, 45, 0, 0, time.UTC)

	tests := []struct {
		name       string
		workID     string
		want       PublicEligibilityDecision
		wantReason PublicEligibilityReason
	}{
		{
			name:       "controlled non-biomedical JCR category is rejected",
			workID:     rejected.workID,
			want:       PublicEligibilityDecisionRejected,
			wantReason: PublicEligibilityReasonNoExactSubjectMetricLink,
		},
		{
			name:       "declared metric year without metric is missing",
			workID:     missing.workID,
			want:       PublicEligibilityDecisionMissing,
			wantReason: PublicEligibilityReasonMetricYearMissing,
		},
		{
			name:       "title and journal title without strict ISSN stay missing",
			workID:     titleOnly.workID,
			want:       PublicEligibilityDecisionMissing,
			wantReason: PublicEligibilityReasonVenueNotVerifiedJournal,
		},
	}
	for index, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			assessment, err := service.Assess(context.Background(), PublicEligibilityInput{
				WorkID:            test.workID,
				PolicyVersion:     BiomedicalPublicEligibilityPolicyVersion,
				MetricYear:        2025,
				SubjectVersionKey: eligibilitySubjectVersionKey,
				AssessedAt:        assessedAt.Add(time.Duration(index) * time.Second),
			})
			if err != nil {
				t.Fatalf("Assess() error = %v", err)
			}
			if assessment.Decision != test.want ||
				assessment.Evidence.Reason != test.wantReason ||
				len(assessment.Evidence.Matches) != 0 {
				t.Fatalf("assessment = %#v", assessment)
			}
		})
	}

	rows, err := pool.Query(biomedScopeTestContext(t), `
		SELECT decision, evidence ->> 'reason'
		FROM biomedical_publication_eligibility_decisions
		ORDER BY decision, evidence ->> 'reason'
	`)
	if err != nil {
		t.Fatalf("query rejected/missing decisions: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var decision, reason string
		if err := rows.Scan(&decision, &reason); err != nil {
			t.Fatalf("scan rejected/missing decision: %v", err)
		}
		got = append(got, decision+"|"+reason)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate rejected/missing decisions: %v", err)
	}
	want := []string{
		"missing|metric_year_evidence_missing",
		"missing|venue_not_verified_journal",
		"rejected|no_exact_subject_metric_link",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("persisted decisions = %#v, want %#v", got, want)
	}
}

func TestPostgresPublicEligibilityStoreRequiresExactSubjectVersionKeyAndYear(t *testing.T) {
	pool := openBiomedScopeTestPool(t)
	fixture := insertBiomedScopeFixture(t, pool, "Oncology", true)
	store, err := NewPostgresPublicEligibilityStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresPublicEligibilityStore() error = %v", err)
	}
	service, err := NewPublicEligibilityService(store)
	if err != nil {
		t.Fatalf("NewPublicEligibilityService() error = %v", err)
	}

	_, err = service.Assess(context.Background(), PublicEligibilityInput{
		WorkID:            fixture.workID,
		PolicyVersion:     BiomedicalPublicEligibilityPolicyVersion,
		MetricYear:        2024,
		SubjectVersionKey: eligibilitySubjectVersionKey,
		AssessedAt:        time.Date(2026, time.July, 17, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Assess(wrong year) error = %v", err)
	}
	var wrongYearDecision string
	if err := pool.QueryRow(biomedScopeTestContext(t), `
		SELECT decision
		FROM biomedical_publication_eligibility_decisions
		WHERE work_id = $1 AND metric_year = 2024
	`, fixture.workID).Scan(&wrongYearDecision); err != nil {
		t.Fatalf("query wrong-year decision: %v", err)
	}
	if wrongYearDecision != "missing" {
		t.Fatalf("wrong-year decision = %q, want missing", wrongYearDecision)
	}

	_, err = service.Assess(context.Background(), PublicEligibilityInput{
		WorkID:            fixture.workID,
		PolicyVersion:     BiomedicalPublicEligibilityPolicyVersion,
		MetricYear:        2025,
		SubjectVersionKey: "Biomedical-JCR-Subjects/V1",
		AssessedAt:        time.Date(2026, time.July, 17, 11, 1, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Assess(case-changed Subject version) error = %v", err)
	}
}

type biomedScopeFixture struct {
	workID           string
	venueID          string
	subjectVersionID string
}

func insertBiomedScopeFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	category string,
	verifiedVenue bool,
) biomedScopeFixture {
	t.Helper()
	ctx := biomedScopeTestContext(t)

	var venueID string
	issn := any(nil)
	if verifiedVenue {
		sequence := biomedScopeTestDatabaseID.Add(1) % 1000
		issn = fmt.Sprintf("1234-%03dX", sequence)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO venues (
			venue_type,
			display_title,
			issn_l
		) VALUES (
			'journal',
			'Oncology and Biomedical Research Journal',
			$1
		)
		RETURNING id::text
	`, issn).Scan(&venueID); err != nil {
		t.Fatalf("insert eligibility Venue: %v", err)
	}
	var workID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO works (
			canonical_key,
			status,
			title,
			abstract,
			venue_id
		) VALUES (
			$1,
			'active',
			'Oncology biomarker discovery',
			'MeSH-like biomedical words are not controlled eligibility evidence.',
			$2
		)
		RETURNING id::text
	`,
		fmt.Sprintf("openalex:W%d", biomedScopeTestDatabaseID.Add(1)),
		venueID,
	).Scan(&workID); err != nil {
		t.Fatalf("insert eligibility Work: %v", err)
	}
	if category != "" {
		if _, err := pool.Exec(ctx, `
			INSERT INTO venue_metric_snapshots (
				venue_id,
				metric_year,
				category,
				jif,
				quartile,
				metric_status,
				source_name,
				source_license,
				captured_at
			) VALUES (
				$1,
				2025,
				$2,
				1,
				'Q4',
				'known',
				'synthetic-biomed-scope-test',
				'synthetic-only',
				$3
			)
		`,
			venueID,
			category,
			time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
		); err != nil {
			t.Fatalf("insert eligibility metric %q: %v", category, err)
		}
	}
	importer, err := NewPostgresSubjectImporter(
		pool,
		func() time.Time {
			return time.Date(2026, time.July, 17, 9, 30, 0, 0, time.UTC)
		},
	)
	if err != nil {
		t.Fatalf("NewPostgresSubjectImporter() error = %v", err)
	}
	if _, err := importer.Import(
		context.Background(),
		strings.NewReader(
			"source,registry_version,slug,display_label,jcr_category\n"+
				"scope-test,biomedical-jcr-subjects/v1,oncology,Oncology,Oncology\n",
		),
	); err != nil {
		t.Fatalf("Import(Subject registry) error = %v", err)
	}
	var subjectVersionID string
	if err := pool.QueryRow(ctx, `
		SELECT id::text
		FROM subject_versions
		WHERE version_key = $1
	`, eligibilitySubjectVersionKey).Scan(&subjectVersionID); err != nil {
		t.Fatalf("query eligibility Subject version: %v", err)
	}
	return biomedScopeFixture{
		workID:           workID,
		venueID:          venueID,
		subjectVersionID: subjectVersionID,
	}
}

func openBiomedScopeTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := newBiomedScopeTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open biomedical scope PostgreSQL test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping biomedical scope PostgreSQL test pool: %v", err)
	}
	if err := database.Up(ctx, pool); err != nil {
		t.Fatalf("migrate biomedical scope PostgreSQL test database: %v", err)
	}
	return pool
}

func newBiomedScopeTestDatabase(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, biomedScopeTestPostgresURL)
	if err != nil {
		t.Fatalf("connect to biomedical scope PostgreSQL Testcontainer: %v", err)
	}
	defer connection.Close(context.Background())

	name := fmt.Sprintf(
		"biomed_scope_test_%d",
		biomedScopeTestDatabaseID.Add(1),
	)
	if _, err := connection.Exec(
		ctx,
		"CREATE DATABASE "+pgx.Identifier{name}.Sanitize(),
	); err != nil {
		t.Fatalf("create isolated biomedical scope test database: %v", err)
	}
	databaseURL, err := url.Parse(biomedScopeTestPostgresURL)
	if err != nil {
		t.Fatalf("parse biomedical scope Testcontainer URL: %v", err)
	}
	databaseURL.Path = "/" + name

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			15*time.Second,
		)
		defer cleanupCancel()
		cleanupConnection, cleanupErr := pgx.Connect(
			cleanupCtx,
			biomedScopeTestPostgresURL,
		)
		if cleanupErr != nil {
			t.Errorf("connect for biomedical scope database cleanup: %v", cleanupErr)
			return
		}
		defer cleanupConnection.Close(context.Background())
		if _, cleanupErr = cleanupConnection.Exec(
			cleanupCtx,
			"DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)",
		); cleanupErr != nil &&
			!strings.Contains(cleanupErr.Error(), "does not exist") {
			t.Errorf("drop isolated biomedical scope test database: %v", cleanupErr)
		}
	})
	return databaseURL.String()
}

func biomedScopeTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}
