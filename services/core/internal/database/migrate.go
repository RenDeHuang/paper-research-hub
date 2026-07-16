package database

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RenDeHuang/paper-research-hub/services/core/migrations"
)

const migrationAdvisoryLockKey int64 = 0x5041504552485542

var (
	migrationFilenamePattern = regexp.MustCompile(`^([0-9]{6})_([a-z0-9_]+)\.sql$`)
	migrationNamePattern     = regexp.MustCompile(`^[a-z0-9_]+$`)
)

type Migration struct {
	Version int64
	Name    string
	SQL     string
}

func EmbeddedMigrations() ([]Migration, error) {
	return loadMigrations(migrations.Files)
}

func Up(ctx context.Context, pool *pgxpool.Pool) error {
	embedded, err := EmbeddedMigrations()
	if err != nil {
		return err
	}
	return UpMigrations(ctx, pool, embedded)
}

func UpMigrations(ctx context.Context, pool *pgxpool.Pool, migrationSet []Migration) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if pool == nil {
		return errors.New("PostgreSQL pool is required")
	}
	ordered, err := validateMigrations(migrationSet)
	if err != nil {
		return err
	}

	connection, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire PostgreSQL connection for migrations: %w", err)
	}
	defer connection.Release()

	if _, err := connection.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationAdvisoryLockKey); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, unlockErr := connection.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationAdvisoryLockKey); unlockErr != nil && returnErr == nil {
			returnErr = fmt.Errorf("release migration advisory lock: %w", unlockErr)
		}
	}()

	if _, err := connection.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version bigint PRIMARY KEY,
			name text NOT NULL,
			checksum char(64) NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	for _, migration := range ordered {
		label := migrationLabel(migration)
		checksum := migrationChecksum(migration.SQL)
		var appliedName, appliedChecksum string
		err := connection.QueryRow(ctx, `
			SELECT name, checksum
			FROM schema_migrations
			WHERE version = $1
		`, migration.Version).Scan(&appliedName, &appliedChecksum)
		switch {
		case err == nil:
			if appliedName != migration.Name {
				return fmt.Errorf(
					"migration metadata mismatch for version %06d: applied name %q, current name %q",
					migration.Version,
					appliedName,
					migration.Name,
				)
			}
			if appliedChecksum != checksum {
				return fmt.Errorf("checksum mismatch for migration %s", label)
			}
			continue
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("read migration %s state: %w", label, err)
		}

		tx, err := connection.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", label, err)
		}
		if _, err := tx.Exec(ctx, migration.SQL); err != nil {
			_ = tx.Rollback(context.Background())
			return fmt.Errorf("apply migration %s: %w", label, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO schema_migrations (version, name, checksum)
			VALUES ($1, $2, $3)
		`, migration.Version, migration.Name, checksum); err != nil {
			_ = tx.Rollback(context.Background())
			return fmt.Errorf("record migration %s: %w", label, err)
		}
		if err := tx.Commit(ctx); err != nil {
			_ = tx.Rollback(context.Background())
			return fmt.Errorf("commit migration %s: %w", label, err)
		}
	}
	return nil
}

func loadMigrations(source fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	result := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		matches := migrationFilenamePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse migration version in %q: %w", entry.Name(), err)
		}
		contents, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		result = append(result, Migration{
			Version: version,
			Name:    matches[2],
			SQL:     string(contents),
		})
	}
	return validateMigrations(result)
}

func validateMigrations(migrationSet []Migration) ([]Migration, error) {
	if len(migrationSet) == 0 {
		return nil, errors.New("at least one migration is required")
	}
	ordered := slices.Clone(migrationSet)
	slices.SortFunc(ordered, func(left, right Migration) int {
		return cmp.Compare(left.Version, right.Version)
	})
	versions := make(map[int64]struct{}, len(ordered))
	for _, migration := range ordered {
		if migration.Version <= 0 {
			return nil, fmt.Errorf("migration version must be positive: %d", migration.Version)
		}
		if !migrationNamePattern.MatchString(migration.Name) {
			return nil, fmt.Errorf("migration %06d has invalid name %q", migration.Version, migration.Name)
		}
		if strings.TrimSpace(migration.SQL) == "" {
			return nil, fmt.Errorf("migration %s has empty SQL", migrationLabel(migration))
		}
		if _, duplicate := versions[migration.Version]; duplicate {
			return nil, fmt.Errorf("duplicate migration version %06d", migration.Version)
		}
		versions[migration.Version] = struct{}{}
	}
	return ordered, nil
}

func migrationChecksum(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return hex.EncodeToString(sum[:])
}

func migrationLabel(migration Migration) string {
	return fmt.Sprintf("%06d_%s", migration.Version, migration.Name)
}
