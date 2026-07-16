package venue

import (
	"bytes"
	"context"
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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

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

func TestPostgresJCRStoreConcurrentDuplicateFileIsIdempotent(t *testing.T) {
	pool := openMigratedVenueTestPool(t)
	insertSyntheticFixtureVenues(t, pool)
	store, err := NewPostgresJCRStore(pool, PostgresJCRStoreConfig{
		SourceLicense: "synthetic-only",
	})
	if err != nil {
		t.Fatalf("NewPostgresJCRStore() error = %v", err)
	}
	fixture := readCommittedJCRFixture(t)
	start := make(chan struct{})
	results := make(chan ImportResult, 2)
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	for index := range 2 {
		index := index
		go func() {
			defer wait.Done()
			importer, importerErr := NewJCRImporter(
				store,
				store,
				func() time.Time {
					return time.Date(
						2026,
						time.July,
						16,
						11,
						index,
						0,
						0,
						time.UTC,
					)
				},
			)
			if importerErr != nil {
				errorsFound <- importerErr
				return
			}
			<-start
			result, importErr := importer.Import(
				context.Background(),
				bytes.NewReader(fixture),
			)
			if importErr != nil {
				errorsFound <- importErr
				return
			}
			results <- result
		}()
	}
	close(start)
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

	ctx := venueTestContext(t)
	var receipts, metrics, aliases int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipts").Scan(&receipts); err != nil {
		t.Fatalf("count concurrent receipts: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM venue_metric_snapshots").Scan(&metrics); err != nil {
		t.Fatalf("count concurrent metrics: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM venue_aliases").Scan(&aliases); err != nil {
		t.Fatalf("count concurrent aliases: %v", err)
	}
	if receipts != 1 || metrics != 5 || aliases != 3 {
		t.Fatalf(
			"concurrent persistence = receipts %d metrics %d aliases %d, want 1/5/3",
			receipts,
			metrics,
			aliases,
		)
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
		var receipts, metrics int
		ctx := venueTestContext(t)
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM jcr_import_receipts").Scan(&receipts); err != nil {
			t.Fatalf("count identical receipts: %v", err)
		}
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM venue_metric_snapshots").Scan(&metrics); err != nil {
			t.Fatalf("count identical metrics: %v", err)
		}
		if receipts != 2 || metrics != 1 {
			t.Fatalf("identical persistence = receipts %d metrics %d, want 2/1", receipts, metrics)
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
			'{"reason":"journal_policy_not_applicable","venue_type":"preprint"}',
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
