package venue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/biomed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
)

const (
	venueTestPostgresImage    = "postgres:18-alpine"
	venueTestPostgresUser     = "venue_test"
	venueTestPostgresPassword = "venue-test-password"
	venueTestPostgresDatabase = "postgres"
)

var (
	venueTestPostgresURL string
	venueTestDatabaseID  atomic.Uint64
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(
		ctx,
		venueTestPostgresImage,
		postgres.WithDatabase(venueTestPostgresDatabase),
		postgres.WithUsername(venueTestPostgresUser),
		postgres.WithPassword(venueTestPostgresPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start required %s Testcontainer: %v\n", venueTestPostgresImage, err)
		os.Exit(1)
	}

	venueTestPostgresURL, err = container.ConnectionString(ctx, "sslmode=disable")
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

func TestPostgresJCRStoreImportsFixtureWithReceiptTraceability(t *testing.T) {
	pool := openMigratedVenueTestPool(t)
	insertSyntheticFixtureVenues(t, pool)
	const sourceLicense = "user-authorized-synthetic-license"
	store, err := NewPostgresJCRStore(pool, PostgresJCRStoreConfig{
		SourceLicense: sourceLicense,
	})
	if err != nil {
		t.Fatalf("NewPostgresJCRStore() error = %v", err)
	}
	importedAt := time.Date(2026, time.July, 16, 10, 15, 0, 0, time.UTC)
	importer, err := NewJCRImporter(store, store, func() time.Time { return importedAt })
	if err != nil {
		t.Fatalf("NewJCRImporter() error = %v", err)
	}

	fixture := readCommittedJCRFixture(t)
	result, err := importer.Import(context.Background(), bytes.NewReader(fixture))
	if err != nil {
		t.Fatalf("Import(fixture) error = %v", err)
	}
	if result.Source() != "synthetic-jcr-fixture" ||
		result.InputRows() != 5 ||
		result.InsertedRows() != 5 ||
		result.UnchangedRows() != 0 ||
		!result.ImportedAt().Equal(importedAt) {
		t.Fatalf("ImportResult = %#v, want source/counts/timestamp from fixture", result)
	}

	ctx := venueTestContext(t)
	var (
		receiptID                           string
		source                              string
		storedImportedAt                    time.Time
		inputRows, inserted, unchanged      int
		metricCount, linkedMetricCount      int
		zeroJIFCount, aliasCount, linkCount int
		metricReceiptLinkCount              int
	)
	if err := pool.QueryRow(ctx, `
		SELECT id::text, source, imported_at, input_rows, inserted_rows, unchanged_rows
		FROM jcr_import_receipts
		WHERE file_sha256 = $1
	`, result.FileSHA256()).Scan(
		&receiptID,
		&source,
		&storedImportedAt,
		&inputRows,
		&inserted,
		&unchanged,
	); err != nil {
		t.Fatalf("query persisted receipt: %v", err)
	}
	if source != result.Source() ||
		!storedImportedAt.Equal(importedAt) ||
		inputRows != 5 ||
		inserted != 5 ||
		unchanged != 0 {
		t.Fatalf(
			"stored receipt = source %q time %v counts %d/%d/%d",
			source,
			storedImportedAt,
			inputRows,
			inserted,
			unchanged,
		)
	}
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*),
			count(*) FILTER (WHERE jcr_import_receipt_id = $1),
			count(*) FILTER (WHERE jif = 0),
			count(*) FILTER (WHERE source_license = $2)
		FROM venue_metric_snapshots
	`, receiptID, sourceLicense).Scan(
		&metricCount,
		&linkedMetricCount,
		&zeroJIFCount,
		&inserted,
	); err != nil {
		t.Fatalf("query persisted metric provenance: %v", err)
	}
	if metricCount != 5 || linkedMetricCount != 5 || zeroJIFCount != 1 || inserted != 5 {
		t.Fatalf(
			"persisted metrics = total %d linked %d zero %d licensed %d, want 5/5/1/5",
			metricCount,
			linkedMetricCount,
			zeroJIFCount,
			inserted,
		)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM venue_aliases").Scan(&aliasCount); err != nil {
		t.Fatalf("count persisted venue aliases: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM jcr_import_receipt_aliases
		WHERE import_receipt_id = $1
	`, receiptID).Scan(&linkCount); err != nil {
		t.Fatalf("count receipt alias links: %v", err)
	}
	if aliasCount != 3 || linkCount != 3 {
		t.Fatalf("persisted aliases/links = %d/%d, want 3/3", aliasCount, linkCount)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM jcr_import_receipt_metrics
		WHERE import_receipt_id = $1
	`, receiptID).Scan(&metricReceiptLinkCount); err != nil {
		t.Fatalf("count receipt metric links: %v", err)
	}
	if metricReceiptLinkCount != 5 {
		t.Fatalf("receipt metric links = %d, want 5", metricReceiptLinkCount)
	}

	foundReceipt, found, err := store.FindImport(ctx, result.FileSHA256())
	if err != nil || !found {
		t.Fatalf("FindImport() = %#v, %v, %v; want persisted receipt", foundReceipt, found, err)
	}
	if foundReceipt.Source() != result.Source() || !foundReceipt.ImportedAt().Equal(importedAt) {
		t.Fatalf("FindImport() receipt = %#v, want original source and timestamp", foundReceipt)
	}

	zeroKey, err := NewMetricKey(
		fixtureVenueSubthreshold,
		2025,
		"Zero Impact Synthetic Studies",
	)
	if err != nil {
		t.Fatalf("NewMetricKey() error = %v", err)
	}
	zeroMetric, found, err := store.FindMetric(ctx, zeroKey)
	if err != nil || !found {
		t.Fatalf("FindMetric(zero) = %#v, %v, %v", zeroMetric, found, err)
	}
	if !zeroMetric.HasJIF() || zeroMetric.JIF().String() != "0" {
		t.Fatalf("FindMetric(zero).JIF() = %q, want exact 0", zeroMetric.JIF().String())
	}

	identifiers, err := NewISSNSet(
		mustParseISSN(t, ISSNRoleLinking, "0000-0078"),
		mustParseISSN(t, ISSNRolePrint, "0000-0086"),
		mustParseISSN(t, ISSNRoleElectronic, "0000-0094"),
	)
	if err != nil {
		t.Fatalf("NewISSNSet() error = %v", err)
	}
	matches, err := store.FindVenuesByISSNs(ctx, identifiers)
	if err != nil {
		t.Fatalf("FindVenuesByISSNs() error = %v", err)
	}
	if len(matches) != 1 || matches[0].ID() != fixtureVenueSubthreshold {
		t.Fatalf("FindVenuesByISSNs() = %#v, want exact subthreshold venue", matches)
	}
}

func TestPostgresSubjectImporterPersistsVersionedReceiptAndIsIdempotent(t *testing.T) {
	pool := openMigratedVenueTestPool(t)
	importedAt := time.Date(2026, time.July, 17, 3, 4, 5, 0, time.UTC)
	importer, err := biomed.NewPostgresSubjectImporter(
		pool,
		func() time.Time { return importedAt },
	)
	if err != nil {
		t.Fatalf("NewPostgresSubjectImporter() error = %v", err)
	}
	contents := subjectRegistryCSV(
		"biomedical-jcr-subjects/v1",
		[]subjectRegistryTestRow{
			{slug: "oncology", label: "Oncology", category: "Oncology"},
			{
				slug:     "genetics-heredity",
				label:    "Genetics & Heredity",
				category: "Genetics & Heredity",
			},
		},
	)
	receipt, err := importer.Import(context.Background(), strings.NewReader(contents))
	if err != nil {
		t.Fatalf("Import(Subject registry) error = %v", err)
	}
	digest := sha256.Sum256([]byte(contents))
	if receipt.Source() != "medpaperhub-reviewed-jcr-category-allowlist" ||
		receipt.RegistryVersion() != "biomedical-jcr-subjects/v1" ||
		receipt.FileSHA256() != fmt.Sprintf("%x", digest[:]) ||
		receipt.SubjectCount() != 2 ||
		receipt.RuleCount() != 2 ||
		!receipt.ImportedAt().Equal(importedAt) {
		t.Fatalf("Subject receipt = %#v", receipt)
	}

	ctx := venueTestContext(t)
	var (
		receiptID                                              string
		versionID                                              string
		source, registryVersion, fileSHA, versionKey           string
		storedImportedAt                                       time.Time
		subjectCount, ruleCount, joinedSubjects, joinedRules   int
		categoryProofs, subjectVersionProofs, receiptPathCount int
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			id::text,
			source,
			registry_version,
			file_sha256,
			subject_count,
			rule_count,
			imported_at
		FROM subject_import_receipts
	`).Scan(
		&receiptID,
		&source,
		&registryVersion,
		&fileSHA,
		&subjectCount,
		&ruleCount,
		&storedImportedAt,
	); err != nil {
		t.Fatalf("query Subject receipt: %v", err)
	}
	if source != receipt.Source() ||
		registryVersion != receipt.RegistryVersion() ||
		fileSHA != receipt.FileSHA256() ||
		subjectCount != 2 ||
		ruleCount != 2 ||
		!storedImportedAt.Equal(importedAt) {
		t.Fatalf(
			"stored Subject receipt = %q/%q/%q counts %d/%d at %v",
			source,
			registryVersion,
			fileSHA,
			subjectCount,
			ruleCount,
			storedImportedAt,
		)
	}
	if err := pool.QueryRow(ctx, `
		SELECT id::text, version_key
		FROM subject_versions
		WHERE subject_import_receipt_id = $1
	`, receiptID).Scan(&versionID, &versionKey); err != nil {
		t.Fatalf("query Subject version: %v", err)
	}
	if versionKey != receipt.RegistryVersion() {
		t.Fatalf("Subject version_key = %q, want %q", versionKey, receipt.RegistryVersion())
	}
	if err := pool.QueryRow(ctx, `
		SELECT
			count(DISTINCT subject.id),
			count(rule.id),
			count(*) FILTER (
				WHERE subject.subject_version_id = version.id
				  AND rule.subject_version_id = version.id
			),
			count(*) FILTER (
				WHERE rule.subject_id = subject.id
				  AND rule.subject_version_id = subject.subject_version_id
			),
			count(*) FILTER (
				WHERE receipt.id = version.subject_import_receipt_id
			)
		FROM subject_import_receipts AS receipt
		JOIN subject_versions AS version
		  ON version.subject_import_receipt_id = receipt.id
		JOIN subjects AS subject
		  ON subject.subject_version_id = version.id
		JOIN biomedical_subject_rules AS rule
		  ON rule.subject_id = subject.id
		 AND rule.subject_version_id = version.id
		WHERE receipt.id = $1
	`, receiptID).Scan(
		&joinedSubjects,
		&joinedRules,
		&subjectVersionProofs,
		&categoryProofs,
		&receiptPathCount,
	); err != nil {
		t.Fatalf("query Subject receipt/version/rule path: %v", err)
	}
	if joinedSubjects != 2 ||
		joinedRules != 2 ||
		subjectVersionProofs != 2 ||
		categoryProofs != 2 ||
		receiptPathCount != 2 {
		t.Fatalf(
			"Subject receipt path counts = subjects %d rules %d version %d subject %d receipt %d",
			joinedSubjects,
			joinedRules,
			subjectVersionProofs,
			categoryProofs,
			receiptPathCount,
		)
	}

	replayed, err := importer.Import(context.Background(), strings.NewReader(contents))
	if err != nil {
		t.Fatalf("Import(idempotent Subject registry) error = %v", err)
	}
	if replayed.FileSHA256() != receipt.FileSHA256() ||
		!replayed.ImportedAt().Equal(receipt.ImportedAt()) {
		t.Fatalf("idempotent Subject receipt = %#v, want original %#v", replayed, receipt)
	}
	var receipts, versions, subjects, rules int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM subject_import_receipts),
			(SELECT count(*) FROM subject_versions),
			(SELECT count(*) FROM subjects),
			(SELECT count(*) FROM biomedical_subject_rules)
	`).Scan(&receipts, &versions, &subjects, &rules); err != nil {
		t.Fatalf("count idempotent Subject rows: %v", err)
	}
	if receipts != 1 || versions != 1 || subjects != 2 || rules != 2 {
		t.Fatalf(
			"idempotent Subject rows = receipts %d versions %d subjects %d rules %d",
			receipts,
			versions,
			subjects,
			rules,
		)
	}
}

func TestPostgresSubjectImporterRejectsSameSourceVersionDifferentHashAtomically(t *testing.T) {
	pool := openMigratedVenueTestPool(t)
	importer, err := biomed.NewPostgresSubjectImporter(
		pool,
		func() time.Time {
			return time.Date(2026, time.July, 17, 4, 0, 0, 0, time.UTC)
		},
	)
	if err != nil {
		t.Fatalf("NewPostgresSubjectImporter() error = %v", err)
	}
	first := subjectRegistryCSV(
		"biomedical-jcr-subjects/v1",
		[]subjectRegistryTestRow{
			{slug: "oncology", label: "Oncology", category: "Oncology"},
		},
	)
	if _, err := importer.Import(context.Background(), strings.NewReader(first)); err != nil {
		t.Fatalf("Import(first Subject registry) error = %v", err)
	}
	conflicting := subjectRegistryCSV(
		"biomedical-jcr-subjects/v1",
		[]subjectRegistryTestRow{
			{slug: "oncology", label: "Cancer Biology", category: "Oncology"},
		},
	)
	_, err = importer.Import(context.Background(), strings.NewReader(conflicting))
	if !errors.Is(err, biomed.ErrConflictingSubjectRegistry) {
		t.Fatalf(
			"Import(conflicting Subject registry) error = %v, want ErrConflictingSubjectRegistry",
			err,
		)
	}

	ctx := venueTestContext(t)
	var receipts, versions, subjects, rules, conflictingLabels int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM subject_import_receipts),
			(SELECT count(*) FROM subject_versions),
			(SELECT count(*) FROM subjects),
			(SELECT count(*) FROM biomedical_subject_rules),
			(SELECT count(*) FROM subjects WHERE display_label = 'Cancer Biology')
	`).Scan(&receipts, &versions, &subjects, &rules, &conflictingLabels); err != nil {
		t.Fatalf("count conflicting Subject import rows: %v", err)
	}
	if receipts != 1 ||
		versions != 1 ||
		subjects != 1 ||
		rules != 1 ||
		conflictingLabels != 0 {
		t.Fatalf(
			"conflicting Subject rows = receipts %d versions %d subjects %d rules %d conflicting labels %d",
			receipts,
			versions,
			subjects,
			rules,
			conflictingLabels,
		)
	}
}

func TestPostgresJCRStoreReconcilesSubjectsInEitherImportOrder(t *testing.T) {
	importOrders := []struct {
		name         string
		subjectFirst bool
	}{
		{name: "Subject then JCR", subjectFirst: true},
		{name: "JCR then Subject", subjectFirst: false},
	}
	var normalizedResults [][]string
	for _, importOrder := range importOrders {
		t.Run(importOrder.name, func(t *testing.T) {
			pool := openMigratedVenueTestPool(t)
			venueID := insertSingleStoreVenue(t, pool)
			subjectImporter, err := biomed.NewPostgresSubjectImporter(
				pool,
				func() time.Time {
					return time.Date(2026, time.July, 17, 5, 0, 0, 0, time.UTC)
				},
			)
			if err != nil {
				t.Fatalf("NewPostgresSubjectImporter() error = %v", err)
			}
			subjects := subjectRegistryCSV(
				"biomedical-jcr-subjects/v1",
				[]subjectRegistryTestRow{
					{slug: "oncology", label: "Oncology", category: "Oncology"},
				},
			)
			store := mustNewPostgresJCRStore(t, pool)
			metric := mustMetricSnapshot(
				t,
				venueID,
				2025,
				"Oncology",
				"12.5",
				QuartileQ1,
				MetricStatusKnown,
				"synthetic-jcr-fixture",
			)
			importSubjects := func() {
				t.Helper()
				if _, err := subjectImporter.Import(
					context.Background(),
					strings.NewReader(subjects),
				); err != nil {
					t.Fatalf("Import(Subjects) error = %v", err)
				}
			}
			importJCR := func() {
				t.Helper()
				if _, err := store.PersistJCRImport(
					context.Background(),
					mustJCRImport(
						t,
						strings.Repeat("7", 64),
						2026,
						[]MetricSnapshot{metric},
						nil,
					),
				); err != nil {
					t.Fatalf("PersistJCRImport() error = %v", err)
				}
			}
			if importOrder.subjectFirst {
				importSubjects()
				importJCR()
			} else {
				importJCR()
				importSubjects()
			}

			rows, err := pool.Query(venueTestContext(t), `
				SELECT
					metric.category,
					subject.slug,
					rule.jcr_category,
					link.jcr_category
				FROM journal_subject_metrics AS link
				JOIN venue_metric_snapshots AS metric
				  ON metric.id = link.venue_metric_snapshot_id
				JOIN biomedical_subject_rules AS rule
				  ON rule.id = link.subject_rule_id
				JOIN subjects AS subject
				  ON subject.id = rule.subject_id
				ORDER BY metric.category, subject.slug
			`)
			if err != nil {
				t.Fatalf("query reconciled Subject links: %v", err)
			}
			defer rows.Close()
			var result []string
			for rows.Next() {
				var metricCategory, slug, ruleCategory, linkCategory string
				if err := rows.Scan(
					&metricCategory,
					&slug,
					&ruleCategory,
					&linkCategory,
				); err != nil {
					t.Fatalf("scan reconciled Subject link: %v", err)
				}
				if metricCategory != ruleCategory || metricCategory != linkCategory {
					t.Fatalf(
						"reconciled categories = metric %q rule %q link %q",
						metricCategory,
						ruleCategory,
						linkCategory,
					)
				}
				result = append(result, metricCategory+"|"+slug)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate reconciled Subject links: %v", err)
			}
			if len(result) != 1 || result[0] != "Oncology|oncology" {
				t.Fatalf("reconciled Subject links = %#v", result)
			}
			normalizedResults = append(normalizedResults, result)
		})
	}
	if len(normalizedResults) != 2 ||
		fmt.Sprint(normalizedResults[0]) != fmt.Sprint(normalizedResults[1]) {
		t.Fatalf("import orders produced different Subject links: %#v", normalizedResults)
	}
}

func TestReconcileJournalSubjectMetricsRejectsCaseSpacePrefixAndSubstringMatches(t *testing.T) {
	pool := openMigratedVenueTestPool(t)
	subjectImporter, err := biomed.NewPostgresSubjectImporter(
		pool,
		func() time.Time {
			return time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC)
		},
	)
	if err != nil {
		t.Fatalf("NewPostgresSubjectImporter() error = %v", err)
	}
	if _, err := subjectImporter.Import(
		context.Background(),
		strings.NewReader(subjectRegistryCSV(
			"biomedical-jcr-subjects/v1",
			[]subjectRegistryTestRow{
				{slug: "oncology", label: "Oncology", category: "Oncology"},
			},
		)),
	); err != nil {
		t.Fatalf("Import(Subjects) error = %v", err)
	}

	ctx := venueTestContext(t)
	categories := []string{
		"Oncology",
		"oncology",
		" Oncology",
		"Oncology ",
		"Onco",
		"Oncology Research",
		"Clinical Oncology",
	}
	for index, category := range categories {
		var venueID string
		if err := pool.QueryRow(ctx, `
			INSERT INTO venues (venue_type, display_title)
			VALUES ('journal', $1)
			RETURNING id::text
		`, fmt.Sprintf("Exact Category Venue %d", index)).Scan(&venueID); err != nil {
			t.Fatalf("insert exact-category Venue %d: %v", index, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO venue_metric_snapshots (
				venue_id,
				metric_year,
				category,
				jif,
				quartile,
				metric_status,
				source_name,
				source_license,
				captured_at
			) VALUES (
				$1,
				2025,
				$2,
				10,
				'Q1',
				'known',
				'synthetic-exact-match-test',
				'synthetic-only',
				$3
			)
		`, venueID, category, time.Date(2026, time.July, 17, 6, 1, 0, 0, time.UTC)); err != nil {
			t.Fatalf("insert metric category %q: %v", category, err)
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin exact Subject reconciliation: %v", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if _, err := biomed.ReconcileJournalSubjectMetrics(ctx, tx); err != nil {
		t.Fatalf("ReconcileJournalSubjectMetrics() error = %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit exact Subject reconciliation: %v", err)
	}

	var linkedCategories []string
	rows, err := pool.Query(ctx, `
		SELECT metric.category
		FROM journal_subject_metrics AS link
		JOIN venue_metric_snapshots AS metric
		  ON metric.id = link.venue_metric_snapshot_id
		ORDER BY metric.category
	`)
	if err != nil {
		t.Fatalf("query exact Subject categories: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var category string
		if err := rows.Scan(&category); err != nil {
			t.Fatalf("scan exact Subject category: %v", err)
		}
		linkedCategories = append(linkedCategories, category)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate exact Subject categories: %v", err)
	}
	if len(linkedCategories) != 1 || linkedCategories[0] != "Oncology" {
		t.Fatalf(
			"reconciled categories = %#v, want only exact %q",
			linkedCategories,
			"Oncology",
		)
	}
}

func TestPostgresJCRStoreConcurrentDuplicateFileIsIdempotent(t *testing.T) {
	pool := openMigratedVenueTestPool(t)
	venueID := insertSingleStoreVenue(t, pool)
	store := mustNewPostgresJCRStore(t, pool)
	metric := mustMetricSnapshot(
		t,
		venueID,
		2025,
		"AI",
		"10",
		QuartileQ1,
		MetricStatusKnown,
		"synthetic-jcr-fixture",
	)
	batch := mustJCRImport(
		t,
		strings.Repeat("a", 64),
		2026,
		[]MetricSnapshot{metric},
		nil,
	)
	lockKey, err := postgresJCRImportAdvisoryKey(batch.FileSHA256())
	if err != nil {
		t.Fatalf("postgresJCRImportAdvisoryKey() error = %v", err)
	}
	ctx := venueTestContext(t)
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire same-hash lock blocker: %v", err)
	}
	defer blocker.Release()
	if _, err := blocker.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		t.Fatalf("acquire same-hash advisory blocker: %v", err)
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_, _ = blocker.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockKey)
		}
	}()

	results := make(chan ImportReceipt, 2)
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	for range 2 {
		go func() {
			defer wait.Done()
			result, persistErr := store.PersistJCRImport(context.Background(), batch)
			if persistErr != nil {
				errorsFound <- persistErr
				return
			}
			results <- result
		}()
	}
	waitForBlockedPostgresQueries(t, pool, "pg_advisory_xact_lock", 2)
	if _, err := blocker.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockKey); err != nil {
		t.Fatalf("release same-hash advisory blocker: %v", err)
	}
	lockHeld = false
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Fatalf("concurrent Import() error = %v", err)
	}

	var first *ImportResult
	for result := range results {
		result := result
		if first == nil {
			first = &result
			continue
		}
		if result.FileSHA256() != first.FileSHA256() ||
			result.Source() != first.Source() ||
			!result.ImportedAt().Equal(first.ImportedAt()) ||
			result.InputRows() != first.InputRows() ||
			result.InsertedRows() != first.InsertedRows() ||
			result.UnchangedRows() != first.UnchangedRows() {
			t.Fatalf("concurrent results differ: first=%#v second=%#v", *first, result)
		}
	}

	var receipts, metrics, metricLinks int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipts").Scan(&receipts); err != nil {
		t.Fatalf("count concurrent receipts: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM venue_metric_snapshots").Scan(&metrics); err != nil {
		t.Fatalf("count concurrent metrics: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipt_metrics").Scan(&metricLinks); err != nil {
		t.Fatalf("count concurrent receipt metric links: %v", err)
	}
	if receipts != 1 || metrics != 1 || metricLinks != 1 {
		t.Fatalf(
			"concurrent persistence = receipts %d metrics %d links %d, want 1/1/1",
			receipts,
			metrics,
			metricLinks,
		)
	}
}

func TestPostgresJCRStoreConcurrentDifferentHashesCompeteOnMetricKey(t *testing.T) {
	t.Run("identical metric creates two receipt associations", func(t *testing.T) {
		pool := openMigratedVenueTestPool(t)
		venueID := insertSingleStoreVenue(t, pool)
		store := mustNewPostgresJCRStore(t, pool)
		metric := mustMetricSnapshot(
			t,
			venueID,
			2025,
			"AI",
			"10",
			QuartileQ1,
			MetricStatusKnown,
			"synthetic-jcr-fixture",
		)
		batches := []JCRImport{
			mustJCRImport(t, strings.Repeat("b", 64), 2026, []MetricSnapshot{metric}, nil),
			mustJCRImport(t, strings.Repeat("c", 64), 2027, []MetricSnapshot{metric}, nil),
		}

		results, errorsFound := persistBatchesBlockedOnVenue(t, pool, venueID, store, batches)
		for _, err := range errorsFound {
			if err != nil {
				t.Fatalf("concurrent identical PersistJCRImport() error = %v", err)
			}
		}
		if len(results) != 2 {
			t.Fatalf("concurrent identical receipts = %d, want 2", len(results))
		}
		var inserted, unchanged int
		for _, receipt := range results {
			inserted += receipt.InsertedRows()
			unchanged += receipt.UnchangedRows()
		}
		if inserted != 1 || unchanged != 1 {
			t.Fatalf(
				"concurrent identical counts = inserted %d unchanged %d, want 1/1",
				inserted,
				unchanged,
			)
		}

		ctx := venueTestContext(t)
		var receipts, metrics, links int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipts").Scan(&receipts); err != nil {
			t.Fatalf("count different-hash receipts: %v", err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM venue_metric_snapshots").Scan(&metrics); err != nil {
			t.Fatalf("count different-hash metrics: %v", err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipt_metrics").Scan(&links); err != nil {
			t.Fatalf("count different-hash metric links: %v", err)
		}
		if receipts != 2 || metrics != 1 || links != 2 {
			t.Fatalf(
				"different-hash identical persistence = receipts %d metrics %d links %d, want 2/1/2",
				receipts,
				metrics,
				links,
			)
		}
	})

	t.Run("different metric returns domain conflict and rolls back loser", func(t *testing.T) {
		pool := openMigratedVenueTestPool(t)
		venueID := insertSingleStoreVenue(t, pool)
		store := mustNewPostgresJCRStore(t, pool)
		firstMetric := mustMetricSnapshot(
			t,
			venueID,
			2025,
			"AI",
			"9.999",
			QuartileQ2,
			MetricStatusKnown,
			"synthetic-jcr-fixture",
		)
		secondMetric := mustMetricSnapshot(
			t,
			venueID,
			2025,
			"AI",
			"10",
			QuartileQ1,
			MetricStatusKnown,
			"synthetic-jcr-fixture",
		)
		batches := []JCRImport{
			mustJCRImport(t, strings.Repeat("d", 64), 2026, []MetricSnapshot{firstMetric}, nil),
			mustJCRImport(t, strings.Repeat("e", 64), 2027, []MetricSnapshot{secondMetric}, nil),
		}

		results, errorsFound := persistBatchesBlockedOnVenue(t, pool, venueID, store, batches)
		if len(results) != 1 || len(errorsFound) != 2 {
			t.Fatalf(
				"concurrent conflict results/errors = %d/%d, want 1/2",
				len(results),
				len(errorsFound),
			)
		}
		var nilErrors, conflicts int
		for _, err := range errorsFound {
			switch {
			case err == nil:
				nilErrors++
			case errors.Is(err, ErrConflictingMetric):
				conflicts++
			default:
				t.Fatalf("concurrent conflict error = %v, want ErrConflictingMetric", err)
			}
		}
		if nilErrors != 1 || conflicts != 1 {
			t.Fatalf("concurrent errors = nil %d conflict %d, want 1/1", nilErrors, conflicts)
		}

		ctx := venueTestContext(t)
		var receipts, metrics, links int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipts").Scan(&receipts); err != nil {
			t.Fatalf("count conflict receipts: %v", err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM venue_metric_snapshots").Scan(&metrics); err != nil {
			t.Fatalf("count conflict metrics: %v", err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipt_metrics").Scan(&links); err != nil {
			t.Fatalf("count conflict metric links: %v", err)
		}
		if receipts != 1 || metrics != 1 || links != 1 {
			t.Fatalf(
				"different-hash conflict persistence = receipts %d metrics %d links %d, want 1/1/1",
				receipts,
				metrics,
				links,
			)
		}
	})
}

func TestPostgresJCRStoreRejectsIdenticalMetricWithDifferentSourceLicense(t *testing.T) {
	pool := openMigratedVenueTestPool(t)
	venueID := insertSingleStoreVenue(t, pool)
	firstStore, err := NewPostgresJCRStore(pool, PostgresJCRStoreConfig{
		SourceLicense: "license-a",
	})
	if err != nil {
		t.Fatalf("NewPostgresJCRStore(first) error = %v", err)
	}
	secondStore, err := NewPostgresJCRStore(pool, PostgresJCRStoreConfig{
		SourceLicense: "license-b",
	})
	if err != nil {
		t.Fatalf("NewPostgresJCRStore(second) error = %v", err)
	}
	metric := mustMetricSnapshot(
		t,
		venueID,
		2025,
		"AI",
		"10",
		QuartileQ1,
		MetricStatusKnown,
		"synthetic-jcr-fixture",
	)
	first := mustJCRImport(t, strings.Repeat("f", 64), 2026, []MetricSnapshot{metric}, nil)
	second := mustJCRImport(t, first.FileSHA256(), 2027, []MetricSnapshot{metric}, nil)
	if _, err := firstStore.PersistJCRImport(context.Background(), first); err != nil {
		t.Fatalf("PersistJCRImport(first license) error = %v", err)
	}
	if _, err := secondStore.PersistJCRImport(context.Background(), second); !errors.Is(err, ErrConflictingMetric) {
		t.Fatalf("PersistJCRImport(second license) error = %v, want ErrConflictingMetric", err)
	}

	ctx := venueTestContext(t)
	var receipts, metrics, links int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipts").Scan(&receipts); err != nil {
		t.Fatalf("count license receipts: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM venue_metric_snapshots").Scan(&metrics); err != nil {
		t.Fatalf("count license metrics: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipt_metrics").Scan(&links); err != nil {
		t.Fatalf("count license links: %v", err)
	}
	if receipts != 1 || metrics != 1 || links != 1 {
		t.Fatalf(
			"license conflict persistence = receipts %d metrics %d links %d, want 1/1/1",
			receipts,
			metrics,
			links,
		)
	}
}

func TestPreparePostgresAliasesSortsAdvisoryLocksAndDeduplicatesAliases(t *testing.T) {
	const source = "synthetic-jcr-fixture"
	alpha := mustVenueAliasEvidence(t, "venue-a", "Alias Alpha", source)
	beta := mustVenueAliasEvidence(t, "venue-a", "Alias Beta", source)

	plans, err := preparePostgresAliases(
		[]VenueAliasEvidence{beta, alpha, beta},
		source,
	)
	if err != nil {
		t.Fatalf("preparePostgresAliases() error = %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("prepared alias plans = %d, want 2 unique aliases", len(plans))
	}
	if plans[0].lockKey >= plans[1].lockKey {
		t.Fatalf(
			"alias advisory lock order = %d then %d, want ascending lock keys",
			plans[0].lockKey,
			plans[1].lockKey,
		)
	}
	if plans[0].evidence.Alias().Value() != "Alias Alpha" ||
		plans[1].evidence.Alias().Value() != "Alias Beta" {
		t.Fatalf(
			"alias order = %q, %q; want Alias Alpha, Alias Beta",
			plans[0].evidence.Alias().Value(),
			plans[1].evidence.Alias().Value(),
		)
	}
}

func TestPostgresJCRStoreRejectsSameHashDifferentLicenseWithLegacyMissingAssociation(t *testing.T) {
	pool := openVenueTestPoolThroughMigration(t, 4)
	venueID := insertSingleStoreVenue(t, pool)
	ctx := venueTestContext(t)
	metric := mustMetricSnapshot(
		t,
		venueID,
		2025,
		"Legacy Missing Association",
		"10",
		QuartileQ1,
		MetricStatusKnown,
		"synthetic-jcr-fixture",
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id, metric_year, category, jif, quartile, metric_status,
			source_name, source_license, captured_at
		) VALUES (
			$1, $2, $3, 10, 'Q1', 'known',
			'synthetic-jcr-fixture', 'license-a', '2025-01-01T00:00:00Z'
		)
	`, venueID, metric.MetricYear(), metric.Category()); err != nil {
		t.Fatalf("insert legacy metric without receipt association: %v", err)
	}
	const fileSHA = "abababababababababababababababababababababababababababababababab"
	if _, err := pool.Exec(ctx, `
		INSERT INTO jcr_import_receipts (
			file_sha256, source, imported_at, input_rows, inserted_rows, unchanged_rows
		) VALUES (
			$1, 'synthetic-jcr-fixture', '2026-01-01T00:00:00Z', 1, 0, 1
		)
	`, fileSHA); err != nil {
		t.Fatalf("insert legacy unchanged-only receipt: %v", err)
	}
	var associations int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipt_metrics").Scan(&associations); err != nil {
		t.Fatalf("count legacy missing associations: %v", err)
	}
	if associations != 0 {
		t.Fatalf("legacy associations before Store call = %d, want 0", associations)
	}

	store, err := NewPostgresJCRStore(pool, PostgresJCRStoreConfig{
		SourceLicense: "license-b",
	})
	if err != nil {
		t.Fatalf("NewPostgresJCRStore() error = %v", err)
	}
	batch := mustJCRImport(t, fileSHA, 2026, []MetricSnapshot{metric}, nil)
	if _, err := store.PersistJCRImport(context.Background(), batch); !errors.Is(err, ErrConflictingMetric) {
		t.Fatalf(
			"PersistJCRImport(legacy missing association) error = %v, want ErrConflictingMetric",
			err,
		)
	}
}

func TestPostgresJCRStoreConcurrentReverseAliasOrderAvoidsDeadlock(t *testing.T) {
	pool := openMigratedVenueTestPool(t)
	venueID := insertSingleStoreVenue(t, pool)
	store := mustNewPostgresJCRStore(t, pool)
	ctx := venueTestContext(t)

	const (
		aliasAlphaBarrier int64 = 0x4a43524101
		aliasBetaBarrier  int64 = 0x4a43524102
	)
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION block_reverse_alias_inserts()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			IF NEW.alias = 'Concurrent Alias Alpha' THEN
				PERFORM pg_advisory_xact_lock(%d);
			ELSIF NEW.alias = 'Concurrent Alias Beta' THEN
				PERFORM pg_advisory_xact_lock(%d);
			END IF;
			RETURN NEW;
		END;
		$$;

		CREATE TRIGGER block_reverse_alias_inserts
		AFTER INSERT ON venue_aliases
		FOR EACH ROW
		EXECUTE FUNCTION block_reverse_alias_inserts();
	`, aliasAlphaBarrier, aliasBetaBarrier)); err != nil {
		t.Fatalf("install reverse alias barrier trigger: %v", err)
	}

	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire reverse alias barrier connection: %v", err)
	}
	defer blocker.Release()
	for _, key := range []int64{aliasAlphaBarrier, aliasBetaBarrier} {
		if _, err := blocker.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
			t.Fatalf("acquire reverse alias barrier %d: %v", key, err)
		}
	}
	barriersHeld := true
	defer func() {
		if !barriersHeld {
			return
		}
		for _, key := range []int64{aliasAlphaBarrier, aliasBetaBarrier} {
			_, _ = blocker.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", key)
		}
	}()

	alphaAlias := mustVenueAliasEvidence(
		t,
		venueID,
		"Concurrent Alias Alpha",
		"synthetic-jcr-fixture",
	)
	betaAlias := mustVenueAliasEvidence(
		t,
		venueID,
		"Concurrent Alias Beta",
		"synthetic-jcr-fixture",
	)
	alphaMetric := mustMetricSnapshot(
		t,
		venueID,
		2025,
		"Concurrent Alias Alpha Metric",
		"10",
		QuartileQ1,
		MetricStatusKnown,
		"synthetic-jcr-fixture",
	)
	betaMetric := mustMetricSnapshot(
		t,
		venueID,
		2025,
		"Concurrent Alias Beta Metric",
		"10",
		QuartileQ1,
		MetricStatusKnown,
		"synthetic-jcr-fixture",
	)
	batches := []JCRImport{
		mustJCRImport(
			t,
			strings.Repeat("7", 64),
			2026,
			[]MetricSnapshot{alphaMetric},
			[]VenueAliasEvidence{alphaAlias, betaAlias},
		),
		mustJCRImport(
			t,
			strings.Repeat("8", 64),
			2027,
			[]MetricSnapshot{betaMetric},
			[]VenueAliasEvidence{betaAlias, alphaAlias},
		),
	}

	importCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	errorsFound := make(chan error, len(batches))
	var wait sync.WaitGroup
	wait.Add(len(batches))
	for _, batch := range batches {
		batch := batch
		go func() {
			defer wait.Done()
			_, persistErr := store.PersistJCRImport(importCtx, batch)
			errorsFound <- persistErr
		}()
	}
	waitForBlockedPostgresSessions(t, pool, 2)
	for _, key := range []int64{aliasAlphaBarrier, aliasBetaBarrier} {
		if _, err := blocker.Exec(ctx, "SELECT pg_advisory_unlock($1)", key); err != nil {
			t.Fatalf("release reverse alias barrier %d: %v", key, err)
		}
	}
	barriersHeld = false
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			var postgresError *pgconn.PgError
			if errors.As(err, &postgresError) && postgresError.Code == "40P01" {
				t.Fatalf("reverse alias persistence deadlocked: %v", err)
			}
			t.Fatalf("reverse alias persistence error = %v", err)
		}
	}

	var receipts, aliases, aliasLinks, metricLinks int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipts").Scan(&receipts); err != nil {
		t.Fatalf("count reverse alias receipts: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM venue_aliases
		WHERE alias IN ('Concurrent Alias Alpha', 'Concurrent Alias Beta')
	`).Scan(&aliases); err != nil {
		t.Fatalf("count reverse aliases: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipt_aliases").Scan(&aliasLinks); err != nil {
		t.Fatalf("count reverse alias receipt links: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipt_metrics").Scan(&metricLinks); err != nil {
		t.Fatalf("count reverse alias metric links: %v", err)
	}
	if receipts != 2 || aliases != 2 || aliasLinks != 4 || metricLinks != 2 {
		t.Fatalf(
			"reverse alias persistence = receipts %d aliases %d alias links %d metric links %d, want 2/2/4/2",
			receipts,
			aliases,
			aliasLinks,
			metricLinks,
		)
	}
}

func TestPostgresJCRStoreMapsMetricUniqueViolationToDomainConflict(t *testing.T) {
	pool := openMigratedVenueTestPool(t)
	venueID := insertSingleStoreVenue(t, pool)
	store := mustNewPostgresJCRStore(t, pool)
	ctx := venueTestContext(t)
	if _, err := pool.Exec(ctx, `
		CREATE UNIQUE INDEX venue_metric_snapshots_casefold_test_key
		ON venue_metric_snapshots (venue_id, metric_year, lower(category))
	`); err != nil {
		t.Fatalf("create competing metric unique index: %v", err)
	}
	firstMetric := mustMetricSnapshot(
		t,
		venueID,
		2025,
		"AI",
		"10",
		QuartileQ1,
		MetricStatusKnown,
		"synthetic-jcr-fixture",
	)
	if _, err := store.PersistJCRImport(
		context.Background(),
		mustJCRImport(t, strings.Repeat("1", 64), 2026, []MetricSnapshot{firstMetric}, nil),
	); err != nil {
		t.Fatalf("PersistJCRImport(first metric) error = %v", err)
	}
	casefoldConflict := mustMetricSnapshot(
		t,
		venueID,
		2025,
		"ai",
		"10",
		QuartileQ1,
		MetricStatusKnown,
		"synthetic-jcr-fixture",
	)
	conflictSHA := strings.Repeat("2", 64)
	_, err := store.PersistJCRImport(
		context.Background(),
		mustJCRImport(t, conflictSHA, 2027, []MetricSnapshot{casefoldConflict}, nil),
	)
	if !errors.Is(err, ErrConflictingMetric) {
		t.Fatalf("PersistJCRImport(unique violation) error = %v, want ErrConflictingMetric", err)
	}
	var conflictingReceipts int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM jcr_import_receipts WHERE file_sha256 = $1
	`, conflictSHA).Scan(&conflictingReceipts); err != nil {
		t.Fatalf("count unique-violation receipt: %v", err)
	}
	if conflictingReceipts != 0 {
		t.Fatalf("unique-violation receipts = %d, want rollback to 0", conflictingReceipts)
	}
}

func TestPostgresJCRStoreClassifiesIdenticalRowsAndRollsBackConflicts(t *testing.T) {
	t.Run("identical row from different file is unchanged", func(t *testing.T) {
		pool := openMigratedVenueTestPool(t)
		venueID := insertSingleStoreVenue(t, pool)
		store := mustNewPostgresJCRStore(t, pool)
		metric := mustMetricSnapshot(
			t,
			venueID,
			2025,
			"AI",
			"9.999",
			QuartileQ2,
			MetricStatusKnown,
			"synthetic-jcr-fixture",
		)
		first := mustJCRImport(
			t,
			strings.Repeat("1", 64),
			2026,
			[]MetricSnapshot{metric},
			nil,
		)
		if _, err := store.PersistJCRImport(context.Background(), first); err != nil {
			t.Fatalf("PersistJCRImport(first) error = %v", err)
		}
		second := mustJCRImport(
			t,
			strings.Repeat("2", 64),
			2027,
			[]MetricSnapshot{metric},
			nil,
		)
		receipt, err := store.PersistJCRImport(context.Background(), second)
		if err != nil {
			t.Fatalf("PersistJCRImport(identical) error = %v", err)
		}
		if receipt.InsertedRows() != 0 || receipt.UnchangedRows() != 1 {
			t.Fatalf(
				"identical receipt counts = inserted %d unchanged %d, want 0/1",
				receipt.InsertedRows(),
				receipt.UnchangedRows(),
			)
		}
		var receipts, metrics, links int
		ctx := venueTestContext(t)
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipts").Scan(&receipts); err != nil {
			t.Fatalf("count identical receipts: %v", err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM venue_metric_snapshots").Scan(&metrics); err != nil {
			t.Fatalf("count identical metrics: %v", err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipt_metrics").Scan(&links); err != nil {
			t.Fatalf("count identical metric receipt links: %v", err)
		}
		if receipts != 2 || metrics != 1 || links != 2 {
			t.Fatalf(
				"identical persistence = receipts %d metrics %d links %d, want 2/1/2",
				receipts,
				metrics,
				links,
			)
		}
	})

	t.Run("conflict rolls back receipt metric and alias", func(t *testing.T) {
		pool := openMigratedVenueTestPool(t)
		venueID := insertSingleStoreVenue(t, pool)
		store := mustNewPostgresJCRStore(t, pool)
		existing := mustMetricSnapshot(
			t,
			venueID,
			2025,
			"AI",
			"9.999",
			QuartileQ2,
			MetricStatusKnown,
			"synthetic-jcr-fixture",
		)
		first := mustJCRImport(
			t,
			strings.Repeat("3", 64),
			2026,
			[]MetricSnapshot{existing},
			nil,
		)
		if _, err := store.PersistJCRImport(context.Background(), first); err != nil {
			t.Fatalf("PersistJCRImport(first) error = %v", err)
		}

		newMetric := mustMetricSnapshot(
			t,
			venueID,
			2025,
			"Robotics",
			"8",
			QuartileQ2,
			MetricStatusKnown,
			"synthetic-jcr-fixture",
		)
		conflicting := mustMetricSnapshot(
			t,
			venueID,
			2025,
			"AI",
			"10",
			QuartileQ1,
			MetricStatusKnown,
			"synthetic-jcr-fixture",
		)
		alias, err := NewAlias("Must Roll Back Alias", "synthetic-jcr-fixture")
		if err != nil {
			t.Fatalf("NewAlias() error = %v", err)
		}
		aliasEvidence, err := NewVenueAliasEvidence(venueID, alias)
		if err != nil {
			t.Fatalf("NewVenueAliasEvidence() error = %v", err)
		}
		conflictSHA := strings.Repeat("4", 64)
		batch := mustJCRImport(
			t,
			conflictSHA,
			2027,
			[]MetricSnapshot{newMetric, conflicting},
			[]VenueAliasEvidence{aliasEvidence},
		)
		_, err = store.PersistJCRImport(context.Background(), batch)
		if !errors.Is(err, ErrConflictingMetric) {
			t.Fatalf("PersistJCRImport(conflict) error = %v, want ErrConflictingMetric", err)
		}

		ctx := venueTestContext(t)
		var conflictingReceipts, newMetrics, rolledBackAliases, totalMetrics int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM jcr_import_receipts WHERE file_sha256 = $1
		`, conflictSHA).Scan(&conflictingReceipts); err != nil {
			t.Fatalf("count conflicting receipts: %v", err)
		}
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM venue_metric_snapshots
			WHERE venue_id = $1 AND metric_year = 2025 AND category = 'Robotics'
		`, venueID).Scan(&newMetrics); err != nil {
			t.Fatalf("count rolled-back metrics: %v", err)
		}
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM venue_aliases
			WHERE venue_id = $1 AND alias = 'Must Roll Back Alias'
		`, venueID).Scan(&rolledBackAliases); err != nil {
			t.Fatalf("count rolled-back aliases: %v", err)
		}
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM venue_metric_snapshots WHERE venue_id = $1
		`, venueID).Scan(&totalMetrics); err != nil {
			t.Fatalf("count retained metrics: %v", err)
		}
		if conflictingReceipts != 0 ||
			newMetrics != 0 ||
			rolledBackAliases != 0 ||
			totalMetrics != 1 {
			t.Fatalf(
				"conflict rollback = receipts %d new metrics %d aliases %d total metrics %d",
				conflictingReceipts,
				newMetrics,
				rolledBackAliases,
				totalMetrics,
			)
		}
	})
}

func TestPostgresSchemaPersistsNotApplicableAssessment(t *testing.T) {
	pool := openMigratedVenueTestPool(t)
	ctx := venueTestContext(t)
	var venueID, policyID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO venues (venue_type, display_title)
		VALUES ('preprint', 'Synthetic Preprint Server')
		RETURNING id
	`).Scan(&venueID); err != nil {
		t.Fatalf("insert preprint venue: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO venue_policy_versions (policy_name, version_number, definition, effective_at)
		VALUES ('curated-journal', 1, '{"accept":["jif_gte_10","jcr_q1"]}', now())
		RETURNING id
	`).Scan(&policyID); err != nil {
		t.Fatalf("insert policy version: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO venue_policy_assessments (
			venue_id, policy_version_id, metric_year, decision,
			matched_rules, evidence, assessed_at
		) VALUES (
			$1, $2, 2025, 'not_applicable', '[]',
			'{"reason":"venue_type_not_journal","venue_type":"preprint"}',
			now()
		)
	`, venueID, policyID); err != nil {
		t.Fatalf("persist not_applicable assessment: %v", err)
	}
}

const (
	fixtureVenueAlpha        = "00000000-0000-0000-0000-000000000101"
	fixtureVenueUnknown      = "00000000-0000-0000-0000-000000000102"
	fixtureVenueSubthreshold = "00000000-0000-0000-0000-000000000103"
)

func openMigratedVenueTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := newVenueTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open venue PostgreSQL test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping venue PostgreSQL test pool: %v", err)
	}
	if err := database.Up(ctx, pool); err != nil {
		t.Fatalf("migrate venue PostgreSQL test database: %v", err)
	}
	return pool
}

func openVenueTestPoolThroughMigration(t *testing.T, migrationCount int) *pgxpool.Pool {
	t.Helper()
	databaseURL := newVenueTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open partial venue PostgreSQL test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping partial venue PostgreSQL test pool: %v", err)
	}
	migrations, err := database.EmbeddedMigrations()
	if err != nil {
		t.Fatalf("EmbeddedMigrations() error = %v", err)
	}
	if migrationCount < 1 || migrationCount > len(migrations) {
		t.Fatalf("migrationCount = %d, available migrations = %d", migrationCount, len(migrations))
	}
	if err := database.UpMigrations(ctx, pool, migrations[:migrationCount]); err != nil {
		t.Fatalf("migrate venue PostgreSQL test database through %d: %v", migrationCount, err)
	}
	return pool
}

func newVenueTestDatabase(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, venueTestPostgresURL)
	if err != nil {
		t.Fatalf("connect to venue PostgreSQL Testcontainer: %v", err)
	}
	defer connection.Close(context.Background())

	name := fmt.Sprintf("venue_test_%d", venueTestDatabaseID.Add(1))
	if _, err := connection.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create isolated venue test database: %v", err)
	}
	databaseURL, err := url.Parse(venueTestPostgresURL)
	if err != nil {
		t.Fatalf("parse venue Testcontainer URL: %v", err)
	}
	databaseURL.Path = "/" + name

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		cleanupConnection, cleanupErr := pgx.Connect(cleanupCtx, venueTestPostgresURL)
		if cleanupErr != nil {
			t.Errorf("connect for venue database cleanup: %v", cleanupErr)
			return
		}
		defer cleanupConnection.Close(context.Background())
		if _, cleanupErr = cleanupConnection.Exec(
			cleanupCtx,
			"DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)",
		); cleanupErr != nil && !strings.Contains(cleanupErr.Error(), "does not exist") {
			t.Errorf("drop isolated venue test database: %v", cleanupErr)
		}
	})
	return databaseURL.String()
}

func venueTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func waitForBlockedPostgresQueries(
	t *testing.T,
	pool *pgxpool.Pool,
	queryFragment string,
	want int,
) {
	t.Helper()
	ctx := venueTestContext(t)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
			  AND wait_event_type = 'Lock'
			  AND query LIKE '%' || $1 || '%'
		`, queryFragment).Scan(&blocked); err != nil {
			t.Fatalf("query blocked PostgreSQL sessions: %v", err)
		}
		if blocked >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"blocked PostgreSQL queries containing %q = %d, want at least %d",
				queryFragment,
				blocked,
				want,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForBlockedPostgresSessions(
	t *testing.T,
	pool *pgxpool.Pool,
	want int,
) {
	t.Helper()
	ctx := venueTestContext(t)
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
			t.Fatalf("query blocked PostgreSQL sessions: %v", err)
		}
		if blocked >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("blocked PostgreSQL sessions = %d, want at least %d", blocked, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func persistBatchesBlockedOnVenue(
	t *testing.T,
	pool *pgxpool.Pool,
	venueID string,
	store *PostgresJCRStore,
	batches []JCRImport,
) ([]ImportReceipt, []error) {
	t.Helper()
	ctx := venueTestContext(t)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Venue lock blocker: %v", err)
	}
	defer func() {
		_ = blocker.Rollback(context.Background())
	}()
	var lockedVenue string
	if err := blocker.QueryRow(ctx, `
		SELECT id::text FROM venues WHERE id = $1 FOR UPDATE
	`, venueID).Scan(&lockedVenue); err != nil {
		t.Fatalf("lock Venue row: %v", err)
	}

	results := make(chan ImportReceipt, len(batches))
	errorsFound := make(chan error, len(batches))
	var wait sync.WaitGroup
	wait.Add(len(batches))
	for _, batch := range batches {
		batch := batch
		go func() {
			defer wait.Done()
			receipt, persistErr := store.PersistJCRImport(context.Background(), batch)
			if persistErr == nil {
				results <- receipt
			}
			errorsFound <- persistErr
		}()
	}
	waitForBlockedPostgresQueries(t, pool, "INSERT INTO venue_metric_snapshots", len(batches))
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release Venue row blocker: %v", err)
	}
	wait.Wait()
	close(results)
	close(errorsFound)

	collectedResults := make([]ImportReceipt, 0, len(results))
	for result := range results {
		collectedResults = append(collectedResults, result)
	}
	collectedErrors := make([]error, 0, len(errorsFound))
	for err := range errorsFound {
		collectedErrors = append(collectedErrors, err)
	}
	return collectedResults, collectedErrors
}

func insertSyntheticFixtureVenues(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := venueTestContext(t)
	for _, query := range []string{
		`INSERT INTO venues (id, venue_type, display_title, issn_l, issn, eissn)
		 VALUES ('00000000-0000-0000-0000-000000000101', 'journal',
		         'Registry Alpha', '0000-0019', '0000-0027', '0000-0035')`,
		`INSERT INTO venues (id, venue_type, display_title, issn_l, issn, eissn)
		 VALUES ('00000000-0000-0000-0000-000000000102', 'journal',
		         'Registry Unknown', '0000-0043', '0000-0051', '0000-006X')`,
		`INSERT INTO venues (id, venue_type, display_title, issn_l, issn, eissn)
		 VALUES ('00000000-0000-0000-0000-000000000103', 'journal',
		         'Registry Subthreshold', '0000-0078', '0000-0086', '0000-0094')`,
	} {
		if _, err := pool.Exec(ctx, query); err != nil {
			t.Fatalf("insert synthetic fixture Venue: %v", err)
		}
	}
}

func insertSingleStoreVenue(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	const venueID = "00000000-0000-0000-0000-000000000201"
	if _, err := pool.Exec(venueTestContext(t), `
		INSERT INTO venues (id, venue_type, display_title, issn_l, issn, eissn)
		VALUES ($1, 'journal', 'Single Store Venue', '0000-0108', '0000-0116', '0000-0124')
	`, venueID); err != nil {
		t.Fatalf("insert single store Venue: %v", err)
	}
	return venueID
}

func readCommittedJCRFixture(t *testing.T) []byte {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve postgres_store_test.go path")
	}
	path := filepath.Join(
		filepath.Dir(currentFile),
		"..",
		"..",
		"..",
		"..",
		"data",
		"venues",
		"jcr-q1.example.csv",
	)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed JCR fixture: %v", err)
	}
	return contents
}

func mustNewPostgresJCRStore(t *testing.T, pool *pgxpool.Pool) *PostgresJCRStore {
	t.Helper()
	store, err := NewPostgresJCRStore(pool, PostgresJCRStoreConfig{
		SourceLicense: "synthetic-only",
	})
	if err != nil {
		t.Fatalf("NewPostgresJCRStore() error = %v", err)
	}
	return store
}

func mustJCRImport(
	t *testing.T,
	fileSHA string,
	year int,
	rows []MetricSnapshot,
	aliases []VenueAliasEvidence,
) JCRImport {
	t.Helper()
	batch, err := newJCRImport(
		fileSHA,
		"synthetic-jcr-fixture",
		time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC),
		len(rows),
		0,
		rows,
		aliases,
	)
	if err != nil {
		t.Fatalf("newJCRImport() error = %v", err)
	}
	return batch
}

func mustVenueAliasEvidence(
	t *testing.T,
	venueID string,
	value string,
	source string,
) VenueAliasEvidence {
	t.Helper()
	alias, err := NewAlias(value, source)
	if err != nil {
		t.Fatalf("NewAlias() error = %v", err)
	}
	evidence, err := NewVenueAliasEvidence(venueID, alias)
	if err != nil {
		t.Fatalf("NewVenueAliasEvidence() error = %v", err)
	}
	return evidence
}

type subjectRegistryTestRow struct {
	slug     string
	label    string
	category string
}

func subjectRegistryCSV(
	version string,
	rows []subjectRegistryTestRow,
) string {
	var builder strings.Builder
	builder.WriteString("source,registry_version,slug,display_label,jcr_category\n")
	for _, row := range rows {
		fmt.Fprintf(
			&builder,
			"medpaperhub-reviewed-jcr-category-allowlist,%s,%s,%s,%s\n",
			version,
			row.slug,
			row.label,
			row.category,
		)
	}
	return builder.String()
}
