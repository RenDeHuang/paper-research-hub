package database

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
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	testPostgresImage    = "postgres:18-alpine"
	testPostgresUser     = "paper_hub_test"
	testPostgresPassword = "paper-hub-test-password"
	testPostgresDatabase = "postgres"
)

var (
	testPostgresURL string
	testDatabaseID  atomic.Uint64
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(
		ctx,
		testPostgresImage,
		postgres.WithDatabase(testPostgresDatabase),
		postgres.WithUsername(testPostgresUser),
		postgres.WithPassword(testPostgresPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start required %s Testcontainer: %v\n", testPostgresImage, err)
		os.Exit(1)
	}

	testPostgresURL, err = container.ConnectionString(ctx, "sslmode=disable")
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

func newTestDatabase(t *testing.T) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, testPostgresURL)
	if err != nil {
		t.Fatalf("connect to PostgreSQL Testcontainer: %v", err)
	}
	defer conn.Close(context.Background())

	name := fmt.Sprintf("paper_hub_test_%d", testDatabaseID.Add(1))
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create isolated test database: %v", err)
	}

	databaseURL, err := url.Parse(testPostgresURL)
	if err != nil {
		t.Fatalf("parse Testcontainer URL: %v", err)
	}
	databaseURL.Path = "/" + name

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		cleanupConn, cleanupErr := pgx.Connect(cleanupCtx, testPostgresURL)
		if cleanupErr != nil {
			t.Errorf("connect for database cleanup: %v", cleanupErr)
			return
		}
		defer cleanupConn.Close(context.Background())
		if _, cleanupErr = cleanupConn.Exec(
			cleanupCtx,
			"DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)",
		); cleanupErr != nil && !strings.Contains(cleanupErr.Error(), "does not exist") {
			t.Errorf("drop isolated test database: %v", cleanupErr)
		}
	})

	return databaseURL.String()
}
