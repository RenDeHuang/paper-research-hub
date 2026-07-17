package citation

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
)

const (
	citationTestPostgresImage    = "postgres:18-alpine"
	citationTestPostgresUser     = "paper_hub_citation_test"
	citationTestPostgresPassword = "paper-hub-citation-test-password"
	citationTestPostgresDatabase = "postgres"
)

var (
	citationTestPostgresURL string
	citationTestDatabaseID  atomic.Uint64
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(
		ctx,
		citationTestPostgresImage,
		postgres.WithDatabase(citationTestPostgresDatabase),
		postgres.WithUsername(citationTestPostgresUser),
		postgres.WithPassword(citationTestPostgresPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(
			os.Stderr,
			"start required %s Testcontainer: %v\n",
			citationTestPostgresImage,
			err,
		)
		os.Exit(1)
	}

	citationTestPostgresURL, err = container.ConnectionString(
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

type citationPostgresFixture struct {
	Pool           *pgxpool.Pool
	WorkID         uuid.UUID
	CitedWorkID    uuid.UUID
	SourceRecordID uuid.UUID
	IngestionJobID uuid.UUID
	ObservedAt     time.Time
	RetrievedAt    time.Time
}

func openCitationPostgresFixture(t *testing.T) citationPostgresFixture {
	t.Helper()

	databaseURL := newCitationTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open citation test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := database.Up(ctx, pool); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}

	var fixture citationPostgresFixture
	fixture.Pool = pool
	if err := pool.QueryRow(ctx, `
		INSERT INTO works (canonical_key, status, title)
		VALUES ('openalex:W2001', 'active', 'Citation source work')
		RETURNING id
	`).Scan(&fixture.WorkID); err != nil {
		t.Fatalf("insert citation source Work: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO works (canonical_key, status, title)
		VALUES ('doi:10.1000/cited-work', 'active', 'Cited work')
		RETURNING id
	`).Scan(&fixture.CitedWorkID); err != nil {
		t.Fatalf("insert cited Work: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO source_records (
			source,
			source_record_id,
			source_identity,
			source_time,
			content_hash,
			raw_payload
		) VALUES (
			'openalex',
			'W2001',
			'{"id":"W2001"}',
			'2026-07-17T08:00:00Z',
			'citation-source-record-hash',
			'{"id":"W2001","cited_by_count":42}'
		)
		RETURNING id, source_time, retrieved_at
	`).Scan(
		&fixture.SourceRecordID,
		&fixture.ObservedAt,
		&fixture.RetrievedAt,
	); err != nil {
		t.Fatalf("insert citation SourceRecord: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO source_record_works (source_record_id, work_id)
		VALUES ($1, $2)
	`, fixture.SourceRecordID, fixture.WorkID); err != nil {
		t.Fatalf("link citation SourceRecord to Work: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_jobs (
			source,
			job_type,
			idempotency_key,
			status,
			payload,
			max_attempts,
			batch_key,
			stage
		) VALUES (
			'openalex',
			'citation-fixture',
			$1,
			'succeeded',
			'{}',
			1,
			$1,
			'project'
		)
		RETURNING id
	`, "citation-fixture-"+fixture.WorkID.String()).Scan(
		&fixture.IngestionJobID,
	); err != nil {
		t.Fatalf("insert citation ingestion job: %v", err)
	}

	contentHash := fmt.Sprintf(
		"%x",
		sha256.Sum256([]byte(fixture.WorkID.String())),
	)
	var rawEventID, normalizedID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_raw_events (
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
			'openalex',
			'openalex:W2001',
			'upsert',
			'W2001',
			$2,
			'W2001',
			0,
			$3,
			'json',
			convert_to('{"id":"W2001"}', 'UTF8')
		)
		RETURNING id
	`, fixture.IngestionJobID, fixture.ObservedAt, contentHash).Scan(
		&rawEventID,
	); err != nil {
		t.Fatalf("insert citation raw event: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_normalized_records (
			raw_event_id,
			source_record_uuid,
			normalization_policy_version,
			payload_schema_version,
			normalized_payload
		) VALUES (
			$1,
			$2,
			'citation-normalization/v1',
			'normalized-record/v2',
			'{"id":"W2001","cited_by_count":42}'
		)
		RETURNING id
	`, rawEventID, fixture.SourceRecordID).Scan(&normalizedID); err != nil {
		t.Fatalf("insert citation normalized assertion: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ingestion_projection_assertions (
			normalized_assertion_id,
			raw_event_id,
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
			'citation-scope/v1',
			'citation-projection/v1',
			'{"id":"W2001","cited_by_count":42}'
		)
	`, normalizedID, rawEventID, fixture.SourceRecordID, fixture.WorkID, fixture.IngestionJobID); err != nil {
		t.Fatalf("insert citation projection assertion: %v", err)
	}

	return fixture
}

func newCitationTestDatabase(t *testing.T) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, citationTestPostgresURL)
	if err != nil {
		t.Fatalf("connect to citation PostgreSQL Testcontainer: %v", err)
	}
	defer conn.Close(context.Background())

	name := fmt.Sprintf(
		"paper_hub_citation_test_%d",
		citationTestDatabaseID.Add(1),
	)
	if _, err := conn.Exec(
		ctx,
		"CREATE DATABASE "+pgx.Identifier{name}.Sanitize(),
	); err != nil {
		t.Fatalf("create isolated citation test database: %v", err)
	}

	databaseURL, err := url.Parse(citationTestPostgresURL)
	if err != nil {
		t.Fatalf("parse citation Testcontainer URL: %v", err)
	}
	databaseURL.Path = "/" + name

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(
			context.Background(),
			15*time.Second,
		)
		defer cleanupCancel()

		cleanupConn, cleanupErr := pgx.Connect(
			cleanupCtx,
			citationTestPostgresURL,
		)
		if cleanupErr != nil {
			t.Errorf(
				"connect for citation database cleanup: %v",
				cleanupErr,
			)
			return
		}
		defer cleanupConn.Close(context.Background())

		if _, cleanupErr = cleanupConn.Exec(
			cleanupCtx,
			"DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)",
		); cleanupErr != nil &&
			!strings.Contains(cleanupErr.Error(), "does not exist") {
			t.Errorf("drop isolated citation database: %v", cleanupErr)
		}
	})

	return databaseURL.String()
}
