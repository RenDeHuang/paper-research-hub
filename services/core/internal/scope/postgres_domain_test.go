package scope

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
)

const (
	scopeTestPostgresImage    = "postgres:18-alpine"
	scopeTestPostgresUser     = "scope_test"
	scopeTestPostgresPassword = "scope-test-password"
	scopeTestPostgresDatabase = "postgres"
)

var (
	scopeTestPostgresURL string
	scopeTestDatabaseID  atomic.Uint64
)

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(
		ctx,
		scopeTestPostgresImage,
		postgres.WithDatabase(scopeTestPostgresDatabase),
		postgres.WithUsername(scopeTestPostgresUser),
		postgres.WithPassword(scopeTestPostgresPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start required %s Testcontainer: %v\n", scopeTestPostgresImage, err)
		os.Exit(1)
	}
	scopeTestPostgresURL, err = container.ConnectionString(ctx, "sslmode=disable")
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

func TestRegistryDomainImportPersistsReceiptAndExactManyDomainRules(t *testing.T) {
	pool := openScopeTestPool(t)
	importedAt := time.Date(2026, time.July, 18, 4, 0, 0, 0, time.UTC)
	importer, err := NewPostgresDomainImporter(
		pool,
		func() time.Time { return importedAt },
	)
	if err != nil {
		t.Fatalf("NewPostgresDomainImporter() error = %v", err)
	}
	contents := domainRegistryFixture()
	first, err := importer.Import(context.Background(), bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("Import(first) error = %v", err)
	}
	second, err := importer.Import(context.Background(), bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("Import(idempotent) error = %v", err)
	}
	if first != second ||
		first.RegistryName() != "medpaperhub-research-domains" ||
		first.RegistryVersion() != ResearchDomainRegistryVersion ||
		first.DomainCount() != 3 ||
		first.RuleCount() != 5 ||
		!first.ImportedAt().Equal(importedAt) {
		t.Fatalf("domain receipts = first %#v second %#v", first, second)
	}

	ctx := scopeTestContext(t)
	var (
		versionCount, domainCount, ruleCount int
		storedHash                           string
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM domain_versions),
			(SELECT count(*) FROM research_domains),
			(SELECT count(*) FROM domain_category_rules),
			(SELECT file_sha256 FROM domain_versions)
	`).Scan(&versionCount, &domainCount, &ruleCount, &storedHash); err != nil {
		t.Fatalf("query domain Registry receipt chain: %v", err)
	}
	if versionCount != 1 || domainCount != 3 || ruleCount != 5 ||
		storedHash != first.FileSHA256() {
		t.Fatalf(
			"domain Registry counts/hash = %d/%d/%d %q",
			versionCount,
			domainCount,
			ruleCount,
			storedHash,
		)
	}

	store, err := NewPostgresDomainStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresDomainStore() error = %v", err)
	}
	automatic, err := store.DomainsForJCRCategory(
		ctx,
		ResearchDomainRegistryVersion,
		formatTestUUID(9999),
		"Multidisciplinary Sciences",
	)
	if err != nil {
		t.Fatalf("DomainsForJCRCategory(automatic) error = %v", err)
	}
	if len(automatic) != 0 {
		t.Fatalf("automatic multidisciplinary domains = %#v, want none", automatic)
	}
	unknown, err := store.DomainsForJCRCategory(
		ctx,
		ResearchDomainRegistryVersion,
		formatTestUUID(9999),
		"multidisciplinary sciences",
	)
	if err != nil {
		t.Fatalf("DomainsForJCRCategory(normalized) error = %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("normalized category domains = %#v, want none", unknown)
	}

	conflicting := bytes.Replace(
		contents,
		[]byte("Medicine, General & Internal"),
		[]byte("Medicine"),
		1,
	)
	if _, err := importer.Import(
		context.Background(),
		bytes.NewReader(conflicting),
	); !errors.Is(err, ErrConflictingDomainRegistry) {
		t.Fatalf("Import(conflicting version) error = %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE domain_versions
		SET rule_count = rule_count + 1
	`); err == nil {
		t.Fatal("immutable domain version UPDATE error = nil")
	}
	if _, err := pool.Exec(ctx, "DELETE FROM domain_category_rules"); err == nil {
		t.Fatal("immutable domain rule DELETE error = nil")
	}
}

func TestWorkDomainAssertionsBindSpecificWorkForArticleLevelCategories(
	t *testing.T,
) {
	pool := openScopeTestPool(t)
	importer, err := NewPostgresDomainImporter(pool, time.Now)
	if err != nil {
		t.Fatalf("NewPostgresDomainImporter() error = %v", err)
	}
	if _, err := importer.Import(
		context.Background(),
		bytes.NewReader(domainRegistryFixture()),
	); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	firstWork := insertScopeProjectionFixture(t, pool, "domain-first")
	secondWork := insertScopeProjectionFixture(t, pool, "domain-second")
	ctx := scopeTestContext(t)
	var medicineRuleID string
	if err := pool.QueryRow(ctx, `
		SELECT rule.id::text
		FROM domain_category_rules AS rule
		JOIN research_domains AS domain
		  ON domain.id = rule.domain_id
		JOIN domain_versions AS version
		  ON version.id = rule.domain_version_id
		WHERE version.registry_version = $1
		  AND rule.jcr_category = 'Multidisciplinary Sciences'
		  AND domain.domain_key = 'medicine'
	`, ResearchDomainRegistryVersion).Scan(&medicineRuleID); err != nil {
		t.Fatalf("query multidisciplinary medicine rule: %v", err)
	}
	store, err := NewPostgresDomainStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresDomainStore() error = %v", err)
	}
	before, err := store.DomainsForJCRCategory(
		ctx,
		ResearchDomainRegistryVersion,
		firstWork.workID,
		"Multidisciplinary Sciences",
	)
	if err != nil {
		t.Fatalf("DomainsForJCRCategory(before assertion) error = %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("domains before article assertion = %#v, want none", before)
	}
	assertion := WorkDomainAssertion{
		ProjectionAssertionID: firstWork.projectionAssertionID,
		NormalizedAssertionID: firstWork.normalizedAssertionID,
		SourceRecordID:        firstWork.sourceRecordID,
		WorkID:                firstWork.workID,
		DomainRegistryVersion: ResearchDomainRegistryVersion,
		DomainCategoryRuleID:  medicineRuleID,
		Domain:                ResearchDomainMedicine,
		SourcePath:            "$.article.domain",
		AssertedAt:            firstWork.assertedAt,
	}
	assertionID, err := store.PersistWorkDomainAssertion(ctx, assertion)
	if err != nil {
		t.Fatalf("PersistWorkDomainAssertion() error = %v", err)
	}
	if assertionID == "" {
		t.Fatal("PersistWorkDomainAssertion() ID is empty")
	}
	firstDomains, err := store.DomainsForJCRCategory(
		ctx,
		ResearchDomainRegistryVersion,
		firstWork.workID,
		"Multidisciplinary Sciences",
	)
	if err != nil {
		t.Fatalf("DomainsForJCRCategory(first Work) error = %v", err)
	}
	if got, want := firstDomains, []ResearchDomain{
		ResearchDomainMedicine,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first Work domains = %#v, want %#v", got, want)
	}
	secondDomains, err := store.DomainsForJCRCategory(
		ctx,
		ResearchDomainRegistryVersion,
		secondWork.workID,
		"Multidisciplinary Sciences",
	)
	if err != nil {
		t.Fatalf("DomainsForJCRCategory(second Work) error = %v", err)
	}
	if len(secondDomains) != 0 {
		t.Fatalf("second Work domains = %#v, want none", secondDomains)
	}
	automatic, err := store.DomainsForJCRCategory(
		ctx,
		ResearchDomainRegistryVersion,
		secondWork.workID,
		"Medicine, General & Internal",
	)
	if err != nil {
		t.Fatalf("DomainsForJCRCategory(automatic) error = %v", err)
	}
	if got, want := automatic, []ResearchDomain{
		ResearchDomainMedicine,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("automatic domains = %#v, want %#v", got, want)
	}

	forged := assertion
	forged.WorkID = secondWork.workID
	forged.SourcePath = "$.forged"
	if _, err := store.PersistWorkDomainAssertion(ctx, forged); err == nil {
		t.Fatal("PersistWorkDomainAssertion(forged provenance) error = nil")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE work_domain_assertions
		SET source_path = '$.mutated'
		WHERE id = $1
	`, assertionID); err == nil {
		t.Fatal("immutable Work domain assertion UPDATE error = nil")
	}
	if _, err := pool.Exec(
		ctx,
		"DELETE FROM work_domain_assertions WHERE id = $1",
		assertionID,
	); err == nil {
		t.Fatal("immutable Work domain assertion DELETE error = nil")
	}
}

func TestRegistryDomainReceiptSealsAndRejectsPostCommitPollution(t *testing.T) {
	pool := openScopeTestPool(t)
	importedAt := time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC)
	importer, err := NewPostgresDomainImporter(
		pool,
		func() time.Time { return importedAt },
	)
	if err != nil {
		t.Fatalf("NewPostgresDomainImporter() error = %v", err)
	}
	receipt, err := importer.Import(
		context.Background(),
		bytes.NewReader(domainRegistryFixture()),
	)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if !receipt.SealedAt().Equal(importedAt) {
		t.Fatalf("receipt sealed_at = %s, want %s", receipt.SealedAt(), importedAt)
	}
	ctx := scopeTestContext(t)
	var versionID, medicineID string
	var sealedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT
			version.id::text,
			domain.id::text,
			version.sealed_at
		FROM domain_versions AS version
		JOIN research_domains AS domain
		  ON domain.domain_version_id = version.id
		 AND domain.domain_key = 'medicine'
	`).Scan(&versionID, &medicineID, &sealedAt); err != nil {
		t.Fatalf("query sealed domain Registry: %v", err)
	}
	if !sealedAt.Equal(importedAt) {
		t.Fatalf("stored sealed_at = %s, want %s", sealedAt, importedAt)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO research_domains (
			domain_version_id, domain_key, display_label
		) VALUES ($1, 'medicine', 'Medicine')
	`, versionID); err == nil {
		t.Fatal("post-seal research domain INSERT error = nil")
	} else {
		assertScopeConstraint(t, err, "research_domains_parent_unsealed")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO domain_category_rules (
			domain_version_id, domain_id, jcr_category,
			article_level_required
		) VALUES ($1, $2, 'Polluted Category', false)
	`, versionID, medicineID); err == nil {
		t.Fatal("post-seal domain rule INSERT error = nil")
	} else {
		assertScopeConstraint(t, err, "domain_category_rules_parent_unsealed")
	}
	if _, err := pool.Exec(ctx, `
		UPDATE domain_versions
		SET sealed_at = sealed_at
		WHERE id = $1
	`, versionID); err == nil {
		t.Fatal("sealed domain receipt UPDATE error = nil")
	}
}

func TestRegistryDomainReceiptCannotCommitUnsealed(t *testing.T) {
	pool := openScopeTestPool(t)
	ctx := scopeTestContext(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin unsealed domain receipt transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO domain_versions (
			registry_name, registry_version, file_sha256,
			domain_count, rule_count, imported_at
		) VALUES (
			'manual-domain-import',
			'research-domains-jcr-subjects/v2',
			$1, 3, 3, $2
		)
	`, strings.Repeat("d", 64), time.Now().UTC()); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("insert unsealed domain receipt: %v", err)
	}
	err = tx.Commit(ctx)
	if err == nil {
		t.Fatal("commit unsealed domain receipt error = nil")
	}
	assertScopeConstraint(t, err, "domain_versions_must_be_sealed")
}

func TestRegistryDomainReconciliationUsesExactCategoryBytes(t *testing.T) {
	pool := openScopeTestPool(t)
	importer, err := NewPostgresDomainImporter(pool, time.Now)
	if err != nil {
		t.Fatalf("NewPostgresDomainImporter() error = %v", err)
	}
	if _, err := importer.Import(
		context.Background(),
		bytes.NewReader(domainRegistryFixture()),
	); err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	ctx := scopeTestContext(t)
	var venueID, exactMetricID, normalizedMetricID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO venues (
			venue_type, display_title, issn_l
		) VALUES (
			'journal', 'Exact Domain Journal', '9876-5432'
		)
		RETURNING id::text
	`).Scan(&venueID); err != nil {
		t.Fatalf("insert journal Venue: %v", err)
	}
	for category, target := range map[string]*string{
		"Medicine, General & Internal": &exactMetricID,
		"medicine, general & internal": &normalizedMetricID,
	} {
		if err := pool.QueryRow(ctx, `
			INSERT INTO venue_metric_snapshots (
				venue_id, metric_year, category, jif, quartile, metric_status,
				source_name, source_license, registry_version, edition_year,
				jif_rank, category_journal_count, jif_percentile
			) VALUES (
				$1, 2025, $2, 5, 'Q1', 'known',
				'authorized-jcr', 'authorized-license', 'jcr-registry/v2', 2026,
				1, 100, 99
			)
			RETURNING id::text
		`, venueID, category).Scan(target); err != nil {
			t.Fatalf("insert metric %q: %v", category, err)
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reconciliation: %v", err)
	}
	linked, err := ReconcileJournalDomainMetrics(ctx, tx)
	if err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("ReconcileJournalDomainMetrics() error = %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit reconciliation: %v", err)
	}
	if linked != 1 {
		t.Fatalf("reconciled links = %d, want 1 exact link", linked)
	}
	var gotMetricID, category, domain string
	if err := pool.QueryRow(ctx, `
		SELECT
			metric.id::text,
			link.jcr_category,
			domain.domain_key
		FROM journal_domain_metrics AS link
		JOIN venue_metric_snapshots AS metric
		  ON metric.id = link.venue_metric_snapshot_id
		JOIN domain_category_rules AS rule
		  ON rule.id = link.domain_category_rule_id
		JOIN research_domains AS domain
		  ON domain.id = rule.domain_id
	`).Scan(&gotMetricID, &category, &domain); err != nil {
		t.Fatalf("query exact domain link: %v", err)
	}
	if gotMetricID != exactMetricID ||
		gotMetricID == normalizedMetricID ||
		category != "Medicine, General & Internal" ||
		domain != "medicine" {
		t.Fatalf("exact domain link = %q/%q/%q", gotMetricID, category, domain)
	}
}

func domainRegistryFixture() []byte {
	return []byte(
		"registry_name,registry_version,domain,display_label,jcr_category,article_level_required\n" +
			"medpaperhub-research-domains,research-domains-jcr-subjects/v2,medicine,Medicine,\"Medicine, General & Internal\",false\n" +
			"medpaperhub-research-domains,research-domains-jcr-subjects/v2,biology,Biology,Biology,false\n" +
			"medpaperhub-research-domains,research-domains-jcr-subjects/v2,computer_science,Computer Science,\"Computer Science, Artificial Intelligence\",false\n" +
			"medpaperhub-research-domains,research-domains-jcr-subjects/v2,medicine,Medicine,Multidisciplinary Sciences,true\n" +
			"medpaperhub-research-domains,research-domains-jcr-subjects/v2,biology,Biology,Multidisciplinary Sciences,true\n",
	)
}

func assertScopeConstraint(t *testing.T, err error, constraint string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		t.Fatalf("error = %v, want PostgreSQL constraint %q", err, constraint)
	}
	if postgresError.ConstraintName != constraint {
		t.Fatalf(
			"PostgreSQL constraint = %q, want %q: %v",
			postgresError.ConstraintName,
			constraint,
			err,
		)
	}
}

func openScopeTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := newScopeTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open scope test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := database.Up(ctx, pool); err != nil {
		t.Fatalf("migrate scope test database: %v", err)
	}
	return pool
}

func newScopeTestDatabase(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, scopeTestPostgresURL)
	if err != nil {
		t.Fatalf("connect to scope PostgreSQL Testcontainer: %v", err)
	}
	defer conn.Close(context.Background())

	name := fmt.Sprintf("scope_test_%d", scopeTestDatabaseID.Add(1))
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create isolated scope test database: %v", err)
	}
	databaseURL, err := url.Parse(scopeTestPostgresURL)
	if err != nil {
		t.Fatalf("parse scope Testcontainer URL: %v", err)
	}
	databaseURL.Path = "/" + name
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		cleanup, cleanupErr := pgx.Connect(cleanupCtx, scopeTestPostgresURL)
		if cleanupErr != nil {
			t.Errorf("connect for scope database cleanup: %v", cleanupErr)
			return
		}
		defer cleanup.Close(context.Background())
		if _, cleanupErr = cleanup.Exec(
			cleanupCtx,
			"DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)",
		); cleanupErr != nil && !strings.Contains(cleanupErr.Error(), "does not exist") {
			t.Errorf("drop scope test database: %v", cleanupErr)
		}
	})
	return databaseURL.String()
}

func scopeTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}
