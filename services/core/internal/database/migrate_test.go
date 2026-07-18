package database

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	"jcr_import_receipt_metrics",
	"fulltext_assets",
	"mesh_descriptors",
	"mesh_qualifiers",
	"publication_types",
	"work_mesh_headings",
	"work_mesh_qualifiers",
	"work_publication_types",
	"subject_import_receipts",
	"subject_versions",
	"subjects",
	"biomedical_subject_rules",
	"journal_subject_metrics",
	"biomedical_publication_eligibility_decisions",
	"citation_snapshots",
	"citation_edges",
	"reference_edges",
	"citation_analysis_work_snapshots",
	"citation_analysis_percentiles",
	"publication_trend_snapshots",
	"journal_pattern_snapshots",
	"research_opportunity_snapshots",
	"research_opportunity_supporting_works",
	"work_publication_event_assertions",
}

var expectedSchemaTablesWithoutGeneratedID = []string{
	"public_catalog_home",
	"public_catalog_biomedical_manifest",
	"public_catalog_subjects",
	"public_catalog_journals",
	"work_publication_states",
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
	for _, table := range append(
		append(slices.Clone(expectedSchemaTables), expectedSchemaTablesWithoutGeneratedID...),
		"schema_migrations",
	) {
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
		{version: 4, name: "venue_policy_semantics"},
		{version: 5, name: "jcr_integrity_followup"},
		{version: 6, name: "ingestion_provenance"},
		{version: 7, name: "public_catalog"},
		{version: 8, name: "public_search"},
		{version: 9, name: "analysis_runs"},
		{version: 10, name: "repeatable_ingestion_jobs"},
		{version: 11, name: "biomedical_semantics"},
		{version: 12, name: "normalized_assertion_versions"},
		{version: 13, name: "biomedical_publication_eligibility"},
		{version: 14, name: "biomedical_catalog_snapshots"},
		{version: 15, name: "citation_evidence"},
		{version: 16, name: "citation_analysis_snapshots"},
		{version: 17, name: "biomedical_analysis_snapshots"},
		{version: 18, name: "publication_event_assertions"},
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

func TestEmbeddedMigrationsPreservePriorChecksumsAndIncludeCurrentCatalogMigrations(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	if got := migrationChecksum(migrations[0].SQL); got != initialMigrationChecksum {
		t.Fatalf("000001_initial checksum = %s, want immutable %s", got, initialMigrationChecksum)
	}
	if len(migrations) != 18 {
		t.Fatalf("embedded migration count = %d, want 18", len(migrations))
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
	if migrations[3].Version != 4 || migrations[3].Name != "venue_policy_semantics" {
		t.Fatalf("fourth migration = %#v, want 000004_venue_policy_semantics", migrations[3])
	}
	if migrations[4].Version != 5 || migrations[4].Name != "jcr_integrity_followup" {
		t.Fatalf("fifth migration = %#v, want 000005_jcr_integrity_followup", migrations[4])
	}
	if migrations[5].Version != 6 || migrations[5].Name != "ingestion_provenance" {
		t.Fatalf("sixth migration = %#v, want 000006_ingestion_provenance", migrations[5])
	}
	if migrations[6].Version != 7 || migrations[6].Name != "public_catalog" {
		t.Fatalf("seventh migration = %#v, want 000007_public_catalog", migrations[6])
	}
	if migrations[7].Version != 8 || migrations[7].Name != "public_search" {
		t.Fatalf("eighth migration = %#v, want 000008_public_search", migrations[7])
	}
	if migrations[8].Version != 9 || migrations[8].Name != "analysis_runs" {
		t.Fatalf("ninth migration = %#v, want 000009_analysis_runs", migrations[8])
	}
	if migrations[9].Version != 10 || migrations[9].Name != "repeatable_ingestion_jobs" {
		t.Fatalf(
			"tenth migration = %#v, want 000010_repeatable_ingestion_jobs",
			migrations[9],
		)
	}
	if migrations[10].Version != 11 || migrations[10].Name != "biomedical_semantics" {
		t.Fatalf(
			"eleventh migration = %#v, want 000011_biomedical_semantics",
			migrations[10],
		)
	}
	if migrations[11].Version != 12 ||
		migrations[11].Name != "normalized_assertion_versions" {
		t.Fatalf(
			"twelfth migration = %#v, want 000012_normalized_assertion_versions",
			migrations[11],
		)
	}
	if migrations[12].Version != 13 ||
		migrations[12].Name != "biomedical_publication_eligibility" {
		t.Fatalf(
			"thirteenth migration = %#v, want 000013_biomedical_publication_eligibility",
			migrations[12],
		)
	}
	if migrations[13].Version != 14 ||
		migrations[13].Name != "biomedical_catalog_snapshots" {
		t.Fatalf(
			"fourteenth migration = %#v, want 000014_biomedical_catalog_snapshots",
			migrations[13],
		)
	}
	if migrations[14].Version != 15 ||
		migrations[14].Name != "citation_evidence" {
		t.Fatalf(
			"fifteenth migration = %#v, want 000015_citation_evidence",
			migrations[14],
		)
	}
	if migrations[15].Version != 16 ||
		migrations[15].Name != "citation_analysis_snapshots" {
		t.Fatalf(
			"sixteenth migration = %#v, want 000016_citation_analysis_snapshots",
			migrations[15],
		)
	}
	if migrations[16].Version != 17 ||
		migrations[16].Name != "biomedical_analysis_snapshots" {
		t.Fatalf(
			"seventeenth migration = %#v, want 000017_biomedical_analysis_snapshots",
			migrations[16],
		)
	}
	if migrations[17].Version != 18 ||
		migrations[17].Name != "publication_event_assertions" {
		t.Fatalf(
			"eighteenth migration = %#v, want 000018_publication_event_assertions",
			migrations[17],
		)
	}
}

func TestPublicationEventAssertionSchema(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	requiredColumns := map[string]map[string]bool{
		"work_publication_event_assertions": {
			"id":                      true,
			"projection_assertion_id": true,
			"normalized_assertion_id": true,
			"source_record_id":        true,
			"work_id":                 true,
			"event_kind":              true,
			"event_date":              false,
			"date_precision":          true,
			"source_date":             true,
			"status_raw":              true,
			"publication_model_raw":   false,
			"source_path":             true,
			"ordinal":                 true,
			"created_at":              true,
		},
		"work_publication_states": {
			"work_id":                    true,
			"projection_assertion_id":    true,
			"normalized_assertion_id":    true,
			"source_record_id":           true,
			"print_published_on":         false,
			"print_published_state":      true,
			"electronic_published_on":    false,
			"electronic_published_state": true,
			"ahead_of_print_on":          false,
			"ahead_of_print_state":       true,
			"accepted_on":                false,
			"accepted_state":             true,
			"publication_model_raw":      false,
			"publication_status_raw":     false,
			"updated_at":                 true,
		},
	}
	for table, columns := range requiredColumns {
		for column, wantNotNull := range columns {
			var nullable string
			if err := pool.QueryRow(ctx, `
				SELECT is_nullable
				FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = $1
				  AND column_name = $2
			`, table, column).Scan(&nullable); err != nil {
				t.Fatalf("%s.%s metadata: %v", table, column, err)
			}
			if gotNotNull := nullable == "NO"; gotNotNull != wantNotNull {
				t.Fatalf(
					"%s.%s NOT NULL = %t, want %t",
					table,
					column,
					gotNotNull,
					wantNotNull,
				)
			}
		}
	}

	type foreignKeyExpectation struct {
		childColumns      []string
		referencedTable   string
		referencedColumns []string
	}
	expectedForeignKeys := map[string]foreignKeyExpectation{
		"work_publication_event_assertions_projection_provenance_fkey": {
			childColumns: []string{
				"projection_assertion_id",
				"normalized_assertion_id",
				"source_record_id",
				"work_id",
			},
			referencedTable: "ingestion_projection_assertions",
			referencedColumns: []string{
				"id",
				"normalized_assertion_id",
				"source_record_uuid",
				"work_id",
			},
		},
		"work_publication_event_assertions_normalized_source_fkey": {
			childColumns: []string{
				"normalized_assertion_id",
				"source_record_id",
			},
			referencedTable: "ingestion_normalized_records",
			referencedColumns: []string{
				"id",
				"source_record_uuid",
			},
		},
		"work_publication_event_assertions_source_record_work_fkey": {
			childColumns: []string{
				"source_record_id",
				"work_id",
			},
			referencedTable: "source_record_works",
			referencedColumns: []string{
				"source_record_id",
				"work_id",
			},
		},
		"work_publication_states_projection_provenance_fkey": {
			childColumns: []string{
				"projection_assertion_id",
				"normalized_assertion_id",
				"source_record_id",
				"work_id",
			},
			referencedTable: "ingestion_projection_assertions",
			referencedColumns: []string{
				"id",
				"normalized_assertion_id",
				"source_record_uuid",
				"work_id",
			},
		},
		"work_publication_states_normalized_source_fkey": {
			childColumns: []string{
				"normalized_assertion_id",
				"source_record_id",
			},
			referencedTable: "ingestion_normalized_records",
			referencedColumns: []string{
				"id",
				"source_record_uuid",
			},
		},
		"work_publication_states_source_record_work_fkey": {
			childColumns: []string{
				"source_record_id",
				"work_id",
			},
			referencedTable: "source_record_works",
			referencedColumns: []string{
				"source_record_id",
				"work_id",
			},
		},
	}
	for constraint, expected := range expectedForeignKeys {
		var referencedTable string
		var childColumns, referencedColumns []string
		if err := pool.QueryRow(ctx, `
			SELECT
				referenced_relation.relname,
				array_agg(
					child_attribute.attname
					ORDER BY key_position.ordinality
				),
				array_agg(
					referenced_attribute.attname
					ORDER BY key_position.ordinality
				)
			FROM pg_constraint AS constraint_row
			JOIN pg_class AS referenced_relation
			  ON referenced_relation.oid = constraint_row.confrelid
			JOIN LATERAL generate_subscripts(
				constraint_row.conkey,
				1
			) AS key_position(ordinality)
			  ON true
			JOIN pg_attribute AS child_attribute
			  ON child_attribute.attrelid = constraint_row.conrelid
			 AND child_attribute.attnum =
			     constraint_row.conkey[key_position.ordinality]
			JOIN pg_attribute AS referenced_attribute
			  ON referenced_attribute.attrelid = constraint_row.confrelid
			 AND referenced_attribute.attnum =
			     constraint_row.confkey[key_position.ordinality]
			WHERE constraint_row.conname = $1
			  AND constraint_row.contype = 'f'
			GROUP BY referenced_relation.relname
		`, constraint).Scan(
			&referencedTable,
			&childColumns,
			&referencedColumns,
		); err != nil {
			t.Fatalf("query constraint %s: %v", constraint, err)
		}
		if referencedTable != expected.referencedTable ||
			!slices.Equal(childColumns, expected.childColumns) ||
			!slices.Equal(referencedColumns, expected.referencedColumns) {
			t.Errorf(
				"constraint %s = child %v REFERENCES %s%v, want child %v REFERENCES %s%v",
				constraint,
				childColumns,
				referencedTable,
				referencedColumns,
				expected.childColumns,
				expected.referencedTable,
				expected.referencedColumns,
			)
		}
	}

	expectedIndexes := map[string][]string{
		"idx_work_publication_event_assertions_event_date": {
			"(event_kind, event_date DESC, work_id)",
		},
		"idx_work_publication_event_assertions_work_projection": {
			"(work_id, projection_assertion_id)",
		},
		"idx_work_publication_event_assertions_source_ordinal": {
			"(source_record_id, ordinal)",
		},
	}
	for index, fragments := range expectedIndexes {
		var definition string
		if err := pool.QueryRow(ctx, `
			SELECT indexdef
			FROM pg_indexes
			WHERE schemaname = 'public'
			  AND indexname = $1
		`, index).Scan(&definition); err != nil {
			t.Fatalf("query index %s: %v", index, err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(definition, fragment) {
				t.Errorf("index %s = %q, want %q", index, definition, fragment)
			}
		}
	}

	var immutableAssertionTriggers, immutableStateTriggers int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_trigger
		WHERE tgrelid = 'work_publication_event_assertions'::regclass
		  AND tgname = 'work_publication_event_assertions_immutable'
		  AND NOT tgisinternal
	`).Scan(&immutableAssertionTriggers); err != nil {
		t.Fatalf("query publication assertion immutable trigger: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_trigger
		WHERE tgrelid = 'work_publication_states'::regclass
		  AND tgname LIKE '%immutable%'
		  AND NOT tgisinternal
	`).Scan(&immutableStateTriggers); err != nil {
		t.Fatalf("query publication state immutable triggers: %v", err)
	}
	if immutableAssertionTriggers != 1 || immutableStateTriggers != 0 {
		t.Fatalf(
			"publication immutability triggers = assertions %d, states %d; want 1, 0",
			immutableAssertionTriggers,
			immutableStateTriggers,
		)
	}

	workID := insertWork(t, pool, "doi:10.1000/publication-event-schema")
	sourceRecordID := insertSourceRecord(
		t,
		pool,
		workID,
		"pubmed",
		"publication-event-schema",
		"publication-event-schema-hash",
	)
	projectionAssertionID := insertBiomedicalProjectionAssertion(
		t,
		pool,
		workID,
		sourceRecordID,
		"publication-event-schema",
	)
	var normalizedAssertionID string
	if err := pool.QueryRow(ctx, `
		SELECT normalized_assertion_id::text
		FROM ingestion_projection_assertions
		WHERE id = $1
	`, projectionAssertionID).Scan(&normalizedAssertionID); err != nil {
		t.Fatalf("query publication normalized assertion: %v", err)
	}
	mismatchedProjectionAssertionID := insertBiomedicalProjectionAssertion(
		t,
		pool,
		workID,
		sourceRecordID,
		"publication-event-mismatched-normalized",
	)
	var mismatchedNormalizedAssertionID string
	if err := pool.QueryRow(ctx, `
		SELECT normalized_assertion_id::text
		FROM ingestion_projection_assertions
		WHERE id = $1
	`, mismatchedProjectionAssertionID).Scan(
		&mismatchedNormalizedAssertionID,
	); err != nil {
		t.Fatalf("query mismatched publication normalized assertion: %v", err)
	}
	if mismatchedNormalizedAssertionID == normalizedAssertionID {
		t.Fatal("publication projection fixtures reused one normalized assertion")
	}

	var eventAssertionID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO work_publication_event_assertions (
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			work_id,
			event_kind,
			event_date,
			date_precision,
			source_date,
			status_raw,
			publication_model_raw,
			source_path,
			ordinal
		) VALUES (
			$1, $2, $3, $4,
			'accepted', DATE '2026-07-15', 'day',
			'{"year":2026,"month":7,"day":15}',
			'accepted', 'Print-Electronic',
			'/PubmedArticle/PubmedData/History/PubMedPubDate[1]',
			1
		)
		RETURNING id
	`,
		projectionAssertionID,
		normalizedAssertionID,
		sourceRecordID,
		workID,
	), &eventAssertionID)

	var sourceDatePreserved bool
	var sourcePath, statusRaw, publicationModelRaw string
	var ordinal int
	if err := pool.QueryRow(ctx, `
		SELECT
			source_date = '{"year":2026,"month":7,"day":15}'::jsonb,
			source_path,
			status_raw,
			publication_model_raw,
			ordinal
		FROM work_publication_event_assertions
		WHERE id = $1
	`, eventAssertionID).Scan(
		&sourceDatePreserved,
		&sourcePath,
		&statusRaw,
		&publicationModelRaw,
		&ordinal,
	); err != nil {
		t.Fatalf("query preserved publication event evidence: %v", err)
	}
	if !sourceDatePreserved ||
		sourcePath != "/PubmedArticle/PubmedData/History/PubMedPubDate[1]" ||
		statusRaw != "accepted" ||
		publicationModelRaw != "Print-Electronic" ||
		ordinal != 1 {
		t.Fatalf(
			"preserved publication evidence = source date %t, path %q, status %q, model %q, ordinal %d",
			sourceDatePreserved,
			sourcePath,
			statusRaw,
			publicationModelRaw,
			ordinal,
		)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO work_publication_event_assertions (
			projection_assertion_id, normalized_assertion_id,
			source_record_id, work_id, event_kind, event_date,
			date_precision, source_date, status_raw, source_path, ordinal
		) VALUES (
			$1, $2, $3, $4, 'electronic_published', NULL,
			'month', '{"year":2026,"month":7}', 'epublish',
			'/PubmedArticle/PubmedData/History/PubMedPubDate[2]', 2
		)
	`,
		projectionAssertionID,
		normalizedAssertionID,
		sourceRecordID,
		workID,
	); err != nil {
		t.Fatalf("insert partial publication event evidence: %v", err)
	}

	_, err := pool.Exec(ctx, `
		INSERT INTO work_publication_event_assertions (
			projection_assertion_id, normalized_assertion_id,
			source_record_id, work_id, event_kind, event_date,
			date_precision, source_date, status_raw, source_path, ordinal
		) VALUES (
			$1, $2, $3, $4, 'submitted', DATE '2026-07-15',
			'day', '{"year":2026,"month":7,"day":15}', 'submitted',
			'/PubmedArticle/PubmedData/History/PubMedPubDate[3]', 3
		)
	`, projectionAssertionID, normalizedAssertionID, sourceRecordID, workID)
	assertPostgresError(
		t,
		err,
		"23514",
		"work_publication_event_assertions_event_kind_check",
	)

	_, err = pool.Exec(ctx, `
		INSERT INTO work_publication_event_assertions (
			projection_assertion_id, normalized_assertion_id,
			source_record_id, work_id, event_kind, event_date,
			date_precision, source_date, status_raw, source_path, ordinal
		) VALUES (
			$1, $2, $3, $4, 'print_published', DATE '2026-07-15',
			'hour', '{"year":2026,"month":7,"day":15}', 'ppublish',
			'/PubmedArticle/PubmedData/History/PubMedPubDate[4]', 4
		)
	`, projectionAssertionID, normalizedAssertionID, sourceRecordID, workID)
	assertPostgresError(
		t,
		err,
		"23514",
		"work_publication_event_assertions_date_precision_check",
	)

	for _, test := range []struct {
		name      string
		eventDate string
		precision string
		ordinal   int
	}{
		{
			name:      "day precision without exact date",
			eventDate: "NULL",
			precision: "day",
			ordinal:   5,
		},
		{
			name:      "partial precision with invented exact date",
			eventDate: "DATE '2026-07-01'",
			precision: "month",
			ordinal:   6,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := fmt.Sprintf(`
				INSERT INTO work_publication_event_assertions (
					projection_assertion_id, normalized_assertion_id,
					source_record_id, work_id, event_kind, event_date,
					date_precision, source_date, status_raw, source_path, ordinal
				) VALUES (
					$1, $2, $3, $4, 'ahead_of_print', %s,
					$5, '{"year":2026,"month":7}', 'aheadofprint',
					'/PubmedArticle/PubmedData/History/PubMedPubDate[%d]', %d
				)
			`, test.eventDate, test.ordinal, test.ordinal)
			_, shapeErr := pool.Exec(
				ctx,
				query,
				projectionAssertionID,
				normalizedAssertionID,
				sourceRecordID,
				workID,
				test.precision,
			)
			assertPostgresError(
				t,
				shapeErr,
				"23514",
				"work_publication_event_assertions_date_shape_check",
			)
		})
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO work_publication_event_assertions (
			projection_assertion_id, normalized_assertion_id,
			source_record_id, work_id, event_kind, event_date,
			date_precision, source_date, status_raw, source_path, ordinal
		) VALUES (
			$1, $2, $3, $4, 'accepted', DATE '2026-07-15',
			'day', '{"year":2026,"month":7,"day":15}', 'accepted',
			'/PubmedArticle/PubmedData/History/PubMedPubDate[1]', 1
		)
	`, projectionAssertionID, normalizedAssertionID, sourceRecordID, workID)
	assertPostgresError(
		t,
		err,
		"23505",
		"work_publication_event_assertions_projection_ordinal_key",
	)

	otherWorkID := insertWork(t, pool, "doi:10.1000/publication-event-other-work")
	otherSourceRecordID := insertSourceRecord(
		t,
		pool,
		otherWorkID,
		"pubmed",
		"publication-event-other-work",
		"publication-event-other-work-hash",
	)
	crossSourceRecordID := insertSourceRecord(
		t,
		pool,
		workID,
		"crossref",
		"publication-event-cross-source",
		"publication-event-cross-source-hash",
	)
	for _, test := range []struct {
		name           string
		sourceRecordID string
		workID         string
		ordinal        int
	}{
		{
			name:           "cross Work",
			sourceRecordID: sourceRecordID,
			workID:         otherWorkID,
			ordinal:        7,
		},
		{
			name:           "cross source",
			sourceRecordID: crossSourceRecordID,
			workID:         workID,
			ordinal:        8,
		},
	} {
		t.Run(test.name+" assertion provenance", func(t *testing.T) {
			_, provenanceErr := pool.Exec(ctx, `
				INSERT INTO work_publication_event_assertions (
					projection_assertion_id, normalized_assertion_id,
					source_record_id, work_id, event_kind, event_date,
					date_precision, source_date, status_raw, source_path, ordinal
				) VALUES (
					$1, $2, $3, $4, 'accepted', DATE '2026-07-15',
					'day', '{"year":2026,"month":7,"day":15}', 'accepted',
					'/PubmedArticle/PubmedData/History/PubMedPubDate[9]', $5
				)
			`,
				projectionAssertionID,
				normalizedAssertionID,
				test.sourceRecordID,
				test.workID,
				test.ordinal,
			)
			assertPostgresError(t, provenanceErr, "23503", "")
		})
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO work_publication_event_assertions (
			projection_assertion_id, normalized_assertion_id,
			source_record_id, work_id, event_kind, event_date,
			date_precision, source_date, status_raw, source_path, ordinal
		) VALUES (
			$1, $2, $3, $4, 'accepted', DATE '2026-07-15',
			'day', '{"year":2026,"month":7,"day":15}', 'accepted',
			'/PubmedArticle/PubmedData/History/PubMedPubDate[10]', 9
		)
	`,
		projectionAssertionID,
		mismatchedNormalizedAssertionID,
		sourceRecordID,
		workID,
	)
	assertPostgresError(
		t,
		err,
		"23503",
		"work_publication_event_assertions_projection_provenance_fkey",
	)

	if _, err := pool.Exec(ctx, `
		INSERT INTO work_publication_states (
			work_id,
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			print_published_on,
			print_published_state,
			electronic_published_on,
			electronic_published_state,
			ahead_of_print_on,
			ahead_of_print_state,
			accepted_on,
			accepted_state,
			publication_model_raw,
			publication_status_raw
		) VALUES (
			$1, $2, $3, $4,
			DATE '2026-07-16', 'known',
			NULL, 'missing',
			NULL, 'conflict',
			DATE '2026-07-15', 'known',
			'Print-Electronic', 'ppublish'
		)
	`, workID, projectionAssertionID, normalizedAssertionID, sourceRecordID); err != nil {
		t.Fatalf("insert current publication state: %v", err)
	}

	_, err = pool.Exec(ctx, `
		UPDATE work_publication_states
		SET electronic_published_state = 'unknown'
		WHERE work_id = $1
	`, workID)
	assertPostgresError(
		t,
		err,
		"23514",
		"work_publication_states_electronic_state_check",
	)

	stateShapes := []struct {
		name        string
		dateColumn  string
		stateColumn string
		constraint  string
	}{
		{
			name:        "print published",
			dateColumn:  "print_published_on",
			stateColumn: "print_published_state",
			constraint:  "work_publication_states_print_state_check",
		},
		{
			name:        "electronic published",
			dateColumn:  "electronic_published_on",
			stateColumn: "electronic_published_state",
			constraint:  "work_publication_states_electronic_state_check",
		},
		{
			name:        "ahead of print",
			dateColumn:  "ahead_of_print_on",
			stateColumn: "ahead_of_print_state",
			constraint:  "work_publication_states_ahead_of_print_state_check",
		},
		{
			name:        "accepted",
			dateColumn:  "accepted_on",
			stateColumn: "accepted_state",
			constraint:  "work_publication_states_accepted_state_check",
		},
	}
	invalidStateShapes := []struct {
		name       string
		dateValue  string
		stateValue string
	}{
		{
			name:       "known without date",
			dateValue:  "NULL",
			stateValue: "known",
		},
		{
			name:       "missing with date",
			dateValue:  "DATE '2026-07-15'",
			stateValue: "missing",
		},
		{
			name:       "conflict with date",
			dateValue:  "DATE '2026-07-15'",
			stateValue: "conflict",
		},
	}
	for _, stateShape := range stateShapes {
		for _, invalidShape := range invalidStateShapes {
			t.Run(
				stateShape.name+" rejects "+invalidShape.name,
				func(t *testing.T) {
					query := fmt.Sprintf(`
						UPDATE work_publication_states
						SET %s = %s,
						    %s = $2
						WHERE work_id = $1
					`,
						stateShape.dateColumn,
						invalidShape.dateValue,
						stateShape.stateColumn,
					)
					_, shapeErr := pool.Exec(
						ctx,
						query,
						workID,
						invalidShape.stateValue,
					)
					assertPostgresError(
						t,
						shapeErr,
						"23514",
						stateShape.constraint,
					)
				},
			)
		}
	}

	if _, err := pool.Exec(ctx, `
		UPDATE work_publication_states
		SET accepted_on = NULL,
		    accepted_state = 'conflict',
		    publication_model_raw = 'Electronic',
		    updated_at = now()
		WHERE work_id = $1
	`, workID); err != nil {
		t.Fatalf("update replaceable publication state: %v", err)
	}
	var acceptedState, publicationModel string
	var acceptedOn *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT accepted_on, accepted_state, publication_model_raw
		FROM work_publication_states
		WHERE work_id = $1
	`, workID).Scan(&acceptedOn, &acceptedState, &publicationModel); err != nil {
		t.Fatalf("query updated publication state: %v", err)
	}
	if acceptedOn != nil ||
		acceptedState != "conflict" ||
		publicationModel != "Electronic" {
		t.Fatalf(
			"updated publication state = date %v, state %q, model %q",
			acceptedOn,
			acceptedState,
			publicationModel,
		)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO work_publication_states (
			work_id, projection_assertion_id, normalized_assertion_id,
			source_record_id, print_published_state,
			electronic_published_state, ahead_of_print_state, accepted_state
		) VALUES (
			$1, $2, $3, $4, 'missing', 'missing', 'missing', 'missing'
		)
	`, otherWorkID, projectionAssertionID, normalizedAssertionID, otherSourceRecordID)
	assertPostgresError(t, err, "23503", "")

	for _, query := range []string{
		`UPDATE work_publication_event_assertions
		 SET ordinal = ordinal
		 WHERE id = $1`,
		`DELETE FROM work_publication_event_assertions WHERE id = $1`,
	} {
		_, mutationErr := pool.Exec(ctx, query, eventAssertionID)
		assertPostgresError(t, mutationErr, "55000", "")
	}
}

func TestCitationEvidenceSchemaIsSourceSpecificAppendOnlyAndProvenanceBound(
	t *testing.T,
) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	requiredColumns := map[string][]string{
		"citation_snapshots": {
			"id",
			"work_id",
			"source",
			"observed_at",
			"count",
			"source_record_id",
			"ingestion_job_id",
			"retrieved_at",
			"coverage",
			"definition_version",
			"dataset_version",
			"created_at",
		},
		"citation_edges": {
			"id",
			"citing_work_id",
			"cited_work_id",
			"citing_identifier",
			"cited_identifier",
			"source",
			"source_record_id",
			"ingestion_job_id",
			"retrieved_at",
			"definition_version",
			"dataset_version",
			"created_at",
		},
		"reference_edges": {
			"id",
			"citing_work_id",
			"cited_work_id",
			"citing_identifier",
			"cited_identifier",
			"source",
			"source_record_id",
			"ingestion_job_id",
			"retrieved_at",
			"definition_version",
			"dataset_version",
			"created_at",
		},
	}
	for table, columns := range requiredColumns {
		for _, column := range columns {
			var nullable string
			if err := pool.QueryRow(ctx, `
				SELECT is_nullable
				FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = $1
				  AND column_name = $2
			`, table, column).Scan(&nullable); err != nil {
				t.Fatalf("%s.%s metadata: %v", table, column, err)
			}
			if table == "citation_snapshots" ||
				(column != "citing_work_id" && column != "cited_work_id") {
				if nullable != "NO" {
					t.Fatalf("%s.%s nullable = %q, want NO", table, column, nullable)
				}
			}
		}
	}

	workID := insertWork(t, pool, "openalex:W1001")
	sourceRecordID := insertSourceRecord(
		t,
		pool,
		workID,
		"pubmed",
		"citations-1001",
		"citation-evidence-hash",
	)
	projectionAssertionID := insertBiomedicalProjectionAssertion(
		t,
		pool,
		workID,
		sourceRecordID,
		"citation-evidence",
	)
	var jobID string
	if err := pool.QueryRow(ctx, `
		SELECT job_id::text
		FROM ingestion_projection_assertions
		WHERE id = $1
	`, projectionAssertionID).Scan(&jobID); err != nil {
		t.Fatalf("query citation evidence ingestion job: %v", err)
	}

	var observedAt, retrievedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT source_time, retrieved_at
		FROM source_records
		WHERE id = $1
	`, sourceRecordID).Scan(&observedAt, &retrievedAt); err != nil {
		t.Fatalf("query citation source evidence times: %v", err)
	}
	var snapshotID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO citation_snapshots (
			work_id,
			source,
			observed_at,
			count,
			source_record_id,
			ingestion_job_id,
			retrieved_at,
			coverage,
			definition_version,
			dataset_version
		) VALUES (
			$1,
			'pubmed',
			$2,
			42,
			$3,
			$4,
			$5,
			1,
			'pubmed-citation-count/v1',
			'pubmed/2026-07-17'
		)
		RETURNING id
	`, workID, observedAt, sourceRecordID, jobID, retrievedAt), &snapshotID)

	_, duplicateErr := pool.Exec(ctx, `
		INSERT INTO citation_snapshots (
			work_id, source, observed_at, count, source_record_id,
			ingestion_job_id, retrieved_at, coverage,
			definition_version, dataset_version
		) VALUES (
			$1, 'pubmed', $2, 43, $3, $4, $5, 1,
			'pubmed-citation-count/v1', 'pubmed/2026-07-17'
		)
	`, workID, observedAt, sourceRecordID, jobID, retrievedAt)
	assertPostgresError(
		t,
		duplicateErr,
		"23505",
		"citation_snapshots_work_source_observed_key",
	)

	for _, query := range []string{
		`UPDATE citation_snapshots SET count = count + 1 WHERE id = $1`,
		`DELETE FROM citation_snapshots WHERE id = $1`,
	} {
		_, err := pool.Exec(ctx, query, snapshotID)
		assertPostgresError(t, err, "55000", "")
	}

	_, mismatchedSourceErr := pool.Exec(ctx, `
		INSERT INTO citation_snapshots (
			work_id, source, observed_at, count, source_record_id,
			ingestion_job_id, retrieved_at, coverage,
			definition_version, dataset_version
		) VALUES (
			$1, 'openalex', $2, 1, $3, $4, $5, 1,
			'openalex-cited-by-count/v1', 'openalex/2026-07-17'
		)
	`, workID, observedAt, sourceRecordID, jobID, retrievedAt)
	assertPostgresError(
		t,
		mismatchedSourceErr,
		"23514",
		"citation_evidence_provenance",
	)

	citedWorkID := insertWork(t, pool, "doi:10.1000/cited")
	var citationEdgeID, referenceEdgeID string
	for table, destination := range map[string]*string{
		"citation_edges":  &citationEdgeID,
		"reference_edges": &referenceEdgeID,
	} {
		mustScanID(t, pool.QueryRow(ctx, fmt.Sprintf(`
			INSERT INTO %s (
				citing_work_id,
				cited_work_id,
				citing_identifier,
				cited_identifier,
				source,
				source_record_id,
				ingestion_job_id,
				retrieved_at,
				definition_version,
				dataset_version
			) VALUES (
				$1,
				$2,
				'pmid:1001',
				'doi:10.1000/cited',
				'pubmed',
				$3,
				$4,
				$5,
				'citation-edge/v1',
				'pubmed/2026-07-17'
			)
			RETURNING id
		`, table), workID, citedWorkID, sourceRecordID, jobID, retrievedAt), destination)
	}

	for table, id := range map[string]string{
		"citation_edges":  citationEdgeID,
		"reference_edges": referenceEdgeID,
	} {
		_, err := pool.Exec(
			ctx,
			fmt.Sprintf("UPDATE %s SET retrieved_at = retrieved_at + interval '1 second' WHERE id = $1", table),
			id,
		)
		assertPostgresError(t, err, "55000", "")
	}

	_, selfEdgeErr := pool.Exec(ctx, `
		INSERT INTO citation_edges (
			citing_work_id, cited_work_id, citing_identifier, cited_identifier,
			source, source_record_id, ingestion_job_id, retrieved_at,
			definition_version, dataset_version
		) VALUES (
			$1, $1, 'pmid:1001', 'pmid:1001',
			'pubmed', $2, $3, $4, 'citation-edge/v1', 'pubmed/2026-07-17'
		)
	`, workID, sourceRecordID, jobID, retrievedAt)
	assertPostgresError(
		t,
		selfEdgeErr,
		"23514",
		"citation_edges_not_self_check",
	)
}

func TestCitationAnalysisSchemaBindsRunsSnapshotsAndExactBiomedicalCohorts(
	t *testing.T,
) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	requiredColumns := map[string][]string{
		"citation_analysis_work_snapshots": {
			"id",
			"analysis_run_id",
			"work_id",
			"source",
			"as_of",
			"velocity_window_days",
			"citation_count_state",
			"current_snapshot_id",
			"citation_count",
			"citation_velocity_state",
			"baseline_snapshot_id",
			"citation_velocity",
			"source_revision",
			"formula_version",
			"evidence",
			"generated_at",
			"created_at",
		},
		"citation_analysis_percentiles": {
			"id",
			"analysis_run_id",
			"work_id",
			"subject_version_id",
			"subject_id",
			"publication_year",
			"publication_type_id",
			"source",
			"citation_snapshot_id",
			"citation_count",
			"cohort_key",
			"cohort_size",
			"minimum_cohort_size",
			"percentile_state",
			"midrank",
			"citation_percentile",
			"source_revision",
			"formula_version",
			"evidence",
			"generated_at",
			"created_at",
		},
	}
	for table, columns := range requiredColumns {
		for _, column := range columns {
			var exists bool
			if err := pool.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1
					FROM information_schema.columns
					WHERE table_schema = 'public'
					  AND table_name = $1
					  AND column_name = $2
				)
			`, table, column).Scan(&exists); err != nil {
				t.Fatalf("%s.%s metadata: %v", table, column, err)
			}
			if !exists {
				t.Fatalf("%s.%s was not created", table, column)
			}
		}
	}

	workID := insertWork(t, pool, "openalex:W3001")
	sourceRecordID := insertSourceRecord(
		t,
		pool,
		workID,
		"pubmed",
		"citation-analysis-3001",
		"citation-analysis-source-hash",
	)
	projectionAssertionID := insertBiomedicalProjectionAssertion(
		t,
		pool,
		workID,
		sourceRecordID,
		"citation-analysis",
	)
	var jobID string
	if err := pool.QueryRow(ctx, `
		SELECT job_id::text
		FROM ingestion_projection_assertions
		WHERE id = $1
	`, projectionAssertionID).Scan(&jobID); err != nil {
		t.Fatalf("query citation analysis ingestion job: %v", err)
	}
	var observedAt, retrievedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT source_time, retrieved_at
		FROM source_records
		WHERE id = $1
	`, sourceRecordID).Scan(&observedAt, &retrievedAt); err != nil {
		t.Fatalf("query citation analysis source times: %v", err)
	}
	asOf := observedAt.Add(time.Hour)
	var citationSnapshotID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO citation_snapshots (
			work_id,
			source,
			observed_at,
			count,
			source_record_id,
			ingestion_job_id,
			retrieved_at,
			coverage,
			definition_version,
			dataset_version
		) VALUES (
			$1,
			'pubmed',
			$2,
			7,
			$3,
			$4,
			$5,
			1,
			'pubmed-citation-count/v1',
			'pubmed/2026-07-17'
		)
		RETURNING id
	`, workID, observedAt, sourceRecordID, jobID, retrievedAt), &citationSnapshotID)

	subjectFixture := insertBiomedicalSubjectRegistryFixture(
		t,
		pool,
		"citation-analysis",
		[]string{"ONCOLOGY"},
	)
	var publicationTypeID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO publication_types (publication_type_ui)
		VALUES ('D016428')
		RETURNING id
	`), &publicationTypeID)

	var analysisRunID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO analysis_runs (
			analysis_type,
			model_provider,
			model_name,
			prompt_version,
			status,
			input_payload,
			started_at
		) VALUES (
			'citation_intelligence',
			'internal',
			'deterministic',
			'citation-intelligence/v1',
			'running',
			'{"source":"pubmed"}',
			$1
		)
		RETURNING id
	`, asOf), &analysisRunID)

	sourceRevision := strings.Repeat("c", 64)
	var workSnapshotID, percentileID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO citation_analysis_work_snapshots (
			analysis_run_id,
			work_id,
			source,
			as_of,
			velocity_window_days,
			citation_count_state,
			current_snapshot_id,
			citation_count,
			citation_velocity_state,
			source_revision,
			formula_version,
			evidence,
			generated_at
		) VALUES (
			$1,
			$2,
			'pubmed',
			$5,
			30,
			'known',
			$3,
			7,
			'insufficient_evidence',
			$4,
			'citation-intelligence/v1',
			'{"missing_signals":["baseline_snapshot"]}',
			$5
		)
		RETURNING id
	`,
		analysisRunID,
		workID,
		citationSnapshotID,
		sourceRevision,
		asOf,
	), &workSnapshotID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO citation_analysis_percentiles (
			analysis_run_id,
			work_id,
			subject_version_id,
			subject_id,
			publication_year,
			publication_type_id,
			source,
			citation_snapshot_id,
			citation_count,
			cohort_key,
			cohort_size,
			minimum_cohort_size,
			percentile_state,
			source_revision,
			formula_version,
			evidence,
			generated_at
		) VALUES (
			$1,
			$2,
			$3,
			$4,
			2026,
			$5,
			'pubmed',
			$6,
			7,
			'subject-version:subject:2026:publication-type',
			1,
			5,
			'insufficient_evidence',
			$7,
			'citation-intelligence/v1',
			'{"supporting_work_ids":["openalex:W3001"]}',
			$8
		)
		RETURNING id
	`,
		analysisRunID,
		workID,
		subjectFixture.VersionID,
		subjectFixture.SubjectIDs[0],
		publicationTypeID,
		citationSnapshotID,
		sourceRevision,
		asOf,
	), &percentileID)

	for table, id := range map[string]string{
		"citation_analysis_work_snapshots": workSnapshotID,
		"citation_analysis_percentiles":    percentileID,
	} {
		for _, operation := range []string{"UPDATE", "DELETE"} {
			var query string
			if operation == "UPDATE" {
				query = fmt.Sprintf(
					"UPDATE %s SET generated_at = generated_at + interval '1 second' WHERE id = $1",
					table,
				)
			} else {
				query = fmt.Sprintf("DELETE FROM %s WHERE id = $1", table)
			}
			_, err := pool.Exec(ctx, query, id)
			assertPostgresError(t, err, "55000", "")
		}
	}

	var mismatchedRunID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO analysis_runs (
			analysis_type,
			model_provider,
			model_name,
			prompt_version,
			status,
			input_payload,
			started_at
		) VALUES (
			'citation_intelligence',
			'internal',
			'deterministic',
			'citation-intelligence/v1',
			'running',
			'{"source":"openalex"}',
			$1
		)
		RETURNING id
	`, asOf), &mismatchedRunID)
	_, mismatchedSourceErr := pool.Exec(ctx, `
		INSERT INTO citation_analysis_work_snapshots (
			analysis_run_id,
			work_id,
			source,
			as_of,
			velocity_window_days,
			citation_count_state,
			current_snapshot_id,
			citation_count,
			citation_velocity_state,
			source_revision,
			formula_version,
			evidence,
			generated_at
		) VALUES (
			$1,
			$2,
			'openalex',
			$5,
			30,
			'known',
			$3,
			7,
			'insufficient_evidence',
			$4,
			'citation-intelligence/v1',
			'{"missing_signals":["baseline_snapshot"]}',
			$5
		)
	`, mismatchedRunID, workID, citationSnapshotID, sourceRevision, asOf)
	assertPostgresError(
		t,
		mismatchedSourceErr,
		"23503",
		"citation_analysis_work_current_snapshot_fkey",
	)
}

func TestBiomedicalAnalysisSnapshotSchemaIsStructuredImmutableAndIndexed(
	t *testing.T,
) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	requiredColumns := map[string][]string{
		"publication_trend_snapshots": {
			"id",
			"analysis_run_id",
			"entity_type",
			"entity_id",
			"state",
			"model",
			"model_selection_rule",
			"dispersion_threshold",
			"predeclared_dispersion_alpha",
			"recent_window_start",
			"recent_window_end",
			"recent_paper_count",
			"recent_rate_per_day",
			"baseline_window_start",
			"baseline_window_end",
			"baseline_paper_count",
			"baseline_rate_per_day",
			"independent_journal_count",
			"independent_team_count",
			"rate_ratio",
			"confidence_level",
			"confidence_interval_lower",
			"confidence_interval_upper",
			"p_value",
			"adjusted_p_value",
			"cohort_revision",
			"formula_version",
			"payload",
			"evidence",
			"generated_at",
			"created_at",
		},
		"journal_pattern_snapshots": {
			"id",
			"analysis_run_id",
			"pattern_key",
			"journal_id",
			"subject_version_id",
			"subject_id",
			"pattern_kind",
			"feature_type",
			"feature_value",
			"state",
			"measure",
			"journal_feature_paper_count",
			"journal_paper_count",
			"field_feature_paper_count",
			"field_baseline_paper_count",
			"journal_exposure",
			"field_baseline_exposure",
			"covered_paper_count",
			"eligible_paper_count",
			"coverage",
			"effect_value",
			"confidence_level",
			"confidence_interval_lower",
			"confidence_interval_upper",
			"p_value",
			"adjusted_p_value",
			"cohort_revision",
			"formula_version",
			"payload",
			"evidence",
			"generated_at",
			"created_at",
		},
		"research_opportunity_snapshots": {
			"id",
			"analysis_run_id",
			"rule",
			"rule_version",
			"entity_type",
			"entity_id",
			"state",
			"supporting_work_count",
			"covered_paper_count",
			"eligible_paper_count",
			"coverage",
			"confidence_level",
			"primary_metric",
			"primary_estimate",
			"primary_confidence_interval_lower",
			"primary_confidence_interval_upper",
			"secondary_metric",
			"secondary_estimate",
			"secondary_confidence_interval_lower",
			"secondary_confidence_interval_upper",
			"limitations",
			"cohort_revision",
			"payload",
			"evidence",
			"generated_at",
			"created_at",
		},
		"research_opportunity_supporting_works": {
			"id",
			"analysis_run_id",
			"research_opportunity_snapshot_id",
			"work_id",
			"ordinal",
			"created_at",
		},
	}
	for table, columns := range requiredColumns {
		for _, column := range columns {
			var nullable string
			if err := pool.QueryRow(ctx, `
				SELECT is_nullable
				FROM information_schema.columns
				WHERE table_schema = 'public'
				  AND table_name = $1
				  AND column_name = $2
			`, table, column).Scan(&nullable); err != nil {
				t.Fatalf("%s.%s metadata: %v", table, column, err)
			}
			if nullable != "NO" &&
				column != "rate_ratio" &&
				column != "confidence_level" &&
				column != "confidence_interval_lower" &&
				column != "confidence_interval_upper" &&
				column != "p_value" &&
				column != "adjusted_p_value" &&
				column != "journal_exposure" &&
				column != "field_baseline_exposure" &&
				column != "effect_value" &&
				column != "secondary_metric" &&
				column != "secondary_estimate" &&
				column != "secondary_confidence_interval_lower" &&
				column != "secondary_confidence_interval_upper" {
				t.Fatalf("%s.%s nullable = %q, want NO", table, column, nullable)
			}
		}
	}

	assertNamesExist(t, pool, `
		SELECT conname
		FROM pg_constraint
		WHERE connamespace = 'public'::regnamespace
	`, []string{
		"publication_trend_snapshots_run_fkey",
		"publication_trend_snapshots_identity_key",
		"publication_trend_snapshots_model_selection_shape_check",
		"publication_trend_snapshots_window_check",
		"publication_trend_snapshots_estimate_shape_check",
		"publication_trend_snapshots_payload_check",
		"publication_trend_snapshots_evidence_check",
		"journal_pattern_snapshots_run_fkey",
		"journal_pattern_snapshots_journal_fkey",
		"journal_pattern_snapshots_subject_fkey",
		"journal_pattern_snapshots_identity_key",
		"journal_pattern_snapshots_count_shape_check",
		"journal_pattern_snapshots_exposure_shape_check",
		"journal_pattern_snapshots_estimate_shape_check",
		"journal_pattern_snapshots_payload_check",
		"journal_pattern_snapshots_evidence_check",
		"research_opportunity_snapshots_run_fkey",
		"research_opportunity_snapshots_identity_key",
		"research_opportunity_snapshots_supporting_work_shape_check",
		"research_opportunity_snapshots_rule_metric_shape_check",
		"research_opportunity_snapshots_estimate_shape_check",
		"research_opportunity_snapshots_state_shape_check",
		"research_opportunity_snapshots_payload_check",
		"research_opportunity_snapshots_evidence_check",
		"research_opportunity_supporting_works_run_fkey",
		"research_opportunity_supporting_works_snapshot_fkey",
		"research_opportunity_supporting_works_work_fkey",
		"research_opportunity_supporting_works_snapshot_work_key",
		"research_opportunity_supporting_works_snapshot_ordinal_key",
	})
	assertNamesExist(t, pool, `
		SELECT tgname
		FROM pg_trigger
		WHERE NOT tgisinternal
	`, []string{
		"publication_trend_snapshots_run_guard",
		"publication_trend_snapshots_immutable",
		"journal_pattern_snapshots_run_guard",
		"journal_pattern_snapshots_immutable",
		"research_opportunity_snapshots_run_guard",
		"research_opportunity_snapshots_immutable",
		"research_opportunity_supporting_works_run_guard",
		"research_opportunity_supporting_works_immutable",
		"analysis_runs_biomedical_snapshot_completion",
	})
	assertNamesExist(t, pool, `
		SELECT indexname
		FROM pg_indexes
		WHERE schemaname = 'public'
	`, []string{
		"idx_publication_trend_snapshots_lookup",
		"idx_journal_pattern_snapshots_lookup",
		"idx_research_opportunity_snapshots_lookup",
		"idx_research_opportunity_supporting_works_lookup",
	})

	var journalIndexDefinition string
	if err := pool.QueryRow(ctx, `
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = 'public'
		  AND indexname = 'idx_journal_pattern_snapshots_lookup'
	`).Scan(&journalIndexDefinition); err != nil {
		t.Fatalf("query journal pattern lookup index: %v", err)
	}
	if strings.Count(journalIndexDefinition, "state") != 1 {
		t.Fatalf(
			"journal pattern lookup index state occurrences = %d, want 1: %s",
			strings.Count(journalIndexDefinition, "state"),
			journalIndexDefinition,
		)
	}
}

func TestBiomedicalAnalysisSnapshotsRequireMatchingRunningRunsAndCompleteImmutably(
	t *testing.T,
) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	generatedAt := time.Date(2026, 7, 1, 0, 10, 0, 0, time.UTC)
	runStartedAt := generatedAt.Add(time.Hour)
	runCompletedAt := runStartedAt.Add(time.Minute)

	insertRun := func(analysisType, status string) string {
		t.Helper()
		var runID string
		mustScanID(t, pool.QueryRow(ctx, `
			INSERT INTO analysis_runs (
				analysis_type,
				model_provider,
				model_name,
				prompt_version,
				status,
				input_payload,
				started_at
			) VALUES (
				$1,
				'internal',
				'deterministic',
				$1 || '/v1',
				$2,
				'{"cohort_revision":"fixture"}',
				$3
			)
			RETURNING id
		`, analysisType, status, runStartedAt), &runID)
		return runID
	}

	trendRunID := insertRun("publication_trends", "running")
	journalRunID := insertRun("journal_editorial_patterns", "running")
	opportunityRunID := insertRun("research_opportunities", "running")

	var journalID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (
			venue_type,
			display_title,
			issn_l
		) VALUES (
			'journal',
			'Biomedical Analysis Snapshot Journal',
			'1234-5679'
		)
		RETURNING id
	`), &journalID)
	subjectFixture := insertBiomedicalSubjectRegistryFixture(
		t,
		pool,
		"biomedical-analysis-snapshots",
		[]string{"ONCOLOGY"},
	)
	workIDs := make([]string, 0, 5)
	for index := 0; index < 5; index++ {
		workIDs = append(
			workIDs,
			insertWork(t, pool, fmt.Sprintf("openalex:W91%02d", index)),
		)
	}

	insertTrend := func(runID, entityID, state string, includeEstimate bool) (string, error) {
		t.Helper()
		var snapshotID string
		var rateRatio, confidenceLevel, lower, upper, pValue, adjustedPValue any
		if includeEstimate {
			rateRatio = 2.5
			confidenceLevel = 0.95
			lower = 1.49
			upper = 4.19
			pValue = 0.0005
			adjustedPValue = 0.001
		}
		err := pool.QueryRow(ctx, `
			INSERT INTO publication_trend_snapshots (
				analysis_run_id,
				entity_type,
				entity_id,
				state,
				model,
				model_selection_rule,
				dispersion_threshold,
				predeclared_dispersion_alpha,
				recent_window_start,
				recent_window_end,
				recent_paper_count,
				recent_rate_per_day,
				baseline_window_start,
				baseline_window_end,
				baseline_paper_count,
				baseline_rate_per_day,
				independent_journal_count,
				independent_team_count,
				rate_ratio,
				confidence_level,
				confidence_interval_lower,
				confidence_interval_upper,
				p_value,
				adjusted_p_value,
				cohort_revision,
				formula_version,
				payload,
				evidence,
				generated_at
			) VALUES (
				$1,
				'subject',
				$2,
				$3,
				'poisson',
				'fixed_poisson',
				0,
				0,
				'2026-05-06T00:00:00Z',
				'2026-07-01T00:00:00Z',
				20,
				0.3571428571,
				'2025-05-07T00:00:00Z',
				'2026-05-06T00:00:00Z',
				52,
				0.1428571429,
				4,
				8,
				$4,
				$5,
				$6,
				$7,
				$8,
				$9,
				$10,
				'biomedical-trends/v1',
				'{"entity_label":"Oncology"}',
				'{"window_policy":"56d-vs-364d"}',
				$11
			)
			RETURNING id
		`,
			runID,
			entityID,
			state,
			rateRatio,
			confidenceLevel,
			lower,
			upper,
			pValue,
			adjustedPValue,
			strings.Repeat("a", 64),
			generatedAt,
		).Scan(&snapshotID)
		return snapshotID, err
	}

	insertJournalPattern := func(
		runID, patternKey, patternKind, state string,
		includeEstimate bool,
	) (string, error) {
		t.Helper()
		var snapshotID string
		var effectValue, confidenceLevel, lower, upper, pValue, adjustedPValue any
		if includeEstimate {
			effectValue = 1.71
			confidenceLevel = 0.95
			lower = 0.99
			upper = 2.98
			pValue = 0.055
			adjustedPValue = 0.08
		}
		err := pool.QueryRow(ctx, `
			INSERT INTO journal_pattern_snapshots (
				analysis_run_id,
				pattern_key,
				journal_id,
				subject_version_id,
				subject_id,
				pattern_kind,
				feature_type,
				feature_value,
				state,
				measure,
				journal_feature_paper_count,
				journal_paper_count,
				field_feature_paper_count,
				field_baseline_paper_count,
				covered_paper_count,
				eligible_paper_count,
				coverage,
				effect_value,
				confidence_level,
				confidence_interval_lower,
				confidence_interval_upper,
				p_value,
				adjusted_p_value,
				cohort_revision,
				formula_version,
				payload,
				evidence,
				generated_at
			) VALUES (
				$1,
				$2,
				$3,
				$4,
				$5,
				$6,
				'mesh',
				'Neoplasms',
				$7,
				'odds_ratio',
				30,
				100,
				40,
				200,
				270,
				300,
				0.9,
				$8,
				$9,
				$10,
				$11,
				$12,
				$13,
				$14,
				'journal-editorial-patterns/v1',
				'{"feature_label":"Neoplasms"}',
				'{"cohort":"oncology"}',
				$15
			)
			RETURNING id
		`,
			runID,
			patternKey,
			journalID,
			subjectFixture.VersionID,
			subjectFixture.SubjectIDs[0],
			patternKind,
			state,
			effectValue,
			confidenceLevel,
			lower,
			upper,
			pValue,
			adjustedPValue,
			strings.Repeat("b", 64),
			generatedAt,
		).Scan(&snapshotID)
		return snapshotID, err
	}

	insertOpportunity := func(
		runID, entityID, state string,
		coveredPaperCount, eligiblePaperCount int,
	) (string, string, error) {
		t.Helper()
		var snapshotID string
		err := pool.QueryRow(ctx, `
			INSERT INTO research_opportunity_snapshots (
				analysis_run_id,
				rule,
				rule_version,
				entity_type,
				entity_id,
				state,
				supporting_work_count,
				covered_paper_count,
				eligible_paper_count,
				coverage,
				confidence_level,
				primary_metric,
				primary_estimate,
				primary_confidence_interval_lower,
				primary_confidence_interval_upper,
				secondary_metric,
				secondary_estimate,
				secondary_confidence_interval_lower,
				secondary_confidence_interval_upper,
				limitations,
				cohort_revision,
				payload,
				evidence,
				generated_at
			) VALUES (
				$1,
				'rapid_growth_low_rct_share',
				'biomedical-opportunity-rules/v1',
				'subject',
				$2,
				$3,
				5,
				$4::bigint,
				$5::bigint,
				$4::numeric / $5::numeric,
				0.95,
				'trend_rate_ratio',
				2.5,
				1.4,
				4.2,
				'rct_share',
				0.1,
				0.05,
				0.2,
				ARRAY['observational source coverage'],
				$6,
				'{"title":"Rapid growth with low RCT share"}',
				'{"supporting_sources":["pubmed"]}',
				$7
			)
			RETURNING id
		`,
			runID,
			entityID,
			state,
			coveredPaperCount,
			eligiblePaperCount,
			strings.Repeat("c", 64),
			generatedAt,
		).Scan(&snapshotID)
		if err != nil {
			return "", "", err
		}

		var firstSupportingWorkID string
		for index, workID := range workIDs {
			var supportingWorkID string
			if err := pool.QueryRow(ctx, `
				INSERT INTO research_opportunity_supporting_works (
					analysis_run_id,
					research_opportunity_snapshot_id,
					work_id,
					ordinal
				) VALUES ($1, $2, $3, $4)
				RETURNING id
			`, runID, snapshotID, workID, index+1).Scan(&supportingWorkID); err != nil {
				return "", "", err
			}
			if index == 0 {
				firstSupportingWorkID = supportingWorkID
			}
		}
		return snapshotID, firstSupportingWorkID, nil
	}

	trendSnapshotID, err := insertTrend(
		trendRunID,
		subjectFixture.SubjectIDs[0],
		"sufficient_evidence",
		true,
	)
	if err != nil {
		t.Fatalf("insert biomedical trend snapshot: %v", err)
	}
	journalSnapshotID, err := insertJournalPattern(
		journalRunID,
		"journal|oncology|mesh|neoplasms",
		"editorial_pattern",
		"sufficient_evidence",
		true,
	)
	if err != nil {
		t.Fatalf("insert journal editorial pattern snapshot: %v", err)
	}
	opportunitySnapshotID, opportunitySupportingWorkID, err := insertOpportunity(
		opportunityRunID,
		subjectFixture.SubjectIDs[0],
		"triggered",
		5,
		5,
	)
	if err != nil {
		t.Fatalf("insert research opportunity snapshot: %v", err)
	}

	_, err = pool.Exec(ctx, "DELETE FROM works WHERE id = $1", workIDs[0])
	assertPostgresError(
		t,
		err,
		"23001",
		"research_opportunity_supporting_works_work_fkey",
	)

	wrongTypeCases := []struct {
		name string
		call func() error
	}{
		{
			name: "trend",
			call: func() error {
				_, callErr := insertTrend(
					journalRunID,
					"wrong-type-trend",
					"sufficient_evidence",
					true,
				)
				return callErr
			},
		},
		{
			name: "journal pattern",
			call: func() error {
				_, callErr := insertJournalPattern(
					opportunityRunID,
					"wrong-type-journal-pattern",
					"editorial_pattern",
					"sufficient_evidence",
					true,
				)
				return callErr
			},
		},
		{
			name: "research opportunity",
			call: func() error {
				_, _, callErr := insertOpportunity(
					trendRunID,
					"wrong-type-opportunity",
					"triggered",
					5,
					5,
				)
				return callErr
			},
		},
	}
	for _, test := range wrongTypeCases {
		t.Run(test.name+" rejects a different analysis type", func(t *testing.T) {
			assertPostgresError(
				t,
				test.call(),
				"23514",
				"biomedical_analysis_running_run",
			)
		})
	}

	pendingCases := []struct {
		name         string
		analysisType string
		call         func(string) error
	}{
		{
			name:         "trend",
			analysisType: "publication_trends",
			call: func(runID string) error {
				_, callErr := insertTrend(
					runID,
					"pending-trend",
					"sufficient_evidence",
					true,
				)
				return callErr
			},
		},
		{
			name:         "journal pattern",
			analysisType: "journal_editorial_patterns",
			call: func(runID string) error {
				_, callErr := insertJournalPattern(
					runID,
					"pending-journal-pattern",
					"editorial_pattern",
					"sufficient_evidence",
					true,
				)
				return callErr
			},
		},
		{
			name:         "research opportunity",
			analysisType: "research_opportunities",
			call: func(runID string) error {
				_, _, callErr := insertOpportunity(
					runID,
					"pending-opportunity",
					"triggered",
					5,
					5,
				)
				return callErr
			},
		},
	}
	for _, test := range pendingCases {
		t.Run(test.name+" rejects a non-running analysis run", func(t *testing.T) {
			assertPostgresError(
				t,
				test.call(insertRun(test.analysisType, "pending")),
				"23514",
				"biomedical_analysis_running_run",
			)
		})
	}

	_, err = insertTrend(
		trendRunID,
		"invalid-insufficient-trend-shape",
		"insufficient_evidence",
		true,
	)
	assertPostgresError(
		t,
		err,
		"23514",
		"publication_trend_snapshots_estimate_shape_check",
	)
	_, err = insertJournalPattern(
		journalRunID,
		"invalid-pattern-kind",
		"preference",
		"sufficient_evidence",
		true,
	)
	assertPostgresError(
		t,
		err,
		"23514",
		"journal_pattern_snapshots_kind_check",
	)
	_, _, err = insertOpportunity(
		opportunityRunID,
		"invalid-triggered-opportunity",
		"triggered",
		1,
		5,
	)
	assertPostgresError(
		t,
		err,
		"23514",
		"research_opportunity_snapshots_state_shape_check",
	)

	_, err = pool.Exec(ctx, `
		UPDATE analysis_runs
		SET analysis_type = 'research_opportunities'
		WHERE id = $1
	`, trendRunID)
	assertPostgresError(
		t,
		err,
		"23514",
		"biomedical_analysis_run_binding",
	)

	emptyTrendRunID := insertRun("publication_trends", "running")
	_, err = pool.Exec(ctx, `
		UPDATE analysis_runs
		SET status = 'succeeded',
		    output_payload = '{"snapshot_count":0}',
		    completed_at = $2
		WHERE id = $1
	`, emptyTrendRunID, runCompletedAt)
	assertPostgresError(
		t,
		err,
		"23514",
		"biomedical_analysis_run_completion",
	)

	_, err = pool.Exec(ctx, `
		UPDATE analysis_runs
		SET status = 'succeeded',
		    output_payload = '{"snapshot_count":1}',
		    completed_at = $2
		WHERE id = $1
	`, trendRunID, generatedAt.Add(-time.Second))
	assertPostgresError(
		t,
		err,
		"23514",
		"biomedical_analysis_run_completion",
	)

	for _, runID := range []string{
		trendRunID,
		journalRunID,
		opportunityRunID,
	} {
		if _, err := pool.Exec(ctx, `
			UPDATE analysis_runs
			SET status = 'succeeded',
			    output_payload = '{"snapshot_count":1}',
			    completed_at = $2
			WHERE id = $1
		`, runID, runCompletedAt); err != nil {
			t.Fatalf("complete biomedical analysis run %s: %v", runID, err)
		}
	}

	completedInsertCases := []struct {
		name string
		call func() error
	}{
		{
			name: "trend",
			call: func() error {
				_, callErr := insertTrend(
					trendRunID,
					"completed-trend",
					"sufficient_evidence",
					true,
				)
				return callErr
			},
		},
		{
			name: "journal pattern",
			call: func() error {
				_, callErr := insertJournalPattern(
					journalRunID,
					"completed-journal-pattern",
					"editorial_pattern",
					"sufficient_evidence",
					true,
				)
				return callErr
			},
		},
		{
			name: "research opportunity",
			call: func() error {
				_, _, callErr := insertOpportunity(
					opportunityRunID,
					"completed-opportunity",
					"triggered",
					5,
					5,
				)
				return callErr
			},
		},
	}
	for _, test := range completedInsertCases {
		t.Run(test.name+" rejects inserts after completion", func(t *testing.T) {
			assertPostgresError(
				t,
				test.call(),
				"23514",
				"biomedical_analysis_running_run",
			)
		})
	}

	extraWorkID := insertWork(t, pool, "openalex:W9199")
	_, err = pool.Exec(ctx, `
		INSERT INTO research_opportunity_supporting_works (
			analysis_run_id,
			research_opportunity_snapshot_id,
			work_id,
			ordinal
		) VALUES ($1, $2, $3, 6)
	`, opportunityRunID, opportunitySnapshotID, extraWorkID)
	assertPostgresError(
		t,
		err,
		"23514",
		"biomedical_analysis_running_run",
	)

	for table, snapshotID := range map[string]string{
		"publication_trend_snapshots":    trendSnapshotID,
		"journal_pattern_snapshots":      journalSnapshotID,
		"research_opportunity_snapshots": opportunitySnapshotID,
	} {
		for _, query := range []string{
			fmt.Sprintf(
				"UPDATE %s SET generated_at = generated_at + interval '1 second' WHERE id = $1",
				table,
			),
			fmt.Sprintf("DELETE FROM %s WHERE id = $1", table),
		} {
			_, mutationErr := pool.Exec(ctx, query, snapshotID)
			assertPostgresError(t, mutationErr, "55000", "")
		}
	}
	for _, query := range []string{
		`UPDATE research_opportunity_supporting_works
		 SET ordinal = ordinal
		 WHERE id = $1`,
		`DELETE FROM research_opportunity_supporting_works WHERE id = $1`,
	} {
		_, mutationErr := pool.Exec(ctx, query, opportunitySupportingWorkID)
		assertPostgresError(t, mutationErr, "55000", "")
	}
}

func TestNormalizedAssertionSchemaVersionsAndBindsDownstreamState(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	expectedColumns := []struct {
		table  string
		column string
	}{
		{table: "ingestion_normalized_records", column: "id"},
		{table: "ingestion_normalized_records", column: "payload_schema_version"},
		{table: "ingestion_projection_assertions", column: "normalized_assertion_id"},
		{table: "ingestion_source_states", column: "normalized_assertion_id"},
		{table: "work_projection_states", column: "normalized_assertion_id"},
	}
	for _, expected := range expectedColumns {
		var nullable string
		if err := pool.QueryRow(ctx, `
			SELECT is_nullable
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = $1
			  AND column_name = $2
		`, expected.table, expected.column).Scan(&nullable); err != nil {
			t.Fatalf("%s.%s metadata: %v", expected.table, expected.column, err)
		}
		if expected.table != "ingestion_source_states" && nullable != "NO" {
			t.Fatalf(
				"%s.%s nullable = %q, want NO",
				expected.table,
				expected.column,
				nullable,
			)
		}
	}

	var normalizedPrimaryKey string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid = 'ingestion_normalized_records'::regclass
		  AND contype = 'p'
	`).Scan(&normalizedPrimaryKey); err != nil {
		t.Fatalf("query normalized assertion primary key: %v", err)
	}
	if normalizedPrimaryKey != "PRIMARY KEY (id)" {
		t.Fatalf(
			"normalized assertion primary key = %q, want explicit assertion ID",
			normalizedPrimaryKey,
		)
	}

	expectedConstraints := map[string][]string{
		"ingestion_normalized_records_raw_policy_schema_key": {
			"raw_event_id",
			"normalization_policy_version",
			"payload_schema_version",
		},
		"ingestion_projection_assertions_normalized_policy_key": {
			"normalized_assertion_id",
			"scope_policy_version",
			"projection_policy_version",
		},
		"ingestion_projection_assertions_normalized_assertion_fkey": {
			"normalized_assertion_id",
			"raw_event_id",
			"source_record_uuid",
		},
		"ingestion_source_states_normalized_assertion_fkey": {
			"normalized_assertion_id",
			"raw_event_id",
			"source_record_uuid",
		},
		"work_projection_states_normalized_assertion_fkey": {
			"normalized_assertion_id",
			"raw_event_id",
			"source_record_uuid",
		},
	}
	for constraint, columns := range expectedConstraints {
		var definition string
		if err := pool.QueryRow(ctx, `
			SELECT pg_get_constraintdef(oid)
			FROM pg_constraint
			WHERE conname = $1
		`, constraint).Scan(&definition); err != nil {
			t.Fatalf("query constraint %s: %v", constraint, err)
		}
		for _, column := range columns {
			if !strings.Contains(definition, column) {
				t.Errorf(
					"constraint %s = %q, want column %s",
					constraint,
					definition,
					column,
				)
			}
		}
	}
}

func TestNormalizedAssertionSchemaUpgradeFromV11RetainsLegacyAndAllowsNewSchema(
	t *testing.T,
) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	if len(migrations) != 18 {
		t.Fatalf("embedded migration count = %d, want 18", len(migrations))
	}

	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:11]); err != nil {
		t.Fatalf("apply migrations through v11: %v", err)
	}

	workID := insertWork(t, pool, "doi:10.1000/normalized-upgrade")
	sourceRecordID := insertSourceRecord(
		t,
		pool,
		workID,
		"pubmed",
		"normalized-upgrade-source",
		"normalized-upgrade-source-hash",
	)
	var jobID, rawEventID, legacyProjectionID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO ingestion_jobs (
			source, job_type, idempotency_key, status, payload, max_attempts,
			batch_key, stage
		) VALUES (
			'pubmed', 'normalized-upgrade', 'normalized-upgrade-job',
			'succeeded', '{}', 1, 'normalized-upgrade', 'project'
		)
		RETURNING id
	`), &jobID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO ingestion_raw_events (
			job_id, logical_source, event_key, event_kind, source_record_id,
			source_time, tie_break_key, position, content_hash, raw_format, raw_payload
		) VALUES (
			$1, 'pubmed', 'pubmed:normalized-upgrade', 'upsert',
			'normalized-upgrade-source', now(), 'legacy', 1,
			'1212121212121212121212121212121212121212121212121212121212121212',
			'xml', convert_to('<PubmedArticle/>', 'UTF8')
		)
		RETURNING id
	`, jobID), &rawEventID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO ingestion_normalized_records (
			raw_event_id, source_record_uuid, normalization_policy_version,
			normalized_payload
		) VALUES (
			$1, $2, 'normalization/pubmed-v1',
			'{"source":"pubmed","source_record_id":"normalized-upgrade-source","title":"legacy"}'
		)
	`, rawEventID, sourceRecordID); err != nil {
		t.Fatalf("insert v11 normalized record: %v", err)
	}
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO ingestion_projection_assertions (
			raw_event_id, source_record_uuid, work_id, job_id,
			scope_policy_version, projection_policy_version, record_payload
		) VALUES (
			$1, $2, $3, $4, 'scope/pubmed-v1', 'projection/pubmed-v1',
			'{"source":"pubmed","source_record_id":"normalized-upgrade-source","title":"legacy"}'
		)
		RETURNING id
	`, rawEventID, sourceRecordID, workID, jobID), &legacyProjectionID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO ingestion_source_states (
			logical_source, event_key, raw_event_id, source_record_uuid, work_id,
			source_time, tie_break_key, position, scope_status,
			scope_policy_version, projection_policy_version, is_deleted
		) VALUES (
			'pubmed', 'pubmed:normalized-upgrade', $1, $2, $3,
			now(), 'legacy', 1, 'included',
			'scope/pubmed-v1', 'projection/pubmed-v1', false
		)
	`, rawEventID, sourceRecordID, workID); err != nil {
		t.Fatalf("insert v11 source state: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_projection_states (
			work_id, raw_event_id, source_record_uuid, source_time, tie_break_key,
			position, scope_policy_version, projection_policy_version
		) VALUES (
			$1, $2, $3, now(), 'legacy', 1,
			'scope/pubmed-v1', 'projection/pubmed-v1'
		)
	`, workID, rawEventID, sourceRecordID); err != nil {
		t.Fatalf("insert v11 work projection state: %v", err)
	}

	if err := UpMigrations(ctx, pool, migrations[11:]); err != nil {
		t.Fatalf("apply v12 normalized assertion migration: %v", err)
	}

	var (
		legacyNormalizedID          string
		legacyPayloadSchema         string
		projectionNormalizedID      string
		sourceStateNormalizedID     string
		workStateNormalizedID       string
		preservedLegacyProjectionID string
	)
	if err := pool.QueryRow(ctx, `
		SELECT id::text, payload_schema_version
		FROM ingestion_normalized_records
		WHERE raw_event_id = $1
	`, rawEventID).Scan(
		&legacyNormalizedID,
		&legacyPayloadSchema,
	); err != nil {
		t.Fatalf("query upgraded legacy normalized assertion: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT id::text, normalized_assertion_id::text
		FROM ingestion_projection_assertions
		WHERE id = $1
	`, legacyProjectionID).Scan(
		&preservedLegacyProjectionID,
		&projectionNormalizedID,
	); err != nil {
		t.Fatalf("query upgraded legacy projection assertion: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT normalized_assertion_id::text
		FROM ingestion_source_states
		WHERE logical_source = 'pubmed'
		  AND event_key = 'pubmed:normalized-upgrade'
	`).Scan(&sourceStateNormalizedID); err != nil {
		t.Fatalf("query upgraded source-state normalized binding: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT normalized_assertion_id::text
		FROM work_projection_states
		WHERE work_id = $1
	`, workID).Scan(&workStateNormalizedID); err != nil {
		t.Fatalf("query upgraded work-state normalized binding: %v", err)
	}
	if legacyPayloadSchema != "normalized-record/v1" ||
		preservedLegacyProjectionID != legacyProjectionID ||
		projectionNormalizedID != legacyNormalizedID ||
		sourceStateNormalizedID != legacyNormalizedID ||
		workStateNormalizedID != legacyNormalizedID {
		t.Fatalf(
			"legacy bindings = schema %q normalized %q projection %q source %q work %q",
			legacyPayloadSchema,
			legacyNormalizedID,
			projectionNormalizedID,
			sourceStateNormalizedID,
			workStateNormalizedID,
		)
	}

	var currentNormalizedID, currentProjectionID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO ingestion_normalized_records (
			raw_event_id, source_record_uuid, normalization_policy_version,
			payload_schema_version, normalized_payload
		) VALUES (
			$1, $2, 'normalization/pubmed-v1', 'normalized-record/v2',
			'{
				"source":"pubmed",
				"source_record_id":"normalized-upgrade-source",
				"title":"current",
				"mesh_headings":[]
			}'
		)
		RETURNING id
	`, rawEventID, sourceRecordID), &currentNormalizedID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO ingestion_projection_assertions (
			normalized_assertion_id, raw_event_id, source_record_uuid, work_id, job_id,
			scope_policy_version, projection_policy_version, record_payload
		) VALUES (
			$1, $2, $3, $4, $5, 'scope/pubmed-v1', 'projection/pubmed-v1',
			'{
				"source":"pubmed",
				"source_record_id":"normalized-upgrade-source",
				"title":"current projection"
			}'
		)
		RETURNING id
	`,
		currentNormalizedID,
		rawEventID,
		sourceRecordID,
		workID,
		jobID,
	), &currentProjectionID)

	var normalizedCount, projectionCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM ingestion_normalized_records WHERE raw_event_id = $1),
			(SELECT count(*) FROM ingestion_projection_assertions WHERE raw_event_id = $1)
	`, rawEventID).Scan(&normalizedCount, &projectionCount); err != nil {
		t.Fatalf("count versioned normalized/projection assertions: %v", err)
	}
	if currentNormalizedID == legacyNormalizedID ||
		currentProjectionID == legacyProjectionID ||
		normalizedCount != 2 ||
		projectionCount != 2 {
		t.Fatalf(
			"versioned assertions = normalized %q/%q projection %q/%q counts %d/%d",
			legacyNormalizedID,
			currentNormalizedID,
			legacyProjectionID,
			currentProjectionID,
			normalizedCount,
			projectionCount,
		)
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

func TestBiomedicalSemanticSchemaMetadata(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	semanticTables := []string{
		"mesh_descriptors",
		"mesh_qualifiers",
		"publication_types",
		"work_mesh_headings",
		"work_mesh_qualifiers",
		"work_publication_types",
		"subject_import_receipts",
		"subject_versions",
		"subjects",
		"biomedical_subject_rules",
		"journal_subject_metrics",
	}
	for _, table := range semanticTables {
		var registered *string
		if err := pool.QueryRow(ctx, "SELECT to_regclass('public.' || $1)::text", table).Scan(&registered); err != nil {
			t.Fatalf("query %s registration: %v", table, err)
		}
		if registered == nil {
			t.Fatalf("biomedical semantic table %q was not created", table)
		}

		var dataType, defaultValue string
		if err := pool.QueryRow(ctx, `
			SELECT data_type, column_default
			FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = 'id'
		`, table).Scan(&dataType, &defaultValue); err != nil {
			t.Fatalf("%s.id metadata: %v", table, err)
		}
		if dataType != "uuid" || !strings.Contains(defaultValue, "gen_random_uuid()") {
			t.Fatalf("%s.id = (%q, %q), want database-generated UUID", table, dataType, defaultValue)
		}
	}

	var treeNumberTable *string
	if err := pool.QueryRow(ctx, `
		SELECT to_regclass('public.mesh_descriptor_tree_numbers')::text
	`).Scan(&treeNumberTable); err != nil {
		t.Fatalf("query MeSH Tree Number table: %v", err)
	}
	if treeNumberTable != nil {
		t.Fatal("mesh_descriptor_tree_numbers must not exist because PubMed article XML has no Tree Number")
	}

	var canonicalDisplayColumns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = ANY($1::text[])
		  AND column_name IN ('name', 'display_name', 'display_label', 'label')
	`, []string{"mesh_descriptors", "mesh_qualifiers", "publication_types"}).Scan(&canonicalDisplayColumns); err != nil {
		t.Fatalf("query canonical identity display columns: %v", err)
	}
	if canonicalDisplayColumns != 0 {
		t.Fatalf("canonical identity tables contain %d mutable display-label columns, want 0", canonicalDisplayColumns)
	}

	assertNamesExist(t, pool, `
		SELECT conname
		FROM pg_constraint
		WHERE connamespace = 'public'::regnamespace
	`, []string{
		"ingestion_projection_assertions_id_source_record_work_key",
		"mesh_descriptors_descriptor_ui_key",
		"mesh_descriptors_descriptor_ui_check",
		"mesh_qualifiers_qualifier_ui_key",
		"mesh_qualifiers_qualifier_ui_check",
		"publication_types_publication_type_ui_key",
		"publication_types_publication_type_ui_check",
		"work_mesh_headings_projection_descriptor_key",
		"work_mesh_headings_source_record_work_fkey",
		"work_mesh_headings_projection_assertion_fkey",
		"work_mesh_qualifiers_heading_qualifier_key",
		"work_mesh_qualifiers_source_record_work_fkey",
		"work_mesh_qualifiers_projection_assertion_fkey",
		"work_mesh_qualifiers_heading_assertion_fkey",
		"work_publication_types_projection_type_key",
		"work_publication_types_source_record_work_fkey",
		"work_publication_types_projection_assertion_fkey",
		"subject_import_receipts_source_registry_key",
		"subject_import_receipts_file_sha256_key",
		"subject_versions_subject_import_receipt_id_fkey",
		"subject_versions_subject_import_receipt_id_key",
		"subject_versions_version_key_key",
		"subjects_subject_version_id_fkey",
		"subjects_version_slug_key",
		"subjects_id_subject_version_key",
		"biomedical_subject_rules_version_category_key",
		"biomedical_subject_rules_subject_version_fkey",
		"biomedical_subject_rules_id_category_key",
		"venue_metric_snapshots_id_category_key",
		"journal_subject_metrics_metric_category_fkey",
		"journal_subject_metrics_rule_category_fkey",
	})
	assertNamesExist(t, pool, `
		SELECT proname
		FROM pg_proc
		WHERE pronamespace = 'public'::regnamespace
	`, []string{
		"enforce_subject_import_receipt_integrity",
		"enforce_subject_import_receipt_child_transaction",
	})
	assertNamesExist(t, pool, `
		SELECT tgname
		FROM pg_trigger
		WHERE NOT tgisinternal
	`, []string{
		"subject_import_receipts_content_integrity",
		"subject_versions_receipt_transaction",
		"subjects_receipt_transaction",
		"biomedical_subject_rules_receipt_transaction",
	})

	var receiptTriggerDeferrable, receiptTriggerInitiallyDeferred bool
	if err := pool.QueryRow(ctx, `
		SELECT tgdeferrable, tginitdeferred
		FROM pg_trigger
		WHERE tgrelid = 'subject_import_receipts'::regclass
		  AND tgname = 'subject_import_receipts_content_integrity'
	`).Scan(&receiptTriggerDeferrable, &receiptTriggerInitiallyDeferred); err != nil {
		t.Fatalf("query Subject receipt integrity trigger semantics: %v", err)
	}
	if !receiptTriggerDeferrable || !receiptTriggerInitiallyDeferred {
		t.Fatalf(
			"Subject receipt integrity trigger = deferrable %t, initially deferred %t; want true, true",
			receiptTriggerDeferrable,
			receiptTriggerInitiallyDeferred,
		)
	}

	for _, constraint := range []string{
		"work_mesh_headings_source_record_work_fkey",
		"work_mesh_qualifiers_source_record_work_fkey",
		"work_publication_types_source_record_work_fkey",
	} {
		var deleteAction string
		var deferrable, initiallyDeferred bool
		if err := pool.QueryRow(ctx, `
			SELECT confdeltype::text, condeferrable, condeferred
			FROM pg_constraint
			WHERE connamespace = 'public'::regnamespace AND conname = $1
		`, constraint).Scan(&deleteAction, &deferrable, &initiallyDeferred); err != nil {
			t.Fatalf("query %s delete action and deferrability: %v", constraint, err)
		}
		if deleteAction != "a" || !deferrable || !initiallyDeferred {
			t.Fatalf(
				"%s = delete action %q, deferrable %t, initially deferred %t; want %q, true, true",
				constraint,
				deleteAction,
				deferrable,
				initiallyDeferred,
				"a",
			)
		}
	}

	for _, index := range []struct {
		name    string
		columns []string
	}{
		{
			name:    "idx_work_mesh_headings_work",
			columns: []string{"work_id", "projection_assertion_id", "id"},
		},
		{
			name:    "idx_work_mesh_headings_source_record",
			columns: []string{"source_record_id", "projection_assertion_id", "id"},
		},
		{
			name:    "idx_work_mesh_headings_descriptor",
			columns: []string{"descriptor_id", "work_id", "id"},
		},
		{
			name:    "idx_work_mesh_qualifiers_work",
			columns: []string{"work_id", "projection_assertion_id", "id"},
		},
		{
			name:    "idx_work_mesh_qualifiers_source_record",
			columns: []string{"source_record_id", "projection_assertion_id", "id"},
		},
		{
			name:    "idx_work_mesh_qualifiers_qualifier",
			columns: []string{"qualifier_id", "work_mesh_heading_id", "id"},
		},
		{
			name:    "idx_work_publication_types_work",
			columns: []string{"work_id", "projection_assertion_id", "id"},
		},
		{
			name:    "idx_work_publication_types_source_record",
			columns: []string{"source_record_id", "projection_assertion_id", "id"},
		},
		{
			name:    "idx_work_publication_types_type",
			columns: []string{"publication_type_id", "work_id", "id"},
		},
		{
			name:    "idx_subjects_version_display_label",
			columns: []string{"subject_version_id", "display_label", "id"},
		},
		{
			name:    "idx_biomedical_subject_rules_subject",
			columns: []string{"subject_id", "subject_version_id", "id"},
		},
		{
			name:    "idx_journal_subject_metrics_rule",
			columns: []string{"subject_rule_id", "venue_metric_snapshot_id", "id"},
		},
	} {
		var accessMethod string
		var unique, hasPredicate bool
		var columns []string
		if err := pool.QueryRow(ctx, `
			SELECT
				access_method.amname,
				index_metadata.indisunique,
				index_metadata.indpred IS NOT NULL,
				array_agg(attribute.attname ORDER BY indexed_column.ordinality)
			FROM pg_class AS index_relation
			JOIN pg_namespace AS index_namespace
			  ON index_namespace.oid = index_relation.relnamespace
			JOIN pg_index AS index_metadata
			  ON index_metadata.indexrelid = index_relation.oid
			JOIN pg_am AS access_method
			  ON access_method.oid = index_relation.relam
			JOIN unnest(index_metadata.indkey)
			     WITH ORDINALITY AS indexed_column(attnum, ordinality)
			  ON true
			JOIN pg_attribute AS attribute
			  ON attribute.attrelid = index_metadata.indrelid
			 AND attribute.attnum = indexed_column.attnum
			WHERE index_namespace.nspname = 'public'
			  AND index_relation.relname = $1
			GROUP BY access_method.amname, index_metadata.indisunique, index_metadata.indpred
		`, index.name).Scan(&accessMethod, &unique, &hasPredicate, &columns); err != nil {
			t.Fatalf("query %s metadata: %v", index.name, err)
		}
		if accessMethod != "btree" ||
			unique ||
			hasPredicate ||
			!slices.Equal(columns, index.columns) {
			t.Fatalf(
				"%s = method %q, unique %t, predicate %t, columns %v; want btree, false, false, %v",
				index.name,
				accessMethod,
				unique,
				hasPredicate,
				columns,
				index.columns,
			)
		}
	}

	for _, identity := range []struct {
		table      string
		uiColumn   string
		constraint string
		definition string
	}{
		{
			table:      "mesh_descriptors",
			uiColumn:   "descriptor_ui",
			constraint: "mesh_descriptors_descriptor_ui_key",
			definition: "UNIQUE (descriptor_ui)",
		},
		{
			table:      "mesh_qualifiers",
			uiColumn:   "qualifier_ui",
			constraint: "mesh_qualifiers_qualifier_ui_key",
			definition: "UNIQUE (qualifier_ui)",
		},
		{
			table:      "publication_types",
			uiColumn:   "publication_type_ui",
			constraint: "publication_types_publication_type_ui_key",
			definition: "UNIQUE (publication_type_ui)",
		},
	} {
		rows, err := pool.Query(ctx, `
			SELECT conname, pg_get_constraintdef(oid)
			FROM pg_constraint
			WHERE conrelid = $1::regclass AND contype = 'u'
			ORDER BY conname
		`, "public."+identity.table)
		if err != nil {
			t.Fatalf("query %s unique constraints: %v", identity.table, err)
		}
		var uniqueCount int
		for rows.Next() {
			var name, definition string
			if err := rows.Scan(&name, &definition); err != nil {
				rows.Close()
				t.Fatalf("scan %s unique constraint: %v", identity.table, err)
			}
			uniqueCount++
			if name != identity.constraint || definition != identity.definition {
				rows.Close()
				t.Fatalf(
					"%s unique constraint = (%q, %q), want (%q, %q)",
					identity.table,
					name,
					definition,
					identity.constraint,
					identity.definition,
				)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate %s unique constraints: %v", identity.table, err)
		}
		rows.Close()
		if uniqueCount != 1 {
			t.Fatalf("%s unique constraint count = %d, want exactly 1 UI-only unique", identity.table, uniqueCount)
		}
	}

}

func TestBiomedicalSemanticSchemaCanonicalIdentity(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	var descriptorID, qualifierID, publicationTypeID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO mesh_descriptors (descriptor_ui)
		VALUES ('D009369')
		RETURNING id
	`), &descriptorID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO mesh_qualifiers (qualifier_ui)
		VALUES ('Q000627')
		RETURNING id
	`), &qualifierID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO publication_types (publication_type_ui)
		VALUES ('D016428')
		RETURNING id
	`), &publicationTypeID)

	for _, identity := range []struct {
		table           string
		uiColumn        string
		validUI         string
		checkConstraint string
		keyConstraint   string
	}{
		{
			table:           "mesh_descriptors",
			uiColumn:        "descriptor_ui",
			validUI:         "D009369",
			checkConstraint: "mesh_descriptors_descriptor_ui_check",
			keyConstraint:   "mesh_descriptors_descriptor_ui_key",
		},
		{
			table:           "mesh_qualifiers",
			uiColumn:        "qualifier_ui",
			validUI:         "Q000627",
			checkConstraint: "mesh_qualifiers_qualifier_ui_check",
			keyConstraint:   "mesh_qualifiers_qualifier_ui_key",
		},
		{
			table:           "publication_types",
			uiColumn:        "publication_type_ui",
			validUI:         "D016428",
			checkConstraint: "publication_types_publication_type_ui_check",
			keyConstraint:   "publication_types_publication_type_ui_key",
		},
	} {
		for _, invalidUI := range []string{"", " " + identity.validUI, identity.validUI + " "} {
			_, err := pool.Exec(
				ctx,
				fmt.Sprintf("INSERT INTO %s (%s) VALUES ($1)", identity.table, identity.uiColumn),
				invalidUI,
			)
			assertPostgresError(t, err, "23514", identity.checkConstraint)
		}
		_, err := pool.Exec(
			ctx,
			fmt.Sprintf("INSERT INTO %s (%s) VALUES ($1)", identity.table, identity.uiColumn),
			identity.validUI,
		)
		assertPostgresError(t, err, "23505", identity.keyConstraint)
	}

}

func TestBiomedicalSemanticSchemaSubjectReceiptIntegrity(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	fixture := insertBiomedicalSubjectRegistryFixture(
		t,
		pool,
		"receipt-primary",
		[]string{"ONCOLOGY", "CARDIAC & CARDIOVASCULAR SYSTEMS"},
	)
	receiptID := fixture.ReceiptID
	subjectVersionID := fixture.VersionID
	oncologySubjectID := fixture.SubjectIDs[0]

	_, err := pool.Exec(ctx, `
		INSERT INTO subject_import_receipts (
			source, registry_version, file_sha256, subject_count, rule_count, imported_at
		) VALUES (
			$1, $2,
			'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
			2, 2, now()
		)
	`, fixture.Source, fixture.RegistryVersion)
	assertPostgresError(t, err, "23505", "subject_import_receipts_source_registry_key")

	_, err = pool.Exec(ctx, `
		INSERT INTO subject_import_receipts (
			source, registry_version, file_sha256, subject_count, rule_count, imported_at
		) VALUES (
			'clarivate-conflict', 'conflicting-hash',
			$1,
			2, 2, now()
		)
	`, fixture.FileSHA256)
	assertPostgresError(t, err, "23505", "subject_import_receipts_file_sha256_key")

	for _, invalidReceipt := range []struct {
		query      string
		constraint string
	}{
		{
			query: `
				INSERT INTO subject_import_receipts (
					source, registry_version, file_sha256, subject_count, rule_count, imported_at
				) VALUES ('clarivate', 'bad-hash', 'ABC', 0, 0, now())
			`,
			constraint: "subject_import_receipts_file_sha256_check",
		},
		{
			query: `
				INSERT INTO subject_import_receipts (
					source, registry_version, file_sha256, subject_count, rule_count, imported_at
				) VALUES (
					'clarivate', 'bad-count',
					'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
					-1, 0, now()
				)
			`,
			constraint: "subject_import_receipts_counts_check",
		},
	} {
		_, err := pool.Exec(ctx, invalidReceipt.query)
		assertPostgresError(t, err, "23514", invalidReceipt.constraint)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO subject_versions (subject_import_receipt_id, version_key)
		VALUES ('00000000-0000-0000-0000-000000000001', 'missing-receipt-version')
	`)
	assertPostgresError(t, err, "23503", "subject_versions_subject_import_receipt_id_fkey")

	_, err = pool.Exec(ctx, `
		INSERT INTO subjects (subject_version_id, slug, display_label)
		VALUES ('00000000-0000-0000-0000-000000000002', 'missing-version', 'Missing Version')
	`)
	assertPostgresError(t, err, "23503", "subjects_subject_version_id_fkey")

	for _, mismatch := range []struct {
		name             string
		includeVersion   bool
		declaredSubjects int
		declaredRules    int
		actualSubjects   int
		actualRules      int
	}{
		{
			name:           "missing version",
			includeVersion: false,
		},
		{
			name:             "subject count mismatch",
			includeVersion:   true,
			declaredSubjects: 1,
			actualSubjects:   0,
		},
		{
			name:             "rule count mismatch",
			includeVersion:   true,
			declaredSubjects: 1,
			declaredRules:    1,
			actualSubjects:   1,
			actualRules:      0,
		},
	} {
		commitErr := commitBiomedicalSubjectRegistryCounts(
			t,
			pool,
			"mismatch-"+strings.ReplaceAll(mismatch.name, " ", "-"),
			mismatch.includeVersion,
			mismatch.declaredSubjects,
			mismatch.declaredRules,
			mismatch.actualSubjects,
			mismatch.actualRules,
		)
		assertPostgresError(
			t,
			commitErr,
			"23514",
			"subject_import_receipts_content_integrity",
		)
	}

	for _, lateChild := range []struct {
		name  string
		query string
		args  []any
	}{
		{
			name: "version",
			query: `
				INSERT INTO subject_versions (subject_import_receipt_id, version_key)
				VALUES ($1, 'late-version')
			`,
			args: []any{receiptID},
		},
		{
			name: "subject",
			query: `
				INSERT INTO subjects (subject_version_id, slug, display_label)
				VALUES ($1, 'late-subject', 'Late Subject')
			`,
			args: []any{subjectVersionID},
		},
		{
			name: "rule",
			query: `
				INSERT INTO biomedical_subject_rules (
					subject_version_id, subject_id, jcr_category
				) VALUES ($1, $2, 'LATE CATEGORY')
			`,
			args: []any{subjectVersionID, oncologySubjectID},
		},
	} {
		_, lateChildErr := pool.Exec(ctx, lateChild.query, lateChild.args...)
		assertPostgresError(
			t,
			lateChildErr,
			"23514",
			"subject_import_receipt_children_current_transaction",
		)
	}

	var versionCount, subjectCount, ruleCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			(
				SELECT count(*)
				FROM subject_versions
				WHERE subject_import_receipt_id = $1
			),
			(
				SELECT count(*)
				FROM subjects AS subject
				JOIN subject_versions AS version
				  ON version.id = subject.subject_version_id
				WHERE version.subject_import_receipt_id = $1
			),
			(
				SELECT count(*)
				FROM biomedical_subject_rules AS rule
				JOIN subject_versions AS version
				  ON version.id = rule.subject_version_id
				WHERE version.subject_import_receipt_id = $1
			)
	`, receiptID).Scan(&versionCount, &subjectCount, &ruleCount); err != nil {
		t.Fatalf("query sealed subject receipt counts: %v", err)
	}
	if versionCount != 1 || subjectCount != 2 || ruleCount != 2 {
		t.Fatalf(
			"sealed receipt content = versions %d, subjects %d, rules %d; want 1, 2, 2",
			versionCount,
			subjectCount,
			ruleCount,
		)
	}

	assertBiomedicalSubjectValidationConstraints(t, pool)

}

func TestBiomedicalSemanticSchemaProjectionProvenance(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	var err error

	var descriptorID, qualifierID, publicationTypeID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO mesh_descriptors (descriptor_ui)
		VALUES ('D009369')
		RETURNING id
	`), &descriptorID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO mesh_qualifiers (qualifier_ui)
		VALUES ('Q000627')
		RETURNING id
	`), &qualifierID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO publication_types (publication_type_ui)
		VALUES ('D016428')
		RETURNING id
	`), &publicationTypeID)

	workID := insertWork(t, pool, "openreview:biomedical-a")
	secondWorkID := insertWork(t, pool, "openreview:biomedical-b")
	sourceRecordID := insertSourceRecord(
		t,
		pool,
		workID,
		"pubmed",
		"biomedical-source-a",
		"biomedical-source-hash-a",
	)
	secondSourceRecordID := insertSourceRecord(
		t,
		pool,
		secondWorkID,
		"pubmed",
		"biomedical-source-b",
		"biomedical-source-hash-b",
	)

	projectionAssertionID := insertBiomedicalProjectionAssertion(
		t,
		pool,
		workID,
		sourceRecordID,
		"a",
	)
	secondProjectionAssertionID := insertBiomedicalProjectionAssertion(
		t,
		pool,
		secondWorkID,
		secondSourceRecordID,
		"b",
	)
	mismatchedProjectionAssertionID := insertBiomedicalProjectionAssertion(
		t,
		pool,
		secondWorkID,
		sourceRecordID,
		"c",
	)

	assertDeferredConstraintViolation := func(
		constraint string,
		query string,
		args ...any,
	) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin %s violation transaction: %v", constraint, err)
		}
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatalf("insert before checking deferred %s: %v", constraint, err)
		}
		_, constraintErr := tx.Exec(ctx, "SET CONSTRAINTS "+constraint+" IMMEDIATE")
		if rollbackErr := tx.Rollback(context.Background()); rollbackErr != nil {
			t.Fatalf("rollback %s violation transaction: %v", constraint, rollbackErr)
		}
		assertPostgresError(t, constraintErr, "23503", constraint)
	}

	var (
		projectionDescriptorID      string
		validationDescriptorID      string
		projectionQualifierID       string
		validationQualifierID       string
		projectionPublicationTypeID string
		validationPublicationTypeID string
	)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO mesh_descriptors (descriptor_ui) VALUES ('D000101') RETURNING id
	`), &projectionDescriptorID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO mesh_descriptors (descriptor_ui) VALUES ('D000102') RETURNING id
	`), &validationDescriptorID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO mesh_qualifiers (qualifier_ui) VALUES ('Q000101') RETURNING id
	`), &projectionQualifierID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO mesh_qualifiers (qualifier_ui) VALUES ('Q000102') RETURNING id
	`), &validationQualifierID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO publication_types (publication_type_ui) VALUES ('D000201') RETURNING id
	`), &projectionPublicationTypeID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO publication_types (publication_type_ui) VALUES ('D000202') RETURNING id
	`), &validationPublicationTypeID)

	var headingID, secondHeadingID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO work_mesh_headings (
			projection_assertion_id, source_record_id, work_id, descriptor_id,
			source_path, descriptor_label, is_major_topic
		) VALUES (
			$1, $2, $3, $4,
			'/PubmedArticle/MedlineCitation/MeshHeadingList/MeshHeading[1]/DescriptorName',
			'Neoplasms', true
		)
		RETURNING id
	`, projectionAssertionID, sourceRecordID, workID, descriptorID), &headingID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO work_mesh_headings (
			projection_assertion_id, source_record_id, work_id, descriptor_id,
			source_path, descriptor_label, is_major_topic
		) VALUES (
			$1, $2, $3, $4,
			'/PubmedArticle/MedlineCitation/MeshHeadingList/MeshHeading[1]/DescriptorName',
			'Cancer', false
		)
		RETURNING id
	`, secondProjectionAssertionID, secondSourceRecordID, secondWorkID, descriptorID), &secondHeadingID)

	_, err = pool.Exec(ctx, `
		INSERT INTO work_mesh_headings (
			projection_assertion_id, source_record_id, work_id, descriptor_id,
			source_path, descriptor_label, is_major_topic
		) VALUES (
			$1, $2, $3, $4, '/mismatch/DescriptorName', 'Neoplasms', false
		)
	`, mismatchedProjectionAssertionID, sourceRecordID, secondWorkID, descriptorID)
	assertPostgresError(t, err, "23503", "work_mesh_headings_source_record_work_fkey")

	assertDeferredConstraintViolation(
		"work_mesh_headings_projection_assertion_fkey",
		`
			INSERT INTO work_mesh_headings (
				projection_assertion_id, source_record_id, work_id, descriptor_id,
				source_path, descriptor_label, is_major_topic
			) VALUES (
				$1, $2, $3, $4, '/wrong-projection/DescriptorName',
				'Wrong projection descriptor', false
			)
		`,
		secondProjectionAssertionID,
		sourceRecordID,
		workID,
		projectionDescriptorID,
	)

	_, err = pool.Exec(ctx, `
		INSERT INTO work_mesh_headings (
			projection_assertion_id, source_record_id, work_id, descriptor_id,
			source_path, descriptor_label, is_major_topic
		) VALUES (
			$1, $2, $3, $4, '/duplicate/DescriptorName', 'Cancer duplicate', false
		)
	`, projectionAssertionID, sourceRecordID, workID, descriptorID)
	assertPostgresError(t, err, "23505", "work_mesh_headings_projection_descriptor_key")

	for _, invalidHeading := range []struct {
		name       string
		sourcePath any
		label      any
		majorTopic any
		code       string
		constraint string
		column     string
	}{
		{
			name:       "blank source path",
			sourcePath: "",
			label:      "Validation descriptor",
			majorTopic: false,
			code:       "23514",
			constraint: "work_mesh_headings_source_path_check",
		},
		{
			name:       "blank label",
			sourcePath: "/validation/DescriptorName",
			label:      "",
			majorTopic: false,
			code:       "23514",
			constraint: "work_mesh_headings_descriptor_label_check",
		},
		{
			name:       "null major topic",
			sourcePath: "/validation/DescriptorName",
			label:      "Validation descriptor",
			majorTopic: nil,
			code:       "23502",
			column:     "is_major_topic",
		},
	} {
		_, headingErr := pool.Exec(ctx, `
			INSERT INTO work_mesh_headings (
				projection_assertion_id, source_record_id, work_id, descriptor_id,
				source_path, descriptor_label, is_major_topic
			) VALUES ($1, $2, $3, $4, $5, $6, $7)
		`,
			projectionAssertionID,
			sourceRecordID,
			workID,
			validationDescriptorID,
			invalidHeading.sourcePath,
			invalidHeading.label,
			invalidHeading.majorTopic,
		)
		if invalidHeading.code == "23502" {
			assertPostgresColumnError(
				t,
				headingErr,
				invalidHeading.code,
				"work_mesh_headings",
				invalidHeading.column,
			)
		} else {
			assertPostgresError(t, headingErr, invalidHeading.code, invalidHeading.constraint)
		}
	}

	var qualifierAssertionID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO work_mesh_qualifiers (
			projection_assertion_id, source_record_id, work_id, work_mesh_heading_id,
			qualifier_id, source_path, qualifier_label, is_major_topic
		) VALUES (
			$1, $2, $3, $4, $5,
			'/PubmedArticle/MedlineCitation/MeshHeadingList/MeshHeading[1]/QualifierName[1]',
			'therapy', false
		)
		RETURNING id
	`, projectionAssertionID, sourceRecordID, workID, headingID, qualifierID), &qualifierAssertionID)

	_, err = pool.Exec(ctx, `
		INSERT INTO work_mesh_qualifiers (
			projection_assertion_id, source_record_id, work_id, work_mesh_heading_id,
			qualifier_id, source_path, qualifier_label, is_major_topic
		) VALUES (
			$1, $2, $3, $4, $5, '/wrong/QualifierName', 'therapy', false
		)
	`, projectionAssertionID, sourceRecordID, workID, secondHeadingID, qualifierID)
	assertPostgresError(t, err, "23503", "work_mesh_qualifiers_heading_assertion_fkey")

	assertDeferredConstraintViolation(
		"work_mesh_qualifiers_projection_assertion_fkey",
		`
			INSERT INTO work_mesh_qualifiers (
				projection_assertion_id, source_record_id, work_id, work_mesh_heading_id,
				qualifier_id, source_path, qualifier_label, is_major_topic
			) VALUES (
				$1, $2, $3, $4, $5, '/wrong-projection/QualifierName',
				'wrong projection qualifier', false
			)
		`,
		secondProjectionAssertionID,
		sourceRecordID,
		workID,
		headingID,
		projectionQualifierID,
	)

	_, err = pool.Exec(ctx, `
		INSERT INTO work_mesh_qualifiers (
			projection_assertion_id, source_record_id, work_id, work_mesh_heading_id,
			qualifier_id, source_path, qualifier_label, is_major_topic
		) VALUES (
			$1, $2, $3, $4, $5, '/duplicate/QualifierName', 'drug therapy', true
		)
	`, projectionAssertionID, sourceRecordID, workID, headingID, qualifierID)
	assertPostgresError(t, err, "23505", "work_mesh_qualifiers_heading_qualifier_key")

	for _, invalidQualifier := range []struct {
		name       string
		sourcePath any
		label      any
		majorTopic any
		code       string
		constraint string
		column     string
	}{
		{
			name:       "blank source path",
			sourcePath: "",
			label:      "validation qualifier",
			majorTopic: false,
			code:       "23514",
			constraint: "work_mesh_qualifiers_source_path_check",
		},
		{
			name:       "blank label",
			sourcePath: "/validation/QualifierName",
			label:      "",
			majorTopic: false,
			code:       "23514",
			constraint: "work_mesh_qualifiers_qualifier_label_check",
		},
		{
			name:       "null major topic",
			sourcePath: "/validation/QualifierName",
			label:      "validation qualifier",
			majorTopic: nil,
			code:       "23502",
			column:     "is_major_topic",
		},
	} {
		_, qualifierErr := pool.Exec(ctx, `
			INSERT INTO work_mesh_qualifiers (
				projection_assertion_id, source_record_id, work_id, work_mesh_heading_id,
				qualifier_id, source_path, qualifier_label, is_major_topic
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`,
			projectionAssertionID,
			sourceRecordID,
			workID,
			headingID,
			validationQualifierID,
			invalidQualifier.sourcePath,
			invalidQualifier.label,
			invalidQualifier.majorTopic,
		)
		if invalidQualifier.code == "23502" {
			assertPostgresColumnError(
				t,
				qualifierErr,
				invalidQualifier.code,
				"work_mesh_qualifiers",
				invalidQualifier.column,
			)
		} else {
			assertPostgresError(t, qualifierErr, invalidQualifier.code, invalidQualifier.constraint)
		}
	}

	var publicationTypeAssertionID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO work_publication_types (
			projection_assertion_id, source_record_id, work_id, publication_type_id,
			source_path, publication_type_label
		) VALUES (
			$1, $2, $3, $4,
			'/PubmedArticle/MedlineCitation/Article/PublicationTypeList/PublicationType[1]',
			'Journal Article'
		)
		RETURNING id
	`, projectionAssertionID, sourceRecordID, workID, publicationTypeID), &publicationTypeAssertionID)

	assertDeferredConstraintViolation(
		"work_publication_types_projection_assertion_fkey",
		`
			INSERT INTO work_publication_types (
				projection_assertion_id, source_record_id, work_id, publication_type_id,
				source_path, publication_type_label
			) VALUES (
				$1, $2, $3, $4, '/wrong-projection/PublicationType',
				'Wrong Projection Publication Type'
			)
		`,
		secondProjectionAssertionID,
		sourceRecordID,
		workID,
		projectionPublicationTypeID,
	)

	if _, err := pool.Exec(ctx, `
		INSERT INTO work_publication_types (
			projection_assertion_id, source_record_id, work_id, publication_type_id,
			source_path, publication_type_label
		) VALUES (
			$1, $2, $3, $4,
			'/PubmedArticle/MedlineCitation/Article/PublicationTypeList/PublicationType[1]',
			'Research Article'
		)
	`, secondProjectionAssertionID, secondSourceRecordID, secondWorkID, publicationTypeID); err != nil {
		t.Fatalf("same Publication Type UI with a different source label was rejected: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO work_publication_types (
			projection_assertion_id, source_record_id, work_id, publication_type_id,
			source_path, publication_type_label
		) VALUES (
			$1, $2, $3, $4, '/duplicate/PublicationType', 'Duplicate label'
		)
	`, projectionAssertionID, sourceRecordID, workID, publicationTypeID)
	assertPostgresError(t, err, "23505", "work_publication_types_projection_type_key")

	for _, invalidPublicationType := range []struct {
		sourcePath string
		label      string
		constraint string
	}{
		{
			sourcePath: "",
			label:      "Validation Publication Type",
			constraint: "work_publication_types_source_path_check",
		},
		{
			sourcePath: "/validation/PublicationType",
			label:      "",
			constraint: "work_publication_types_label_check",
		},
	} {
		_, publicationTypeErr := pool.Exec(ctx, `
			INSERT INTO work_publication_types (
				projection_assertion_id, source_record_id, work_id, publication_type_id,
				source_path, publication_type_label
			) VALUES ($1, $2, $3, $4, $5, $6)
		`,
			projectionAssertionID,
			sourceRecordID,
			workID,
			validationPublicationTypeID,
			invalidPublicationType.sourcePath,
			invalidPublicationType.label,
		)
		assertPostgresError(
			t,
			publicationTypeErr,
			"23514",
			invalidPublicationType.constraint,
		)
	}

	var descriptorAssertionCount int
	var descriptorLabels []string
	if err := pool.QueryRow(ctx, `
		SELECT count(*), array_agg(descriptor_label ORDER BY descriptor_label)
		FROM work_mesh_headings
		WHERE descriptor_id = $1
	`, descriptorID).Scan(&descriptorAssertionCount, &descriptorLabels); err != nil {
		t.Fatalf("query source-specific descriptor labels: %v", err)
	}
	if descriptorAssertionCount != 2 ||
		len(descriptorLabels) != 2 ||
		descriptorLabels[0] != "Cancer" ||
		descriptorLabels[1] != "Neoplasms" {
		t.Fatalf(
			"descriptor source assertions = count %d, labels %v; want 2 and [Cancer Neoplasms]",
			descriptorAssertionCount,
			descriptorLabels,
		)
	}

}

func TestBiomedicalSemanticSchemaJournalSubjectMetrics(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	subjectFixture := insertBiomedicalSubjectRegistryFixture(
		t,
		pool,
		"journal-metrics",
		[]string{"ONCOLOGY", "CARDIAC & CARDIOVASCULAR SYSTEMS"},
	)
	oncologyRuleID := subjectFixture.RuleIDs[0]
	cardiologyRuleID := subjectFixture.RuleIDs[1]

	var venueID, metricSnapshotID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title, issn_l)
		VALUES ('journal', 'Biomedical Semantics Journal', '9876-543X')
		RETURNING id
	`), &venueID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license
		) VALUES (
			$1, 2025, 'ONCOLOGY', 12.5, 'Q1', 'known', 'clarivate', 'licensed'
		)
		RETURNING id
	`, venueID), &metricSnapshotID)

	_, err := pool.Exec(ctx, `
		INSERT INTO journal_subject_metrics (
			venue_metric_snapshot_id, subject_rule_id, jcr_category
		) VALUES ($1, $2, 'CARDIAC & CARDIOVASCULAR SYSTEMS')
	`, metricSnapshotID, cardiologyRuleID)
	assertPostgresError(t, err, "23503", "journal_subject_metrics_metric_category_fkey")

	_, err = pool.Exec(ctx, `
		INSERT INTO journal_subject_metrics (
			venue_metric_snapshot_id, subject_rule_id, jcr_category
		) VALUES ($1, $2, 'ONCOLOGY')
	`, metricSnapshotID, cardiologyRuleID)
	assertPostgresError(t, err, "23503", "journal_subject_metrics_rule_category_fkey")

	var journalSubjectMetricID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO journal_subject_metrics (
			venue_metric_snapshot_id, subject_rule_id, jcr_category
		) VALUES ($1, $2, 'ONCOLOGY')
		RETURNING id
	`, metricSnapshotID, oncologyRuleID), &journalSubjectMetricID)

}

func TestBiomedicalSemanticSchemaImmutability(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	projectionFixture := insertBiomedicalProjectionFixture(t, pool, "immutable")
	subjectFixture := insertBiomedicalSubjectRegistryFixture(
		t,
		pool,
		"immutable",
		[]string{"IMMUTABLE CATEGORY"},
	)

	var venueID, metricSnapshotID, journalSubjectMetricID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title, issn_l)
		VALUES ('journal', 'Immutable Biomedical Journal', '1357-246X')
		RETURNING id
	`), &venueID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license
		) VALUES (
			$1, 2025, 'IMMUTABLE CATEGORY', 10, 'Q1', 'known', 'clarivate', 'licensed'
		)
		RETURNING id
	`, venueID), &metricSnapshotID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO journal_subject_metrics (
			venue_metric_snapshot_id, subject_rule_id, jcr_category
		) VALUES ($1, $2, 'IMMUTABLE CATEGORY')
		RETURNING id
	`, metricSnapshotID, subjectFixture.RuleIDs[0]), &journalSubjectMetricID)

	immutableRows := []struct {
		table string
		id    string
	}{
		{table: "mesh_descriptors", id: projectionFixture.DescriptorID},
		{table: "mesh_qualifiers", id: projectionFixture.QualifierID},
		{table: "publication_types", id: projectionFixture.PublicationTypeID},
		{table: "work_mesh_headings", id: projectionFixture.HeadingID},
		{table: "work_mesh_qualifiers", id: projectionFixture.QualifierAssertionID},
		{table: "work_publication_types", id: projectionFixture.PublicationTypeAssertionID},
		{table: "subject_import_receipts", id: subjectFixture.ReceiptID},
		{table: "subject_versions", id: subjectFixture.VersionID},
		{table: "subjects", id: subjectFixture.SubjectIDs[0]},
		{table: "biomedical_subject_rules", id: subjectFixture.RuleIDs[0]},
		{table: "journal_subject_metrics", id: journalSubjectMetricID},
	}
	for _, row := range immutableRows {
		t.Run(row.table+" immutable", func(t *testing.T) {
			_, updateErr := pool.Exec(
				ctx,
				fmt.Sprintf(
					"UPDATE %s SET created_at = created_at + interval '1 second' WHERE id = $1",
					row.table,
				),
				row.id,
			)
			assertPostgresError(t, updateErr, "55000", "")

			_, deleteErr := pool.Exec(
				ctx,
				fmt.Sprintf("DELETE FROM %s WHERE id = $1", row.table),
				row.id,
			)
			assertPostgresError(t, deleteErr, "55000", "")
		})
	}
}

func TestBiomedicalSemanticSchemaUpgradeFromV10PreservesProvenance(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	if len(migrations) != 18 {
		t.Fatalf("embedded migration count = %d, want 18", len(migrations))
	}

	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:10]); err != nil {
		t.Fatalf("apply migrations through v10: %v", err)
	}

	workID := insertWork(t, pool, "openreview:v10-upgrade")
	sourceRecordID := insertSourceRecord(
		t,
		pool,
		workID,
		"pubmed",
		"v10-upgrade-source",
		"v10-upgrade-source-hash",
	)
	projectionAssertionID := insertBiomedicalProjectionAssertion(
		t,
		pool,
		workID,
		sourceRecordID,
		"v10-upgrade",
	)

	var venueID, metricSnapshotID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title, issn_l)
		VALUES ('journal', 'V10 Upgrade Journal', '2468-135X')
		RETURNING id
	`), &venueID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license
		) VALUES (
			$1, 2025, 'UPGRADE CATEGORY', 11, 'Q1', 'known', 'clarivate', 'licensed'
		)
		RETURNING id
	`, venueID), &metricSnapshotID)

	var projectionCountBefore, metricCountBefore int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM ingestion_projection_assertions").
		Scan(&projectionCountBefore); err != nil {
		t.Fatalf("count v10 projection assertions: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM venue_metric_snapshots").
		Scan(&metricCountBefore); err != nil {
		t.Fatalf("count v10 Venue metric snapshots: %v", err)
	}

	if err := UpMigrations(ctx, pool, migrations[10:]); err != nil {
		t.Fatalf("apply v11 biomedical semantics and v12 normalized assertion migrations: %v", err)
	}

	var preservedProjectionID, normalizedAssertionID, preservedMetricID, preservedCategory string
	if err := pool.QueryRow(ctx, `
		SELECT assertion.id::text, assertion.normalized_assertion_id::text
		FROM ingestion_projection_assertions AS assertion
		JOIN ingestion_normalized_records AS normalized
		  ON normalized.id = assertion.normalized_assertion_id
		 AND normalized.raw_event_id = assertion.raw_event_id
		 AND normalized.source_record_uuid = assertion.source_record_uuid
		WHERE assertion.id = $1
	`, projectionAssertionID).Scan(
		&preservedProjectionID,
		&normalizedAssertionID,
	); err != nil {
		t.Fatalf("query preserved projection assertion: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT id::text, category
		FROM venue_metric_snapshots
		WHERE id = $1
	`, metricSnapshotID).Scan(&preservedMetricID, &preservedCategory); err != nil {
		t.Fatalf("query preserved Venue metric snapshot: %v", err)
	}
	var projectionCountAfter, metricCountAfter int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM ingestion_projection_assertions").
		Scan(&projectionCountAfter); err != nil {
		t.Fatalf("count upgraded projection assertions: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM venue_metric_snapshots").
		Scan(&metricCountAfter); err != nil {
		t.Fatalf("count upgraded Venue metric snapshots: %v", err)
	}
	if preservedProjectionID != projectionAssertionID ||
		normalizedAssertionID == "" ||
		preservedMetricID != metricSnapshotID ||
		preservedCategory != "UPGRADE CATEGORY" ||
		projectionCountAfter != projectionCountBefore ||
		metricCountAfter != metricCountBefore {
		t.Fatalf(
			"v10->v12 preservation = projection %q/%q normalized %q count %d/%d, metric %q/%q category %q count %d/%d",
			preservedProjectionID,
			projectionAssertionID,
			normalizedAssertionID,
			projectionCountAfter,
			projectionCountBefore,
			preservedMetricID,
			metricSnapshotID,
			preservedCategory,
			metricCountAfter,
			metricCountBefore,
		)
	}

	var descriptorID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO mesh_descriptors (descriptor_ui)
		VALUES ('D800001')
		RETURNING id
	`), &descriptorID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_mesh_headings (
			projection_assertion_id, source_record_id, work_id, descriptor_id,
			source_path, descriptor_label, is_major_topic
		) VALUES (
			$1, $2, $3, $4, '/upgrade/DescriptorName', 'Upgrade Descriptor', false
		)
	`, projectionAssertionID, sourceRecordID, workID, descriptorID); err != nil {
		t.Fatalf("reference preserved v10 projection assertion from v12 semantic row: %v", err)
	}

	subjectFixture := insertBiomedicalSubjectRegistryFixture(
		t,
		pool,
		"v10-upgrade",
		[]string{"UPGRADE CATEGORY"},
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO journal_subject_metrics (
			venue_metric_snapshot_id, subject_rule_id, jcr_category
		) VALUES ($1, $2, 'UPGRADE CATEGORY')
	`, metricSnapshotID, subjectFixture.RuleIDs[0]); err != nil {
		t.Fatalf("reference preserved v10 metric snapshot from v11 semantic row: %v", err)
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
		"jcr_import_receipt_metrics_import_receipt_id_fkey",
		"jcr_import_receipt_metrics_metric_snapshot_id_fkey",
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
		"jcr_import_receipt_metrics_immutable",
		"jcr_import_receipts_metric_integrity",
		"venue_metric_snapshots_receipt_transaction",
		"venue_metric_snapshots_receipt_transaction_deferred",
		"jcr_import_receipt_metrics_receipt_transaction",
		"venue_policy_assessments_venue_type_semantics",
	})
	var rowLevelCountTriggers int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_trigger
		WHERE NOT tgisinternal
		  AND tgname IN (
		      'jcr_import_receipt_metrics_integrity',
		      'venue_metric_snapshots_receipt_integrity'
		  )
	`).Scan(&rowLevelCountTriggers); err != nil {
		t.Fatalf("query removed row-level JCR count triggers: %v", err)
	}
	if rowLevelCountTriggers != 0 {
		t.Fatalf("row-level JCR count triggers = %d, want 0", rowLevelCountTriggers)
	}
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
		"ingestion_jobs_active_idempotency_key",
		"idx_venue_metric_snapshots_lookup",
		"idx_venue_metric_snapshots_import_receipt",
		"idx_jcr_import_receipt_aliases_alias",
		"idx_jcr_import_receipt_metrics_metric",
		"idx_jcr_import_receipt_metrics_receipt_metric",
		"idx_fulltext_assets_public",
	})

	expectedDeleteRules := map[string]string{
		"source_record_works_source_record_id_fkey":          "r",
		"source_record_works_work_id_fkey":                   "c",
		"field_assertions_work_id_fkey":                      "n",
		"paper_versions_source_record_id_fkey":               "r",
		"field_assertions_source_record_id_fkey":             "r",
		"paper_versions_source_record_work_fkey":             "a",
		"field_assertions_source_record_work_fkey":           "a",
		"work_code_repositories_work_id_fkey":                "c",
		"work_code_repositories_repository_id_fkey":          "r",
		"venue_metric_snapshots_venue_id_fkey":               "r",
		"venue_metric_snapshots_jcr_import_receipt_id_fkey":  "r",
		"venue_policy_assessments_policy_version_fkey":       "r",
		"jcr_import_receipt_aliases_import_receipt_id_fkey":  "r",
		"jcr_import_receipt_aliases_venue_alias_id_fkey":     "r",
		"jcr_import_receipt_metrics_import_receipt_id_fkey":  "r",
		"jcr_import_receipt_metrics_metric_snapshot_id_fkey": "r",
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

	var metricReceiptDeferrable, metricReceiptDeferred bool
	if err := pool.QueryRow(ctx, `
		SELECT condeferrable, condeferred
		FROM pg_constraint
		WHERE conname = 'venue_metric_snapshots_jcr_import_receipt_id_fkey'
	`).Scan(&metricReceiptDeferrable, &metricReceiptDeferred); err != nil {
		t.Fatalf("query metric receipt FK deferral: %v", err)
	}
	if !metricReceiptDeferrable || !metricReceiptDeferred {
		t.Fatalf(
			"metric receipt FK deferral = %v/%v, want true/true",
			metricReceiptDeferrable,
			metricReceiptDeferred,
		)
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin JCR provenance transaction: %v", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	var receiptID string
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO jcr_import_receipts (
			file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
		) VALUES ($1, 'synthetic-jcr-fixture', $2, 1, 1, 0)
		RETURNING id
	`, fileSHA, importedAt), &receiptID)

	var metricID string
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license, captured_at, jcr_import_receipt_id
		) VALUES (
			$1, 2025, 'Zero JIF Studies', 0, 'Q2', 'known',
			'synthetic-jcr-fixture', 'synthetic-only', $2, $3
		)
		RETURNING id
	`, journalID, importedAt, receiptID), &metricID)
	if _, err := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipt_aliases (import_receipt_id, venue_alias_id)
		VALUES ($1, $2)
	`, receiptID, aliasID); err != nil {
		t.Fatalf("link imported alias to receipt: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipt_metrics (import_receipt_id, metric_snapshot_id)
		VALUES ($1, $2)
	`, receiptID, metricID); err != nil {
		t.Fatalf("link imported metric to receipt: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit JCR provenance transaction: %v", err)
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
		`UPDATE jcr_import_receipt_metrics SET metric_snapshot_id = metric_snapshot_id WHERE import_receipt_id = $1`,
		`DELETE FROM jcr_import_receipt_metrics WHERE import_receipt_id = $1`,
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
			'{"reason":"venue_type_not_journal","venue_type":"conference"}',
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
			'{"reason":"venue_type_not_journal","venue_type":"conference"}',
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

func TestVenuePolicyAssessmentMigrationEnforcesActualVenueType(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	var journalID, conferenceID, preprintID, policyID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('journal', 'Policy Journal')
		RETURNING id
	`), &journalID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('conference', 'Policy Conference')
		RETURNING id
	`), &conferenceID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('preprint', 'Policy Preprint')
		RETURNING id
	`), &preprintID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_policy_versions (policy_name, version_number, definition, effective_at)
		VALUES ('venue-type-policy', 1, '{"accept":["jif_gte_10","jcr_q1"]}', now())
		RETURNING id
	`), &policyID)

	validAssessments := []struct {
		venueID    string
		metricYear int
		decision   string
		matched    string
		evidence   string
	}{
		{
			venueID:    journalID,
			metricYear: 2025,
			decision:   "accepted",
			matched:    `["jif_gte_10"]`,
			evidence:   `{"venue_type":"journal","categories":["AI"]}`,
		},
		{
			venueID:    journalID,
			metricYear: 2026,
			decision:   "rejected",
			matched:    `[]`,
			evidence:   `{"venue_type":"journal","categories":["AI"]}`,
		},
		{
			venueID:    journalID,
			metricYear: 2027,
			decision:   "unknown",
			matched:    `[]`,
			evidence:   `{"venue_type":"journal","reason":"missing_metric"}`,
		},
		{
			venueID:    conferenceID,
			metricYear: 2025,
			decision:   "not_applicable",
			matched:    `[]`,
			evidence:   `{"venue_type":"conference","reason":"venue_type_not_journal"}`,
		},
		{
			venueID:    preprintID,
			metricYear: 2025,
			decision:   "not_applicable",
			matched:    `[]`,
			evidence:   `{"venue_type":"preprint","reason":"venue_type_not_journal"}`,
		},
	}
	for _, assessment := range validAssessments {
		if _, err := pool.Exec(ctx, `
			INSERT INTO venue_policy_assessments (
				venue_id, policy_version_id, metric_year, decision,
				matched_rules, evidence, assessed_at
			) VALUES ($1, $2, $3, $4, $5, $6, now())
		`,
			assessment.venueID,
			policyID,
			assessment.metricYear,
			assessment.decision,
			assessment.matched,
			assessment.evidence,
		); err != nil {
			t.Fatalf("persist valid %s assessment: %v", assessment.decision, err)
		}
	}

	invalidAssessments := []struct {
		name         string
		venueID      string
		metricYear   int
		decision     string
		matchedRules string
		evidence     string
	}{
		{
			name:         "journal not applicable",
			venueID:      journalID,
			metricYear:   2028,
			decision:     "not_applicable",
			matchedRules: `[]`,
			evidence:     `{"venue_type":"journal","reason":"venue_type_not_journal"}`,
		},
		{
			name:         "conference accepted",
			venueID:      conferenceID,
			metricYear:   2026,
			decision:     "accepted",
			matchedRules: `["jcr_q1"]`,
			evidence:     `{"venue_type":"conference"}`,
		},
		{
			name:         "preprint unknown",
			venueID:      preprintID,
			metricYear:   2026,
			decision:     "unknown",
			matchedRules: `[]`,
			evidence:     `{"venue_type":"preprint","reason":"missing_metric"}`,
		},
		{
			name:         "mismatched evidence type",
			venueID:      conferenceID,
			metricYear:   2027,
			decision:     "not_applicable",
			matchedRules: `[]`,
			evidence:     `{"venue_type":"preprint","reason":"venue_type_not_journal"}`,
		},
		{
			name:         "wrong not applicable reason",
			venueID:      conferenceID,
			metricYear:   2028,
			decision:     "not_applicable",
			matchedRules: `[]`,
			evidence:     `{"venue_type":"conference","reason":"journal_policy_not_applicable"}`,
		},
		{
			name:         "journal evidence type mismatch",
			venueID:      journalID,
			metricYear:   2029,
			decision:     "unknown",
			matchedRules: `[]`,
			evidence:     `{"venue_type":"conference","reason":"missing_metric"}`,
		},
	}
	for _, assessment := range invalidAssessments {
		_, err := pool.Exec(ctx, `
			INSERT INTO venue_policy_assessments (
				venue_id, policy_version_id, metric_year, decision,
				matched_rules, evidence, assessed_at
			) VALUES ($1, $2, $3, $4, $5, $6, now())
		`,
			assessment.venueID,
			policyID,
			assessment.metricYear,
			assessment.decision,
			assessment.matchedRules,
			assessment.evidence,
		)
		if err == nil {
			t.Fatalf("invalid assessment %q was accepted", assessment.name)
		}
	}
}

func TestVenuePolicySemanticsMigrationBackfillsMetricReceiptLinks(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:3]); err != nil {
		t.Fatalf("apply migrations through 000003: %v", err)
	}

	var venueID, receiptID, metricID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('journal', 'Pre-000004 Receipt Journal')
		RETURNING id
	`), &venueID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO jcr_import_receipts (
			file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
		) VALUES (
			'cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
			'synthetic-jcr-fixture', now(), 1, 1, 0
		)
		RETURNING id
	`), &receiptID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license, captured_at, jcr_import_receipt_id
		) VALUES (
			$1, 2025, 'Legacy Receipt Category', 10, 'Q1', 'known',
			'synthetic-jcr-fixture', 'synthetic-only', now(), $2
		)
		RETURNING id
	`, venueID, receiptID), &metricID)

	if err := UpMigrations(ctx, pool, migrations); err != nil {
		t.Fatalf("upgrade through 000005: %v", err)
	}
	var links int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM jcr_import_receipt_metrics
		WHERE import_receipt_id = $1
		  AND metric_snapshot_id = $2
	`, receiptID, metricID).Scan(&links); err != nil {
		t.Fatalf("query backfilled receipt metric link: %v", err)
	}
	if links != 1 {
		t.Fatalf("backfilled receipt metric links = %d, want 1", links)
	}
}

func TestJCRIntegrityFollowupBackfillsUniqueLegacyUnchangedReceipt(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:3]); err != nil {
		t.Fatalf("apply migrations through 000003: %v", err)
	}

	var venueID, aliasID, receiptID, metricID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('journal', 'Unique Legacy Receipt Journal')
		RETURNING id
	`), &venueID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_aliases (venue_id, alias, source)
		VALUES ($1, 'Unique Legacy Receipt Journal', 'synthetic-jcr-fixture')
		RETURNING id
	`, venueID), &aliasID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license, captured_at
		) VALUES (
			$1, 2025, 'Unique Legacy Category', 10, 'Q1', 'known',
			'synthetic-jcr-fixture', 'license-a', '2025-01-01T00:00:00Z'
		)
		RETURNING id
	`, venueID), &metricID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO jcr_import_receipts (
			file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
		) VALUES (
			'dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd',
			'synthetic-jcr-fixture', '2026-01-01T00:00:00Z', 1, 0, 1
		)
		RETURNING id
	`), &receiptID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO jcr_import_receipt_aliases (import_receipt_id, venue_alias_id)
		VALUES ($1, $2)
	`, receiptID, aliasID); err != nil {
		t.Fatalf("link legacy receipt alias: %v", err)
	}

	if err := Up(ctx, pool); err != nil {
		t.Fatalf("upgrade unique legacy receipt: %v", err)
	}
	var links int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM jcr_import_receipt_metrics
		WHERE import_receipt_id = $1
		  AND metric_snapshot_id = $2
	`, receiptID, metricID).Scan(&links); err != nil {
		t.Fatalf("query unique legacy receipt link: %v", err)
	}
	if links != 1 {
		t.Fatalf("unique legacy receipt links = %d, want 1", links)
	}
}

func TestJCRIntegrityFollowupBackfillsSingleCandidateUsingReceiptMultiplicity(t *testing.T) {
	const multiplicity = 7

	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:3]); err != nil {
		t.Fatalf("apply migrations through 000003: %v", err)
	}

	var venueID, aliasID, receiptID, metricID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('journal', 'Duplicate Legacy Receipt Journal')
		RETURNING id
	`), &venueID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_aliases (venue_id, alias, source)
		VALUES ($1, 'Duplicate Legacy Receipt Journal', 'synthetic-jcr-fixture')
		RETURNING id
	`, venueID), &aliasID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license, captured_at
		) VALUES (
			$1, 2025, 'Duplicate Legacy Category', 10, 'Q1', 'known',
			'synthetic-jcr-fixture', 'license-a', '2025-01-01T00:00:00Z'
		)
		RETURNING id
	`, venueID), &metricID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO jcr_import_receipts (
			file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
		) VALUES (
			'cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd',
			'synthetic-jcr-fixture', '2026-01-01T00:00:00Z', $1, 0, $1
		)
		RETURNING id
	`, multiplicity), &receiptID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO jcr_import_receipt_aliases (import_receipt_id, venue_alias_id)
		VALUES ($1, $2)
	`, receiptID, aliasID); err != nil {
		t.Fatalf("link duplicate legacy receipt alias: %v", err)
	}

	if err := Up(ctx, pool); err != nil {
		t.Fatalf("upgrade duplicate legacy receipt: %v", err)
	}
	var links int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM jcr_import_receipt_metrics
		WHERE import_receipt_id = $1
		  AND metric_snapshot_id = $2
	`, receiptID, metricID).Scan(&links); err != nil {
		t.Fatalf("query duplicate legacy receipt links: %v", err)
	}
	if links != multiplicity {
		t.Fatalf(
			"single-candidate legacy receipt links = %d, want multiplicity %d",
			links,
			multiplicity,
		)
	}
}

func TestJCRIntegrityFollowupRejectsLegacyStateAmbiguousBetweenAAAndAB(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:3]); err != nil {
		t.Fatalf("apply migrations through 000003: %v", err)
	}

	var venueID, aliasID, receiptID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('journal', 'AA Versus AB Legacy Journal')
		RETURNING id
	`), &venueID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_aliases (venue_id, alias, source)
		VALUES ($1, 'AA Versus AB Legacy Journal', 'synthetic-jcr-fixture')
		RETURNING id
	`, venueID), &aliasID)
	for _, category := range []string{"Candidate A", "Candidate B"} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO venue_metric_snapshots (
				venue_id, metric_year, category, jif, quartile, metric_status,
				source_name, source_license, captured_at
			) VALUES (
				$1, 2025, $2, 10, 'Q1', 'known',
				'synthetic-jcr-fixture', 'license-a', '2025-01-01T00:00:00Z'
			)
		`, venueID, category); err != nil {
			t.Fatalf("insert ambiguous candidate %q: %v", category, err)
		}
	}
	const fileSHA = "cececececececececececececececececececececececececececececececece"
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO jcr_import_receipts (
			file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
		) VALUES (
			$1, 'synthetic-jcr-fixture', '2026-01-01T00:00:00Z', 2, 0, 2
		)
		RETURNING id
	`, fileSHA), &receiptID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO jcr_import_receipt_aliases (import_receipt_id, venue_alias_id)
		VALUES ($1, $2)
	`, receiptID, aliasID); err != nil {
		t.Fatalf("link A/A versus A/B legacy receipt alias: %v", err)
	}

	err = Up(ctx, pool)
	if err == nil ||
		!strings.Contains(err.Error(), "cannot deterministically associate legacy JCR receipt") ||
		!strings.Contains(err.Error(), fileSHA) ||
		!strings.Contains(err.Error(), "exact candidate metric keys 2") {
		t.Fatalf("upgrade A/A versus A/B ambiguous receipt error = %v", err)
	}
}

func TestJCRIntegrityFollowupRejectsAmbiguousLegacyUnchangedReceipts(t *testing.T) {
	for _, test := range []struct {
		name             string
		fileSHA          string
		candidateMetrics int
	}{
		{
			name:             "no candidate metric",
			fileSHA:          "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			candidateMetrics: 0,
		},
		{
			name:             "multiple candidate metrics",
			fileSHA:          "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			candidateMetrics: 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			migrations, err := EmbeddedMigrations()
			if err != nil {
				t.Fatalf("EmbeddedMigrations() error = %v", err)
			}
			pool := openTestPool(t)
			ctx := testContext(t)
			if err := UpMigrations(ctx, pool, migrations[:3]); err != nil {
				t.Fatalf("apply migrations through 000003: %v", err)
			}

			var venueID, aliasID, receiptID string
			mustScanID(t, pool.QueryRow(ctx, `
				INSERT INTO venues (venue_type, display_title)
				VALUES ('journal', 'Ambiguous Legacy Receipt Journal')
				RETURNING id
			`), &venueID)
			mustScanID(t, pool.QueryRow(ctx, `
				INSERT INTO venue_aliases (venue_id, alias, source)
				VALUES ($1, 'Ambiguous Legacy Receipt Journal', 'synthetic-jcr-fixture')
				RETURNING id
			`, venueID), &aliasID)
			for index := range test.candidateMetrics {
				if _, err := pool.Exec(ctx, `
					INSERT INTO venue_metric_snapshots (
						venue_id, metric_year, category, jif, quartile, metric_status,
						source_name, source_license, captured_at
					) VALUES (
						$1, 2025, $2, 10, 'Q1', 'known',
						'synthetic-jcr-fixture', 'license-a', '2025-01-01T00:00:00Z'
					)
				`, venueID, fmt.Sprintf("Candidate Category %d", index+1)); err != nil {
					t.Fatalf("insert candidate metric %d: %v", index+1, err)
				}
			}
			mustScanID(t, pool.QueryRow(ctx, `
				INSERT INTO jcr_import_receipts (
					file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
				) VALUES ($1, 'synthetic-jcr-fixture', '2026-01-01T00:00:00Z', 1, 0, 1)
				RETURNING id
			`, test.fileSHA), &receiptID)
			if _, err := pool.Exec(ctx, `
				INSERT INTO jcr_import_receipt_aliases (import_receipt_id, venue_alias_id)
				VALUES ($1, $2)
			`, receiptID, aliasID); err != nil {
				t.Fatalf("link ambiguous legacy receipt alias: %v", err)
			}

			err = Up(ctx, pool)
			if err == nil ||
				!strings.Contains(err.Error(), "cannot deterministically associate legacy JCR receipt") ||
				!strings.Contains(err.Error(), test.fileSHA) {
				t.Fatalf("upgrade ambiguous legacy receipt error = %v", err)
			}
		})
	}
}

func TestJCRIntegrityFollowupEnforcesDeferredReceiptRowAssociations(t *testing.T) {
	t.Run("missing association fails at commit", func(t *testing.T) {
		pool := openMigratedTestPool(t)
		ctx := testContext(t)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin missing association transaction: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO jcr_import_receipts (
				file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
			) VALUES (
				'abababababababababababababababababababababababababababababababab',
				'synthetic-jcr-fixture', now(), 1, 0, 1
			)
		`); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatalf("insert receipt without association: %v", err)
		}
		err = tx.Commit(ctx)
		var postgresError *pgconn.PgError
		if err == nil ||
			!errors.As(err, &postgresError) ||
			postgresError.Code != "23514" {
			t.Fatalf("commit missing association error = %v, want deferred check violation", err)
		}
	})

	t.Run("duplicate input rows retain duplicate associations", func(t *testing.T) {
		pool := openMigratedTestPool(t)
		ctx := testContext(t)
		var venueID string
		mustScanID(t, pool.QueryRow(ctx, `
			INSERT INTO venues (venue_type, display_title)
			VALUES ('journal', 'Duplicate Association Journal')
			RETURNING id
		`), &venueID)

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin duplicate association transaction: %v", err)
		}
		var receiptID, metricID string
		mustScanID(t, tx.QueryRow(ctx, `
			INSERT INTO jcr_import_receipts (
				file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
			) VALUES (
				'acacacacacacacacacacacacacacacacacacacacacacacacacacacacacacacac',
				'synthetic-jcr-fixture', now(), 2, 1, 1
			)
			RETURNING id
		`), &receiptID)
		mustScanID(t, tx.QueryRow(ctx, `
			INSERT INTO venue_metric_snapshots (
				venue_id, metric_year, category, jif, quartile, metric_status,
				source_name, source_license, captured_at, jcr_import_receipt_id
			) VALUES (
				$1, 2025, 'Duplicate Association Category', 10, 'Q1', 'known',
				'synthetic-jcr-fixture', 'license-a', now(), $2
			)
			RETURNING id
		`, venueID, receiptID), &metricID)
		for range 2 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO jcr_import_receipt_metrics (
					import_receipt_id, metric_snapshot_id
				) VALUES ($1, $2)
			`, receiptID, metricID); err != nil {
				_ = tx.Rollback(context.Background())
				t.Fatalf("insert duplicate receipt association: %v", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit duplicate receipt associations: %v", err)
		}
	})

	t.Run("metric receipt foreign key is immutable", func(t *testing.T) {
		pool := openMigratedTestPool(t)
		ctx := testContext(t)
		var venueID string
		mustScanID(t, pool.QueryRow(ctx, `
			INSERT INTO venues (venue_type, display_title)
			VALUES ('journal', 'Receipt Creator Integrity Journal')
			RETURNING id
		`), &venueID)

		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin valid receipt creator transaction: %v", err)
		}
		var receiptID, metricID string
		mustScanID(t, tx.QueryRow(ctx, `
			INSERT INTO jcr_import_receipts (
				file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
			) VALUES (
				'adadadadadadadadadadadadadadadadadadadadadadadadadadadadadadadad',
				'synthetic-jcr-fixture', now(), 1, 1, 0
			)
			RETURNING id
		`), &receiptID)
		mustScanID(t, tx.QueryRow(ctx, `
			INSERT INTO venue_metric_snapshots (
				venue_id, metric_year, category, jif, quartile, metric_status,
				source_name, source_license, captured_at, jcr_import_receipt_id
			) VALUES (
				$1, 2025, 'Receipt Creator Category', 10, 'Q1', 'known',
				'synthetic-jcr-fixture', 'license-a', now(), $2
			)
			RETURNING id
		`, venueID, receiptID), &metricID)
		if _, err := tx.Exec(ctx, `
			INSERT INTO jcr_import_receipt_metrics (
				import_receipt_id, metric_snapshot_id
			) VALUES ($1, $2)
		`, receiptID, metricID); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatalf("link valid receipt creator metric: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit valid receipt creator transaction: %v", err)
		}

		if _, err := pool.Exec(ctx, `
			UPDATE venue_metric_snapshots
			SET jcr_import_receipt_id = NULL
			WHERE id = $1
		`, metricID); err == nil {
			t.Fatal("metric import receipt foreign key mutation was accepted")
		}
	})
}

func TestJCRIntegrityFollowupSealsReceiptChildrenToCreationTransaction(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	var venueID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('journal', 'Sealed Receipt Journal')
		RETURNING id
	`), &venueID)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin sealed receipt transaction: %v", err)
	}
	var receiptID, metricID string
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO jcr_import_receipts (
			file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
		) VALUES (
			$1, 'synthetic-jcr-fixture', now(), 1, 1, 0
		)
		RETURNING id
	`, strings.Repeat("1", 64)), &receiptID)

	var receiptCreatedInCurrentTransaction bool
	if err := tx.QueryRow(ctx, `
		SELECT xmin = pg_current_xact_id()::text::xid
		FROM jcr_import_receipts
		WHERE id = $1
	`, receiptID).Scan(&receiptCreatedInCurrentTransaction); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("compare receipt xmin in creating transaction: %v", err)
	}
	if !receiptCreatedInCurrentTransaction {
		_ = tx.Rollback(context.Background())
		t.Fatal("receipt xmin did not equal PG18 current transaction xid")
	}

	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license, captured_at, jcr_import_receipt_id
		) VALUES (
			$1, 2025, 'Sealed Receipt Category', 10, 'Q1', 'known',
			'synthetic-jcr-fixture', 'license-a', now(), $2
		)
		RETURNING id
	`, venueID, receiptID), &metricID)
	if _, err := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipt_metrics (
			import_receipt_id,
			metric_snapshot_id
		) VALUES ($1, $2)
	`, receiptID, metricID); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("insert same-transaction receipt association: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit sealed receipt transaction: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		SELECT xmin = pg_current_xact_id()::text::xid
		FROM jcr_import_receipts
		WHERE id = $1
	`, receiptID).Scan(&receiptCreatedInCurrentTransaction); err != nil {
		t.Fatalf("compare receipt xmin after commit: %v", err)
	}
	if receiptCreatedInCurrentTransaction {
		t.Fatal("committed receipt xmin still matched a later transaction")
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin post-commit association append: %v", err)
	}
	_, associationErr := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipt_metrics (
			import_receipt_id,
			metric_snapshot_id
		) VALUES ($1, $2)
	`, receiptID, metricID)
	_ = tx.Rollback(context.Background())
	assertPostgresError(
		t,
		associationErr,
		"23514",
		"jcr_import_receipt_children_current_transaction",
	)

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin post-commit creator metric append: %v", err)
	}
	_, metricErr := tx.Exec(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license, captured_at, jcr_import_receipt_id
		) VALUES (
			$1, 2025, 'Post Commit Pollution Category', 9, 'Q2', 'known',
			'synthetic-jcr-fixture', 'license-a', now(), $2
		)
	`, venueID, receiptID)
	_ = tx.Rollback(context.Background())
	assertPostgresError(
		t,
		metricErr,
		"23514",
		"jcr_import_receipt_children_current_transaction",
	)

	var associations, creatorMetrics int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM jcr_import_receipt_metrics
		WHERE import_receipt_id = $1
	`, receiptID).Scan(&associations); err != nil {
		t.Fatalf("count sealed receipt associations: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM venue_metric_snapshots
		WHERE jcr_import_receipt_id = $1
	`, receiptID).Scan(&creatorMetrics); err != nil {
		t.Fatalf("count sealed receipt creator metrics: %v", err)
	}
	if associations != 1 || creatorMetrics != 1 {
		t.Fatalf(
			"sealed receipt counts = associations %d creator metrics %d, want 1/1",
			associations,
			creatorMetrics,
		)
	}
}

func TestJCRIntegrityFollowupRunsDeferredCountOncePerReceipt(t *testing.T) {
	const rowCount = 1000

	pool := openMigratedTestPool(t)
	ctx := testContext(t)
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire trigger audit connection: %v", err)
	}
	defer connection.Release()

	if _, err := connection.Exec(ctx, `
		CREATE TEMP TABLE jcr_receipt_trigger_audit (
			receipt_id uuid NOT NULL
		);

		CREATE OR REPLACE FUNCTION enforce_jcr_import_receipt_metric_integrity()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			INSERT INTO pg_temp.jcr_receipt_trigger_audit (receipt_id)
			VALUES (NEW.id);
			RETURN NULL;
		END;
		$$;
	`); err != nil {
		t.Fatalf("instrument JCR receipt integrity trigger: %v", err)
	}

	var venueID string
	mustScanID(t, connection.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('journal', 'High Row Count Receipt Journal')
		RETURNING id
	`), &venueID)

	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatalf("begin high-row receipt transaction: %v", err)
	}
	var receiptID string
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO jcr_import_receipts (
			file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
		) VALUES (
			'efefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefef',
			'synthetic-jcr-fixture', now(), $1, $1, 0
		)
		RETURNING id
	`, rowCount), &receiptID)
	if _, err := tx.Exec(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license, captured_at, jcr_import_receipt_id
		)
		SELECT
			$1,
			2025,
			'High Row Category ' || series,
			10,
			'Q1',
			'known',
			'synthetic-jcr-fixture',
			'license-a',
			now(),
			$2
		FROM generate_series(1, $3) AS series
	`, venueID, receiptID, rowCount); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("insert high-row metric snapshots: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipt_metrics (
			import_receipt_id,
			metric_snapshot_id
		)
		SELECT
			$1,
			id
		FROM venue_metric_snapshots
		WHERE jcr_import_receipt_id = $1
	`, receiptID); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("insert high-row receipt associations: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit high-row receipt transaction: %v", err)
	}

	var triggerCalls int
	if err := connection.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_temp.jcr_receipt_trigger_audit
	`).Scan(&triggerCalls); err != nil {
		t.Fatalf("count JCR receipt integrity trigger calls: %v", err)
	}
	if triggerCalls != 1 {
		t.Fatalf(
			"JCR deferred count trigger calls = %d for %d rows, want 1 receipt-level call",
			triggerCalls,
			rowCount,
		)
	}
}

func TestJCRIntegrityFollowupMigrationSerializesWithLegacyWriterLockOrder(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:4]); err != nil {
		t.Fatalf("apply migrations through 000004: %v", err)
	}

	var venueID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('journal', 'Legacy Writer Lock Order Journal')
		RETURNING id
	`), &venueID)

	writerTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin legacy writer transaction: %v", err)
	}
	var receiptID, metricID string
	mustScanID(t, writerTx.QueryRow(ctx, `
		INSERT INTO jcr_import_receipts (
			file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
		) VALUES (
			$1, 'synthetic-jcr-fixture', now(), 1, 1, 0
		)
		RETURNING id
	`, strings.Repeat("2", 64)), &receiptID)
	mustScanID(t, writerTx.QueryRow(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license, captured_at, jcr_import_receipt_id
		) VALUES (
			$1, 2025, 'Legacy Writer Lock Category', 10, 'Q1', 'known',
			'synthetic-jcr-fixture', 'license-a', now(), $2
		)
		RETURNING id
	`, venueID, receiptID), &metricID)

	migrationCtx, cancelMigration := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelMigration()
	migrationResult := make(chan error, 1)
	go func() {
		migrationResult <- UpMigrations(migrationCtx, pool, migrations[4:])
	}()
	waitForBlockedDatabaseSessions(t, pool, 1)

	_, associationErr := writerTx.Exec(ctx, `
		INSERT INTO jcr_import_receipt_metrics (
			import_receipt_id,
			metric_snapshot_id
		) VALUES ($1, $2)
	`, receiptID, metricID)
	if associationErr == nil {
		associationErr = writerTx.Commit(ctx)
	} else {
		_ = writerTx.Rollback(context.Background())
	}
	migrationErr := <-migrationResult
	assertNoPostgresDeadlock(t, "legacy writer", associationErr)
	assertNoPostgresDeadlock(t, "000005 migration", migrationErr)
	if associationErr != nil {
		t.Fatalf("legacy writer receipt→metric→association error = %v", associationErr)
	}
	if migrationErr != nil {
		t.Fatalf("000005 migration after legacy writer error = %v", migrationErr)
	}
}

func TestJCRIntegrityFollowupMigrationBlocksLegacyWriterInForwardLockOrder(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:4]); err != nil {
		t.Fatalf("apply migrations through 000004: %v", err)
	}

	var venueID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('journal', 'Migration First Lock Order Journal')
		RETURNING id
	`), &venueID)

	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire migration barrier connection: %v", err)
	}
	defer blocker.Release()
	blockerTx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration barrier transaction: %v", err)
	}
	if _, err := blockerTx.Exec(ctx, `
		LOCK TABLE venue_policy_assessments IN ACCESS EXCLUSIVE MODE
	`); err != nil {
		_ = blockerTx.Rollback(context.Background())
		t.Fatalf("lock migration tail barrier: %v", err)
	}

	migrationCtx, cancelMigration := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelMigration()
	migrationResult := make(chan error, 1)
	go func() {
		migrationResult <- UpMigrations(migrationCtx, pool, migrations[4:])
	}()
	waitForBlockedDatabaseSessions(t, pool, 1)

	writerCtx, cancelWriter := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelWriter()
	writerResult := make(chan error, 1)
	go func() {
		writerTx, beginErr := pool.Begin(writerCtx)
		if beginErr != nil {
			writerResult <- beginErr
			return
		}
		defer func() {
			_ = writerTx.Rollback(context.Background())
		}()

		var receiptID, metricID string
		if err := writerTx.QueryRow(writerCtx, `
			INSERT INTO jcr_import_receipts (
				file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
			) VALUES (
				$1, 'synthetic-jcr-fixture', now(), 1, 1, 0
			)
			RETURNING id
		`, strings.Repeat("3", 64)).Scan(&receiptID); err != nil {
			writerResult <- err
			return
		}
		if err := writerTx.QueryRow(writerCtx, `
			INSERT INTO venue_metric_snapshots (
				venue_id, metric_year, category, jif, quartile, metric_status,
				source_name, source_license, captured_at, jcr_import_receipt_id
			) VALUES (
				$1, 2025, 'Migration First Lock Category', 10, 'Q1', 'known',
				'synthetic-jcr-fixture', 'license-a', now(), $2
			)
			RETURNING id
		`, venueID, receiptID).Scan(&metricID); err != nil {
			writerResult <- err
			return
		}
		if _, err := writerTx.Exec(writerCtx, `
			INSERT INTO jcr_import_receipt_metrics (
				import_receipt_id,
				metric_snapshot_id
			) VALUES ($1, $2)
		`, receiptID, metricID); err != nil {
			writerResult <- err
			return
		}
		writerResult <- writerTx.Commit(writerCtx)
	}()
	waitForBlockedDatabaseSessions(t, pool, 2)

	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("release migration tail barrier: %v", err)
	}
	migrationErr := <-migrationResult
	writerErr := <-writerResult
	assertNoPostgresDeadlock(t, "000005 migration", migrationErr)
	assertNoPostgresDeadlock(t, "migration-blocked legacy writer", writerErr)
	if migrationErr != nil {
		t.Fatalf("migration-first 000005 error = %v", migrationErr)
	}
	if writerErr != nil {
		t.Fatalf("legacy writer after migration error = %v", writerErr)
	}
}

func TestJCRIntegrityFollowupRejectsInvalidHistoricalPolicyAssessments(t *testing.T) {
	for _, test := range []struct {
		name                  string
		venueType             string
		decision              string
		matchedRules          string
		evidence              string
		dropDecisionSemantics bool
	}{
		{
			name:         "journal not applicable",
			venueType:    "journal",
			decision:     "not_applicable",
			matchedRules: `[]`,
			evidence:     `{"venue_type":"journal","reason":"venue_type_not_journal"}`,
		},
		{
			name:         "conference accepted",
			venueType:    "conference",
			decision:     "accepted",
			matchedRules: `["jcr_q1"]`,
			evidence:     `{"venue_type":"conference"}`,
		},
		{
			name:         "preprint rejected",
			venueType:    "preprint",
			decision:     "rejected",
			matchedRules: `[]`,
			evidence:     `{"venue_type":"preprint"}`,
		},
		{
			name:         "repository unknown",
			venueType:    "repository",
			decision:     "unknown",
			matchedRules: `[]`,
			evidence:     `{"venue_type":"repository","reason":"missing_metric"}`,
		},
		{
			name:         "evidence venue type mismatch",
			venueType:    "journal",
			decision:     "unknown",
			matchedRules: `[]`,
			evidence:     `{"venue_type":"conference","reason":"missing_metric"}`,
		},
		{
			name:         "invalid not applicable reason",
			venueType:    "conference",
			decision:     "not_applicable",
			matchedRules: `[]`,
			evidence:     `{"venue_type":"conference","reason":"journal_policy_not_applicable"}`,
		},
		{
			name:                  "invalid matched rules",
			venueType:             "conference",
			decision:              "not_applicable",
			matchedRules:          `["jcr_q1"]`,
			evidence:              `{"venue_type":"conference","reason":"venue_type_not_journal"}`,
			dropDecisionSemantics: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			migrations, err := EmbeddedMigrations()
			if err != nil {
				t.Fatalf("EmbeddedMigrations() error = %v", err)
			}
			pool := openTestPool(t)
			ctx := testContext(t)
			if err := UpMigrations(ctx, pool, migrations[:3]); err != nil {
				t.Fatalf("apply migrations through 000003: %v", err)
			}
			if test.dropDecisionSemantics {
				if _, err := pool.Exec(ctx, `
					ALTER TABLE venue_policy_assessments
					DROP CONSTRAINT venue_policy_assessments_decision_semantics_check
				`); err != nil {
					t.Fatalf("drop decision semantics for corrupt-history fixture: %v", err)
				}
			}

			var venueID, policyID, assessmentID string
			mustScanID(t, pool.QueryRow(ctx, `
				INSERT INTO venues (venue_type, display_title)
				VALUES ($1, 'Invalid Historical Assessment Venue')
				RETURNING id
			`, test.venueType), &venueID)
			mustScanID(t, pool.QueryRow(ctx, `
				INSERT INTO venue_policy_versions (
					policy_name, version_number, definition, effective_at
				) VALUES (
					'invalid-history-policy', 1,
					'{"accept":["jif_gte_10","jcr_q1"]}', now()
				)
				RETURNING id
			`), &policyID)
			mustScanID(t, pool.QueryRow(ctx, `
				INSERT INTO venue_policy_assessments (
					venue_id, policy_version_id, metric_year, decision,
					matched_rules, evidence, assessed_at
				) VALUES ($1, $2, 2025, $3, $4, $5, now())
				RETURNING id
			`,
				venueID,
				policyID,
				test.decision,
				test.matchedRules,
				test.evidence,
			), &assessmentID)

			err = Up(ctx, pool)
			if err == nil ||
				!strings.Contains(err.Error(), "invalid historical venue_policy_assessment") ||
				!strings.Contains(err.Error(), assessmentID) {
				t.Fatalf("upgrade invalid historical assessment error = %v", err)
			}
		})
	}
}

func TestJCRIntegrityFollowupAcceptsValidHistoricalPolicyAssessments(t *testing.T) {
	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:3]); err != nil {
		t.Fatalf("apply migrations through 000003: %v", err)
	}

	var journalID, conferenceID, policyID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('journal', 'Valid Historical Journal')
		RETURNING id
	`), &journalID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('conference', 'Valid Historical Conference')
		RETURNING id
	`), &conferenceID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO venue_policy_versions (
			policy_name, version_number, definition, effective_at
		) VALUES (
			'valid-history-policy', 1,
			'{"accept":["jif_gte_10","jcr_q1"]}', now()
		)
		RETURNING id
	`), &policyID)
	for _, assessment := range []struct {
		venueID    string
		metricYear int
		decision   string
		matched    string
		evidence   string
	}{
		{
			venueID:    journalID,
			metricYear: 2025,
			decision:   "accepted",
			matched:    `["jif_gte_10"]`,
			evidence:   `{"venue_type":"journal"}`,
		},
		{
			venueID:    journalID,
			metricYear: 2026,
			decision:   "rejected",
			matched:    `[]`,
			evidence:   `{"venue_type":"journal"}`,
		},
		{
			venueID:    journalID,
			metricYear: 2027,
			decision:   "unknown",
			matched:    `[]`,
			evidence:   `{"venue_type":"journal","reason":"missing_metric"}`,
		},
		{
			venueID:    conferenceID,
			metricYear: 2025,
			decision:   "not_applicable",
			matched:    `[]`,
			evidence:   `{"venue_type":"conference","reason":"venue_type_not_journal"}`,
		},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO venue_policy_assessments (
				venue_id, policy_version_id, metric_year, decision,
				matched_rules, evidence, assessed_at
			) VALUES ($1, $2, $3, $4, $5, $6, now())
		`,
			assessment.venueID,
			policyID,
			assessment.metricYear,
			assessment.decision,
			assessment.matched,
			assessment.evidence,
		); err != nil {
			t.Fatalf("insert valid historical %s assessment: %v", assessment.decision, err)
		}
	}

	if err := Up(ctx, pool); err != nil {
		t.Fatalf("upgrade valid historical assessments: %v", err)
	}
}

func TestMigrationSecondRunIsIdempotent(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	migrations, err := EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	var beforeCount int
	var beforeState string
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*),
			jsonb_agg(
				jsonb_build_object(
					'version', version,
					'name', name,
					'checksum', checksum,
					'applied_at', applied_at
				)
				ORDER BY version
			)::text
		FROM schema_migrations
	`).Scan(&beforeCount, &beforeState); err != nil {
		t.Fatalf("query migration state before second run: %v", err)
	}
	if err := Up(ctx, pool); err != nil {
		t.Fatalf("second Up() error = %v", err)
	}
	var afterCount int
	var afterState string
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*),
			jsonb_agg(
				jsonb_build_object(
					'version', version,
					'name', name,
					'checksum', checksum,
					'applied_at', applied_at
				)
				ORDER BY version
			)::text
		FROM schema_migrations
	`).Scan(&afterCount, &afterState); err != nil {
		t.Fatalf("query migration state after second run: %v", err)
	}
	if beforeCount != len(migrations) ||
		afterCount != beforeCount ||
		afterState != beforeState {
		t.Fatalf(
			"migration state changed: before=(%d,%s) after=(%d,%s), want %d immutable records",
			beforeCount,
			beforeState,
			afterCount,
			afterState,
			len(migrations),
		)
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
	if len(migrations) != 18 {
		t.Fatalf("embedded migration count = %d, want 18", len(migrations))
	}

	pool := openTestPool(t)
	ctx := testContext(t)
	if err := UpMigrations(ctx, pool, migrations[:1]); err != nil {
		t.Fatalf("apply original 000001_initial: %v", err)
	}

	var workID, sourceRecordID, paperVersionID, externalIdentifierID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO works (canonical_key, status, title, published_at)
		VALUES (
			'doi:10.1000/Legacy',
			'active',
			'Legacy Work',
			TIMESTAMPTZ '2020-01-01 00:00:00+00'
		)
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

	var appliedCount, publicationEventCount, publicationStateCount int
	var preservedChecksum string
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(checksum) FILTER (WHERE version = 1)
		FROM schema_migrations
	`).Scan(&appliedCount, &preservedChecksum); err != nil {
		t.Fatalf("query upgraded migration records: %v", err)
	}
	if appliedCount != len(migrations) || preservedChecksum != initialMigrationChecksum {
		t.Fatalf(
			"upgraded migrations = count %d, initial checksum %s; want %d, %s",
			appliedCount,
			preservedChecksum,
			len(migrations),
			initialMigrationChecksum,
		)
	}
	if err := pool.QueryRow(ctx, `
		SELECT
			(
				SELECT count(*)
				FROM work_publication_event_assertions
				WHERE work_id = $1
			),
			(
				SELECT count(*)
				FROM work_publication_states
				WHERE work_id = $1
			)
	`, workID).Scan(&publicationEventCount, &publicationStateCount); err != nil {
		t.Fatalf("query upgraded publication event rows: %v", err)
	}
	if publicationEventCount != 0 || publicationStateCount != 0 {
		t.Fatalf(
			"legacy works.published_at backfilled publication meaning: assertions %d, states %d",
			publicationEventCount,
			publicationStateCount,
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

func TestJCRRegistryV2MigrationAddsExactCategoryEvidenceAndPolicy(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	rows, err := pool.Query(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'venue_metric_snapshots'
		  AND column_name IN (
			'registry_version',
			'edition_year',
			'jif_rank',
			'category_journal_count',
			'jif_percentile'
		  )
	`)
	if err != nil {
		t.Fatalf("query JCR Registry v2 columns: %v", err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatalf("scan JCR Registry v2 column: %v", err)
		}
		columns[column] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate JCR Registry v2 columns: %v", err)
	}
	for _, column := range []string{
		"registry_version",
		"edition_year",
		"jif_rank",
		"category_journal_count",
		"jif_percentile",
	} {
		if !columns[column] {
			t.Fatalf("JCR Registry v2 column %q is missing", column)
		}
	}

	var policyCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM venue_policy_versions
		WHERE policy_name = 'journal-all-q1'
		  AND version_number = 2
		  AND definition ->> 'policy_version' = 'journal-all-q1/v2'
		  AND definition -> 'accept' = '["jcr_q1"]'::jsonb
	`).Scan(&policyCount); err != nil {
		t.Fatalf("query journal-all-q1/v2 policy: %v", err)
	}
	if policyCount != 1 {
		t.Fatalf("journal-all-q1/v2 policy rows = %d, want 1", policyCount)
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
		) VALUES (
			$1, $2, 2025, 'unknown', '[]',
			'{"reason":"missing licensed metric","venue_type":"journal"}',
			now()
		)
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

type biomedicalSubjectRegistryFixture struct {
	ReceiptID       string
	VersionID       string
	SubjectIDs      []string
	RuleIDs         []string
	Source          string
	RegistryVersion string
	FileSHA256      string
}

func insertBiomedicalSubjectRegistryFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	suffix string,
	categories []string,
) biomedicalSubjectRegistryFixture {
	t.Helper()
	ctx := testContext(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin biomedical Subject registry fixture: %v", err)
	}
	fixture := biomedicalSubjectRegistryFixture{
		Source:          "clarivate-" + suffix,
		RegistryVersion: "registry-" + suffix,
		FileSHA256:      fmt.Sprintf("%x", sha256.Sum256([]byte("subject-registry-"+suffix))),
		SubjectIDs:      make([]string, 0, len(categories)),
		RuleIDs:         make([]string, 0, len(categories)),
	}
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO subject_import_receipts (
			source, registry_version, file_sha256, subject_count, rule_count, imported_at
		) VALUES ($1, $2, $3, $4, $4, now())
		RETURNING id
	`, fixture.Source, fixture.RegistryVersion, fixture.FileSHA256, len(categories)), &fixture.ReceiptID)
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO subject_versions (subject_import_receipt_id, version_key)
		VALUES ($1, $2)
		RETURNING id
	`, fixture.ReceiptID, "version-"+suffix), &fixture.VersionID)
	for index, category := range categories {
		var subjectID, ruleID string
		mustScanID(t, tx.QueryRow(ctx, `
			INSERT INTO subjects (subject_version_id, slug, display_label)
			VALUES ($1, $2, $3)
			RETURNING id
		`,
			fixture.VersionID,
			fmt.Sprintf("subject-%d", index+1),
			fmt.Sprintf("Subject %d", index+1),
		), &subjectID)
		mustScanID(t, tx.QueryRow(ctx, `
			INSERT INTO biomedical_subject_rules (
				subject_version_id, subject_id, jcr_category
			) VALUES ($1, $2, $3)
			RETURNING id
		`, fixture.VersionID, subjectID, category), &ruleID)
		fixture.SubjectIDs = append(fixture.SubjectIDs, subjectID)
		fixture.RuleIDs = append(fixture.RuleIDs, ruleID)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit biomedical Subject registry fixture: %v", err)
	}
	return fixture
}

func commitBiomedicalSubjectRegistryCounts(
	t *testing.T,
	pool *pgxpool.Pool,
	suffix string,
	includeVersion bool,
	declaredSubjects int,
	declaredRules int,
	actualSubjects int,
	actualRules int,
) error {
	t.Helper()
	ctx := testContext(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin mismatched biomedical Subject registry: %v", err)
	}
	var receiptID string
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO subject_import_receipts (
			source, registry_version, file_sha256, subject_count, rule_count, imported_at
		) VALUES ($1, $2, $3, $4, $5, now())
		RETURNING id
	`,
		"clarivate-"+suffix,
		"registry-"+suffix,
		fmt.Sprintf("%x", sha256.Sum256([]byte("subject-registry-"+suffix))),
		declaredSubjects,
		declaredRules,
	), &receiptID)
	if !includeVersion {
		return tx.Commit(ctx)
	}

	var versionID string
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO subject_versions (subject_import_receipt_id, version_key)
		VALUES ($1, $2)
		RETURNING id
	`, receiptID, "version-"+suffix), &versionID)
	subjectIDs := make([]string, 0, actualSubjects)
	for index := range actualSubjects {
		var subjectID string
		mustScanID(t, tx.QueryRow(ctx, `
			INSERT INTO subjects (subject_version_id, slug, display_label)
			VALUES ($1, $2, $3)
			RETURNING id
		`,
			versionID,
			fmt.Sprintf("subject-%d", index+1),
			fmt.Sprintf("Subject %d", index+1),
		), &subjectID)
		subjectIDs = append(subjectIDs, subjectID)
	}
	for index := range actualRules {
		if len(subjectIDs) == 0 {
			_ = tx.Rollback(context.Background())
			t.Fatal("actual biomedical Subject rules require at least one Subject")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO biomedical_subject_rules (
				subject_version_id, subject_id, jcr_category
			) VALUES ($1, $2, $3)
		`, versionID, subjectIDs[index%len(subjectIDs)], fmt.Sprintf("CATEGORY %d", index+1)); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatalf("insert mismatched biomedical Subject rule: %v", err)
		}
	}
	return tx.Commit(ctx)
}

func assertBiomedicalSubjectValidationConstraints(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := testContext(t)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin invalid Subject slug transaction: %v", err)
	}
	var receiptID, versionID string
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO subject_import_receipts (
			source, registry_version, file_sha256, subject_count, rule_count, imported_at
		) VALUES (
			'clarivate-invalid-slug', 'invalid-slug',
			'1010101010101010101010101010101010101010101010101010101010101010',
			0, 0, now()
		)
		RETURNING id
	`), &receiptID)
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO subject_versions (subject_import_receipt_id, version_key)
		VALUES ($1, 'invalid-slug-version')
		RETURNING id
	`, receiptID), &versionID)
	_, invalidSlugErr := tx.Exec(ctx, `
		INSERT INTO subjects (subject_version_id, slug, display_label)
		VALUES ($1, 'Clinical-Neurology', 'Invalid Slug')
	`, versionID)
	_ = tx.Rollback(context.Background())
	assertPostgresError(t, invalidSlugErr, "23514", "subjects_slug_check")

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin mismatched Subject rule transaction: %v", err)
	}
	insertVersionAndSubject := func(suffix, hash string) (string, string) {
		t.Helper()
		var localReceiptID, localVersionID, subjectID string
		mustScanID(t, tx.QueryRow(ctx, `
			INSERT INTO subject_import_receipts (
				source, registry_version, file_sha256, subject_count, rule_count, imported_at
			) VALUES ($1, $1, $2, 1, 0, now())
			RETURNING id
		`, suffix, hash), &localReceiptID)
		mustScanID(t, tx.QueryRow(ctx, `
			INSERT INTO subject_versions (subject_import_receipt_id, version_key)
			VALUES ($1, $2)
			RETURNING id
		`, localReceiptID, "version-"+suffix), &localVersionID)
		mustScanID(t, tx.QueryRow(ctx, `
			INSERT INTO subjects (subject_version_id, slug, display_label)
			VALUES ($1, $2, $3)
			RETURNING id
		`, localVersionID, "subject-"+suffix, "Subject "+suffix), &subjectID)
		return localVersionID, subjectID
	}
	firstVersionID, _ := insertVersionAndSubject(
		"mismatched-rule-a",
		"2020202020202020202020202020202020202020202020202020202020202020",
	)
	_, secondSubjectID := insertVersionAndSubject(
		"mismatched-rule-b",
		"3030303030303030303030303030303030303030303030303030303030303030",
	)
	_, mismatchedRuleErr := tx.Exec(ctx, `
		INSERT INTO biomedical_subject_rules (
			subject_version_id, subject_id, jcr_category
		) VALUES ($1, $2, 'MISMATCHED CATEGORY')
	`, firstVersionID, secondSubjectID)
	_ = tx.Rollback(context.Background())
	assertPostgresError(
		t,
		mismatchedRuleErr,
		"23503",
		"biomedical_subject_rules_subject_version_fkey",
	)

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin duplicate Subject category transaction: %v", err)
	}
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO subject_import_receipts (
			source, registry_version, file_sha256, subject_count, rule_count, imported_at
		) VALUES (
			'clarivate-duplicate-category', 'duplicate-category',
			'4040404040404040404040404040404040404040404040404040404040404040',
			2, 1, now()
		)
		RETURNING id
	`), &receiptID)
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO subject_versions (subject_import_receipt_id, version_key)
		VALUES ($1, 'duplicate-category-version')
		RETURNING id
	`, receiptID), &versionID)
	var firstSubjectID, secondSubjectIDForDuplicate string
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO subjects (subject_version_id, slug, display_label)
		VALUES ($1, 'first-subject', 'First Subject')
		RETURNING id
	`, versionID), &firstSubjectID)
	mustScanID(t, tx.QueryRow(ctx, `
		INSERT INTO subjects (subject_version_id, slug, display_label)
		VALUES ($1, 'second-subject', 'Second Subject')
		RETURNING id
	`, versionID), &secondSubjectIDForDuplicate)
	if _, err := tx.Exec(ctx, `
		INSERT INTO biomedical_subject_rules (
			subject_version_id, subject_id, jcr_category
		) VALUES ($1, $2, 'DUPLICATE CATEGORY')
	`, versionID, firstSubjectID); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("insert first Subject category rule: %v", err)
	}
	_, duplicateCategoryErr := tx.Exec(ctx, `
		INSERT INTO biomedical_subject_rules (
			subject_version_id, subject_id, jcr_category
		) VALUES ($1, $2, 'DUPLICATE CATEGORY')
	`, versionID, secondSubjectIDForDuplicate)
	_ = tx.Rollback(context.Background())
	assertPostgresError(
		t,
		duplicateCategoryErr,
		"23505",
		"biomedical_subject_rules_version_category_key",
	)
}

type biomedicalProjectionFixture struct {
	DescriptorID               string
	QualifierID                string
	PublicationTypeID          string
	HeadingID                  string
	QualifierAssertionID       string
	PublicationTypeAssertionID string
	ProjectionAssertionID      string
	SourceRecordID             string
	WorkID                     string
}

func insertBiomedicalProjectionAssertion(
	t *testing.T,
	pool *pgxpool.Pool,
	workID string,
	sourceRecordID string,
	suffix string,
) string {
	t.Helper()
	ctx := testContext(t)
	contentHash := fmt.Sprintf("%x", sha256.Sum256([]byte("projection-"+suffix)))
	var jobID, rawEventID, projectionAssertionID string
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO ingestion_jobs (
			source, job_type, idempotency_key, status, payload, max_attempts, batch_key, stage
		) VALUES ('pubmed', 'projection', $1, 'succeeded', '{}', 1, $1, 'project')
		RETURNING id
	`, "biomedical-projection-"+suffix), &jobID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO ingestion_raw_events (
			job_id, logical_source, event_key, event_kind, source_record_id,
			source_time, tie_break_key, position, content_hash, raw_format, raw_payload
		) VALUES (
			$1, 'pubmed', $2, 'upsert', $2, now(), $2, 0, $3, 'xml',
			convert_to('<PubmedArticle/>', 'UTF8')
		)
		RETURNING id
	`, jobID, "biomedical-event-"+suffix, contentHash), &rawEventID)
	var hasNormalizedAssertionID bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'ingestion_projection_assertions'
			  AND column_name = 'normalized_assertion_id'
		)
	`).Scan(&hasNormalizedAssertionID); err != nil {
		t.Fatalf("query normalized assertion schema: %v", err)
	}
	if hasNormalizedAssertionID {
		var normalizedAssertionID string
		mustScanID(t, pool.QueryRow(ctx, `
			INSERT INTO ingestion_normalized_records (
				raw_event_id, source_record_uuid, normalization_policy_version,
				payload_schema_version, normalized_payload
			) VALUES (
				$1, $2, 'biomedical-normalization-v1', 'normalized-record/v2',
				jsonb_build_object('fixture', $3::text)
			)
			RETURNING id
		`, rawEventID, sourceRecordID, suffix), &normalizedAssertionID)
		mustScanID(t, pool.QueryRow(ctx, `
			INSERT INTO ingestion_projection_assertions (
				normalized_assertion_id, raw_event_id, source_record_uuid, work_id, job_id,
				scope_policy_version, projection_policy_version, record_payload
			) VALUES (
				$1, $2, $3, $4, $5,
				'biomedical-scope-v1', 'biomedical-projection-v1',
				jsonb_build_object('fixture', $6::text)
			)
			RETURNING id
		`,
			normalizedAssertionID,
			rawEventID,
			sourceRecordID,
			workID,
			jobID,
			suffix,
		), &projectionAssertionID)
		return projectionAssertionID
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO ingestion_normalized_records (
			raw_event_id, source_record_uuid, normalization_policy_version,
			normalized_payload
		) VALUES (
			$1, $2, 'biomedical-normalization-v1',
			jsonb_build_object('fixture', $3::text)
		)
	`, rawEventID, sourceRecordID, suffix); err != nil {
		t.Fatalf("insert legacy normalized assertion: %v", err)
	}
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO ingestion_projection_assertions (
			raw_event_id, source_record_uuid, work_id, job_id,
			scope_policy_version, projection_policy_version, record_payload
		) VALUES (
			$1, $2, $3, $4, 'biomedical-scope-v1', 'biomedical-projection-v1',
			jsonb_build_object('fixture', $5::text)
		)
		RETURNING id
	`, rawEventID, sourceRecordID, workID, jobID, suffix), &projectionAssertionID)
	return projectionAssertionID
}

func insertBiomedicalProjectionFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	suffix string,
) biomedicalProjectionFixture {
	t.Helper()
	ctx := testContext(t)
	var fixture biomedicalProjectionFixture
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO mesh_descriptors (descriptor_ui) VALUES ('D900001') RETURNING id
	`), &fixture.DescriptorID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO mesh_qualifiers (qualifier_ui) VALUES ('Q900001') RETURNING id
	`), &fixture.QualifierID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO publication_types (publication_type_ui) VALUES ('D900002') RETURNING id
	`), &fixture.PublicationTypeID)
	fixture.WorkID = insertWork(t, pool, "openreview:fixture-"+suffix)
	fixture.SourceRecordID = insertSourceRecord(
		t,
		pool,
		fixture.WorkID,
		"pubmed",
		"source-"+suffix,
		"source-hash-"+suffix,
	)
	fixture.ProjectionAssertionID = insertBiomedicalProjectionAssertion(
		t,
		pool,
		fixture.WorkID,
		fixture.SourceRecordID,
		suffix,
	)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO work_mesh_headings (
			projection_assertion_id, source_record_id, work_id, descriptor_id,
			source_path, descriptor_label, is_major_topic
		) VALUES ($1, $2, $3, $4, '/fixture/DescriptorName', 'Fixture Descriptor', false)
		RETURNING id
	`,
		fixture.ProjectionAssertionID,
		fixture.SourceRecordID,
		fixture.WorkID,
		fixture.DescriptorID,
	), &fixture.HeadingID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO work_mesh_qualifiers (
			projection_assertion_id, source_record_id, work_id, work_mesh_heading_id,
			qualifier_id, source_path, qualifier_label, is_major_topic
		) VALUES (
			$1, $2, $3, $4, $5, '/fixture/QualifierName', 'fixture qualifier', false
		)
		RETURNING id
	`,
		fixture.ProjectionAssertionID,
		fixture.SourceRecordID,
		fixture.WorkID,
		fixture.HeadingID,
		fixture.QualifierID,
	), &fixture.QualifierAssertionID)
	mustScanID(t, pool.QueryRow(ctx, `
		INSERT INTO work_publication_types (
			projection_assertion_id, source_record_id, work_id, publication_type_id,
			source_path, publication_type_label
		) VALUES (
			$1, $2, $3, $4, '/fixture/PublicationType', 'Fixture Publication Type'
		)
		RETURNING id
	`,
		fixture.ProjectionAssertionID,
		fixture.SourceRecordID,
		fixture.WorkID,
		fixture.PublicationTypeID,
	), &fixture.PublicationTypeAssertionID)
	return fixture
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

func assertPostgresColumnError(
	t *testing.T,
	err error,
	code string,
	table string,
	column string,
) {
	t.Helper()
	if err == nil {
		t.Fatalf("PostgreSQL operation succeeded, want SQLSTATE %s on %s.%s", code, table, column)
	}
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) {
		t.Fatalf("error = %T %v, want *pgconn.PgError with SQLSTATE %s", err, err, code)
	}
	if pgError.Code != code || pgError.TableName != table || pgError.ColumnName != column {
		t.Fatalf(
			"PostgreSQL error = SQLSTATE %s, table %q, column %q; want %s, %q, %q: %v",
			pgError.Code,
			pgError.TableName,
			pgError.ColumnName,
			code,
			table,
			column,
			pgError,
		)
	}
}

func assertNoPostgresDeadlock(t *testing.T, operation string, err error) {
	t.Helper()
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "40P01" {
		t.Fatalf("%s deadlocked: %v", operation, err)
	}
}

func waitForBlockedDatabaseSessions(
	t *testing.T,
	pool *pgxpool.Pool,
	want int,
) {
	t.Helper()
	ctx := testContext(t)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock'
		`).Scan(&blocked); err != nil {
			t.Fatalf("query blocked database sessions: %v", err)
		}
		if blocked >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("blocked database sessions = %d, want at least %d", blocked, want)
		}
		time.Sleep(10 * time.Millisecond)
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
