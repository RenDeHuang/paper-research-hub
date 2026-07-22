package workfamily

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
)

const (
	workFamilyTestPostgresImage    = "postgres:18-alpine"
	workFamilyTestPostgresUser     = "paper_hub_work_family_test"
	workFamilyTestPostgresPassword = "paper-hub-work-family-test-password"
	workFamilyTestPostgresDatabase = "postgres"
)

var (
	workFamilyTestPostgresURL string
	workFamilyTestDatabaseID  atomic.Uint64
)

type relationDecisionAssertionLockContextKey struct{}

type advisoryLockAttemptCapture struct {
	key     int64
	reached chan struct{}
	mu      sync.Mutex
	seen    map[uint32]struct{}
}

func (capture *advisoryLockAttemptCapture) TraceQueryStart(
	ctx context.Context,
	conn *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "pg_advisory_xact_lock") &&
		len(data.Args) == 1 &&
		data.Args[0] == capture.key {
		backendPID := conn.PgConn().PID()
		capture.mu.Lock()
		_, seen := capture.seen[backendPID]
		if !seen {
			capture.seen[backendPID] = struct{}{}
		}
		capture.mu.Unlock()
		if !seen {
			capture.reached <- struct{}{}
		}
	}
	return ctx
}

func (*advisoryLockAttemptCapture) TraceQueryEnd(
	context.Context,
	*pgx.Conn,
	pgx.TraceQueryEndData,
) {
}

type activeRelationEdgesQueryCapture struct {
	mu      sync.Mutex
	queries []string
}

func (capture *activeRelationEdgesQueryCapture) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "work_relation_decisions AS successor") &&
		(strings.Contains(data.SQL, "affected_work(work_id)") ||
			strings.Contains(data.SQL, "component_assertions")) {
		capture.mu.Lock()
		capture.queries = append(capture.queries, data.SQL)
		capture.mu.Unlock()
	}
	return ctx
}

func (*activeRelationEdgesQueryCapture) TraceQueryEnd(
	context.Context,
	*pgx.Conn,
	pgx.TraceQueryEndData,
) {
}

func (capture *activeRelationEdgesQueryCapture) Queries() []string {
	capture.mu.Lock()
	defer capture.mu.Unlock()

	return append([]string(nil), capture.queries...)
}

func TestActiveRelationEdgesQueryCaptureDoesNotBlockOnThirdMatch(
	t *testing.T,
) {
	capture := &activeRelationEdgesQueryCapture{}
	returned := make(chan struct{})
	go func() {
		for index := 0; index < 3; index++ {
			capture.TraceQueryStart(
				context.Background(),
				nil,
				pgx.TraceQueryStartData{
					SQL: `
						WITH RECURSIVE affected_work(work_id) AS (
							SELECT $1::uuid
						)
						SELECT *
						FROM work_relation_decisions AS successor
					`,
				},
			)
		}
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("third matching query blocked on capture channel capacity")
	}
	queries := capture.Queries()
	if len(queries) != 3 {
		t.Fatalf("captured matching query count = %d, want 3", len(queries))
	}
}

type relationDecisionAssertionLockBarrier struct {
	reached     chan uint32
	release     chan struct{}
	reachedOnce sync.Once
	releaseOnce sync.Once
}

func (barrier *relationDecisionAssertionLockBarrier) TraceQueryStart(
	ctx context.Context,
	conn *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "FROM work_relation_assertions") &&
		strings.Contains(data.SQL, "WHERE id = $1") &&
		strings.Contains(data.SQL, "FOR UPDATE") {
		return context.WithValue(
			ctx,
			relationDecisionAssertionLockContextKey{},
			conn.PgConn().PID(),
		)
	}
	return ctx
}

func (barrier *relationDecisionAssertionLockBarrier) TraceQueryEnd(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryEndData,
) {
	backendPID, matched := ctx.Value(
		relationDecisionAssertionLockContextKey{},
	).(uint32)
	if !matched || data.Err != nil {
		return
	}
	barrier.reachedOnce.Do(func() {
		barrier.reached <- backendPID
		select {
		case <-barrier.release:
		case <-ctx.Done():
		}
	})
}

func (barrier *relationDecisionAssertionLockBarrier) Release() {
	barrier.releaseOnce.Do(func() {
		close(barrier.release)
	})
}

type timelineQueryBarrier struct {
	reached     chan struct{}
	release     chan struct{}
	reachedOnce sync.Once
	releaseOnce sync.Once
}

func (barrier *timelineQueryBarrier) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryStartData,
) context.Context {
	if strings.Contains(data.SQL, "JOIN works AS work") &&
		strings.Contains(data.SQL, "membership.ended_at IS NULL") {
		barrier.reachedOnce.Do(func() {
			close(barrier.reached)
			select {
			case <-barrier.release:
			case <-ctx.Done():
			}
		})
	}
	return ctx
}

func (*timelineQueryBarrier) TraceQueryEnd(
	context.Context,
	*pgx.Conn,
	pgx.TraceQueryEndData,
) {
}

func (barrier *timelineQueryBarrier) Release() {
	barrier.releaseOnce.Do(func() {
		close(barrier.release)
	})
}

func TestTimelineQueryBarrierStopsWaitingWhenContextIsCanceled(t *testing.T) {
	barrier := &timelineQueryBarrier{
		reached: make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(barrier.Release)
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan struct{})
	go func() {
		barrier.TraceQueryStart(
			ctx,
			nil,
			pgx.TraceQueryStartData{
				SQL: `
					SELECT work.id
					FROM work_family_memberships AS membership
					JOIN works AS work ON work.id = membership.work_id
					WHERE membership.ended_at IS NULL
				`,
			},
		)
		close(returned)
	}()

	select {
	case <-barrier.reached:
	case <-time.After(time.Second):
		barrier.Release()
		t.Fatal("timeline barrier did not reach matching query")
	}
	cancel()

	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		barrier.Release()
		<-returned
		t.Fatal("timeline barrier ignored context cancellation")
	}
}

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(
		ctx,
		workFamilyTestPostgresImage,
		postgres.WithDatabase(workFamilyTestPostgresDatabase),
		postgres.WithUsername(workFamilyTestPostgresUser),
		postgres.WithPassword(workFamilyTestPostgresPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(
			os.Stderr,
			"start required %s Testcontainer: %v\n",
			workFamilyTestPostgresImage,
			err,
		)
		os.Exit(1)
	}

	workFamilyTestPostgresURL, err = container.ConnectionString(
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

type workFamilyPostgresFixture struct {
	Pool *pgxpool.Pool
}

func openWorkFamilyPostgresFixture(t *testing.T) workFamilyPostgresFixture {
	t.Helper()

	databaseURL := newWorkFamilyTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open Work Family test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := database.Up(ctx, pool); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}
	return workFamilyPostgresFixture{Pool: pool}
}

func (fixture workFamilyPostgresFixture) insertWork(
	t *testing.T,
	canonicalKey string,
	title string,
	publishedAt time.Time,
) uuid.UUID {
	t.Helper()

	var workID uuid.UUID
	if err := fixture.Pool.QueryRow(t.Context(), `
		INSERT INTO works (
			canonical_key,
			status,
			title,
			published_at
		) VALUES ($1, 'active', $2, $3)
		RETURNING id
	`, canonicalKey, title, publishedAt).Scan(&workID); err != nil {
		t.Fatalf("insert Work %q: %v", canonicalKey, err)
	}
	return workID
}

func (fixture workFamilyPostgresFixture) insertSourceRecord(
	t *testing.T,
	workID uuid.UUID,
	source string,
	sourceRecordKey string,
) uuid.UUID {
	t.Helper()

	var sourceRecordID uuid.UUID
	if err := fixture.Pool.QueryRow(t.Context(), `
		INSERT INTO source_records (
			source,
			source_record_id,
			source_identity,
			source_time,
			content_hash,
			raw_payload
		) VALUES (
			$1,
			$2,
			jsonb_build_object('id', $2::text),
			now(),
			$3,
			jsonb_build_object('id', $2::text)
		)
		RETURNING id
	`, source, sourceRecordKey, "hash-"+sourceRecordKey).Scan(
		&sourceRecordID,
	); err != nil {
		t.Fatalf("insert SourceRecord %q: %v", sourceRecordKey, err)
	}
	if _, err := fixture.Pool.Exec(t.Context(), `
		INSERT INTO source_record_works (source_record_id, work_id)
		VALUES ($1, $2)
	`, sourceRecordID, workID); err != nil {
		t.Fatalf("link SourceRecord %q to Work: %v", sourceRecordKey, err)
	}
	return sourceRecordID
}

func newWorkFamilyTestDatabase(t *testing.T) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, workFamilyTestPostgresURL)
	if err != nil {
		t.Fatalf("connect to Work Family PostgreSQL Testcontainer: %v", err)
	}
	defer conn.Close(context.Background())

	name := fmt.Sprintf(
		"paper_hub_work_family_test_%d",
		workFamilyTestDatabaseID.Add(1),
	)
	if _, err := conn.Exec(
		ctx,
		"CREATE DATABASE "+pgx.Identifier{name}.Sanitize(),
	); err != nil {
		t.Fatalf("create isolated Work Family test database: %v", err)
	}

	databaseURL, err := url.Parse(workFamilyTestPostgresURL)
	if err != nil {
		t.Fatalf("parse Work Family Testcontainer URL: %v", err)
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
			workFamilyTestPostgresURL,
		)
		if cleanupErr != nil {
			t.Errorf(
				"connect for Work Family database cleanup: %v",
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
			t.Errorf("drop isolated Work Family database: %v", cleanupErr)
		}
	})

	return databaseURL.String()
}

func assertWorkFamilyPostgresCode(t *testing.T, err error, code string) {
	t.Helper()

	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		t.Fatalf("error = %v, want PostgreSQL code %s", err, code)
	}
	if postgresError.Code != code {
		t.Fatalf(
			"PostgreSQL code = %s, want %s: %v",
			postgresError.Code,
			code,
			err,
		)
	}
}
