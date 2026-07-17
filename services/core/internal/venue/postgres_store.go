package venue

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/biomed"
)

const postgresJCRAliasAdvisoryNamespace int32 = 0x4a435241

type PostgresJCRStoreConfig struct {
	SourceLicense string
}

type PostgresJCRStore struct {
	pool          *pgxpool.Pool
	sourceLicense string
}

var (
	_ JCRRepository = (*PostgresJCRStore)(nil)
	_ JCRSink       = (*PostgresJCRStore)(nil)
)

func NewPostgresJCRStore(
	pool *pgxpool.Pool,
	config PostgresJCRStoreConfig,
) (*PostgresJCRStore, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	sourceLicense := strings.TrimSpace(config.SourceLicense)
	if sourceLicense == "" {
		return nil, errors.New("JCR source license is required")
	}
	return &PostgresJCRStore{
		pool:          pool,
		sourceLicense: sourceLicense,
	}, nil
}

func (store *PostgresJCRStore) FindImport(
	ctx context.Context,
	fileSHA256 string,
) (ImportReceipt, bool, error) {
	if err := ctx.Err(); err != nil {
		return ImportReceipt{}, false, err
	}
	if store == nil || store.pool == nil {
		return ImportReceipt{}, false, errors.New("PostgresJCRStore is not initialized")
	}
	return findPostgresImport(ctx, store.pool, fileSHA256)
}

func (store *PostgresJCRStore) FindVenuesByISSNs(
	ctx context.Context,
	identifiers ISSNSet,
) ([]Venue, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if store == nil || store.pool == nil {
		return nil, errors.New("PostgresJCRStore is not initialized")
	}
	if identifiers.Empty() {
		return nil, errors.New("exact ISSN lookup requires at least one identifier")
	}

	rows, err := store.pool.Query(ctx, `
		SELECT
			id::text,
			venue_type,
			display_title,
			issn_l,
			issn,
			eissn
		FROM venues
		WHERE
			($1::text IS NOT NULL AND $1 IN (issn_l, issn, eissn))
			OR
			($2::text IS NOT NULL AND $2 IN (issn_l, issn, eissn))
			OR
			($3::text IS NOT NULL AND $3 IN (issn_l, issn, eissn))
		ORDER BY id
	`,
		nullableISSNRole(identifiers, ISSNRoleLinking),
		nullableISSNRole(identifiers, ISSNRolePrint),
		nullableISSNRole(identifiers, ISSNRoleElectronic),
	)
	if err != nil {
		return nil, fmt.Errorf("query Venues by exact ISSNs: %w", err)
	}
	defer rows.Close()

	var result []Venue
	for rows.Next() {
		var (
			id, rawType, displayTitle string
			issnL, printISSN, eISSN   pgtype.Text
		)
		if err := rows.Scan(
			&id,
			&rawType,
			&displayTitle,
			&issnL,
			&printISSN,
			&eISSN,
		); err != nil {
			return nil, fmt.Errorf("scan Venue exact ISSN match: %w", err)
		}
		venueIdentifiers, err := postgresVenueISSNs(issnL, printISSN, eISSN)
		if err != nil {
			return nil, fmt.Errorf("restore Venue %q ISSNs: %w", id, err)
		}
		alias, err := NewAlias(displayTitle, "venue_registry")
		if err != nil {
			return nil, fmt.Errorf("restore Venue %q display alias: %w", id, err)
		}
		item, err := NewVenue(
			id,
			VenueType(rawType),
			venueIdentifiers,
			[]Alias{alias},
		)
		if err != nil {
			return nil, fmt.Errorf("restore Venue %q: %w", id, err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Venue exact ISSN matches: %w", err)
	}
	return result, nil
}

func (store *PostgresJCRStore) FindMetric(
	ctx context.Context,
	key MetricKey,
) (MetricSnapshot, bool, error) {
	if err := ctx.Err(); err != nil {
		return MetricSnapshot{}, false, err
	}
	if store == nil || store.pool == nil {
		return MetricSnapshot{}, false, errors.New("PostgresJCRStore is not initialized")
	}
	stored, found, err := findPostgresMetric(ctx, store.pool, key, false)
	if err != nil || !found {
		return MetricSnapshot{}, found, err
	}
	return stored.snapshot, true, nil
}

func (store *PostgresJCRStore) PersistJCRImport(
	ctx context.Context,
	batch JCRImport,
) (ImportReceipt, error) {
	if err := ctx.Err(); err != nil {
		return ImportReceipt{}, err
	}
	if store == nil || store.pool == nil {
		return ImportReceipt{}, errors.New("PostgresJCRStore is not initialized")
	}
	lockKey, err := postgresJCRImportAdvisoryKey(batch.FileSHA256())
	if err != nil {
		return ImportReceipt{}, fmt.Errorf("invalid JCR import file hash: %w", err)
	}
	if _, err := NewImportReceipt(
		batch.FileSHA256(),
		batch.Source(),
		batch.ImportedAt(),
		batch.InputRows(),
		len(batch.Rows()),
		batch.UnchangedRows(),
	); err != nil {
		return ImportReceipt{}, fmt.Errorf("invalid JCR import batch: %w", err)
	}
	aliases, err := preparePostgresAliases(batch.Aliases(), batch.Source())
	if err != nil {
		return ImportReceipt{}, err
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ImportReceipt{}, fmt.Errorf("begin JCR import transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock($1)",
		lockKey,
	); err != nil {
		return ImportReceipt{}, fmt.Errorf("acquire JCR import advisory lock: %w", err)
	}
	var previousAliasLock int32
	haveAliasLock := false
	for _, alias := range aliases {
		if haveAliasLock && alias.lockKey == previousAliasLock {
			continue
		}
		if _, err := tx.Exec(
			ctx,
			"SELECT pg_advisory_xact_lock($1::integer, $2::integer)",
			postgresJCRAliasAdvisoryNamespace,
			alias.lockKey,
		); err != nil {
			return ImportReceipt{}, fmt.Errorf("acquire JCR alias advisory lock: %w", err)
		}
		previousAliasLock = alias.lockKey
		haveAliasLock = true
	}

	existingReceipt, found, err := findPostgresImport(ctx, tx, batch.FileSHA256())
	if err != nil {
		return ImportReceipt{}, err
	}
	if found {
		if err := validateExistingPostgresImport(
			ctx,
			tx,
			batch,
			existingReceipt,
			store.sourceLicense,
		); err != nil {
			return ImportReceipt{}, err
		}
		if _, err := biomed.ReconcileJournalSubjectMetrics(ctx, tx); err != nil {
			return ImportReceipt{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ImportReceipt{}, fmt.Errorf("commit idempotent JCR import lookup: %w", err)
		}
		return existingReceipt, nil
	}

	var receiptID string
	if err := tx.QueryRow(ctx, "SELECT gen_random_uuid()::text").Scan(&receiptID); err != nil {
		return ImportReceipt{}, fmt.Errorf("allocate JCR import receipt ID: %w", err)
	}

	rows := batch.Rows()
	slices.SortFunc(rows, func(left, right MetricSnapshot) int {
		return cmp.Compare(left.Key().String(), right.Key().String())
	})
	metricIDs := make([]string, 0, len(rows))
	insertedRows := 0
	unchangedRows := batch.UnchangedRows()
	for _, snapshot := range rows {
		if snapshot.Source() != batch.Source() {
			return ImportReceipt{}, fmt.Errorf(
				"metric source %q does not match import source %q",
				snapshot.Source(),
				batch.Source(),
			)
		}
		metricID, inserted, err := insertPostgresMetric(
			ctx,
			tx,
			snapshot,
			store.sourceLicense,
			batch.ImportedAt(),
			receiptID,
		)
		if err != nil {
			return ImportReceipt{}, err
		}
		if inserted {
			insertedRows++
			metricIDs = append(metricIDs, metricID)
			continue
		}

		existing, found, err := findPostgresMetric(ctx, tx, snapshot.Key(), false)
		if err != nil {
			return ImportReceipt{}, err
		}
		if !found {
			return ImportReceipt{}, fmt.Errorf(
				"metric insert for venue %q, metric_year %d, category %q reported a conflict but no row is visible",
				snapshot.VenueID(),
				snapshot.MetricYear(),
				snapshot.Category(),
			)
		}
		if !existing.snapshot.Equal(snapshot) ||
			existing.sourceLicense != store.sourceLicense {
			return ImportReceipt{}, fmt.Errorf(
				"%w for venue %q, metric_year %d, category %q",
				ErrConflictingMetric,
				snapshot.VenueID(),
				snapshot.MetricYear(),
				snapshot.Category(),
			)
		}
		unchangedRows++
		metricIDs = append(metricIDs, existing.id)
	}
	if insertedRows+unchangedRows != batch.InputRows() {
		return ImportReceipt{}, fmt.Errorf(
			"JCR persistence counts are inconsistent: input=%d inserted=%d unchanged=%d",
			batch.InputRows(),
			insertedRows,
			unchangedRows,
		)
	}

	receipt, err := NewImportReceipt(
		batch.FileSHA256(),
		batch.Source(),
		batch.ImportedAt(),
		batch.InputRows(),
		insertedRows,
		unchangedRows,
	)
	if err != nil {
		return ImportReceipt{}, fmt.Errorf("build persisted JCR receipt: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipts (
			id,
			file_sha256,
			source,
			imported_at,
			input_rows,
			inserted_rows,
			unchanged_rows
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`,
		receiptID,
		receipt.FileSHA256(),
		receipt.Source(),
		receipt.ImportedAt(),
		receipt.InputRows(),
		receipt.InsertedRows(),
		receipt.UnchangedRows(),
	); err != nil {
		return ImportReceipt{}, fmt.Errorf("insert JCR import receipt: %w", err)
	}

	for _, metricID := range metricIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO jcr_import_receipt_metrics (
				import_receipt_id,
				metric_snapshot_id
			) VALUES (
				$1,
				$2
			)
		`,
			receiptID,
			metricID,
		); err != nil {
			return ImportReceipt{}, fmt.Errorf("link JCR import receipt metric: %w", err)
		}
	}

	for _, alias := range aliases {
		aliasID, err := persistPostgresAlias(ctx, tx, alias.evidence)
		if err != nil {
			return ImportReceipt{}, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO jcr_import_receipt_aliases (
				import_receipt_id,
				venue_alias_id
			) VALUES ($1, $2)
		`, receiptID, aliasID); err != nil {
			return ImportReceipt{}, fmt.Errorf("link JCR import receipt alias: %w", err)
		}
	}

	if _, err := biomed.ReconcileJournalSubjectMetrics(ctx, tx); err != nil {
		return ImportReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		if mapped := mapPostgresMetricConflict(err, MetricKey{}); mapped != nil {
			return ImportReceipt{}, mapped
		}
		return ImportReceipt{}, fmt.Errorf("commit JCR import transaction: %w", err)
	}
	return receipt, nil
}

type postgresAliasPlan struct {
	evidence VenueAliasEvidence
	key      string
	lockKey  int32
}

func preparePostgresAliases(
	values []VenueAliasEvidence,
	importSource string,
) ([]postgresAliasPlan, error) {
	normalizedSource := strings.TrimSpace(importSource)
	plansByKey := make(map[string]postgresAliasPlan, len(values))
	for _, value := range values {
		alias, err := NewAlias(
			value.Alias().Value(),
			value.Alias().Source(),
		)
		if err != nil {
			return nil, fmt.Errorf("normalize JCR alias: %w", err)
		}
		if alias.Source() != normalizedSource {
			return nil, fmt.Errorf(
				"alias source %q does not match import source %q",
				alias.Source(),
				normalizedSource,
			)
		}
		evidence, err := NewVenueAliasEvidence(value.VenueID(), alias)
		if err != nil {
			return nil, fmt.Errorf("normalize JCR alias evidence: %w", err)
		}
		key := fmt.Sprintf(
			"%d:%s:%d:%s:%d:%s",
			len(evidence.VenueID()),
			evidence.VenueID(),
			len(alias.Value()),
			alias.Value(),
			len(alias.Source()),
			alias.Source(),
		)
		if _, duplicate := plansByKey[key]; duplicate {
			continue
		}
		digest := sha256.Sum256([]byte(key))
		plansByKey[key] = postgresAliasPlan{
			evidence: evidence,
			key:      key,
			lockKey:  int32(binary.BigEndian.Uint32(digest[:4])),
		}
	}
	plans := make([]postgresAliasPlan, 0, len(plansByKey))
	for _, plan := range plansByKey {
		plans = append(plans, plan)
	}
	slices.SortFunc(plans, func(left, right postgresAliasPlan) int {
		if order := cmp.Compare(left.lockKey, right.lockKey); order != 0 {
			return order
		}
		return cmp.Compare(left.key, right.key)
	})
	return plans, nil
}

func postgresJCRImportAdvisoryKey(fileSHA256 string) (int64, error) {
	normalizedSHA := strings.ToLower(strings.TrimSpace(fileSHA256))
	decoded, err := hex.DecodeString(normalizedSHA)
	if err != nil || len(decoded) != 32 {
		return 0, errors.New("JCR import advisory key requires a SHA-256 hex digest")
	}
	return int64(binary.BigEndian.Uint64(decoded[:8])), nil
}

func insertPostgresMetric(
	ctx context.Context,
	tx pgx.Tx,
	snapshot MetricSnapshot,
	sourceLicense string,
	capturedAt time.Time,
	receiptID string,
) (string, bool, error) {
	var jif any
	if snapshot.HasJIF() {
		jif = snapshot.JIF().String()
	}
	var quartile any
	if snapshot.Quartile() != "" {
		quartile = string(snapshot.Quartile())
	}
	var metricID string
	err := tx.QueryRow(ctx, `
		INSERT INTO venue_metric_snapshots (
			venue_id,
			metric_year,
			category,
			jif,
			quartile,
			metric_status,
			source_name,
			source_license,
			captured_at,
			jcr_import_receipt_id
		) VALUES (
			$1,
			$2,
			$3,
			$4::numeric,
			$5,
			$6,
			$7,
			$8,
			$9,
			$10
		)
		ON CONFLICT (venue_id, metric_year, category) DO NOTHING
		RETURNING id::text
	`,
		snapshot.VenueID(),
		snapshot.MetricYear(),
		snapshot.Category(),
		jif,
		quartile,
		snapshot.Status(),
		snapshot.Source(),
		sourceLicense,
		capturedAt,
		receiptID,
	).Scan(&metricID)
	switch {
	case err == nil:
		return metricID, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return "", false, nil
	default:
		if mapped := mapPostgresMetricConflict(err, snapshot.Key()); mapped != nil {
			return "", false, mapped
		}
		return "", false, fmt.Errorf(
			"insert JCR metric for venue %q, metric_year %d, category %q: %w",
			snapshot.VenueID(),
			snapshot.MetricYear(),
			snapshot.Category(),
			err,
		)
	}
}

func mapPostgresMetricConflict(err error, key MetricKey) error {
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23505" {
		return nil
	}
	if key.String() == "" {
		return fmt.Errorf("%w: PostgreSQL unique violation: %w", ErrConflictingMetric, err)
	}
	return fmt.Errorf(
		"%w for venue %q, metric_year %d, category %q: %w",
		ErrConflictingMetric,
		key.VenueID(),
		key.MetricYear(),
		key.Category(),
		err,
	)
}

type postgresRowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func findPostgresImport(
	ctx context.Context,
	querier postgresRowQuerier,
	fileSHA256 string,
) (ImportReceipt, bool, error) {
	normalizedSHA := strings.ToLower(strings.TrimSpace(fileSHA256))
	var (
		source                         string
		importedAt                     pgtype.Timestamptz
		inputRows, inserted, unchanged int
	)
	err := querier.QueryRow(ctx, `
		SELECT
			source,
			imported_at,
			input_rows,
			inserted_rows,
			unchanged_rows
		FROM jcr_import_receipts
		WHERE file_sha256 = $1
	`, normalizedSHA).Scan(
		&source,
		&importedAt,
		&inputRows,
		&inserted,
		&unchanged,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ImportReceipt{}, false, nil
	}
	if err != nil {
		return ImportReceipt{}, false, fmt.Errorf("find JCR import receipt: %w", err)
	}
	receipt, err := NewImportReceipt(
		normalizedSHA,
		source,
		importedAt.Time,
		inputRows,
		inserted,
		unchanged,
	)
	if err != nil {
		return ImportReceipt{}, false, fmt.Errorf("restore JCR import receipt: %w", err)
	}
	return receipt, true, nil
}

func validateExistingPostgresImport(
	ctx context.Context,
	querier postgresRowQuerier,
	batch JCRImport,
	receipt ImportReceipt,
	sourceLicense string,
) error {
	if receipt.Source() != batch.Source() ||
		receipt.InputRows() != batch.InputRows() ||
		batch.UnchangedRows() != 0 ||
		len(batch.Rows()) != batch.InputRows() {
		return fmt.Errorf(
			"%w: existing receipt metadata does not match replayed JCR rows",
			ErrConflictingMetric,
		)
	}
	rows := batch.Rows()
	slices.SortFunc(rows, func(left, right MetricSnapshot) int {
		return cmp.Compare(left.Key().String(), right.Key().String())
	})
	for _, snapshot := range rows {
		existing, found, err := findPostgresMetric(
			ctx,
			querier,
			snapshot.Key(),
			true,
		)
		if err != nil {
			return err
		}
		if !found ||
			!existing.snapshot.Equal(snapshot) ||
			existing.sourceLicense != sourceLicense {
			return fmt.Errorf(
				"%w for venue %q, metric_year %d, category %q",
				ErrConflictingMetric,
				snapshot.VenueID(),
				snapshot.MetricYear(),
				snapshot.Category(),
			)
		}
	}
	return nil
}

type storedPostgresMetric struct {
	id            string
	snapshot      MetricSnapshot
	sourceLicense string
}

func findPostgresMetric(
	ctx context.Context,
	querier postgresRowQuerier,
	key MetricKey,
	lock bool,
) (storedPostgresMetric, bool, error) {
	query := `
		SELECT
			id::text,
			jif::text,
			quartile,
			metric_status,
			source_name,
			source_license
		FROM venue_metric_snapshots
		WHERE venue_id = $1
		  AND metric_year = $2
		  AND category = $3
	`
	if lock {
		query += " FOR KEY SHARE"
	}
	var (
		id                               string
		jif, quartile                    pgtype.Text
		rawStatus, source, sourceLicense string
	)
	err := querier.QueryRow(
		ctx,
		query,
		key.VenueID(),
		key.MetricYear(),
		key.Category(),
	).Scan(
		&id,
		&jif,
		&quartile,
		&rawStatus,
		&source,
		&sourceLicense,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedPostgresMetric{}, false, nil
	}
	if err != nil {
		return storedPostgresMetric{}, false, fmt.Errorf(
			"find JCR metric for venue %q, metric_year %d, category %q: %w",
			key.VenueID(),
			key.MetricYear(),
			key.Category(),
			err,
		)
	}

	var decimal *Decimal
	if jif.Valid {
		parsed, err := ParseDecimal(jif.String)
		if err != nil {
			return storedPostgresMetric{}, false, fmt.Errorf("restore JCR metric JIF: %w", err)
		}
		decimal = &parsed
	}
	snapshot, err := NewMetricSnapshot(
		key.VenueID(),
		key.MetricYear(),
		key.Category(),
		decimal,
		Quartile(quartile.String),
		MetricStatus(rawStatus),
		source,
	)
	if err != nil {
		return storedPostgresMetric{}, false, fmt.Errorf("restore JCR metric: %w", err)
	}
	return storedPostgresMetric{
		id:            id,
		snapshot:      snapshot,
		sourceLicense: sourceLicense,
	}, true, nil
}

func persistPostgresAlias(
	ctx context.Context,
	tx pgx.Tx,
	evidence VenueAliasEvidence,
) (string, error) {
	var aliasID string
	err := tx.QueryRow(ctx, `
		INSERT INTO venue_aliases (venue_id, alias, source)
		VALUES ($1, $2, $3)
		ON CONFLICT (venue_id, alias, source) DO NOTHING
		RETURNING id::text
	`,
		evidence.VenueID(),
		evidence.Alias().Value(),
		evidence.Alias().Source(),
	).Scan(&aliasID)
	switch {
	case err == nil:
		return aliasID, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", fmt.Errorf("insert JCR venue alias: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT id::text
		FROM venue_aliases
		WHERE venue_id = $1
		  AND alias = $2
		  AND source = $3
	`,
		evidence.VenueID(),
		evidence.Alias().Value(),
		evidence.Alias().Source(),
	).Scan(&aliasID); err != nil {
		return "", fmt.Errorf("find existing JCR venue alias: %w", err)
	}
	return aliasID, nil
}

func nullableISSNRole(identifiers ISSNSet, role ISSNRole) any {
	value, ok := identifiers.Get(role)
	if !ok {
		return nil
	}
	return value.String()
}

func postgresVenueISSNs(
	issnL pgtype.Text,
	printISSN pgtype.Text,
	eISSN pgtype.Text,
) (ISSNSet, error) {
	values := make([]ISSN, 0, 3)
	for _, candidate := range []struct {
		role  ISSNRole
		value pgtype.Text
	}{
		{role: ISSNRoleLinking, value: issnL},
		{role: ISSNRolePrint, value: printISSN},
		{role: ISSNRoleElectronic, value: eISSN},
	} {
		if !candidate.value.Valid {
			continue
		}
		parsed, err := ParseISSN(candidate.role, candidate.value.String)
		if err != nil {
			return ISSNSet{}, err
		}
		values = append(values, parsed)
	}
	return NewISSNSet(values...)
}
