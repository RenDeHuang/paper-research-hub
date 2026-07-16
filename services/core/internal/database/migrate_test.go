package database

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var expectedSchemaTables = []string{
	"works",
	"paper_versions",
	"source_records",
	"source_record_works",
	"external_identifiers",
	"field_assertions",
	"authors",
	"work_authors",
	"institutions",
	"topics",
	"work_topics",
	"methods",
	"work_methods",
	"datasets",
	"work_datasets",
	"benchmarks",
	"work_benchmarks",
	"models",
	"work_models",
	"code_repositories",
	"work_code_repositories",
	"metric_snapshots",
	"ranking_snapshots",
	"ingestion_cursors",
	"ingestion_jobs",
	"analysis_runs",
	"venues",
	"venue_aliases",
	"venue_metric_snapshots",
	"venue_policy_versions",
	"venue_policy_assessments",
	"fulltext_assets",
}

func TestMigrationFromEmptyDatabaseCreatesExpectedSchema(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	rows, err := pool.Query(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = 'public'
	`)
	if err != nil {
		t.Fatalf("query schema tables: %v", err)
	}
	defer rows.Close()
	actual := map[string]bool{}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan schema table: %v", err)
		}
		actual[table] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate schema tables: %v", err)
	}
	for _, table := range append(expectedSchemaTables, "schema_migrations") {
		if !actual[table] {
			t.Errorf("expected table %q was not created", table)
		}
	}

	var version int64
	var name, checksum string
	var appliedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT version, name, checksum, applied_at
		FROM schema_migrations
	`).Scan(&version, &name, &checksum, &appliedAt); err != nil {
		t.Fatalf("query migration record: %v", err)
	}
	if version != 1 || name != "initial" || len(checksum) != 64 || appliedAt.IsZero() {
		t.Fatalf("migration record = (%d, %q, %q, %s), want version/name/SHA-256/applied_at", version, name, checksum, appliedAt)
	}
}

func TestMigrationUsesDatabaseGeneratedUUIDsAndTimestamptz(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	for _, table := range expectedSchemaTables {
		var dataType, defaultValue string
		err := pool.QueryRow(ctx, `
			SELECT data_type, column_default
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = 'id'
		`, table).Scan(&dataType, &defaultValue)
		if err != nil {
			t.Fatalf("%s.id metadata: %v", table, err)
		}
		if dataType != "uuid" || !strings.Contains(defaultValue, "gen_random_uuid()") {
			t.Errorf("%s.id = (%q, %q), want database-generated UUID", table, dataType, defaultValue)
		}
	}

	var timestampWithoutZoneCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = 'public' AND data_type = 'timestamp without time zone'
	`).Scan(&timestampWithoutZoneCount); err != nil {
		t.Fatalf("query timestamp types: %v", err)
	}
	if timestampWithoutZoneCount != 0 {
		t.Fatalf("schema contains %d timestamp columns without time zone", timestampWithoutZoneCount)
	}
}

func TestMigrationCreatesCriticalConstraintsTriggersIndexesAndDeleteRules(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	assertNamesExist(t, pool, `
		SELECT conname
		FROM pg_constraint
		WHERE connamespace = 'public'::regnamespace
	`, []string{
		"works_canonical_key_check",
		"works_status_check",
		"external_identifiers_scheme_normalized_value_key",
		"source_records_source_source_record_id_content_hash_key",
		"paper_versions_source_record_work_fkey",
		"field_assertions_source_record_work_fkey",
		"metric_snapshots_exactly_one_target_check",
		"metric_snapshots_metric_value_check",
		"ranking_snapshots_exactly_one_subject_check",
		"ranking_snapshots_coverage_check",
		"venues_issn_l_key",
		"venues_issn_key",
		"venues_eissn_key",
		"venues_source_scheme_source_identifier_key",
		"venue_metric_snapshots_venue_id_metric_year_category_key",
		"venue_metric_snapshots_jif_check",
		"venue_metric_snapshots_quartile_check",
		"venue_policy_assessments_decision_check",
		"fulltext_assets_public_reusable_check",
	})
	assertNamesExist(t, pool, `
		SELECT tgname
		FROM pg_trigger
		WHERE NOT tgisinternal
	`, []string{
		"source_records_immutable",
		"venue_metric_snapshots_immutable",
		"venue_policy_versions_immutable",
	})
	assertNamesExist(t, pool, `
		SELECT indexname
		FROM pg_indexes
		WHERE schemaname = 'public'
	`, []string{
		"idx_source_record_works_work_id",
		"idx_field_assertions_source_record_id",
		"idx_work_code_repositories_repository_id",
		"idx_metric_snapshots_work_observed_at",
		"idx_ranking_snapshots_generated_at",
		"idx_ingestion_jobs_dequeue",
		"idx_venue_metric_snapshots_lookup",
		"idx_fulltext_assets_public",
	})

	expectedDeleteRules := map[string]string{
		"source_record_works_source_record_id_fkey":    "r",
		"source_record_works_work_id_fkey":             "c",
		"field_assertions_work_id_fkey":                "n",
		"paper_versions_source_record_id_fkey":         "r",
		"field_assertions_source_record_id_fkey":       "r",
		"paper_versions_source_record_work_fkey":       "a",
		"field_assertions_source_record_work_fkey":     "a",
		"work_code_repositories_work_id_fkey":          "c",
		"work_code_repositories_repository_id_fkey":    "r",
		"venue_metric_snapshots_venue_id_fkey":         "r",
		"venue_policy_assessments_policy_version_fkey": "r",
	}
	for name, want := range expectedDeleteRules {
		var actual string
		if err := pool.QueryRow(ctx, `
			SELECT confdeltype::text
			FROM pg_constraint
			WHERE conname = $1
		`, name).Scan(&actual); err != nil {
			t.Fatalf("query delete rule for %s: %v", name, err)
		}
		if actual != want {
			t.Errorf("%s delete rule = %q, want %q", name, actual, want)
		}
	}
}

func TestMigrationSecondRunIsIdempotent(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	var beforeCount int
	var beforeChecksum string
	if err := pool.QueryRow(ctx, "SELECT count(*), min(checksum) FROM schema_migrations").Scan(&beforeCount, &beforeChecksum); err != nil {
		t.Fatalf("query migration state before second run: %v", err)
	}
	if err := Up(ctx, pool); err != nil {
		t.Fatalf("second Up() error = %v", err)
	}
	var afterCount int
	var afterChecksum string
	if err := pool.QueryRow(ctx, "SELECT count(*), min(checksum) FROM schema_migrations").Scan(&afterCount, &afterChecksum); err != nil {
		t.Fatalf("query migration state after second run: %v", err)
	}
	if beforeCount != 1 || afterCount != beforeCount || afterChecksum != beforeChecksum {
		t.Fatalf("migration state changed: before=(%d,%s) after=(%d,%s)", beforeCount, beforeChecksum, afterCount, afterChecksum)
	}
}

func TestConcurrentMigratorsExecuteMigrationOnce(t *testing.T) {
	pool := openTestPool(t)
	ctx := testContext(t)
	migrations := []Migration{{
		Version: 42,
		Name:    "advisory_lock_probe",
		SQL: `
			CREATE TABLE advisory_lock_probe (id integer PRIMARY KEY);
			SELECT pg_sleep(0.5);
			INSERT INTO advisory_lock_probe (id) VALUES (1);
		`,
	}}

	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	errorsFound := make(chan error, 2)
	for range 2 {
		go func() {
			defer wait.Done()
			<-start
			errorsFound <- UpMigrations(ctx, pool, migrations)
		}()
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent UpMigrations() error = %v", err)
		}
	}

	var applied, inserted int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = 42").Scan(&applied); err != nil {
		t.Fatalf("count migration rows: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM advisory_lock_probe").Scan(&inserted); err != nil {
		t.Fatalf("count probe rows: %v", err)
	}
	if applied != 1 || inserted != 1 {
		t.Fatalf("concurrent migration results = applied %d, inserted %d; want 1, 1", applied, inserted)
	}
}

func TestChangedAppliedMigrationChecksumFailsExplicitly(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	migrations[0].SQL += "\nSELECT 1;\n"

	err = UpMigrations(ctx, pool, migrations)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch for migration 000001_initial") {
		t.Fatalf("UpMigrations() error = %v, want explicit checksum mismatch", err)
	}
}

func TestFailedMigrationRollsBackAndIsNotRecorded(t *testing.T) {
	pool := openTestPool(t)
	ctx := testContext(t)
	err := UpMigrations(ctx, pool, []Migration{{
		Version: 7,
		Name:    "must_rollback",
		SQL: `
			CREATE TABLE must_not_survive (id integer);
			INSERT INTO missing_table VALUES (1);
		`,
	}})
	if err == nil || !strings.Contains(err.Error(), "apply migration 000007_must_rollback") {
		t.Fatalf("UpMigrations() error = %v, want migration failure", err)
	}

	var relation *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('public.must_not_survive')::text").Scan(&relation); err != nil {
		t.Fatalf("query rolled-back relation: %v", err)
	}
	if relation != nil {
		t.Fatalf("failed migration left relation %q behind", *relation)
	}
	var applied int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = 7").Scan(&applied); err != nil {
		t.Fatalf("count failed migration records: %v", err)
	}
	if applied != 0 {
		t.Fatalf("failed migration recorded %d successful rows", applied)
	}
}

func TestCanonicalStatusIdentifierAndProjectionConstraints(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	if _, err := pool.Exec(ctx, `
		INSERT INTO works (canonical_key, status, title)
		VALUES ('pmid:123', 'active', 'Invalid canonical prefix')
	`); err == nil {
		t.Fatal("invalid canonical prefix was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO works (canonical_key, status, title)
		VALUES ('doi:', 'active', 'Empty canonical value')
	`); err == nil {
		t.Fatal("empty canonical value was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO works (canonical_key, status, title)
		VALUES ('doi:10.1000/status', 'invented', 'Invalid status')
	`); err == nil {
		t.Fatal("invalid paper status was accepted")
	}

	workOne := insertWork(t, pool, "doi:10.1000/one")
	workTwo := insertWork(t, pool, "arxiv:2607.00001")
	sourceOne := insertSourceRecord(t, pool, workOne, "openalex", "W1", "hash-one")
	if _, err := pool.Exec(ctx, `
		INSERT INTO paper_versions (work_id, source_record_id, version_label, status)
		VALUES ($1, $2, 'v1', 'active')
	`, workTwo, sourceOne); err == nil {
		t.Fatal("paper version projected a SourceRecord owned by another Work")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO field_assertions (work_id, source_record_id, field_name, asserted_value)
		VALUES ($1, $2, 'title', '{"value":"wrong owner"}')
	`, workTwo, sourceOne); err == nil {
		t.Fatal("field assertion projected a SourceRecord owned by another Work")
	}
	var topicID string
	mustScanID(t, pool.QueryRow(ctx, "INSERT INTO topics (name) VALUES ('Ownership test') RETURNING id"), &topicID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_topics (work_id, topic_id, source_record_id)
		VALUES ($1, $2, $3)
	`, workTwo, topicID, sourceOne); err == nil {
		t.Fatal("work topic projected a SourceRecord owned by another Work")
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO external_identifiers (work_id, scheme, normalized_value)
		VALUES ($1, 'doi', '10.1000/shared')
	`, workOne); err != nil {
		t.Fatalf("insert first identifier: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO external_identifiers (work_id, scheme, normalized_value)
		VALUES ($1, 'doi', '10.1000/shared')
	`, workTwo); err == nil {
		t.Fatal("duplicate normalized external identifier was accepted")
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO paper_versions (work_id, source_record_id, version_label, status)
		VALUES ($1, $2, 'v1', 'active')
	`, workOne, sourceOne); err != nil {
		t.Fatalf("insert valid paper version: %v", err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM source_records WHERE id = $1", sourceOne); err == nil {
		t.Fatal("projected SourceRecord deletion was not RESTRICTed")
	}
}

func TestSourceRecordSnapshotIsCompletelyImmutable(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	workID := insertWork(t, pool, "openalex:W-immutable")
	sourceID := insertSourceRecord(t, pool, workID, "openalex", "W-immutable", "immutable-hash")

	updates := []string{
		`id = gen_random_uuid()`,
		`raw_payload = '{"changed":true}'`,
		`source = 'crossref'`,
		`source_record_id = 'changed-id'`,
		`content_hash = 'changed-hash'`,
		`source_identity = '{"id":"changed"}'`,
		`source_time = source_time + interval '1 second'`,
		`retrieved_at = retrieved_at + interval '1 second'`,
		`created_at = created_at + interval '1 second'`,
	}
	for _, assignment := range updates {
		t.Run(assignment, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin immutable update transaction: %v", err)
			}
			_, updateErr := tx.Exec(ctx, "UPDATE source_records SET "+assignment+" WHERE id = $1", sourceID)
			if rollbackErr := tx.Rollback(context.Background()); rollbackErr != nil {
				t.Fatalf("rollback immutable update transaction: %v", rollbackErr)
			}
			if updateErr == nil {
				t.Fatalf("immutable update %q was accepted", assignment)
			}
		})
	}
}

func TestSourceRecordCannotBeDeletedWithoutProjectionReferences(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	workID := insertWork(t, pool, "openalex:undeletable-source")
	sourceID := insertSourceRecord(t, pool, workID, "openalex", "undeletable-source", "undeletable-hash")

	if _, err := pool.Exec(ctx, "DELETE FROM source_records WHERE id = $1", sourceID); err == nil {
		t.Fatal("unprojected SourceRecord deletion was accepted")
	}
}

func TestSourceRecordWorkOwnershipUsesSeparateAssociation(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	var sourceRecordWorkColumnCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'source_records'
		  AND column_name = 'work_id'
	`).Scan(&sourceRecordWorkColumnCount); err != nil {
		t.Fatalf("query SourceRecord ownership column: %v", err)
	}
	if sourceRecordWorkColumnCount != 0 {
		t.Fatal("source_records.work_id makes Work deletion mutate immutable evidence")
	}

	var associationTable *string
	if err := pool.QueryRow(ctx, `
		SELECT to_regclass('public.source_record_works')::text
	`).Scan(&associationTable); err != nil {
		t.Fatalf("query SourceRecord Work association table: %v", err)
	}
	if associationTable == nil {
		t.Fatal("source_record_works association table was not created")
	}

	workID := insertWork(t, pool, "openalex:separate-source-ownership")
	sourceID := insertSourceRecord(t, pool, workID, "openalex", "separate-source-ownership", "ownership-hash")
	var linkedWorkID string
	if err := pool.QueryRow(ctx, `
		SELECT work_id::text
		FROM source_record_works
		WHERE source_record_id = $1
	`, sourceID).Scan(&linkedWorkID); err != nil {
		t.Fatalf("query SourceRecord Work association: %v", err)
	}
	if linkedWorkID != workID {
		t.Fatalf("SourceRecord linked Work = %q, want %q", linkedWorkID, workID)
	}
}

func TestSourceRecordUniquenessAndJSONTypeConstraints(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	workID := insertWork(t, pool, "openalex:json-constraints")
	insertSourceRecord(t, pool, workID, "openalex", "W-json", "same-hash")

	if _, err := pool.Exec(ctx, `
		INSERT INTO source_records (
			source, source_record_id, source_identity, source_time, content_hash, raw_payload
		) VALUES (
			'openalex', 'W-json', '{"id":"W-json"}', now(), 'same-hash', '{"id":"W-json"}'
		)
	`); err == nil {
		t.Fatal("duplicate (source, source_record_id, content_hash) was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO source_records (
			source, source_record_id, source_identity, source_time, content_hash, raw_payload
		) VALUES (
			'openalex', 'W-array', '{"id":"W-array"}', now(), 'array-hash', '[]'
		)
	`); err == nil {
		t.Fatal("array raw_payload was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ingestion_cursors (source, cursor_name, cursor_value)
		VALUES ('openalex', 'daily', '[]')
	`); err == nil {
		t.Fatal("array ingestion cursor was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO ingestion_jobs (
			source, job_type, idempotency_key, status, payload, max_attempts
		) VALUES ('openalex', 'sync', 'json-array-job', 'pending', '[]', 3)
	`); err == nil {
		t.Fatal("array ingestion job payload was accepted")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO analysis_runs (
			analysis_type, model_provider, model_name, prompt_version, status, input_payload
		) VALUES ('summary', 'provider', 'model', 'v1', 'pending', '[]')
	`); err == nil {
		t.Fatal("array analysis input payload was accepted")
	}
}

func TestMetricAndRankingExactlyOneConstraints(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	workID := insertWork(t, pool, "s2:metric-target")
	var repositoryID, topicID, methodID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO code_repositories (canonical_url, host, owner_name, repository_name)
		VALUES ('https://example.test/org/repo', 'example.test', 'org', 'repo')
		RETURNING id
	`), &repositoryID)
	mustScanID(t, pool.QueryRow(ctx, "INSERT INTO topics (name) VALUES ('Agents') RETURNING id"), &topicID)
	mustScanID(t, pool.QueryRow(ctx, "INSERT INTO methods (name) VALUES ('Planning') RETURNING id"), &methodID)

	invalidMetrics := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name: "no target",
			query: `INSERT INTO metric_snapshots (metric_name, metric_value, observed_at)
				VALUES ('citations', 1, now())`,
		},
		{
			name: "two targets",
			query: `INSERT INTO metric_snapshots (work_id, code_repository_id, metric_name, metric_value, observed_at)
				VALUES ($1, $2, 'citations', 1, now())`,
			args: []any{workID, repositoryID},
		},
		{
			name: "negative",
			query: `INSERT INTO metric_snapshots (work_id, metric_name, metric_value, observed_at)
				VALUES ($1, 'citations', -1, now())`,
			args: []any{workID},
		},
	}
	for _, tt := range invalidMetrics {
		t.Run("metric "+tt.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, tt.query, tt.args...); err == nil {
				t.Fatalf("invalid metric %q was accepted", tt.name)
			}
		})
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO metric_snapshots (work_id, metric_name, metric_value, observed_at)
		VALUES ($1, 'citations', 0, now())
	`, workID); err != nil {
		t.Fatalf("insert valid metric: %v", err)
	}

	invalidRankings := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name: "no subject",
			query: `INSERT INTO ranking_snapshots
				(rank, score, coverage, formula_version, window_start, window_end, generated_at)
				VALUES (1, 1, 0.5, 'v1', now() - interval '1 day', now(), now())`,
		},
		{
			name: "two subjects",
			query: `INSERT INTO ranking_snapshots
				(subject_work_id, subject_topic_id, rank, score, coverage, formula_version, window_start, window_end, generated_at)
				VALUES ($1, $2, 1, 1, 0.5, 'v1', now() - interval '1 day', now(), now())`,
			args: []any{workID, topicID},
		},
		{
			name: "coverage below zero",
			query: `INSERT INTO ranking_snapshots
				(subject_method_id, rank, score, coverage, formula_version, window_start, window_end, generated_at)
				VALUES ($1, 1, 1, -0.1, 'v1', now() - interval '1 day', now(), now())`,
			args: []any{methodID},
		},
		{
			name: "coverage above one",
			query: `INSERT INTO ranking_snapshots
				(subject_method_id, rank, score, coverage, formula_version, window_start, window_end, generated_at)
				VALUES ($1, 1, 1, 1.1, 'v1', now() - interval '1 day', now(), now())`,
			args: []any{methodID},
		},
	}
	for _, tt := range invalidRankings {
		t.Run("ranking "+tt.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, tt.query, tt.args...); err == nil {
				t.Fatalf("invalid ranking %q was accepted", tt.name)
			}
		})
	}
}

func TestDeletingWorkRetainsEvidenceAndGlobalRepository(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	workID := insertWork(t, pool, "openreview:retained-evidence")
	sourceID := insertSourceRecord(t, pool, workID, "openreview", "forum-1", "retained-hash")
	var assertionID, repositoryID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO field_assertions (work_id, source_record_id, field_name, asserted_value)
		VALUES ($1, $2, 'title', '{"value":"retained"}')
		RETURNING id
	`, workID, sourceID), &assertionID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO code_repositories (canonical_url, host, owner_name, repository_name)
		VALUES ('https://example.test/global/repo', 'example.test', 'global', 'repo')
		RETURNING id
	`), &repositoryID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_code_repositories (work_id, repository_id, relation_type)
		VALUES ($1, $2, 'official')
	`, workID, repositoryID); err != nil {
		t.Fatalf("link repository: %v", err)
	}

	if _, err := pool.Exec(ctx, "DELETE FROM works WHERE id = $1", workID); err != nil {
		t.Fatalf("delete work: %v", err)
	}
	var retainedSources, sourceWorkLinks int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM source_records WHERE id = $1", sourceID).Scan(&retainedSources); err != nil {
		t.Fatalf("count retained SourceRecord: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM source_record_works WHERE source_record_id = $1", sourceID).Scan(&sourceWorkLinks); err != nil {
		t.Fatalf("count cleared SourceRecord Work links: %v", err)
	}
	var assertionWorkID *string
	if err := pool.QueryRow(ctx, "SELECT work_id::text FROM field_assertions WHERE id = $1", assertionID).Scan(&assertionWorkID); err != nil {
		t.Fatalf("query retained FieldAssertion: %v", err)
	}
	if retainedSources != 1 || sourceWorkLinks != 0 || assertionWorkID != nil {
		t.Fatalf(
			"retained evidence = sources %d, source Work links %d, assertion Work %v; want 1, 0, nil",
			retainedSources,
			sourceWorkLinks,
			assertionWorkID,
		)
	}
	var repositories, links int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM code_repositories WHERE id = $1", repositoryID).Scan(&repositories); err != nil {
		t.Fatalf("count retained repository: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM work_code_repositories WHERE repository_id = $1", repositoryID).Scan(&links); err != nil {
		t.Fatalf("count cleared repository links: %v", err)
	}
	if repositories != 1 || links != 0 {
		t.Fatalf("repository retention = repositories %d, links %d; want 1, 0", repositories, links)
	}
}

func TestSyntheticJCRFixtureMatchesPreexistingVenuesByExactISSN(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	fixture := loadJCRFixture(t)

	var alphaVenueID, unknownVenueID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title, issn_l)
		VALUES ('journal', 'Preexisting Alpha Venue', '1234-567X')
		RETURNING id
	`), &alphaVenueID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title, issn)
		VALUES ('journal', 'Preexisting Unknown Venue', '9876-543X')
		RETURNING id
	`), &unknownVenueID)

	expectedVenueIDs := map[string]string{
		"1234-567X": alphaVenueID,
		"9876-543X": unknownVenueID,
	}
	var venuesBefore int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM venues").Scan(&venuesBefore); err != nil {
		t.Fatalf("count preexisting venues: %v", err)
	}

	for _, record := range fixture {
		venueID, err := resolveVenueByExactISSN(ctx, pool, record)
		if err != nil {
			t.Fatalf("resolve fixture venue by exact ISSN: %v", err)
		}
		if want := expectedVenueIDs[record["issn_l"]]; venueID != want {
			t.Fatalf("fixture ISSN-L %q matched venue %q, want preexisting venue %q", record["issn_l"], venueID, want)
		}
		metricYear, err := strconv.Atoi(record["metric_year"])
		if err != nil {
			t.Fatalf("parse fixture metric_year: %v", err)
		}
		var jif any
		if record["jif"] != "" {
			value, parseErr := strconv.ParseFloat(record["jif"], 64)
			if parseErr != nil {
				t.Fatalf("parse fixture jif: %v", parseErr)
			}
			jif = value
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO venue_metric_snapshots (
				venue_id, metric_year, category, jif, quartile, metric_status, source_name, source_license
			) VALUES ($1, $2, $3, $4, $5, $6, 'synthetic-test-fixture', 'synthetic-only')
		`, venueID, metricYear, record["category"], jif, nullable(record["quartile"]), record["status"]); err != nil {
			t.Fatalf("insert synthetic JCR fixture row: %v", err)
		}
	}

	var venuesAfter int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM venues").Scan(&venuesAfter); err != nil {
		t.Fatalf("count venues after fixture import: %v", err)
	}
	if venuesAfter != venuesBefore {
		t.Fatalf("fixture import created venues: before=%d after=%d", venuesBefore, venuesAfter)
	}
	var q1Categories, highJIF, unknown int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE quartile = 'Q1' AND venue_id = $1),
			count(*) FILTER (WHERE jif >= 10),
			count(*) FILTER (
				WHERE metric_status = 'unknown'
				  AND jif IS NULL
				  AND quartile IS NULL
				  AND venue_id = $2
			)
		FROM venue_metric_snapshots
	`, alphaVenueID, unknownVenueID).Scan(&q1Categories, &highJIF, &unknown); err != nil {
		t.Fatalf("query synthetic JCR semantics: %v", err)
	}
	if q1Categories < 2 || highJIF < 1 || unknown < 1 {
		t.Fatalf(
			"fixture semantics = Q1 categories %d, JIF>=10 %d, unknown %d",
			q1Categories,
			highJIF,
			unknown,
		)
	}

	var controlledSourceMatches int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM venues
		WHERE source_identifier IN ('synthetic-source-alpha', 'synthetic-source-unknown')
	`).Scan(&controlledSourceMatches); err != nil {
		t.Fatalf("count controlled-source venue matches: %v", err)
	}
	if controlledSourceMatches != 0 {
		t.Fatalf("fixture resolved through %d controlled source identifiers, want ISSN-only matching", controlledSourceMatches)
	}
}

func TestVenueISSNResolutionFailsExplicitlyWhenUnmatchedOrAmbiguous(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	_, err := resolveVenueByExactISSN(ctx, pool, map[string]string{
		"issn_l": "0000-000X",
		"issn":   "0000-000X",
		"eissn":  "0000-000X",
	})
	if err == nil || !strings.Contains(err.Error(), "no venue matches exact ISSN identifiers") {
		t.Fatalf("unmatched ISSN resolution error = %v, want explicit no-match error", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO venues (venue_type, display_title, issn_l)
		VALUES ('journal', 'Ambiguous ISSN-L Venue', '3141-592X')
	`); err != nil {
		t.Fatalf("insert ambiguous ISSN-L venue: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO venues (venue_type, display_title, issn)
		VALUES ('journal', 'Ambiguous ISSN Venue', '2718-281X')
	`); err != nil {
		t.Fatalf("insert ambiguous ISSN venue: %v", err)
	}

	_, err = resolveVenueByExactISSN(ctx, pool, map[string]string{
		"issn_l": "3141-592X",
		"issn":   "2718-281X",
	})
	if err == nil || !strings.Contains(err.Error(), "multiple venues match exact ISSN identifiers") {
		t.Fatalf("ambiguous ISSN resolution error = %v, want explicit ambiguity error", err)
	}
}

func TestVenueMetricsPoliciesAndFulltextConstraints(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	workID := insertWork(t, pool, "doi:10.1000/fulltext")
	sourceID := insertSourceRecord(t, pool, workID, "pmc", "PMC1", "fulltext-source-hash")
	var venueID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (
			venue_type, display_title, issn_l, issn, eissn, source_scheme, source_identifier
		) VALUES ('journal', 'Synthetic Venue', '1111-111X', '1111-111X', '2222-222X', 'openalex', 'S111')
		RETURNING id
	`), &venueID)

	for _, query := range []string{
		`INSERT INTO venues (venue_type, display_title, issn_l) VALUES ('journal', 'Duplicate ISSN-L', '1111-111X')`,
		`INSERT INTO venues (venue_type, display_title, issn) VALUES ('journal', 'Duplicate ISSN', '1111-111X')`,
		`INSERT INTO venues (venue_type, display_title, eissn) VALUES ('journal', 'Duplicate eISSN', '2222-222X')`,
		`INSERT INTO venues (venue_type, display_title, source_scheme, source_identifier)
		 VALUES ('journal', 'Duplicate Source', 'openalex', 'S111')`,
		`INSERT INTO venues (venue_type, display_title, source_scheme)
		 VALUES ('journal', 'Incomplete Source Scheme', 'openalex')`,
		`INSERT INTO venues (venue_type, display_title, source_identifier)
		 VALUES ('journal', 'Incomplete Source ID', 'S-only')`,
	} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatalf("venue uniqueness violation was accepted: %s", query)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO venues (venue_type, display_title, source_scheme, source_identifier)
		VALUES ('journal', 'Synthetic Venue', 'crossref', 'duplicate-title-is-allowed')
	`); err != nil {
		t.Fatalf("venue title was incorrectly treated as a matching key: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO venue_aliases (venue_id, alias, source)
		VALUES ($1, 'Synthetic Venue', 'fixture')
	`, venueID); err != nil {
		t.Fatalf("insert title alias: %v", err)
	}

	invalidMetrics := []string{
		`INSERT INTO venue_metric_snapshots
		 (venue_id, metric_year, category, jif, quartile, metric_status, source_name, source_license)
		 VALUES ($1, 2025, 'AI', -1, 'Q1', 'known', 'fixture', 'synthetic')`,
		`INSERT INTO venue_metric_snapshots
		 (venue_id, metric_year, category, jif, quartile, metric_status, source_name, source_license)
		 VALUES ($1, 2025, 'AI', 10, 'Q5', 'known', 'fixture', 'synthetic')`,
		`INSERT INTO venue_metric_snapshots
		 (venue_id, metric_year, category, jif, quartile, metric_status, source_name, source_license)
		 VALUES ($1, 2025, 'AI', NULL, NULL, 'known', 'fixture', 'synthetic')`,
	}
	for _, query := range invalidMetrics {
		if _, err := pool.Exec(ctx, query, venueID); err == nil {
			t.Fatalf("invalid venue metric was accepted: %s", query)
		}
	}
	var metricID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_metric_snapshots
			(venue_id, metric_year, category, jif, quartile, metric_status, source_name, source_license)
		VALUES ($1, 2025, 'AI', 12.5, 'Q1', 'known', 'fixture', 'synthetic')
		RETURNING id
	`, venueID), &metricID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO venue_metric_snapshots
			(venue_id, metric_year, category, jif, quartile, metric_status, source_name, source_license)
		VALUES ($1, 2025, 'AI', 13, 'Q1', 'known', 'fixture', 'synthetic')
	`, venueID); err == nil {
		t.Fatal("duplicate venue/year/category metric was accepted")
	}
	if _, err := pool.Exec(ctx, "UPDATE venue_metric_snapshots SET jif = 13 WHERE id = $1", metricID); err == nil {
		t.Fatal("historical venue metric update was accepted")
	}
	if _, err := pool.Exec(ctx, "DELETE FROM venue_metric_snapshots WHERE id = $1", metricID); err == nil {
		t.Fatal("historical venue metric delete was accepted")
	}

	var policyID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_policy_versions (policy_name, version_number, definition, effective_at)
		VALUES ('curated-journal', 1, '{"accept":["jif_gte_10","jcr_q1"]}', now())
		RETURNING id
	`), &policyID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO venue_policy_assessments (
			venue_id, policy_version_id, metric_year, decision, matched_rules, evidence, assessed_at
		) VALUES ($1, $2, 2025, 'unknown', '[]', '{"reason":"missing licensed metric"}', now())
	`, venueID, policyID); err != nil {
		t.Fatalf("insert unknown policy assessment: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO venue_policy_assessments (
			venue_id, policy_version_id, metric_year, decision, matched_rules, evidence, assessed_at
		) VALUES ($1, $2, 2025, 'accepted', '[]', '{"reason":"no matched rule"}', now())
	`, venueID, policyID); err == nil {
		t.Fatal("accepted assessment without matched rules was accepted")
	}
	if _, err := pool.Exec(ctx, "UPDATE venue_policy_versions SET definition = '{}' WHERE id = $1", policyID); err == nil {
		t.Fatal("immutable policy version update was accepted")
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO fulltext_assets (
			work_id, source_record_id, source_url, license, content_hash, retrieved_at,
			reusable_status, publication_status
		) VALUES ($1, $2, 'https://example.test/fulltext.xml', 'unknown', 'asset-hash', now(), 'unknown', 'public')
	`, workID, sourceID); err == nil {
		t.Fatal("public fulltext without explicit reusable status was accepted")
	}
	invalidFulltext := []string{
		`INSERT INTO fulltext_assets (
			work_id, source_record_id, source_url, license, content_hash, retrieved_at,
			reusable_status, publication_status
		) VALUES ($1, $2, '', 'CC-BY-4.0', 'missing-url', now(), 'reusable', 'private')`,
		`INSERT INTO fulltext_assets (
			work_id, source_record_id, source_url, license, content_hash, retrieved_at,
			reusable_status, publication_status
		) VALUES ($1, $2, 'https://example.test/no-license', '', 'missing-license', now(), 'reusable', 'private')`,
		`INSERT INTO fulltext_assets (
			work_id, source_record_id, source_url, license, content_hash, retrieved_at,
			reusable_status, publication_status
		) VALUES ($1, $2, 'https://example.test/no-hash', 'CC-BY-4.0', '', now(), 'reusable', 'private')`,
		`INSERT INTO fulltext_assets (
			work_id, source_record_id, source_url, license, content_hash, retrieved_at,
			reusable_status, publication_status
		) VALUES ($1, $2, 'https://example.test/no-retrieved-at', 'CC-BY-4.0', 'missing-time', NULL, 'reusable', 'private')`,
	}
	for _, query := range invalidFulltext {
		if _, err := pool.Exec(ctx, query, workID, sourceID); err == nil {
			t.Fatalf("fulltext missing required provenance was accepted: %s", query)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO fulltext_assets (
			work_id, source_record_id, source_url, license, content_hash, retrieved_at,
			reusable_status, publication_status
		) VALUES ($1, $2, 'https://example.test/fulltext.xml', 'CC-BY-4.0', 'asset-hash', now(), 'reusable', 'public')
	`, workID, sourceID); err != nil {
		t.Fatalf("insert explicitly reusable public fulltext: %v", err)
	}
}

func openMigratedTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := openTestPool(t)
	if err := Up(testContext(t), pool); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
	return pool
}

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg := testDatabaseConfig(newTestDatabase(t))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func insertWork(t *testing.T, pool *pgxpool.Pool, canonicalKey string) string {
	t.Helper()
	var id string
	mustScanID(t, pool.QueryRow(testContext(t), `
		INSERT INTO works (canonical_key, status, title)
		VALUES ($1, 'active', $1)
		RETURNING id
	`, canonicalKey), &id)
	return id
}

func insertSourceRecord(
	t *testing.T,
	pool *pgxpool.Pool,
	workID string,
	source string,
	sourceRecordID string,
	contentHash string,
) string {
	t.Helper()
	var id string
	mustScanID(t, pool.QueryRow(testContext(t), `
		WITH inserted_source AS (
			INSERT INTO source_records (
				source, source_record_id, source_identity, source_time, content_hash, raw_payload
			) VALUES (
				$2, $3,
				jsonb_build_object('id', $3::text),
				now(),
				$4,
				jsonb_build_object('id', $3::text)
			)
			RETURNING id
		),
		linked_source AS (
			INSERT INTO source_record_works (source_record_id, work_id)
			SELECT id, $1
			FROM inserted_source
		)
		SELECT id
		FROM inserted_source
	`, workID, source, sourceRecordID, contentHash), &id)
	return id
}

type scanner interface {
	Scan(...any) error
}

func mustScanID(t *testing.T, row scanner, destination *string) {
	t.Helper()
	if err := row.Scan(destination); err != nil {
		t.Fatalf("scan inserted ID: %v", err)
	}
}

func assertNamesExist(t *testing.T, pool *pgxpool.Pool, query string, expected []string) {
	t.Helper()
	rows, err := pool.Query(testContext(t), query)
	if err != nil {
		t.Fatalf("query schema names: %v", err)
	}
	defer rows.Close()
	actual := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan schema name: %v", err)
		}
		actual[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate schema names: %v", err)
	}
	for _, name := range expected {
		if !actual[name] {
			t.Errorf("expected schema object %q was not created", name)
		}
	}
}

func loadJCRFixture(t *testing.T) []map[string]string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migrate_test.go path")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "..", "data", "venues", "jcr-q1.example.csv")
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open synthetic JCR fixture: %v", err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read synthetic JCR fixture: %v", err)
	}
	if len(records) < 2 {
		t.Fatal("synthetic JCR fixture contains no data rows")
	}
	header := records[0]
	result := make([]map[string]string, 0, len(records)-1)
	for _, row := range records[1:] {
		if len(row) != len(header) {
			t.Fatalf("synthetic JCR fixture row width = %d, want %d", len(row), len(header))
		}
		record := make(map[string]string, len(header))
		for index, key := range header {
			record[key] = row[index]
		}
		result = append(result, record)
	}
	return result
}

func resolveVenueByExactISSN(
	ctx context.Context,
	pool *pgxpool.Pool,
	record map[string]string,
) (string, error) {
	var venueID string
	err := pool.QueryRow(ctx, `
		SELECT resolve_venue_by_issn($1, $2, $3)::text
	`, nullable(record["issn_l"]), nullable(record["issn"]), nullable(record["eissn"])).Scan(&venueID)
	return venueID, err
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func TestMigrationContextCancellationIsReturned(t *testing.T) {
	pool := openTestPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := UpMigrations(ctx, pool, []Migration{{Version: 99, Name: "cancelled", SQL: "SELECT 1"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("UpMigrations() error = %v, want context.Canceled", err)
	}
}

func ExampleMigration() {
	fmt.Println("SQL migrations are embedded and applied explicitly by paper-hub-migrate up")
	// Output: SQL migrations are embedded and applied explicitly by paper-hub-migrate up
}
