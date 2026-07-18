package biomed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	researchscope "github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

const (
	maxSubjectRegistryBytes = 4 << 20
	maxSubjectRegistryRows  = 10_000

	subjectImportAdvisoryNamespace int32 = 0x5355424a
	subjectReconcileNamespace      int32 = 0x53554252
	subjectReconcileLockKey        int32 = 0x4a435243
)

var (
	ErrInvalidSubjectRegistry     = errors.New("invalid Subject registry CSV")
	ErrConflictingSubjectRegistry = errors.New("conflicting Subject registry import")

	subjectRegistryHeader = []string{
		"source",
		"registry_version",
		"slug",
		"display_label",
		"jcr_category",
	}
	subjectSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type SubjectRule struct {
	slug         string
	displayLabel string
	jcrCategory  string
}

func (rule SubjectRule) Slug() string {
	return rule.slug
}

func (rule SubjectRule) DisplayLabel() string {
	return rule.displayLabel
}

func (rule SubjectRule) JCRCategory() string {
	return rule.jcrCategory
}

type SubjectRegistry struct {
	source       string
	version      string
	fileSHA256   string
	subjectRules []SubjectRule
}

func (registry SubjectRegistry) Source() string {
	return registry.source
}

func (registry SubjectRegistry) Version() string {
	return registry.version
}

func (registry SubjectRegistry) FileSHA256() string {
	return registry.fileSHA256
}

func (registry SubjectRegistry) SubjectCount() int {
	return len(registry.subjectRules)
}

func (registry SubjectRegistry) RuleCount() int {
	return len(registry.subjectRules)
}

func (registry SubjectRegistry) Rules() []SubjectRule {
	return append([]SubjectRule(nil), registry.subjectRules...)
}

func ParseSubjectRegistry(source io.Reader) (SubjectRegistry, error) {
	if source == nil {
		return SubjectRegistry{}, errors.New("Subject registry CSV reader is required")
	}
	contents, err := readSubjectRegistry(source)
	if err != nil {
		return SubjectRegistry{}, err
	}
	reader := csv.NewReader(bytes.NewReader(contents))
	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return SubjectRegistry{}, fmt.Errorf("%w: CSV is empty", ErrInvalidSubjectRegistry)
	}
	if err != nil {
		return SubjectRegistry{}, fmt.Errorf(
			"%w: read CSV header: %w",
			ErrInvalidSubjectRegistry,
			err,
		)
	}
	if !slices.Equal(header, subjectRegistryHeader) {
		return SubjectRegistry{}, fmt.Errorf(
			"%w: exact header must be %q",
			ErrInvalidSubjectRegistry,
			strings.Join(subjectRegistryHeader, ","),
		)
	}
	reader.FieldsPerRecord = len(subjectRegistryHeader)

	var (
		registrySource  string
		registryVersion string
		rules           []SubjectRule
	)
	slugs := make(map[string]int)
	categories := make(map[string]int)
	for rowNumber := 2; ; rowNumber++ {
		record, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return SubjectRegistry{}, fmt.Errorf(
				"%w: read CSV row %d: %w",
				ErrInvalidSubjectRegistry,
				rowNumber,
				readErr,
			)
		}
		if len(rules) >= maxSubjectRegistryRows {
			return SubjectRegistry{}, fmt.Errorf(
				"%w: row count exceeds %d",
				ErrInvalidSubjectRegistry,
				maxSubjectRegistryRows,
			)
		}
		for index, field := range record {
			if strings.ContainsRune(field, '\x00') {
				return SubjectRegistry{}, fmt.Errorf(
					"%w: row %d column %q contains NUL",
					ErrInvalidSubjectRegistry,
					rowNumber,
					subjectRegistryHeader[index],
				)
			}
		}

		rowSource, err := exactSubjectRegistryText(record[0], "source", rowNumber)
		if err != nil {
			return SubjectRegistry{}, err
		}
		rowVersion, err := exactSubjectRegistryText(
			record[1],
			"registry_version",
			rowNumber,
		)
		if err != nil {
			return SubjectRegistry{}, err
		}
		slug, err := exactSubjectRegistryText(record[2], "slug", rowNumber)
		if err != nil {
			return SubjectRegistry{}, err
		}
		if len(slug) > 120 || !subjectSlugPattern.MatchString(slug) {
			return SubjectRegistry{}, fmt.Errorf(
				"%w: row %d slug %q must be a stable lowercase hyphenated identifier",
				ErrInvalidSubjectRegistry,
				rowNumber,
				slug,
			)
		}
		displayLabel, err := exactSubjectRegistryText(
			record[3],
			"display_label",
			rowNumber,
		)
		if err != nil {
			return SubjectRegistry{}, err
		}
		jcrCategory, err := exactSubjectRegistryText(
			record[4],
			"jcr_category",
			rowNumber,
		)
		if err != nil {
			return SubjectRegistry{}, err
		}

		if rowNumber == 2 {
			registrySource = rowSource
			registryVersion = rowVersion
		} else {
			if rowSource != registrySource {
				return SubjectRegistry{}, fmt.Errorf(
					"%w: CSV rows must use a single source; row 2 has %q and row %d has %q",
					ErrInvalidSubjectRegistry,
					registrySource,
					rowNumber,
					rowSource,
				)
			}
			if rowVersion != registryVersion {
				return SubjectRegistry{}, fmt.Errorf(
					"%w: CSV rows must use a single registry_version; row 2 has %q and row %d has %q",
					ErrInvalidSubjectRegistry,
					registryVersion,
					rowNumber,
					rowVersion,
				)
			}
		}
		if previousRow, duplicate := slugs[slug]; duplicate {
			return SubjectRegistry{}, fmt.Errorf(
				"%w: duplicate slug %q at rows %d and %d",
				ErrInvalidSubjectRegistry,
				slug,
				previousRow,
				rowNumber,
			)
		}
		if previousRow, duplicate := categories[jcrCategory]; duplicate {
			return SubjectRegistry{}, fmt.Errorf(
				"%w: duplicate JCR Category %q at rows %d and %d",
				ErrInvalidSubjectRegistry,
				jcrCategory,
				previousRow,
				rowNumber,
			)
		}
		slugs[slug] = rowNumber
		categories[jcrCategory] = rowNumber
		rules = append(rules, SubjectRule{
			slug:         slug,
			displayLabel: displayLabel,
			jcrCategory:  jcrCategory,
		})
	}
	if len(rules) == 0 {
		return SubjectRegistry{}, fmt.Errorf(
			"%w: CSV requires at least one Subject row",
			ErrInvalidSubjectRegistry,
		)
	}
	digest := sha256.Sum256(contents)
	return SubjectRegistry{
		source:       registrySource,
		version:      registryVersion,
		fileSHA256:   hex.EncodeToString(digest[:]),
		subjectRules: rules,
	}, nil
}

func readSubjectRegistry(source io.Reader) ([]byte, error) {
	contents, err := io.ReadAll(io.LimitReader(source, maxSubjectRegistryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Subject registry CSV: %w", err)
	}
	if len(contents) > maxSubjectRegistryBytes {
		return nil, fmt.Errorf(
			"%w: input exceeds maximum bytes %d",
			ErrInvalidSubjectRegistry,
			maxSubjectRegistryBytes,
		)
	}
	return contents, nil
}

func exactSubjectRegistryText(
	value string,
	column string,
	rowNumber int,
) (string, error) {
	if value == "" {
		return "", fmt.Errorf(
			"%w: row %d %s is required",
			ErrInvalidSubjectRegistry,
			rowNumber,
			column,
		)
	}
	if value != strings.TrimSpace(value) {
		return "", fmt.Errorf(
			"%w: row %d %s must be exact and whitespace-trimmed",
			ErrInvalidSubjectRegistry,
			rowNumber,
			column,
		)
	}
	return value, nil
}

type SubjectImportReceipt struct {
	source          string
	registryVersion string
	fileSHA256      string
	subjectCount    int
	ruleCount       int
	importedAt      time.Time
}

func (receipt SubjectImportReceipt) Source() string {
	return receipt.source
}

func (receipt SubjectImportReceipt) RegistryVersion() string {
	return receipt.registryVersion
}

func (receipt SubjectImportReceipt) FileSHA256() string {
	return receipt.fileSHA256
}

func (receipt SubjectImportReceipt) SubjectCount() int {
	return receipt.subjectCount
}

func (receipt SubjectImportReceipt) RuleCount() int {
	return receipt.ruleCount
}

func (receipt SubjectImportReceipt) ImportedAt() time.Time {
	return receipt.importedAt
}

type PostgresSubjectImporter struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

func NewPostgresSubjectImporter(
	pool *pgxpool.Pool,
	clock func() time.Time,
) (*PostgresSubjectImporter, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	if clock == nil {
		return nil, errors.New("Subject import clock is required")
	}
	return &PostgresSubjectImporter{pool: pool, clock: clock}, nil
}

func (importer *PostgresSubjectImporter) Import(
	ctx context.Context,
	source io.Reader,
) (SubjectImportReceipt, error) {
	if err := ctx.Err(); err != nil {
		return SubjectImportReceipt{}, err
	}
	if importer == nil || importer.pool == nil || importer.clock == nil {
		return SubjectImportReceipt{}, errors.New("PostgresSubjectImporter is not initialized")
	}
	registry, err := ParseSubjectRegistry(source)
	if err != nil {
		return SubjectImportReceipt{}, err
	}
	importedAt := importer.clock()
	if importedAt.IsZero() {
		return SubjectImportReceipt{}, errors.New("Subject import clock returned a zero timestamp")
	}
	importedAt = importedAt.UTC()

	tx, err := importer.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SubjectImportReceipt{}, fmt.Errorf("begin Subject import transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	lockKey := subjectImportLockKey(registry.Source(), registry.Version())
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1::integer, $2::integer)",
		subjectImportAdvisoryNamespace,
		lockKey,
	); err != nil {
		return SubjectImportReceipt{}, fmt.Errorf("acquire Subject import advisory lock: %w", err)
	}

	existing, found, err := findSubjectImportReceipt(
		ctx,
		tx,
		registry.Source(),
		registry.Version(),
	)
	if err != nil {
		return SubjectImportReceipt{}, err
	}
	if found {
		if existing.FileSHA256() != registry.FileSHA256() ||
			existing.SubjectCount() != registry.SubjectCount() ||
			existing.RuleCount() != registry.RuleCount() {
			return SubjectImportReceipt{}, fmt.Errorf(
				"%w: source %q registry_version %q already has SHA-256 %s",
				ErrConflictingSubjectRegistry,
				registry.Source(),
				registry.Version(),
				existing.FileSHA256(),
			)
		}
		if _, err := ReconcileJournalSubjectMetrics(ctx, tx); err != nil {
			return SubjectImportReceipt{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return SubjectImportReceipt{}, fmt.Errorf(
				"commit idempotent Subject import: %w",
				err,
			)
		}
		return existing, nil
	}

	var existingSource, existingVersion string
	err = tx.QueryRow(ctx, `
		SELECT source, registry_version
		FROM subject_import_receipts
		WHERE file_sha256 = $1
	`, registry.FileSHA256()).Scan(&existingSource, &existingVersion)
	if err == nil {
		return SubjectImportReceipt{}, fmt.Errorf(
			"%w: SHA-256 %s already belongs to %q version %q",
			ErrConflictingSubjectRegistry,
			registry.FileSHA256(),
			existingSource,
			existingVersion,
		)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return SubjectImportReceipt{}, fmt.Errorf(
			"find Subject receipt by file SHA-256: %w",
			err,
		)
	}

	receipt := SubjectImportReceipt{
		source:          registry.Source(),
		registryVersion: registry.Version(),
		fileSHA256:      registry.FileSHA256(),
		subjectCount:    registry.SubjectCount(),
		ruleCount:       registry.RuleCount(),
		importedAt:      importedAt,
	}
	var receiptID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO subject_import_receipts (
			source,
			registry_version,
			file_sha256,
			subject_count,
			rule_count,
			imported_at
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text
	`,
		receipt.Source(),
		receipt.RegistryVersion(),
		receipt.FileSHA256(),
		receipt.SubjectCount(),
		receipt.RuleCount(),
		receipt.ImportedAt(),
	).Scan(&receiptID); err != nil {
		return SubjectImportReceipt{}, mapSubjectImportError(
			"insert Subject import receipt",
			err,
		)
	}

	var versionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO subject_versions (
			subject_import_receipt_id,
			version_key
		) VALUES ($1, $2)
		RETURNING id::text
	`, receiptID, registry.Version()).Scan(&versionID); err != nil {
		return SubjectImportReceipt{}, mapSubjectImportError(
			"insert Subject version",
			err,
		)
	}

	rules := registry.Rules()
	slices.SortFunc(rules, func(left, right SubjectRule) int {
		return strings.Compare(left.Slug(), right.Slug())
	})
	for _, rule := range rules {
		var subjectID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO subjects (
				subject_version_id,
				slug,
				display_label
			) VALUES ($1, $2, $3)
			RETURNING id::text
		`,
			versionID,
			rule.Slug(),
			rule.DisplayLabel(),
		).Scan(&subjectID); err != nil {
			return SubjectImportReceipt{}, mapSubjectImportError(
				fmt.Sprintf("insert Subject %q", rule.Slug()),
				err,
			)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO biomedical_subject_rules (
				subject_version_id,
				subject_id,
				jcr_category
			) VALUES ($1, $2, $3)
		`, versionID, subjectID, rule.JCRCategory()); err != nil {
			return SubjectImportReceipt{}, mapSubjectImportError(
				fmt.Sprintf("insert Subject rule %q", rule.JCRCategory()),
				err,
			)
		}
	}

	if _, err := ReconcileJournalSubjectMetrics(ctx, tx); err != nil {
		return SubjectImportReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SubjectImportReceipt{}, mapSubjectImportError(
			"commit Subject import transaction",
			err,
		)
	}
	return receipt, nil
}

func subjectImportLockKey(source string, version string) int32 {
	digest := sha256.Sum256([]byte(source + "\x00" + version))
	return int32(binary.BigEndian.Uint32(digest[:4]))
}

type subjectReceiptQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func findSubjectImportReceipt(
	ctx context.Context,
	querier subjectReceiptQuerier,
	source string,
	version string,
) (SubjectImportReceipt, bool, error) {
	var receipt SubjectImportReceipt
	err := querier.QueryRow(ctx, `
		SELECT
			source,
			registry_version,
			file_sha256,
			subject_count,
			rule_count,
			imported_at
		FROM subject_import_receipts
		WHERE source = $1
		  AND registry_version = $2
	`, source, version).Scan(
		&receipt.source,
		&receipt.registryVersion,
		&receipt.fileSHA256,
		&receipt.subjectCount,
		&receipt.ruleCount,
		&receipt.importedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SubjectImportReceipt{}, false, nil
	}
	if err != nil {
		return SubjectImportReceipt{}, false, fmt.Errorf(
			"find Subject import receipt: %w",
			err,
		)
	}
	return receipt, true, nil
}

func mapSubjectImportError(operation string, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) &&
		(postgresError.Code == "23505" || postgresError.Code == "23514") {
		return fmt.Errorf("%s: %w: %w", operation, ErrConflictingSubjectRegistry, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func ReconcileJournalSubjectMetrics(
	ctx context.Context,
	tx pgx.Tx,
) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if tx == nil {
		return 0, errors.New("Subject reconciliation transaction is required")
	}
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1::integer, $2::integer)",
		subjectReconcileNamespace,
		subjectReconcileLockKey,
	); err != nil {
		return 0, fmt.Errorf("acquire Subject reconciliation advisory lock: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO journal_subject_metrics (
			venue_metric_snapshot_id,
			subject_rule_id,
			jcr_category
		)
		SELECT
			metric.id,
			rule.id,
			metric.category
		FROM venue_metric_snapshots AS metric
		JOIN biomedical_subject_rules AS rule
		  ON metric.category = rule.jcr_category
		ON CONFLICT (venue_metric_snapshot_id, subject_rule_id) DO NOTHING
	`)
	if err != nil {
		return 0, fmt.Errorf("reconcile exact JCR Category Subject links: %w", err)
	}
	if _, err := researchscope.ReconcileJournalDomainMetrics(ctx, tx); err != nil {
		return 0, fmt.Errorf(
			"reconcile exact JCR Category research domain links: %w",
			err,
		)
	}
	return tag.RowsAffected(), nil
}
