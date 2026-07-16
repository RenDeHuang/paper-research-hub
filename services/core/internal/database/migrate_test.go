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

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	paperdomain "github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
)

const initialMigrationChecksum = "3568e26689b33d651fe0ee5089587ba2d3efd74517eb430b83766f9907881404"

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
	"jcr_import_receipts",
	"jcr_import_receipt_aliases",
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

	rows, err = pool.Query(ctx, `
		SELECT version, name, checksum, applied_at
		FROM schema_migrations
		ORDER BY version
	`)
	if err != nil {
		t.Fatalf("query migration records: %v", err)
	}
	defer rows.Close()
	expectedMigrations := []struct {
		version int64
		name    string
	}{
		{version: 1, name: "initial"},
		{version: 2, name: "integrity_hardening"},
		{version: 3, name: "jcr_import_receipts"},
	}
	var migrationIndex int
	for rows.Next() {
		if migrationIndex >= len(expectedMigrations) {
			t.Fatalf("unexpected extra migration record at index %d", migrationIndex)
		}
		var version int64
		var name, checksum string
		var appliedAt time.Time
		if err := rows.Scan(&version, &name, &checksum, &appliedAt); err != nil {
			t.Fatalf("scan migration record: %v", err)
		}
		expected := expectedMigrations[migrationIndex]
		if version != expected.version || name != expected.name || len(checksum) != 64 || appliedAt.IsZero() {
			t.Fatalf(
				"migration record %d = (%d, %q, %q, %s), want (%d, %q, SHA-256, applied_at)",
				migrationIndex,
				version,
				name,
				checksum,
				appliedAt,
				expected.version,
				expected.name,
			)
		}
		migrationIndex++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migration records: %v", err)
	}
	if migrationIndex != len(expectedMigrations) {
		t.Fatalf("migration record count = %d, want %d", migrationIndex, len(expectedMigrations))
	}
}

func TestEmbeddedMigrationsPreservePriorChecksumsAndAddJCRReceipts(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	if got := migrationChecksum(migrations[0].SQL); got != initialMigrationChecksum {
		t.Fatalf("000001_initial checksum = %s, want immutable %s", got, initialMigrationChecksum)
	}
	if len(migrations) != 3 {
		t.Fatalf("embedded migration count = %d, want 3", len(migrations))
	}
	if migrations[0].Version != 1 || migrations[0].Name != "initial" {
		t.Fatalf("first migration = %#v, want 000001_initial", migrations[0])
	}
	if migrations[1].Version != 2 || migrations[1].Name != "integrity_hardening" {
		t.Fatalf("second migration = %#v, want 000002_integrity_hardening", migrations[1])
	}
	if migrations[2].Version != 3 || migrations[2].Name != "jcr_import_receipts" {
		t.Fatalf("third migration = %#v, want 000003_jcr_import_receipts", migrations[2])
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
		"venue_policy_assessments_decision_semantics_check",
		"jcr_import_receipts_file_sha256_key",
		"jcr_import_receipts_file_sha256_check",
		"jcr_import_receipts_counts_check",
		"venue_metric_snapshots_jcr_import_receipt_id_fkey",
		"jcr_import_receipt_aliases_import_receipt_id_fkey",
		"jcr_import_receipt_aliases_venue_alias_id_fkey",
		"jcr_import_receipt_aliases_import_receipt_id_venue_alias_id_key",
		"fulltext_assets_public_reusable_check",
	})
	assertNamesExist(t, pool, `
		SELECT tgname
		FROM pg_trigger
		WHERE NOT tgisinternal
	`, []string{
		"source_records_immutable",
		"works_status_monotonic",
		"venue_metric_snapshots_immutable",
		"venue_policy_versions_immutable",
		"jcr_import_receipts_immutable",
		"jcr_import_receipt_aliases_immutable",
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
		"idx_venue_metric_snapshots_import_receipt",
		"idx_jcr_import_receipt_aliases_alias",
		"idx_fulltext_assets_public",
	})

	expectedDeleteRules := map[string]string{
		"source_record_works_source_record_id_fkey":         "r",
		"source_record_works_work_id_fkey":                  "c",
		"field_assertions_work_id_fkey":                     "n",
		"paper_versions_source_record_id_fkey":              "r",
		"field_assertions_source_record_id_fkey":            "r",
		"paper_versions_source_record_work_fkey":            "a",
		"field_assertions_source_record_work_fkey":          "a",
		"work_code_repositories_work_id_fkey":               "c",
		"work_code_repositories_repository_id_fkey":         "r",
		"venue_metric_snapshots_venue_id_fkey":              "r",
		"venue_metric_snapshots_jcr_import_receipt_id_fkey": "r",
		"venue_policy_assessments_policy_version_fkey":      "r",
		"jcr_import_receipt_aliases_import_receipt_id_fkey": "r",
		"jcr_import_receipt_aliases_venue_alias_id_fkey":    "r",
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

func TestJCRReceiptMigrationAddsTraceabilityAndNotApplicablePolicySemantics(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	var journalID, conferenceID, aliasID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title, issn_l, issn, eissn)
		VALUES ('journal', 'Synthetic Zero JIF Journal', '0000-0108', '0000-0116', '0000-0124')
		RETURNING id
	`), &journalID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('conference', 'Synthetic Conference')
		RETURNING id
	`), &conferenceID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_aliases (venue_id, alias, source)
		VALUES ($1, 'Synthetic Zero JIF Journal', 'synthetic-jcr-fixture')
		RETURNING id
	`, journalID), &aliasID)

	const fileSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	importedAt := time.Date(2026, time.July, 16, 9, 30, 0, 0, time.UTC)
	var receiptID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO jcr_import_receipts (
			file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
		) VALUES ($1, 'synthetic-jcr-fixture', $2, 1, 1, 0)
		RETURNING id
	`, fileSHA, importedAt), &receiptID)

	var metricID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license, captured_at, jcr_import_receipt_id
		) VALUES (
			$1, 2025, 'Zero JIF Studies', 0, 'Q2', 'known',
			'synthetic-jcr-fixture', 'synthetic-only', $2, $3
		)
		RETURNING id
	`, journalID, importedAt, receiptID), &metricID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO jcr_import_receipt_aliases (import_receipt_id, venue_alias_id)
		VALUES ($1, $2)
	`, receiptID, aliasID); err != nil {
		t.Fatalf("link imported alias to receipt: %v", err)
	}

	var tracedSHA, jif string
	var tracedImportedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT receipt.file_sha256, receipt.imported_at, metric.jif::text
		FROM venue_metric_snapshots AS metric
		JOIN jcr_import_receipts AS receipt
		  ON receipt.id = metric.jcr_import_receipt_id
		WHERE metric.id = $1
	`, metricID).Scan(&tracedSHA, &tracedImportedAt, &jif); err != nil {
		t.Fatalf("query metric import provenance: %v", err)
	}
	if tracedSHA != fileSHA || !tracedImportedAt.Equal(importedAt) || jif != "0" {
		t.Fatalf(
			"metric provenance = SHA %q imported_at %v JIF %q, want %q %v 0",
			tracedSHA,
			tracedImportedAt,
			jif,
			fileSHA,
			importedAt,
		)
	}

	for _, query := range []string{
		`UPDATE jcr_import_receipts SET input_rows = 2 WHERE id = $1`,
		`DELETE FROM jcr_import_receipts WHERE id = $1`,
		`UPDATE jcr_import_receipt_aliases SET venue_alias_id = venue_alias_id WHERE import_receipt_id = $1`,
		`DELETE FROM jcr_import_receipt_aliases WHERE import_receipt_id = $1`,
	} {
		if _, err := pool.Exec(ctx, query, receiptID); err == nil {
			t.Fatalf("immutable JCR import provenance mutation was accepted: %s", query)
		}
	}
	for _, query := range []string{
		`INSERT INTO jcr_import_receipts
		 (file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows)
		 VALUES ('short', 'fixture', now(), 1, 1, 0)`,
		`INSERT INTO jcr_import_receipts
		 (file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows)
		 VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
		         'fixture', now(), 2, 1, 0)`,
		`INSERT INTO jcr_import_receipts
		 (file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows)
		 VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
		         'fixture', now(), 1, 1, 0)`,
	} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatalf("invalid JCR import receipt was accepted: %s", query)
		}
	}

	var policyID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_policy_versions (policy_name, version_number, definition, effective_at)
		VALUES ('curated-journal', 99, '{"accept":["jif_gte_10","jcr_q1"]}', now())
		RETURNING id
	`), &policyID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO venue_policy_assessments (
			venue_id, policy_version_id, metric_year, decision,
			matched_rules, evidence, assessed_at
		) VALUES (
			$1, $2, 2025, 'not_applicable', '[]',
			'{"reason":"journal_policy_not_applicable","venue_type":"conference"}',
			now()
		)
	`, conferenceID, policyID); err != nil {
		t.Fatalf("persist not_applicable policy assessment: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO venue_policy_assessments (
			venue_id, policy_version_id, metric_year, decision,
			matched_rules, evidence, assessed_at
		 ) VALUES (
			$1, $2, 2026, 'not_applicable', '["jcr_q1"]',
			'{"reason":"journal_policy_not_applicable","venue_type":"conference"}',
			now()
		 )`,
		`INSERT INTO venue_policy_assessments (
			venue_id, policy_version_id, metric_year, decision,
			matched_rules, evidence, assessed_at
		 ) VALUES (
			$1, $2, 2027, 'not_applicable', '[]',
			'{"venue_type":"conference"}',
			now()
		 )`,
	} {
		if _, err := pool.Exec(ctx, query, conferenceID, policyID); err == nil {
			t.Fatalf("invalid not_applicable assessment was accepted: %s", query)
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
	if beforeCount != 3 || afterCount != beforeCount || afterChecksum != beforeChecksum {
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

func TestMigrationUpgradesAppliedInitialSchemaWithoutChecksumMismatch(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	if got := migrationChecksum(migrations[0].SQL); got != initialMigrationChecksum {
		t.Fatalf("000001_initial checksum = %s, want immutable %s", got, initialMigrationChecksum)
	}
	if len(migrations) != 3 {
		t.Fatalf("embedded migration count = %d, want 3", len(migrations))
	}

	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:1]); err != nil {
		t.Fatalf("apply original 000001_initial: %v", err)
	}

	var workID, sourceRecordID, paperVersionID, externalIdentifierID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO works (canonical_key, status, title)
		VALUES ('doi:10.1000/Legacy', 'active', 'Legacy Work')
		RETURNING id
	`), &workID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO source_records (
			work_id, source, source_record_id, source_identity, source_time, content_hash, raw_payload
		) VALUES (
			$1, 'crossref', 'legacy-source', '{"id":"legacy-source"}', now(),
			'legacy-hash', '{"id":"legacy-source"}'
		)
		RETURNING id
	`, workID), &sourceRecordID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO paper_versions (work_id, source_record_id, version_label, status)
		VALUES ($1, $2, 'v1', 'active')
		RETURNING id
	`, workID, sourceRecordID), &paperVersionID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO external_identifiers (work_id, source_record_id, scheme, normalized_value)
		VALUES ($1, $2, 'doi', '10.1000/Legacy')
		RETURNING id
	`, workID, sourceRecordID), &externalIdentifierID)

	var appliedInitialChecksum string
	if err := pool.QueryRow(ctx, `
		SELECT checksum
		FROM schema_migrations
		WHERE version = 1
	`).Scan(&appliedInitialChecksum); err != nil {
		t.Fatalf("query applied initial checksum: %v", err)
	}
	if appliedInitialChecksum != initialMigrationChecksum {
		t.Fatalf("applied initial checksum = %s, want %s", appliedInitialChecksum, initialMigrationChecksum)
	}

	if err := Up(ctx, pool); err != nil {
		t.Fatalf("upgrade applied 000001 schema to 000002: %v", err)
	}

	var canonicalKey, normalizedDOI, linkedWorkID string
	if err := pool.QueryRow(ctx, "SELECT canonical_key FROM works WHERE id = $1", workID).Scan(&canonicalKey); err != nil {
		t.Fatalf("query normalized canonical key: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT normalized_value
		FROM external_identifiers
		WHERE id = $1
	`, externalIdentifierID).Scan(&normalizedDOI); err != nil {
		t.Fatalf("query normalized external identifier: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT work_id::text
		FROM source_record_works
		WHERE source_record_id = $1
	`, sourceRecordID).Scan(&linkedWorkID); err != nil {
		t.Fatalf("query backfilled SourceRecord Work link: %v", err)
	}
	if canonicalKey != "doi:10.1000/legacy" ||
		normalizedDOI != "10.1000/legacy" ||
		linkedWorkID != workID {
		t.Fatalf(
			"upgrade backfill = canonical %q, DOI %q, linked Work %q; want normalized values and %q",
			canonicalKey,
			normalizedDOI,
			linkedWorkID,
			workID,
		)
	}

	var sourceRecordWorkColumnCount, paperVersionCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'source_records'
		  AND column_name = 'work_id'
	`).Scan(&sourceRecordWorkColumnCount); err != nil {
		t.Fatalf("query upgraded SourceRecord columns: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM paper_versions
		WHERE id = $1 AND work_id = $2 AND source_record_id = $3
	`, paperVersionID, workID, sourceRecordID).Scan(&paperVersionCount); err != nil {
		t.Fatalf("query preserved paper version: %v", err)
	}
	if sourceRecordWorkColumnCount != 0 || paperVersionCount != 1 {
		t.Fatalf(
			"upgraded ownership = source_records.work_id columns %d, paper versions %d; want 0, 1",
			sourceRecordWorkColumnCount,
			paperVersionCount,
		)
	}

	var appliedCount int
	var preservedChecksum string
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(checksum) FILTER (WHERE version = 1)
		FROM schema_migrations
	`).Scan(&appliedCount, &preservedChecksum); err != nil {
		t.Fatalf("query upgraded migration records: %v", err)
	}
	if appliedCount != 3 || preservedChecksum != initialMigrationChecksum {
		t.Fatalf(
			"upgraded migrations = count %d, initial checksum %s; want 3, %s",
			appliedCount,
			preservedChecksum,
			initialMigrationChecksum,
		)
	}
}

func TestMigrationReconcilesNormalizedDuplicateWorksAndPreservesEvidence(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:1]); err != nil {
		t.Fatalf("apply original 000001_initial: %v", err)
	}

	const (
		survivorWorkID  = "00000000-0000-0000-0000-000000000001"
		duplicateWorkID = "00000000-0000-0000-0000-000000000002"
	)
	var venueID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title, issn_l)
		VALUES ('journal', 'Merge Venue', '1234-5679')
		RETURNING id
	`), &venueID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO works (
			id, canonical_key, status, title, abstract, published_at, venue_id, created_at, updated_at
		)
		VALUES
			(
				$1, 'doi:10.1000/Merge-Me', 'active', 'Merge Work',
				NULL, NULL, NULL,
				'2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z'
			),
			(
				$2, 'doi:10.1000/merge-me', 'retracted', 'Merge Work',
				'Recovered abstract', '2024-02-03T04:05:06Z', $3,
				'2021-01-01T00:00:00Z', '2021-01-01T00:00:00Z'
			)
	`, survivorWorkID, duplicateWorkID, venueID); err != nil {
		t.Fatalf("insert duplicate legacy Works: %v", err)
	}

	sourceOne := insertLegacySourceRecord(t, pool, survivorWorkID, "crossref", "merge-source-one", "merge-hash-one")
	sourceTwo := insertLegacySourceRecord(t, pool, duplicateWorkID, "openalex", "merge-source-two", "merge-hash-two")
	if _, err := pool.Exec(ctx, `
		INSERT INTO field_assertions (work_id, source_record_id, field_name, asserted_value)
		VALUES
			($1, $2, 'title', '{"value":"Merge Work","source":"crossref"}'),
			($3, $4, 'abstract', '{"value":"Preserved evidence","source":"openalex"}')
	`, survivorWorkID, sourceOne, duplicateWorkID, sourceTwo); err != nil {
		t.Fatalf("insert legacy FieldAssertions: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO paper_versions (work_id, source_record_id, version_label, status)
		VALUES
			($1, $2, 'v1', 'active'),
			($3, $4, 'v2', 'active')
	`, survivorWorkID, sourceOne, duplicateWorkID, sourceTwo); err != nil {
		t.Fatalf("insert non-conflicting legacy PaperVersions: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO external_identifiers (work_id, source_record_id, scheme, normalized_value)
		VALUES
			($1, $2, 'doi', '10.1000/Merge-Me'),
			($3, $4, 'doi', '10.1000/merge-me')
	`, survivorWorkID, sourceOne, duplicateWorkID, sourceTwo); err != nil {
		t.Fatalf("insert duplicate legacy ExternalIdentifiers: %v", err)
	}
	var repositoryID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO code_repositories (canonical_url, host, owner_name, repository_name)
		VALUES ('https://example.test/merge/repo', 'example.test', 'merge', 'repo')
		RETURNING id
	`), &repositoryID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_code_repositories (work_id, repository_id, relation_type)
		VALUES
			($1, $3, 'official'),
			($2, $3, 'official')
	`, survivorWorkID, duplicateWorkID, repositoryID); err != nil {
		t.Fatalf("insert duplicate legacy repository links: %v", err)
	}

	if err := Up(ctx, pool); err != nil {
		t.Fatalf("upgrade duplicate normalized Works: %v", err)
	}

	var survivingID string
	if err := pool.QueryRow(ctx, `
		SELECT id::text
		FROM works
		WHERE canonical_key = 'doi:10.1000/merge-me'
	`).Scan(&survivingID); err != nil {
		t.Fatalf("query reconciled Work: %v", err)
	}
	if survivingID != survivorWorkID {
		t.Fatalf("reconciled Work ID = %s, want stable survivor %s", survivingID, survivorWorkID)
	}

	var (
		reconciledStatus      string
		reconciledAbstract    *string
		reconciledPublishedAt *time.Time
		reconciledVenueID     *string
	)
	if err := pool.QueryRow(ctx, `
		SELECT status, abstract, published_at, venue_id::text
		FROM works
		WHERE id = $1
	`, survivorWorkID).Scan(
		&reconciledStatus,
		&reconciledAbstract,
		&reconciledPublishedAt,
		&reconciledVenueID,
	); err != nil {
		t.Fatalf("query reconciled Work metadata: %v", err)
	}
	wantPublishedAt := time.Date(2024, time.February, 3, 4, 5, 6, 0, time.UTC)
	if reconciledStatus != "retracted" ||
		reconciledAbstract == nil ||
		*reconciledAbstract != "Recovered abstract" ||
		reconciledPublishedAt == nil ||
		!reconciledPublishedAt.Equal(wantPublishedAt) ||
		reconciledVenueID == nil ||
		*reconciledVenueID != venueID {
		t.Fatalf(
			"reconciled metadata = status %q, abstract %v, published_at %v, venue %v; want retracted, recovered abstract, %s, %s",
			reconciledStatus,
			reconciledAbstract,
			reconciledPublishedAt,
			reconciledVenueID,
			wantPublishedAt,
			venueID,
		)
	}

	assertWorkReferenceCount(t, pool, "source_record_works", "work_id", survivorWorkID, 2)
	assertWorkReferenceCount(t, pool, "field_assertions", "work_id", survivorWorkID, 2)
	assertWorkReferenceCount(t, pool, "paper_versions", "work_id", survivorWorkID, 2)
	assertWorkReferenceCount(t, pool, "work_code_repositories", "work_id", survivorWorkID, 1)

	var sourceEvidenceCount, externalIdentifierCount, duplicateWorkCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM source_records
		WHERE id IN ($1, $2)
		  AND raw_payload->>'id' IN ('merge-source-one', 'merge-source-two')
	`, sourceOne, sourceTwo).Scan(&sourceEvidenceCount); err != nil {
		t.Fatalf("count preserved SourceRecord evidence: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM external_identifiers
		WHERE work_id = $1
		  AND scheme = 'doi'
		  AND normalized_value = '10.1000/merge-me'
	`, survivorWorkID).Scan(&externalIdentifierCount); err != nil {
		t.Fatalf("count reconciled ExternalIdentifiers: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM works WHERE id = $1", duplicateWorkID).Scan(&duplicateWorkCount); err != nil {
		t.Fatalf("count removed duplicate Work: %v", err)
	}
	if sourceEvidenceCount != 2 || externalIdentifierCount != 1 || duplicateWorkCount != 0 {
		t.Fatalf(
			"reconciled data = SourceRecords %d, ExternalIdentifiers %d, duplicate Works %d; want 2, 1, 0",
			sourceEvidenceCount,
			externalIdentifierCount,
			duplicateWorkCount,
		)
	}
}

func TestMigrationRejectsConflictingDuplicateWorkMetadataWithContext(t *testing.T) {
	testCases := []struct {
		name          string
		field         string
		titleOne      string
		titleTwo      string
		abstractOne   any
		abstractTwo   any
		publishedOne  any
		publishedTwo  any
		statusOne     string
		statusTwo     string
		venueConflict bool
	}{
		{
			name:      "different non-empty titles",
			field:     "title",
			titleOne:  "First title",
			titleTwo:  "Second title",
			statusOne: "active",
			statusTwo: "active",
		},
		{
			name:        "different non-empty abstracts",
			field:       "abstract",
			titleOne:    "Shared title",
			titleTwo:    "Shared title",
			abstractOne: "First abstract",
			abstractTwo: "Second abstract",
			statusOne:   "active",
			statusTwo:   "active",
		},
		{
			name:         "different publication times",
			field:        "published_at",
			titleOne:     "Shared title",
			titleTwo:     "Shared title",
			publishedOne: time.Date(2023, time.January, 2, 3, 4, 5, 0, time.UTC),
			publishedTwo: time.Date(2024, time.January, 2, 3, 4, 5, 0, time.UTC),
			statusOne:    "active",
			statusTwo:    "active",
		},
		{
			name:          "different venues",
			field:         "venue_id",
			titleOne:      "Shared title",
			titleTwo:      "Shared title",
			statusOne:     "active",
			statusTwo:     "active",
			venueConflict: true,
		},
		{
			name:      "different terminal statuses",
			field:     "status",
			titleOne:  "Shared title",
			titleTwo:  "Shared title",
			statusOne: "retracted",
			statusTwo: "rejected",
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			migrations, err := EmbeddedMigrations()
			if err != nil {
				t.Fatalf("EmbeddedMigrations() error = %v", err)
			}
			pool := openTestPool(t)
			ctx := testContext(t)
			if err := UpMigrations(ctx, pool, migrations[:1]); err != nil {
				t.Fatalf("apply original 000001_initial: %v", err)
			}

			var venueOne, venueTwo any
			if tt.venueConflict {
				var firstVenueID, secondVenueID string
				mustScanID(t, pool.QueryRow(ctx, `
					INSERT INTO venues (venue_type, display_title, issn_l)
					VALUES ('journal', 'First Venue', '1111-1119')
					RETURNING id
				`), &firstVenueID)
				mustScanID(t, pool.QueryRow(ctx, `
					INSERT INTO venues (venue_type, display_title, issn_l)
					VALUES ('journal', 'Second Venue', '2222-2227')
					RETURNING id
				`), &secondVenueID)
				venueOne = firstVenueID
				venueTwo = secondVenueID
			}

			const (
				firstWorkID  = "00000000-0000-0000-0000-000000000021"
				secondWorkID = "00000000-0000-0000-0000-000000000022"
			)
			if _, err := pool.Exec(ctx, `
				INSERT INTO works (
					id, canonical_key, status, title, abstract, published_at, venue_id, created_at, updated_at
				)
				VALUES
					(
						$1, 'doi:10.1000/Metadata-Conflict', $3, $5, $7, $9, $11,
						'2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z'
					),
					(
						$2, 'doi:10.1000/metadata-conflict', $4, $6, $8, $10, $12,
						'2021-01-01T00:00:00Z', '2021-01-01T00:00:00Z'
					)
			`,
				firstWorkID,
				secondWorkID,
				tt.statusOne,
				tt.statusTwo,
				tt.titleOne,
				tt.titleTwo,
				tt.abstractOne,
				tt.abstractTwo,
				tt.publishedOne,
				tt.publishedTwo,
				venueOne,
				venueTwo,
			); err != nil {
				t.Fatalf("insert conflicting legacy Works: %v", err)
			}
			firstSource := insertLegacySourceRecord(t, pool, firstWorkID, "crossref", "metadata-source-one", "metadata-hash-one")
			secondSource := insertLegacySourceRecord(t, pool, secondWorkID, "openalex", "metadata-source-two", "metadata-hash-two")

			err = Up(ctx, pool)
			if err == nil ||
				!strings.Contains(err.Error(), "cannot reconcile normalized identity doi:10.1000/metadata-conflict") ||
				!strings.Contains(err.Error(), "field "+tt.field) {
				t.Fatalf("metadata conflict error = %v, want contextual %s conflict", err, tt.field)
			}

			var appliedV2, retainedWorks, retainedSources int
			if queryErr := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = 2").Scan(&appliedV2); queryErr != nil {
				t.Fatalf("count failed v2 migration records: %v", queryErr)
			}
			if queryErr := pool.QueryRow(ctx, "SELECT count(*) FROM works WHERE id IN ($1, $2)", firstWorkID, secondWorkID).Scan(&retainedWorks); queryErr != nil {
				t.Fatalf("count retained conflicting Works: %v", queryErr)
			}
			if queryErr := pool.QueryRow(ctx, "SELECT count(*) FROM source_records WHERE id IN ($1, $2)", firstSource, secondSource).Scan(&retainedSources); queryErr != nil {
				t.Fatalf("count retained conflicting SourceRecords: %v", queryErr)
			}
			if appliedV2 != 0 || retainedWorks != 2 || retainedSources != 2 {
				t.Fatalf(
					"failed metadata reconciliation state = applied v2 %d, Works %d, SourceRecords %d; want 0, 2, 2",
					appliedV2,
					retainedWorks,
					retainedSources,
				)
			}
		})
	}
}

func TestMigrationRejectsAmbiguousDuplicateWorkProjectionWithContext(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:1]); err != nil {
		t.Fatalf("apply original 000001_initial: %v", err)
	}

	const (
		survivorWorkID  = "00000000-0000-0000-0000-000000000011"
		duplicateWorkID = "00000000-0000-0000-0000-000000000012"
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO works (id, canonical_key, status, title, created_at, updated_at)
		VALUES
			($1, 'doi:10.1000/Conflict', 'active', 'Conflict Work', '2020-01-01T00:00:00Z', now()),
			($2, 'doi:10.1000/conflict', 'active', 'Conflict Work', '2021-01-01T00:00:00Z', now())
	`, survivorWorkID, duplicateWorkID); err != nil {
		t.Fatalf("insert conflicting legacy Works: %v", err)
	}
	sourceOne := insertLegacySourceRecord(t, pool, survivorWorkID, "crossref", "conflict-source-one", "conflict-hash-one")
	sourceTwo := insertLegacySourceRecord(t, pool, duplicateWorkID, "openalex", "conflict-source-two", "conflict-hash-two")
	if _, err := pool.Exec(ctx, `
		INSERT INTO paper_versions (work_id, source_record_id, version_label, status)
		VALUES
			($1, $2, 'v1', 'active'),
			($3, $4, 'v1', 'active')
	`, survivorWorkID, sourceOne, duplicateWorkID, sourceTwo); err != nil {
		t.Fatalf("insert ambiguous legacy PaperVersions: %v", err)
	}

	err = Up(ctx, pool)
	if err == nil ||
		!strings.Contains(err.Error(), "cannot reconcile normalized identity doi:10.1000/conflict") ||
		!strings.Contains(err.Error(), "paper_versions") {
		t.Fatalf("ambiguous upgrade error = %v, want contextual paper_versions reconciliation failure", err)
	}

	var appliedV2, retainedWorks, retainedSources int
	if queryErr := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = 2").Scan(&appliedV2); queryErr != nil {
		t.Fatalf("count failed v2 migration records: %v", queryErr)
	}
	if queryErr := pool.QueryRow(ctx, "SELECT count(*) FROM works WHERE id IN ($1, $2)", survivorWorkID, duplicateWorkID).Scan(&retainedWorks); queryErr != nil {
		t.Fatalf("count retained conflicting Works: %v", queryErr)
	}
	if queryErr := pool.QueryRow(ctx, "SELECT count(*) FROM source_records WHERE id IN ($1, $2)", sourceOne, sourceTwo).Scan(&retainedSources); queryErr != nil {
		t.Fatalf("count retained conflicting SourceRecords: %v", queryErr)
	}
	if appliedV2 != 0 || retainedWorks != 2 || retainedSources != 2 {
		t.Fatalf(
			"failed reconciliation state = applied v2 %d, Works %d, SourceRecords %d; want 0, 2, 2",
			appliedV2,
			retainedWorks,
			retainedSources,
		)
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

func TestCanonicalAndExternalIdentifierNormalizationIsSchemeAware(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	const (
		s2Lower = "a0b1c2d3e4f5678901234567890abcdeffedcba9"
		s2Upper = "A0B1C2D3E4F5678901234567890ABCDEFFEDCBA9"
	)
	doiWorkID := insertWork(t, pool, "doi:10.1000/case-sensitive-storage")
	arXivWorkID := insertWork(t, pool, "arxiv:hep-th/9901001")
	s2WorkID := insertWork(t, pool, "s2:"+s2Lower)
	openAlexWorkID := insertWork(t, pool, "openalex:W123")
	openReviewUpperID := insertWork(t, pool, "openreview:Forum_AbC123")
	openReviewLowerID := insertWork(t, pool, "openreview:forum_AbC123")

	invalidCanonicalKeys := []struct {
		name           string
		canonicalKey   string
		wantConstraint string
	}{
		{
			name:           "DOI case variant",
			canonicalKey:   "doi:10.1000/CASE-SENSITIVE-STORAGE",
			wantConstraint: "works_canonical_key_check",
		},
		{
			name:           "arXiv case variant",
			canonicalKey:   "arxiv:HEP-TH/9901001",
			wantConstraint: "works_canonical_key_check",
		},
		{
			name:           "Semantic Scholar case variant",
			canonicalKey:   "s2:" + s2Upper,
			wantConstraint: "works_canonical_key_check",
		},
		{
			name:           "OpenAlex lowercase",
			canonicalKey:   "openalex:w123",
			wantConstraint: "works_canonical_key_check",
		},
		{
			name:           "OpenAlex malformed",
			canonicalKey:   "openalex:W-123",
			wantConstraint: "works_canonical_key_check",
		},
	}
	for _, tt := range invalidCanonicalKeys {
		t.Run("canonical "+tt.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO works (canonical_key, status, title)
				VALUES ($1, 'active', $1)
			`, tt.canonicalKey)
			assertPostgresError(t, err, "23514", tt.wantConstraint)
		})
	}

	validExternalIdentifiers := []struct {
		workID string
		scheme string
		value  string
	}{
		{workID: doiWorkID, scheme: "doi", value: "10.1000/case-sensitive-storage"},
		{workID: arXivWorkID, scheme: "arxiv", value: "hep-th/9901001"},
		{workID: s2WorkID, scheme: "s2", value: s2Lower},
		{workID: openAlexWorkID, scheme: "openalex", value: "W123"},
		{workID: openReviewUpperID, scheme: "openreview", value: "Forum_AbC123"},
		{workID: openReviewLowerID, scheme: "openreview", value: "forum_AbC123"},
	}
	for _, identifier := range validExternalIdentifiers {
		if _, err := pool.Exec(ctx, `
			INSERT INTO external_identifiers (work_id, scheme, normalized_value)
			VALUES ($1, $2, $3)
		`, identifier.workID, identifier.scheme, identifier.value); err != nil {
			t.Fatalf("insert normalized %s identifier %q: %v", identifier.scheme, identifier.value, err)
		}
	}

	invalidExternalIdentifiers := []struct {
		name   string
		workID string
		scheme string
		value  string
	}{
		{
			name:   "DOI case variant",
			workID: doiWorkID,
			scheme: "doi",
			value:  "10.1000/CASE-SENSITIVE-STORAGE",
		},
		{
			name:   "arXiv case variant",
			workID: arXivWorkID,
			scheme: "arxiv",
			value:  "HEP-TH/9901001",
		},
		{
			name:   "Semantic Scholar case variant",
			workID: s2WorkID,
			scheme: "s2",
			value:  s2Upper,
		},
		{
			name:   "OpenAlex lowercase",
			workID: openAlexWorkID,
			scheme: "openalex",
			value:  "w123",
		},
		{
			name:   "OpenAlex malformed",
			workID: openAlexWorkID,
			scheme: "openalex",
			value:  "W-123",
		},
	}
	for _, tt := range invalidExternalIdentifiers {
		t.Run("external "+tt.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO external_identifiers (work_id, scheme, normalized_value)
				VALUES ($1, $2, $3)
			`, tt.workID, tt.scheme, tt.value)
			assertPostgresError(t, err, "23514", "external_identifiers_normalized_value_check")
		})
	}

	var preservedOpenReviewValues int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM external_identifiers
		WHERE scheme = 'openreview'
		  AND normalized_value IN ('Forum_AbC123', 'forum_AbC123')
	`).Scan(&preservedOpenReviewValues); err != nil {
		t.Fatalf("query case-preserved OpenReview identifiers: %v", err)
	}
	if preservedOpenReviewValues != 2 {
		t.Fatalf("case-preserved OpenReview identifiers = %d, want 2", preservedOpenReviewValues)
	}
}

func TestArXivNormalizationMatchesPaperDomainRules(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	testCases := []struct {
		name            string
		raw             string
		want            string
		storedCandidate string
		wantError       bool
	}{
		{
			name:            "legacy math subject class alias",
			raw:             "math.CA/0611800v2",
			want:            "math/0611800",
			storedCandidate: "math.ca/0611800",
		},
		{
			name:            "legacy cs subject class alias",
			raw:             "CS.AI/9901001",
			want:            "cs/9901001",
			storedCandidate: "cs.ai/9901001",
		},
		{
			name:            "controlled legacy archive",
			raw:             "hep-th/9108001v2",
			want:            "hep-th/9108001",
			storedCandidate: "hep-th/9108001v2",
		},
		{
			name:            "modern first four digit month",
			raw:             "0704.0001",
			want:            "0704.0001",
			storedCandidate: "0704.0001",
		},
		{
			name:            "modern last four digit month",
			raw:             "1412.9999v1",
			want:            "1412.9999",
			storedCandidate: "1412.9999v1",
		},
		{
			name:            "modern first five digit month",
			raw:             "1501.00001V12",
			want:            "1501.00001",
			storedCandidate: "1501.00001v12",
		},
		{name: "modern before 0704", raw: "0703.0001", storedCandidate: "0703.0001", wantError: true},
		{name: "four digit era with five digits", raw: "0704.00001", storedCandidate: "0704.00001", wantError: true},
		{name: "1412 with five digits", raw: "1412.00001", storedCandidate: "1412.00001", wantError: true},
		{name: "five digit era with four digits", raw: "1501.0001", storedCandidate: "1501.0001", wantError: true},
		{name: "zero sequence", raw: "2401.00000", storedCandidate: "2401.00000", wantError: true},
		{name: "zero version", raw: "2401.01234v0", storedCandidate: "2401.01234v0", wantError: true},
		{name: "legacy after cutoff", raw: "math/0704001", storedCandidate: "math/0704001", wantError: true},
		{name: "unknown legacy archive", raw: "unknown.AI/0611001", storedCandidate: "unknown.ai/0611001", wantError: true},
		{name: "subject class on non alias archive", raw: "hep-th.X/0611001", storedCandidate: "hep-th.x/0611001", wantError: true},
		{name: "legacy subject class trailing dot", raw: "math.CA./0611800", storedCandidate: "math.ca./0611800", wantError: true},
		{name: "legacy subject class repeated trailing dots", raw: "math.CA../0611800", storedCandidate: "math.ca../0611800", wantError: true},
	}

	for index, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			goNormalized, goErr := paperdomain.NormalizeArXiv(tt.raw)
			if tt.wantError {
				if goErr == nil {
					t.Fatalf("paper.NormalizeArXiv(%q) = %q, want error", tt.raw, goNormalized)
				}
			} else {
				if goErr != nil {
					t.Fatalf("paper.NormalizeArXiv(%q) error = %v", tt.raw, goErr)
				}
				if goNormalized != tt.want {
					t.Fatalf("paper.NormalizeArXiv(%q) = %q, want %q", tt.raw, goNormalized, tt.want)
				}
			}

			var databaseNormalized *string
			if err := pool.QueryRow(ctx, `
				SELECT normalize_paper_identifier('arxiv', $1)
			`, tt.raw).Scan(&databaseNormalized); err != nil {
				t.Fatalf("normalize arXiv in PostgreSQL: %v", err)
			}
			if tt.wantError {
				if databaseNormalized != nil {
					t.Fatalf("database normalized invalid arXiv %q to %q", tt.raw, *databaseNormalized)
				}
			} else if databaseNormalized == nil || *databaseNormalized != goNormalized {
				t.Fatalf(
					"database normalization for %q = %v, want Go output %q",
					tt.raw,
					databaseNormalized,
					goNormalized,
				)
			}

			var workID string
			if tt.wantError {
				workID = insertWork(t, pool, fmt.Sprintf("openreview:arxiv_invalid_%d", index))
				_, err := pool.Exec(ctx, `
					INSERT INTO works (canonical_key, status, title)
					VALUES ($1, 'active', $1)
				`, "arxiv:"+tt.storedCandidate)
				assertPostgresError(t, err, "23514", "works_canonical_key_check")
				_, err = pool.Exec(ctx, `
					INSERT INTO external_identifiers (work_id, scheme, normalized_value)
					VALUES ($1, 'arxiv', $2)
				`, workID, tt.storedCandidate)
				assertPostgresError(t, err, "23514", "external_identifiers_normalized_value_check")
				return
			}

			workID = insertWork(t, pool, "arxiv:"+goNormalized)
			if _, err := pool.Exec(ctx, `
				INSERT INTO external_identifiers (work_id, scheme, normalized_value)
				VALUES ($1, 'arxiv', $2)
			`, workID, goNormalized); err != nil {
				t.Fatalf("insert normalized arXiv external identifier %q: %v", goNormalized, err)
			}
			if tt.storedCandidate == goNormalized {
				return
			}
			_, err := pool.Exec(ctx, `
				INSERT INTO works (canonical_key, status, title)
				VALUES ($1, 'active', $1)
			`, "arxiv:"+tt.storedCandidate)
			assertPostgresError(t, err, "23514", "works_canonical_key_check")
			_, err = pool.Exec(ctx, `
				INSERT INTO external_identifiers (work_id, scheme, normalized_value)
				VALUES ($1, 'arxiv', $2)
			`, workID, tt.storedCandidate)
			assertPostgresError(t, err, "23514", "external_identifiers_normalized_value_check")
		})
	}
}

func TestMigrationDoesNotMergeMalformedLegacyArXivAlias(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:1]); err != nil {
		t.Fatalf("apply original 000001_initial: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO works (canonical_key, status, title, created_at)
		VALUES
			('arxiv:math/0611800', 'active', 'Valid arXiv Work', '2020-01-01T00:00:00Z'),
			('arxiv:math.CA./0611800', 'active', 'Malformed arXiv Work', '2021-01-01T00:00:00Z')
	`); err != nil {
		t.Fatalf("insert legacy arXiv aliases: %v", err)
	}

	err = Up(ctx, pool)
	if err == nil {
		t.Fatal("upgrade merged malformed legacy arXiv alias, want explicit rejection")
	}

	var appliedV2, retainedWorks int
	if queryErr := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = 2").Scan(&appliedV2); queryErr != nil {
		t.Fatalf("count failed v2 migration records: %v", queryErr)
	}
	if queryErr := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM works
		WHERE canonical_key IN ('arxiv:math/0611800', 'arxiv:math.CA./0611800')
	`).Scan(&retainedWorks); queryErr != nil {
		t.Fatalf("count retained legacy arXiv Works: %v", queryErr)
	}
	if appliedV2 != 0 || retainedWorks != 2 {
		t.Fatalf("malformed arXiv rollback state = applied v2 %d, Works %d; want 0, 2", appliedV2, retainedWorks)
	}
}

func TestWorkStatusTransitionsAreMonotonic(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	// This test covers database-level monotonicity only. Versioned repository
	// compare-and-swap for concurrent writers is intentionally deferred to Task 7.
	terminalStatuses := []string{"withdrawn", "retracted", "rejected", "superseded"}
	for _, terminalStatus := range terminalStatuses {
		t.Run(terminalStatus, func(t *testing.T) {
			workID := insertWork(t, pool, "openreview:status_"+terminalStatus)
			if _, err := pool.Exec(ctx, "UPDATE works SET status = $1 WHERE id = $2", terminalStatus, workID); err != nil {
				t.Fatalf("transition active -> %s: %v", terminalStatus, err)
			}
			if _, err := pool.Exec(ctx, "UPDATE works SET status = $1 WHERE id = $2", terminalStatus, workID); err != nil {
				t.Fatalf("repeat terminal status %s: %v", terminalStatus, err)
			}

			forbiddenTargets := []string{"active", "withdrawn", "retracted", "rejected", "superseded"}
			for _, targetStatus := range forbiddenTargets {
				if targetStatus == terminalStatus {
					continue
				}
				_, err := pool.Exec(ctx, "UPDATE works SET status = $1 WHERE id = $2", targetStatus, workID)
				assertPostgresError(t, err, "23514", "works_status_monotonic")
			}
		})
	}
}

func TestSourceRecordSnapshotIsCompletelyImmutable(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	workID := insertWork(t, pool, "openalex:W1001")
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
			assertPostgresError(t, updateErr, "55000", "")
		})
	}
}

func TestSourceRecordCannotBeDeletedWithoutAnyWorkAssociation(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	var sourceID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO source_records (
			source, source_record_id, source_identity, source_time, content_hash, raw_payload
		) VALUES (
			'openalex', 'unassociated-source', '{"id":"unassociated-source"}', now(),
			'unassociated-hash', '{"id":"unassociated-source"}'
		)
		RETURNING id
	`), &sourceID)

	_, err := pool.Exec(ctx, "DELETE FROM source_records WHERE id = $1", sourceID)
	assertPostgresError(t, err, "55000", "")

	var retained int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM source_records WHERE id = $1", sourceID).Scan(&retained); err != nil {
		t.Fatalf("count immutable unassociated SourceRecord: %v", err)
	}
	if retained != 1 {
		t.Fatalf("retained unassociated SourceRecord count = %d, want 1", retained)
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

	workID := insertWork(t, pool, "openalex:W1002")
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
	workID := insertWork(t, pool, "openalex:W1003")
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
	workID := insertWork(t, pool, "s2:0123456789abcdef0123456789abcdef01234567")
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

	var alphaVenueID, unknownVenueID, subthresholdVenueID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title, issn_l, issn, eissn)
		VALUES ('journal', 'Preexisting Alpha Venue', '0000-0019', '0000-0027', '0000-0035')
		RETURNING id
	`), &alphaVenueID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title, issn_l, issn, eissn)
		VALUES ('journal', 'Preexisting Unknown Venue', '0000-0043', '0000-0051', '0000-006X')
		RETURNING id
	`), &unknownVenueID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title, issn_l, issn, eissn)
		VALUES ('journal', 'Preexisting Subthreshold Venue', '0000-0078', '0000-0086', '0000-0094')
		RETURNING id
	`), &subthresholdVenueID)

	expectedVenueIDs := map[string]string{
		"0000-0019": alphaVenueID,
		"0000-0043": unknownVenueID,
		"0000-0078": subthresholdVenueID,
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
	var alphaCategories, alphaQ1Categories, highJIF, highJIFWithoutQ1, unknown int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE venue_id = $1),
			count(*) FILTER (WHERE quartile = 'Q1' AND venue_id = $1),
			count(*) FILTER (WHERE jif >= 10),
			count(*) FILTER (WHERE jif >= 10 AND quartile <> 'Q1'),
			count(*) FILTER (
				WHERE metric_status = 'unknown'
				  AND jif IS NULL
				  AND quartile IS NULL
				  AND venue_id = $2
			)
		FROM venue_metric_snapshots
	`, alphaVenueID, unknownVenueID).Scan(
		&alphaCategories,
		&alphaQ1Categories,
		&highJIF,
		&highJIFWithoutQ1,
		&unknown,
	); err != nil {
		t.Fatalf("query synthetic JCR semantics: %v", err)
	}
	if alphaCategories != 2 ||
		alphaQ1Categories < 1 ||
		highJIF < 1 ||
		highJIFWithoutQ1 < 1 ||
		unknown < 1 {
		t.Fatalf(
			"fixture semantics = alpha categories %d, alpha Q1 %d, JIF>=10 %d, JIF>=10 without Q1 %d, unknown %d",
			alphaCategories,
			alphaQ1Categories,
			highJIF,
			highJIFWithoutQ1,
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
		"issn_l": "0000-0108",
		"issn":   "0000-0116",
		"eissn":  "0000-0124",
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
		VALUES ('journal', 'Ambiguous ISSN Venue', '2718-2819')
	`); err != nil {
		t.Fatalf("insert ambiguous ISSN venue: %v", err)
	}

	_, err = resolveVenueByExactISSN(ctx, pool, map[string]string{
		"issn_l": "3141-592X",
		"issn":   "2718-2819",
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
		) VALUES ('journal', 'Synthetic Venue', '1111-1119', '1111-1119', '2222-2227', 'openalex', 'S111')
		RETURNING id
	`), &venueID)

	for _, query := range []string{
		`INSERT INTO venues (venue_type, display_title, issn_l) VALUES ('journal', 'Duplicate ISSN-L', '1111-1119')`,
		`INSERT INTO venues (venue_type, display_title, issn) VALUES ('journal', 'Duplicate ISSN', '1111-1119')`,
		`INSERT INTO venues (venue_type, display_title, eissn) VALUES ('journal', 'Duplicate eISSN', '2222-2227')`,
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

func insertLegacySourceRecord(
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
		INSERT INTO source_records (
			work_id, source, source_record_id, source_identity, source_time, content_hash, raw_payload
		) VALUES (
			$1, $2, $3,
			jsonb_build_object('id', $3::text),
			now(),
			$4,
			jsonb_build_object('id', $3::text)
		)
		RETURNING id
	`, workID, source, sourceRecordID, contentHash), &id)
	return id
}

func assertWorkReferenceCount(
	t *testing.T,
	pool *pgxpool.Pool,
	table string,
	column string,
	workID string,
	want int,
) {
	t.Helper()
	allowedReferences := map[string]string{
		"source_record_works":    "work_id",
		"field_assertions":       "work_id",
		"paper_versions":         "work_id",
		"work_code_repositories": "work_id",
	}
	if allowedReferences[table] != column {
		t.Fatalf("unsupported Work reference assertion %s.%s", table, column)
	}
	query := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", table, column)
	var got int
	if err := pool.QueryRow(testContext(t), query, workID).Scan(&got); err != nil {
		t.Fatalf("count %s.%s references: %v", table, column, err)
	}
	if got != want {
		t.Fatalf("%s.%s references to Work %s = %d, want %d", table, column, workID, got, want)
	}
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

func assertPostgresError(t *testing.T, err error, code string, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("PostgreSQL operation succeeded, want SQLSTATE %s", code)
	}
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) {
		t.Fatalf("error = %T %v, want *pgconn.PgError with SQLSTATE %s", err, err, code)
	}
	if pgError.Code != code {
		t.Fatalf("PostgreSQL SQLSTATE = %s, want %s: %v", pgError.Code, code, pgError)
	}
	if constraint != "" && pgError.ConstraintName != constraint {
		t.Fatalf(
			"PostgreSQL constraint = %q, want %q: %v",
			pgError.ConstraintName,
			constraint,
			pgError,
		)
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
