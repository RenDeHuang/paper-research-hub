package database

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
)

func TestOpenPoolPingsPostgresAndAppliesStrictLimits(t *testing.T) {
	cfg := testDatabaseConfig(newTestDatabase(t))
	cfg.MinConns = 1
	cfg.MaxConns = 3
	cfg.ConnectTimeout = 7 * time.Second
	cfg.MaxConnLifetime = 45 * time.Minute
	cfg.MaxConnIdleTime = 7 * time.Minute
	cfg.HealthCheckPeriod = 17 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("opened pool Ping() error = %v", err)
	}
	actual := pool.Config()
	if actual.MinConns != cfg.MinConns ||
		actual.MaxConns != cfg.MaxConns ||
		actual.ConnConfig.ConnectTimeout != cfg.ConnectTimeout ||
		actual.MaxConnLifetime != cfg.MaxConnLifetime ||
		actual.MaxConnIdleTime != cfg.MaxConnIdleTime ||
		actual.HealthCheckPeriod != cfg.HealthCheckPeriod {
		t.Fatalf("pool config = %+v, want limits from %+v", actual, cfg)
	}
}

func TestOpenPoolHonorsCancelledContextAndRedactsPassword(t *testing.T) {
	cfg := testDatabaseConfig(newTestDatabase(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pool, err := Open(ctx, cfg)
	if pool != nil {
		pool.Close()
		t.Fatal("Open() returned a pool for a cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Open() error = %v, want context.Canceled", err)
	}
	if strings.Contains(err.Error(), testPostgresPassword) {
		t.Fatalf("Open() error leaked database password: %v", err)
	}
}

func TestClosedPoolRejectsNewAcquisitions(t *testing.T) {
	cfg := testDatabaseConfig(newTestDatabase(t))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	pool.Close()
	if _, err := pool.Acquire(context.Background()); err == nil {
		t.Fatal("Acquire() after Close() error = nil, want closed-pool error")
	}
}

func TestOpenPoolRejectsMalformedConnectionStringWithoutLeakingIt(t *testing.T) {
	cfg := testDatabaseConfig("postgres://paper:malformed-secret@%zz/papers")

	pool, err := Open(context.Background(), cfg)
	if pool != nil {
		pool.Close()
		t.Fatal("Open() returned a pool for malformed DATABASE_URL")
	}
	if err == nil || !strings.Contains(err.Error(), "parse PostgreSQL pool configuration") {
		t.Fatalf("Open() error = %v, want parse error", err)
	}
	if strings.Contains(err.Error(), "malformed-secret") {
		t.Fatalf("Open() error leaked database password: %v", err)
	}
}

func testDatabaseConfig(databaseURL string) config.DatabaseConfig {
	return config.DatabaseConfig{
		URL:               databaseURL,
		MinConns:          0,
		MaxConns:          4,
		ConnectTimeout:    5 * time.Second,
		MaxConnLifetime:   30 * time.Minute,
		MaxConnIdleTime:   5 * time.Minute,
		HealthCheckPeriod: 30 * time.Second,
	}
}
