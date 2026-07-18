package scope

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	domainImportAdvisoryNamespace int32 = 0x444f4d49
	domainReconcileNamespace      int32 = 0x444f4d52
	domainReconcileLockKey        int32 = 0x4a435232
)

var (
	ErrConflictingDomainRegistry = errors.New(
		"conflicting research domain Registry import",
	)
)

type DomainImportReceipt struct {
	registryName    string
	registryVersion string
	fileSHA256      string
	domainCount     int
	ruleCount       int
	importedAt      time.Time
	sealedAt        time.Time
}

func (receipt DomainImportReceipt) RegistryName() string {
	return receipt.registryName
}

func (receipt DomainImportReceipt) RegistryVersion() string {
	return receipt.registryVersion
}

func (receipt DomainImportReceipt) FileSHA256() string {
	return receipt.fileSHA256
}

func (receipt DomainImportReceipt) DomainCount() int {
	return receipt.domainCount
}

func (receipt DomainImportReceipt) RuleCount() int {
	return receipt.ruleCount
}

func (receipt DomainImportReceipt) ImportedAt() time.Time {
	return receipt.importedAt
}

func (receipt DomainImportReceipt) SealedAt() time.Time {
	return receipt.sealedAt
}

type PostgresDomainImporter struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

func NewPostgresDomainImporter(
	pool *pgxpool.Pool,
	clock func() time.Time,
) (*PostgresDomainImporter, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	if clock == nil {
		return nil, errors.New("domain Registry import clock is required")
	}
	return &PostgresDomainImporter{pool: pool, clock: clock}, nil
}

func (importer *PostgresDomainImporter) Import(
	ctx context.Context,
	source io.Reader,
) (DomainImportReceipt, error) {
	if ctx == nil {
		return DomainImportReceipt{}, errors.New(
			"domain Registry import context is required",
		)
	}
	if err := ctx.Err(); err != nil {
		return DomainImportReceipt{}, err
	}
	if importer == nil || importer.pool == nil || importer.clock == nil {
		return DomainImportReceipt{}, errors.New(
			"PostgresDomainImporter is not initialized",
		)
	}
	registry, err := ParseDomainRegistry(source)
	if err != nil {
		return DomainImportReceipt{}, err
	}
	importedAt := importer.clock()
	if importedAt.IsZero() {
		return DomainImportReceipt{}, errors.New(
			"domain Registry import clock returned a zero timestamp",
		)
	}
	receipt := DomainImportReceipt{
		registryName:    registry.Name(),
		registryVersion: registry.Version(),
		fileSHA256:      registry.FileSHA256(),
		domainCount:     registry.DomainCount(),
		ruleCount:       registry.RuleCount(),
		importedAt:      importedAt.UTC(),
		sealedAt:        importedAt.UTC(),
	}

	tx, err := importer.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return DomainImportReceipt{}, fmt.Errorf(
			"begin domain Registry import transaction: %w",
			err,
		)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1::integer, $2::integer)",
		domainImportAdvisoryNamespace,
		registryImportLockKey(registry.Name(), registry.Version()),
	); err != nil {
		return DomainImportReceipt{}, fmt.Errorf(
			"acquire domain Registry import lock: %w",
			err,
		)
	}
	existing, found, err := findDomainImportReceipt(
		ctx,
		tx,
		registry.Version(),
	)
	if err != nil {
		return DomainImportReceipt{}, err
	}
	if found {
		if existing.RegistryName() != receipt.RegistryName() ||
			existing.FileSHA256() != receipt.FileSHA256() ||
			existing.DomainCount() != receipt.DomainCount() ||
			existing.RuleCount() != receipt.RuleCount() {
			return DomainImportReceipt{}, fmt.Errorf(
				"%w: version %q already has SHA-256 %s",
				ErrConflictingDomainRegistry,
				registry.Version(),
				existing.FileSHA256(),
			)
		}
		if _, err := ReconcileJournalDomainMetrics(ctx, tx); err != nil {
			return DomainImportReceipt{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return DomainImportReceipt{}, fmt.Errorf(
				"commit idempotent domain Registry import: %w",
				err,
			)
		}
		return existing, nil
	}

	var versionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO domain_versions (
			registry_name,
			registry_version,
			file_sha256,
			domain_count,
			rule_count,
			imported_at
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text
	`,
		receipt.RegistryName(),
		receipt.RegistryVersion(),
		receipt.FileSHA256(),
		receipt.DomainCount(),
		receipt.RuleCount(),
		receipt.ImportedAt(),
	).Scan(&versionID); err != nil {
		return DomainImportReceipt{}, mapDomainImportError(
			"insert domain Registry receipt",
			err,
		)
	}

	labels := make(map[ResearchDomain]string)
	for _, rule := range registry.Rules() {
		labels[rule.Domain()] = rule.DisplayLabel()
	}
	domainIDs := make(map[ResearchDomain]string, len(ResearchDomains()))
	for _, domain := range ResearchDomains() {
		var domainID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO research_domains (
				domain_version_id,
				domain_key,
				display_label
			) VALUES ($1, $2, $3)
			RETURNING id::text
		`, versionID, domain, labels[domain]).Scan(&domainID); err != nil {
			return DomainImportReceipt{}, mapDomainImportError(
				fmt.Sprintf("insert research domain %q", domain),
				err,
			)
		}
		domainIDs[domain] = domainID
	}
	for _, rule := range registry.Rules() {
		if _, err := tx.Exec(ctx, `
			INSERT INTO domain_category_rules (
				domain_version_id,
				domain_id,
				jcr_category,
				article_level_required
			) VALUES ($1, $2, $3, $4)
		`,
			versionID,
			domainIDs[rule.Domain()],
			rule.JCRCategory(),
			rule.ArticleLevelRequired(),
		); err != nil {
			return DomainImportReceipt{}, mapDomainImportError(
				fmt.Sprintf(
					"insert exact domain Category rule %q/%q",
					rule.Domain(),
					rule.JCRCategory(),
				),
				err,
			)
		}
	}
	if _, err := ReconcileJournalDomainMetrics(ctx, tx); err != nil {
		return DomainImportReceipt{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE domain_versions
		SET sealed_at = $2
		WHERE id = $1
		  AND sealed_at IS NULL
	`, versionID, receipt.SealedAt())
	if err != nil {
		return DomainImportReceipt{}, mapDomainImportError(
			"seal domain Registry receipt",
			err,
		)
	}
	if tag.RowsAffected() != 1 {
		return DomainImportReceipt{}, errors.New(
			"seal domain Registry receipt: expected one unsealed receipt",
		)
	}
	if err := tx.Commit(ctx); err != nil {
		return DomainImportReceipt{}, mapDomainImportError(
			"commit domain Registry import",
			err,
		)
	}
	return receipt, nil
}

type domainReceiptQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func findDomainImportReceipt(
	ctx context.Context,
	querier domainReceiptQuerier,
	version string,
) (DomainImportReceipt, bool, error) {
	var receipt DomainImportReceipt
	var sealedAt *time.Time
	err := querier.QueryRow(ctx, `
		SELECT
			registry_name,
			registry_version,
			file_sha256,
			domain_count,
			rule_count,
			imported_at,
			sealed_at
		FROM domain_versions
		WHERE registry_version = $1
	`, version).Scan(
		&receipt.registryName,
		&receipt.registryVersion,
		&receipt.fileSHA256,
		&receipt.domainCount,
		&receipt.ruleCount,
		&receipt.importedAt,
		&sealedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DomainImportReceipt{}, false, nil
	}
	if err != nil {
		return DomainImportReceipt{}, false, fmt.Errorf(
			"find domain Registry receipt: %w",
			err,
		)
	}
	if sealedAt == nil {
		return DomainImportReceipt{}, false, fmt.Errorf(
			"find domain Registry receipt: version %q is not sealed",
			version,
		)
	}
	receipt.importedAt = receipt.importedAt.UTC()
	receipt.sealedAt = sealedAt.UTC()
	return receipt, true, nil
}

func mapDomainImportError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) &&
		(postgresError.Code == "23505" ||
			postgresError.Code == "23514" ||
			postgresError.Code == "55000") {
		return fmt.Errorf(
			"%s: %w: %w",
			operation,
			ErrConflictingDomainRegistry,
			err,
		)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func registryImportLockKey(name string, version string) int32 {
	digest := sha256.Sum256([]byte(name + "\x00" + version))
	return int32(binary.BigEndian.Uint32(digest[:4]))
}

func ReconcileJournalDomainMetrics(
	ctx context.Context,
	tx pgx.Tx,
) (int64, error) {
	if ctx == nil {
		return 0, errors.New("domain reconciliation context is required")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if tx == nil {
		return 0, errors.New("domain reconciliation transaction is required")
	}
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1::integer, $2::integer)",
		domainReconcileNamespace,
		domainReconcileLockKey,
	); err != nil {
		return 0, fmt.Errorf(
			"acquire domain reconciliation advisory lock: %w",
			err,
		)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO journal_domain_metrics (
			venue_metric_snapshot_id,
			domain_category_rule_id,
			jcr_category
		)
		SELECT
			metric.id,
			rule.id,
			metric.category
		FROM venue_metric_snapshots AS metric
		JOIN domain_category_rules AS rule
		  ON metric.category = rule.jcr_category
		ON CONFLICT (
			venue_metric_snapshot_id,
			domain_category_rule_id
		) DO NOTHING
	`)
	if err != nil {
		return 0, fmt.Errorf(
			"reconcile exact JCR Category domain links: %w",
			err,
		)
	}
	return tag.RowsAffected(), nil
}

type PostgresDomainStore struct {
	pool *pgxpool.Pool
}

func NewPostgresDomainStore(
	pool *pgxpool.Pool,
) (*PostgresDomainStore, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &PostgresDomainStore{pool: pool}, nil
}

func (store *PostgresDomainStore) DomainsForJCRCategory(
	ctx context.Context,
	registryVersion string,
	workID string,
	category string,
) ([]ResearchDomain, error) {
	if ctx == nil {
		return nil, errors.New("domain lookup context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil || store.pool == nil {
		return nil, errors.New("PostgresDomainStore is not initialized")
	}
	if registryVersion != ResearchDomainRegistryVersion {
		return nil, fmt.Errorf(
			"unsupported domain Registry version %q",
			registryVersion,
		)
	}
	if workID == "" || workID != strings.TrimSpace(workID) {
		return nil, errors.New("domain lookup requires an exact Work ID")
	}
	if category == "" || category != strings.TrimSpace(category) {
		return nil, nil
	}
	rows, err := store.pool.Query(ctx, `
		SELECT domain.domain_key
		FROM domain_category_rules AS rule
		JOIN research_domains AS domain
		  ON domain.id = rule.domain_id
		 AND domain.domain_version_id = rule.domain_version_id
		JOIN domain_versions AS version
		  ON version.id = rule.domain_version_id
		WHERE version.registry_version = $1
		  AND version.sealed_at IS NOT NULL
		  AND rule.jcr_category = $3
		  AND (
			  NOT rule.article_level_required
			  OR EXISTS (
				  SELECT 1
				  FROM work_domain_assertions AS assertion
				  WHERE assertion.work_id = $2
				    AND assertion.domain_version_id = rule.domain_version_id
				    AND assertion.domain_category_rule_id = rule.id
				    AND assertion.domain_id = rule.domain_id
			  )
		  )
		ORDER BY CASE domain.domain_key
			WHEN 'medicine' THEN 1
			WHEN 'biology' THEN 2
			WHEN 'computer_science' THEN 3
		END
	`, registryVersion, workID, category)
	if err != nil {
		return nil, fmt.Errorf("query exact JCR Category domains: %w", err)
	}
	defer rows.Close()
	var result []ResearchDomain
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan exact JCR Category domain: %w", err)
		}
		domain, err := ParseResearchDomain(raw)
		if err != nil {
			return nil, fmt.Errorf("restore stored research domain: %w", err)
		}
		result = append(result, domain)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate exact JCR Category domains: %w", err)
	}
	return slices.Clone(result), nil
}

func (store *PostgresDomainStore) PersistWorkDomainAssertion(
	ctx context.Context,
	assertion WorkDomainAssertion,
) (string, error) {
	if ctx == nil {
		return "", errors.New("Work domain assertion context is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if store == nil || store.pool == nil {
		return "", errors.New("PostgresDomainStore is not initialized")
	}
	if err := assertion.Validate(); err != nil {
		return "", err
	}
	var assertionID string
	err := store.pool.QueryRow(ctx, `
		INSERT INTO work_domain_assertions (
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			work_id,
			domain_version_id,
			domain_category_rule_id,
			domain_id,
			source_path,
			asserted_at
		)
		SELECT
			$1,
			$2,
			$3,
			$4,
			version.id,
			rule.id,
			domain.id,
			$8,
			$9
		FROM domain_versions AS version
		JOIN domain_category_rules AS rule
		  ON rule.domain_version_id = version.id
		JOIN research_domains AS domain
		  ON domain.id = rule.domain_id
		 AND domain.domain_version_id = version.id
		WHERE version.registry_version = $5
		  AND version.sealed_at IS NOT NULL
		  AND rule.id = $6
		  AND domain.domain_key = $7
		RETURNING id::text
	`,
		assertion.ProjectionAssertionID,
		assertion.NormalizedAssertionID,
		assertion.SourceRecordID,
		assertion.WorkID,
		assertion.DomainRegistryVersion,
		assertion.DomainCategoryRuleID,
		assertion.Domain,
		assertion.SourcePath,
		assertion.AssertedAt.UTC(),
	).Scan(&assertionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New(
			"Work domain assertion Registry, rule, and domain binding was not found",
		)
	}
	if err != nil {
		return "", fmt.Errorf("persist Work domain assertion: %w", err)
	}
	return assertionID, nil
}
