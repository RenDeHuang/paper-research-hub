package scope

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRegistrySealSerializesConcurrentChildInsert(t *testing.T) {
	tests := []struct {
		name               string
		setup              func(*testing.T, *pgxpool.Pool) registrySealRaceFixture
		sealConstraint     string
		childConstraint    string
		pollutedChildCount int
	}{
		{
			name:               "domain rule",
			setup:              setupDomainRuleSealRace,
			sealConstraint:     "domain_versions_seal",
			childConstraint:    "domain_category_rules_parent_unsealed",
			pollutedChildCount: 4,
		},
		{
			name:               "preprint source",
			setup:              setupPreprintSourceSealRace,
			sealConstraint:     "preprint_source_versions_seal",
			childConstraint:    "trusted_preprint_sources_parent_unsealed",
			pollutedChildCount: 2,
		},
		{
			name:               "conference official host",
			setup:              setupConferenceHostSealRace,
			sealConstraint:     "conference_registry_versions_seal",
			childConstraint:    "conference_official_hosts_parent_unsealed",
			pollutedChildCount: 2,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name+" child commits before seal", func(t *testing.T) {
			pool := openScopeTestPool(t)
			fixture := test.setup(t, pool)
			ctx := scopeTestContext(t)

			childTx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin child transaction: %v", err)
			}
			if _, err := childTx.Exec(ctx, fixture.childInsertSQL, fixture.childArgs...); err != nil {
				_ = childTx.Rollback(context.Background())
				t.Fatalf("insert uncommitted Registry child: %v", err)
			}

			sealTx, err := pool.Begin(ctx)
			if err != nil {
				_ = childTx.Rollback(context.Background())
				t.Fatalf("begin seal transaction: %v", err)
			}
			var sealBackendPID int32
			if err := sealTx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(
				&sealBackendPID,
			); err != nil {
				_ = sealTx.Rollback(context.Background())
				_ = childTx.Rollback(context.Background())
				t.Fatalf("query seal backend PID: %v", err)
			}
			sealResult := make(chan error, 1)
			go func() {
				_, updateErr := sealTx.Exec(context.Background(), fmt.Sprintf(`
					UPDATE %s
					SET sealed_at = $2
					WHERE id = $1
				`, fixture.versionTable), fixture.versionID, fixture.sealedAt)
				sealResult <- updateErr
			}()

			if err := waitForPostgresLockWait(
				t,
				pool,
				sealBackendPID,
				sealResult,
			); err != nil {
				_ = sealTx.Rollback(context.Background())
				_ = childTx.Rollback(context.Background())
				t.Fatal(err)
			}
			if err := childTx.Commit(ctx); err != nil {
				_ = sealTx.Rollback(context.Background())
				t.Fatalf("commit Registry child: %v", err)
			}
			sealErr := <-sealResult
			if sealErr == nil {
				_ = sealTx.Rollback(context.Background())
				t.Fatal("seal after committed child pollution error = nil")
			}
			assertScopeConstraint(t, sealErr, test.sealConstraint)
			if err := sealTx.Rollback(context.Background()); err != nil &&
				err != pgx.ErrTxClosed {
				t.Fatalf("rollback failed seal transaction: %v", err)
			}

			var sealed bool
			if err := pool.QueryRow(ctx, fmt.Sprintf(`
				SELECT sealed_at IS NOT NULL
				FROM %s
				WHERE id = $1
			`, fixture.versionTable), fixture.versionID).Scan(&sealed); err != nil {
				t.Fatalf("query receipt seal state: %v", err)
			}
			if sealed {
				t.Fatal("receipt is sealed after concurrent child pollution")
			}
			var childCount int
			if err := pool.QueryRow(
				ctx,
				fixture.childCountSQL,
				fixture.childCountArgs...,
			).Scan(&childCount); err != nil {
				t.Fatalf("query committed Registry child count: %v", err)
			}
			if childCount != test.pollutedChildCount {
				t.Fatalf(
					"committed Registry child count = %d, want %d",
					childCount,
					test.pollutedChildCount,
				)
			}
		})

		t.Run(test.name+" seal commits before child", func(t *testing.T) {
			pool := openScopeTestPool(t)
			fixture := test.setup(t, pool)
			ctx := scopeTestContext(t)
			if _, err := pool.Exec(ctx, fmt.Sprintf(`
				UPDATE %s
				SET sealed_at = $2
				WHERE id = $1
			`, fixture.versionTable), fixture.versionID, fixture.sealedAt); err != nil {
				t.Fatalf("seal clean Registry receipt: %v", err)
			}
			if _, err := pool.Exec(
				ctx,
				fixture.childInsertSQL,
				fixture.childArgs...,
			); err == nil {
				t.Fatal("post-seal Registry child INSERT error = nil")
			} else {
				assertScopeConstraint(t, err, test.childConstraint)
			}
		})
	}
}

type registrySealRaceFixture struct {
	versionTable   string
	versionID      string
	sealedAt       time.Time
	childInsertSQL string
	childArgs      []any
	childCountSQL  string
	childCountArgs []any
}

func setupDomainRuleSealRace(
	t *testing.T,
	pool *pgxpool.Pool,
) registrySealRaceFixture {
	t.Helper()
	ctx := scopeTestContext(t)
	disableConstraintTrigger(
		t,
		pool,
		"domain_versions",
		"domain_versions_must_be_sealed",
	)
	var versionID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO domain_versions (
			registry_name, registry_version, file_sha256,
			domain_count, rule_count, imported_at
		) VALUES (
			'concurrent-domains',
			'research-domains-jcr-subjects/v2',
			$1, 3, 3, $2
		)
		RETURNING id::text
	`, "1111111111111111111111111111111111111111111111111111111111111111",
		time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
	).Scan(&versionID); err != nil {
		t.Fatalf("insert unsealed domain receipt: %v", err)
	}
	domainIDs := make(map[string]string)
	for _, domain := range []string{"medicine", "biology", "computer_science"} {
		var domainID string
		if err := pool.QueryRow(ctx, `
			INSERT INTO research_domains (
				domain_version_id, domain_key, display_label
			) VALUES ($1, $2, $3)
			RETURNING id::text
		`, versionID, domain, domain).Scan(&domainID); err != nil {
			t.Fatalf("insert baseline domain %q: %v", domain, err)
		}
		domainIDs[domain] = domainID
	}
	for domain, category := range map[string]string{
		"medicine":         "Oncology",
		"biology":          "Biology",
		"computer_science": "Computer Science, Artificial Intelligence",
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO domain_category_rules (
				domain_version_id, domain_id, jcr_category,
				article_level_required
			) VALUES ($1, $2, $3, false)
		`, versionID, domainIDs[domain], category); err != nil {
			t.Fatalf("insert baseline domain rule %q: %v", category, err)
		}
	}
	enableConstraintTrigger(
		t,
		pool,
		"domain_versions",
		"domain_versions_must_be_sealed",
	)
	return registrySealRaceFixture{
		versionTable: "domain_versions",
		versionID:    versionID,
		sealedAt:     time.Date(2026, time.July, 18, 12, 5, 0, 0, time.UTC),
		childInsertSQL: `
			INSERT INTO domain_category_rules (
				domain_version_id, domain_id, jcr_category,
				article_level_required
			) VALUES ($1, $2, 'Polluted Category', false)
		`,
		childArgs: []any{versionID, domainIDs["medicine"]},
		childCountSQL: `
			SELECT count(*)
			FROM domain_category_rules
			WHERE domain_version_id = $1
		`,
		childCountArgs: []any{versionID},
	}
}

func setupPreprintSourceSealRace(
	t *testing.T,
	pool *pgxpool.Pool,
) registrySealRaceFixture {
	t.Helper()
	ctx := scopeTestContext(t)
	disableConstraintTrigger(
		t,
		pool,
		"preprint_source_versions",
		"preprint_source_versions_must_be_sealed",
	)
	var versionID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO preprint_source_versions (
			registry_name, registry_version, file_sha256,
			source_count, rule_count, imported_at
		) VALUES (
			'concurrent-preprints', 'preprint-sources/v1',
			$1, 1, 1, $2
		)
		RETURNING id::text
	`, "2222222222222222222222222222222222222222222222222222222222222222",
		time.Date(2026, time.July, 18, 12, 10, 0, 0, time.UTC),
	).Scan(&versionID); err != nil {
		t.Fatalf("insert unsealed preprint receipt: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO trusted_preprint_sources (
			preprint_source_version_id, source_key, display_name,
			identifier_scheme, official_host, allowed_domains, lifecycle
		) VALUES (
			$1, 'baseline', 'Baseline', 'baseline',
			'baseline.example', ARRAY['medicine'], 'active'
		)
	`, versionID); err != nil {
		t.Fatalf("insert baseline preprint source: %v", err)
	}
	enableConstraintTrigger(
		t,
		pool,
		"preprint_source_versions",
		"preprint_source_versions_must_be_sealed",
	)
	return registrySealRaceFixture{
		versionTable: "preprint_source_versions",
		versionID:    versionID,
		sealedAt:     time.Date(2026, time.July, 18, 12, 15, 0, 0, time.UTC),
		childInsertSQL: `
			INSERT INTO trusted_preprint_sources (
				preprint_source_version_id, source_key, display_name,
				identifier_scheme, official_host, allowed_domains, lifecycle
			) VALUES (
				$1, 'polluted', 'Polluted', 'polluted',
				'polluted.example', ARRAY['medicine'], 'active'
			)
		`,
		childArgs: []any{versionID},
		childCountSQL: `
			SELECT count(*)
			FROM trusted_preprint_sources
			WHERE preprint_source_version_id = $1
		`,
		childCountArgs: []any{versionID},
	}
}

func setupConferenceHostSealRace(
	t *testing.T,
	pool *pgxpool.Pool,
) registrySealRaceFixture {
	t.Helper()
	ctx := scopeTestContext(t)
	disableConstraintTrigger(
		t,
		pool,
		"conference_registry_versions",
		"conference_registry_versions_must_be_sealed",
	)
	var versionID, seriesID, eventID, entryID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO conference_registry_versions (
			registry_name, registry_version, file_sha256,
			series_count, event_count, rule_count, imported_at
		) VALUES (
			'concurrent-conferences', 'conference-venues/v1',
			$1, 1, 1, 1, $2
		)
		RETURNING id::text
	`, "3333333333333333333333333333333333333333333333333333333333333333",
		time.Date(2026, time.July, 18, 12, 20, 0, 0, time.UTC),
	).Scan(&versionID); err != nil {
		t.Fatalf("insert unsealed conference receipt: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO conference_series (
			conference_registry_version_id, provider, series_key, series_name
		) VALUES ($1, 'ieee', 'baseline', 'Baseline Conference')
		RETURNING id::text
	`, versionID).Scan(&seriesID); err != nil {
		t.Fatalf("insert baseline conference series: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO conference_events (
			conference_series_id, event_key, event_name, event_year
		) VALUES ($1, 'baseline-2026', 'Baseline 2026', 2026)
		RETURNING id::text
	`, seriesID).Scan(&eventID); err != nil {
		t.Fatalf("insert baseline conference event: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO conference_registry_entries (
			conference_registry_version_id, conference_event_id,
			allowed_domains, lifecycle, reviewed
		) VALUES ($1, $2, ARRAY['computer_science'], 'active', false)
		RETURNING id::text
	`, versionID, eventID).Scan(&entryID); err != nil {
		t.Fatalf("insert baseline conference entry: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO conference_identifiers (
			conference_registry_entry_id, identifier_scheme, identifier_value
		) VALUES ($1, 'doi_prefix', '10.1000')
	`, entryID); err != nil {
		t.Fatalf("insert baseline conference identifier: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO conference_official_hosts (
			conference_registry_entry_id, official_host
		) VALUES ($1, 'baseline.example')
	`, entryID); err != nil {
		t.Fatalf("insert baseline conference host: %v", err)
	}
	enableConstraintTrigger(
		t,
		pool,
		"conference_registry_versions",
		"conference_registry_versions_must_be_sealed",
	)
	return registrySealRaceFixture{
		versionTable: "conference_registry_versions",
		versionID:    versionID,
		sealedAt:     time.Date(2026, time.July, 18, 12, 25, 0, 0, time.UTC),
		childInsertSQL: `
			INSERT INTO conference_official_hosts (
				conference_registry_entry_id, official_host
			) VALUES ($1, 'polluted.example')
		`,
		childArgs: []any{entryID},
		childCountSQL: `
			SELECT count(*)
			FROM conference_official_hosts
			WHERE conference_registry_entry_id = $1
		`,
		childCountArgs: []any{entryID},
	}
}

func disableConstraintTrigger(
	t *testing.T,
	pool *pgxpool.Pool,
	table string,
	trigger string,
) {
	t.Helper()
	if _, err := pool.Exec(
		scopeTestContext(t),
		fmt.Sprintf("ALTER TABLE %s DISABLE TRIGGER %s", table, trigger),
	); err != nil {
		t.Fatalf("disable %s: %v", trigger, err)
	}
}

func enableConstraintTrigger(
	t *testing.T,
	pool *pgxpool.Pool,
	table string,
	trigger string,
) {
	t.Helper()
	if _, err := pool.Exec(
		scopeTestContext(t),
		fmt.Sprintf("ALTER TABLE %s ENABLE TRIGGER %s", table, trigger),
	); err != nil {
		t.Fatalf("enable %s: %v", trigger, err)
	}
}

func waitForPostgresLockWait(
	t *testing.T,
	pool *pgxpool.Pool,
	backendPID int32,
	result <-chan error,
) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(scopeTestContext(t), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-result:
			return fmt.Errorf(
				"seal did not wait for uncommitted child INSERT: %v",
				err,
			)
		case <-ticker.C:
			var waiting bool
			err := pool.QueryRow(ctx, `
				SELECT wait_event_type = 'Lock'
				FROM pg_stat_activity
				WHERE pid = $1
			`, backendPID).Scan(&waiting)
			if err == nil && waiting {
				return nil
			}
			if err != nil && ctx.Err() == nil {
				return fmt.Errorf("query seal lock wait: %w", err)
			}
		case <-ctx.Done():
			return fmt.Errorf(
				"seal did not enter PostgreSQL lock wait: %w",
				ctx.Err(),
			)
		}
	}
}
