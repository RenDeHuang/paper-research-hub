package contenttruth

import (
	"context"
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
	contentTruthTestPostgresImage    = "postgres:18-alpine"
	contentTruthTestPostgresUser     = "content_truth_test"
	contentTruthTestPostgresPassword = "content-truth-test-password"
	contentTruthTestPostgresDatabase = "postgres"
)

var (
	contentTruthTestPostgresURL string
	contentTruthTestDatabaseID  atomic.Uint64
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(
		ctx,
		contentTruthTestPostgresImage,
		postgres.WithDatabase(contentTruthTestPostgresDatabase),
		postgres.WithUsername(contentTruthTestPostgresUser),
		postgres.WithPassword(contentTruthTestPostgresPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(
			os.Stderr,
			"start required %s Testcontainer: %v\n",
			contentTruthTestPostgresImage,
			err,
		)
		os.Exit(1)
	}

	contentTruthTestPostgresURL, err = container.ConnectionString(
		ctx,
		"sslmode=disable",
	)
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		fmt.Fprintf(
			os.Stderr,
			"resolve contenttruth PostgreSQL Testcontainer connection string: %v\n",
			err,
		)
		os.Exit(1)
	}

	code := m.Run()
	if err := testcontainers.TerminateContainer(container); err != nil {
		fmt.Fprintf(
			os.Stderr,
			"terminate contenttruth PostgreSQL Testcontainer: %v\n",
			err,
		)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func openMigratedContentTruthTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseURL := newContentTruthTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open contenttruth PostgreSQL test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping contenttruth PostgreSQL test pool: %v", err)
	}
	if err := database.Up(ctx, pool); err != nil {
		t.Fatalf("migrate contenttruth PostgreSQL test database: %v", err)
	}
	return pool
}

func newContentTruthTestDatabase(t *testing.T) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, contentTruthTestPostgresURL)
	if err != nil {
		t.Fatalf("connect to contenttruth PostgreSQL Testcontainer: %v", err)
	}
	defer connection.Close(context.Background())

	name := fmt.Sprintf(
		"content_truth_test_%d",
		contentTruthTestDatabaseID.Add(1),
	)
	if _, err := connection.Exec(
		ctx,
		"CREATE DATABASE "+pgx.Identifier{name}.Sanitize(),
	); err != nil {
		t.Fatalf("create isolated contenttruth test database: %v", err)
	}

	databaseURL, err := url.Parse(contentTruthTestPostgresURL)
	if err != nil {
		t.Fatalf("parse contenttruth Testcontainer URL: %v", err)
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
			contentTruthTestPostgresURL,
		)
		if cleanupErr != nil {
			t.Errorf(
				"connect for contenttruth database cleanup: %v",
				cleanupErr,
			)
			return
		}
		defer cleanupConnection.Close(context.Background())
		if _, cleanupErr = cleanupConnection.Exec(
			cleanupCtx,
			"DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)",
		); cleanupErr != nil &&
			!strings.Contains(cleanupErr.Error(), "does not exist") {
			t.Errorf("drop isolated contenttruth test database: %v", cleanupErr)
		}
	})

	return databaseURL.String()
}

func contentTruthTestContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type contentTruthRevisionFixture struct {
	ProjectionAssertionID uuid.UUID
	NormalizedAssertionID uuid.UUID
	SourceRecordID        uuid.UUID
}

func insertContentTruthWork(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	createdAt time.Time,
) uuid.UUID {
	t.Helper()

	workID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO works (
			id,
			canonical_key,
			status,
			title,
			created_at,
			updated_at
		) VALUES ($1, $2, 'active', 'Content truth fixture', $3, $3)
	`,
		workID,
		"doi:10.1000/"+workID.String(),
		createdAt.UTC(),
	); err != nil {
		t.Fatalf("insert contenttruth Work: %v", err)
	}
	return workID
}

func insertContentTruthRevision(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	workID uuid.UUID,
	sourceTime time.Time,
) contentTruthRevisionFixture {
	t.Helper()

	fixture := contentTruthRevisionFixture{
		ProjectionAssertionID: uuid.New(),
		NormalizedAssertionID: uuid.New(),
		SourceRecordID:        uuid.New(),
	}
	jobID := uuid.New()
	rawEventID := uuid.New()
	externalID := uuid.NewString()

	if _, err := tx.Exec(ctx, `
		INSERT INTO source_records (
			id,
			source,
			source_record_id,
			source_identity,
			source_time,
			content_hash,
			raw_payload,
			retrieved_at,
			created_at
		) VALUES (
			$1, 'contenttruth_fixture', $2,
			'{"fixture":true}', $3, $4,
			'{"fixture":true}', $3, $3
		)
	`,
		fixture.SourceRecordID,
		externalID,
		sourceTime.UTC(),
		strings.Repeat("a", 64),
	); err != nil {
		t.Fatalf("insert contenttruth source record: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_record_works (
			source_record_id,
			work_id,
			linked_at
		) VALUES ($1, $2, $3)
	`,
		fixture.SourceRecordID,
		workID,
		sourceTime.UTC(),
	); err != nil {
		t.Fatalf("link contenttruth source record to Work: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_jobs (
			id,
			source,
			job_type,
			idempotency_key,
			status,
			payload,
			attempts,
			max_attempts,
			available_at,
			started_at,
			finished_at,
			created_at,
			updated_at,
			batch_key,
			stage
		) VALUES (
			$1, 'contenttruth_fixture', 'contenttruth_fixture', $2,
			'succeeded', '{"fixture":true}', 1, 1,
			$3, $3, $3, $3, $3, $2, 'project'
		)
	`,
		jobID,
		"contenttruth-"+externalID,
		sourceTime.UTC(),
	); err != nil {
		t.Fatalf("insert contenttruth ingestion job: %v", err)
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
			raw_payload,
			created_at
		) VALUES (
			$1, $2, 'contenttruth_fixture', $3, 'upsert', $3,
			$4, $3, 0, $5, 'json', decode('7b7d', 'hex'), $4
		)
	`,
		rawEventID,
		jobID,
		externalID,
		sourceTime.UTC(),
		strings.Repeat("b", 64),
	); err != nil {
		t.Fatalf("insert contenttruth raw event: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_normalized_records (
			id,
			raw_event_id,
			source_record_uuid,
			normalization_policy_version,
			normalized_payload,
			normalized_at,
			payload_schema_version
		) VALUES (
			$1, $2, $3, 'contenttruth-normalization/v1',
			'{"fixture":true}', $4, 'normalized-record/v4'
		)
	`,
		fixture.NormalizedAssertionID,
		rawEventID,
		fixture.SourceRecordID,
		sourceTime.UTC(),
	); err != nil {
		t.Fatalf("insert contenttruth normalized revision: %v", err)
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
			record_payload,
			projected_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			'contenttruth-scope/v1',
			'contenttruth-projection/v1',
			'{"fixture":true}',
			$7
		)
	`,
		fixture.ProjectionAssertionID,
		rawEventID,
		fixture.NormalizedAssertionID,
		fixture.SourceRecordID,
		workID,
		jobID,
		sourceTime.UTC(),
	); err != nil {
		t.Fatalf("insert contenttruth projection assertion: %v", err)
	}
	return fixture
}
