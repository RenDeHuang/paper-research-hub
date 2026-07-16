package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
)

func TestRealMainRunsUpWithStrictSharedConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var received config.Config
	called := 0
	code := realMain(
		context.Background(),
		[]string{"up"},
		&stdout,
		&stderr,
		mapLookup(map[string]string{
			"DATABASE_URL": "postgres://paper:secret@localhost/papers",
			"API_PORT":     "9090",
			"DB_MAX_CONNS": "24",
		}),
		func(_ context.Context, cfg config.Config) error {
			called++
			received = cfg
			return nil
		},
	)

	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	if called != 1 {
		t.Fatalf("migration runner calls = %d, want 1", called)
	}
	if received.HTTP.Port != 9090 || received.Database.MaxConns != 24 {
		t.Fatalf("runner config = %+v, want shared HTTP/DB configuration", received.Redacted())
	}
	if !strings.Contains(stdout.String(), "migrations applied") {
		t.Fatalf("stdout = %q, want success message", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRealMainFailsWithoutDatabaseURLAndDoesNotLeakSecrets(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	code := realMain(
		context.Background(),
		[]string{"up"},
		&stdout,
		&stderr,
		mapLookup(map[string]string{
			"DATABASE_URL": "mysql://root:database-secret@localhost/papers",
		}),
		func(context.Context, config.Config) error {
			called = true
			return nil
		},
	)

	if code == 0 {
		t.Fatal("realMain() code = 0, want failure")
	}
	if called {
		t.Fatal("migration runner called after configuration failure")
	}
	if strings.Contains(stderr.String(), "database-secret") {
		t.Fatalf("stderr leaked database password: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "DATABASE_URL") {
		t.Fatalf("stderr = %q, want DATABASE_URL validation error", stderr.String())
	}
}

func TestRealMainReturnsNonzeroWhenMigrationFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	migrationError := errors.New("checksum mismatch for migration 000001_initial")
	code := realMain(
		context.Background(),
		[]string{"up"},
		&stdout,
		&stderr,
		mapLookup(map[string]string{
			"DATABASE_URL": "postgres://paper:secret@localhost/papers",
		}),
		func(context.Context, config.Config) error {
			return migrationError
		},
	)

	if code == 0 {
		t.Fatal("realMain() code = 0, want migration failure")
	}
	if !strings.Contains(stderr.String(), migrationError.Error()) {
		t.Fatalf("stderr = %q, want migration error", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestRealMainRejectsUnsupportedCommandBeforeLoadingConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	lookedUp := false
	code := realMain(
		context.Background(),
		[]string{"down"},
		&stdout,
		&stderr,
		func(string) (string, bool) {
			lookedUp = true
			return "", false
		},
		func(context.Context, config.Config) error {
			t.Fatal("migration runner called for unsupported command")
			return nil
		},
	)

	if code == 0 {
		t.Fatal("realMain() code = 0, want usage failure")
	}
	if lookedUp {
		t.Fatal("configuration loaded before command validation")
	}
	if !strings.Contains(stderr.String(), "usage: paper-hub-migrate up") {
		t.Fatalf("stderr = %q, want usage", stderr.String())
	}
}

func mapLookup(values map[string]string) config.LookupEnv {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}
