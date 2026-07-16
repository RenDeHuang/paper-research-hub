package httpapi

import (
	"context"
	"fmt"
	"net/url"
	"os"
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
	httpAPITestPostgresImage    = "postgres:18-alpine"
	httpAPITestPostgresUser     = "paper_hub_httpapi_test"
	httpAPITestPostgresPassword = "paper-hub-httpapi-test-password"
	httpAPITestPostgresDatabase = "postgres"
)

var (
	httpAPITestPostgresURL string
	httpAPITestDatabaseID  atomic.Uint64
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(
		ctx,
		httpAPITestPostgresImage,
		postgres.WithDatabase(httpAPITestPostgresDatabase),
		postgres.WithUsername(httpAPITestPostgresUser),
		postgres.WithPassword(httpAPITestPostgresPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(
			os.Stderr,
			"start required %s Testcontainer: %v\n",
			httpAPITestPostgresImage,
			err,
		)
		os.Exit(1)
	}

	httpAPITestPostgresURL, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = testcontainers.TerminateContainer(container)
		fmt.Fprintf(os.Stderr, "resolve PostgreSQL Testcontainer connection string: %v\n", err)
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

func openHTTPAPITestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	databaseURL := newHTTPAPITestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open HTTP API test pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := database.Up(ctx, pool); err != nil {
		t.Fatalf("apply embedded migrations: %v", err)
	}
	return pool
}

func newHTTPAPITestDatabase(t *testing.T) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, httpAPITestPostgresURL)
	if err != nil {
		t.Fatalf("connect to HTTP API PostgreSQL Testcontainer: %v", err)
	}
	defer conn.Close(context.Background())

	name := fmt.Sprintf("paper_hub_httpapi_test_%d", httpAPITestDatabaseID.Add(1))
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create isolated HTTP API test database: %v", err)
	}

	databaseURL, err := url.Parse(httpAPITestPostgresURL)
	if err != nil {
		t.Fatalf("parse HTTP API Testcontainer URL: %v", err)
	}
	databaseURL.Path = "/" + name

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()

		cleanupConn, cleanupErr := pgx.Connect(cleanupCtx, httpAPITestPostgresURL)
		if cleanupErr != nil {
			t.Errorf("connect for HTTP API database cleanup: %v", cleanupErr)
			return
		}
		defer cleanupConn.Close(context.Background())

		if _, cleanupErr = cleanupConn.Exec(
			cleanupCtx,
			"DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)",
		); cleanupErr != nil && !strings.Contains(cleanupErr.Error(), "does not exist") {
			t.Errorf("drop isolated HTTP API test database: %v", cleanupErr)
		}
	})

	return databaseURL.String()
}
