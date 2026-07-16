package venue

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const postgresJCRImportAdvisoryLockKey int64 = 0x4a4352494d504f52

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
		postgresJCRImportAdvisoryLockKey,
	); err != nil {
		return ImportReceipt{}, fmt.Errorf("acquire JCR import advisory lock: %w", err)
	}

	existingReceipt, found, err := findPostgresImport(ctx, tx, batch.FileSHA256())
	if err != nil {
		return ImportReceipt{}, err
	}
	if found {
		if err := tx.Commit(ctx); err != nil {
			return ImportReceipt{}, fmt.Errorf("commit idempotent JCR import lookup: %w", err)
		}
		return existingReceipt, nil
	}

	rowsToInsert := make([]MetricSnapshot, 0, len(batch.Rows()))
	unchangedRows := batch.UnchangedRows()
	for _, snapshot := range batch.Rows() {
		if snapshot.Source() != batch.Source() {
			return ImportReceipt{}, fmt.Errorf(
				"metric source %q does not match import source %q",
				snapshot.Source(),
				batch.Source(),
			)
		}
		existing, found, err := findPostgresMetric(ctx, tx, snapshot.Key(), true)
		if err != nil {
			return ImportReceipt{}, err
		}
		if !found {
			rowsToInsert = append(rowsToInsert, snapshot)
			continue
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
	}
	insertedRows := len(rowsToInsert)
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
	var receiptID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO jcr_import_receipts (
			file_sha256,
			source,
			imported_at,
			input_rows,
			inserted_rows,
			unchanged_rows
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text
	`,
		receipt.FileSHA256(),
		receipt.Source(),
		receipt.ImportedAt(),
		receipt.InputRows(),
		receipt.InsertedRows(),
		receipt.UnchangedRows(),
	).Scan(&receiptID); err != nil {
		return ImportReceipt{}, fmt.Errorf("insert JCR import receipt: %w", err)
	}

	for _, snapshot := range rowsToInsert {
		var jif any
		if snapshot.HasJIF() {
			jif = snapshot.JIF().String()
		}
		var quartile any
		if snapshot.Quartile() != "" {
			quartile = string(snapshot.Quartile())
		}
		if _, err := tx.Exec(ctx, `
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
		`,
			snapshot.VenueID(),
			snapshot.MetricYear(),
			snapshot.Category(),
			jif,
			quartile,
			snapshot.Status(),
			snapshot.Source(),
			store.sourceLicense,
			receipt.ImportedAt(),
			receiptID,
		); err != nil {
			return ImportReceipt{}, fmt.Errorf(
				"insert JCR metric for venue %q, metric_year %d, category %q: %w",
				snapshot.VenueID(),
				snapshot.MetricYear(),
				snapshot.Category(),
				err,
			)
		}
	}

	for _, aliasEvidence := range batch.Aliases() {
		if aliasEvidence.Alias().Source() != batch.Source() {
			return ImportReceipt{}, fmt.Errorf(
				"alias source %q does not match import source %q",
				aliasEvidence.Alias().Source(),
				batch.Source(),
			)
		}
		aliasID, err := persistPostgresAlias(ctx, tx, aliasEvidence)
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

	if err := tx.Commit(ctx); err != nil {
		return ImportReceipt{}, fmt.Errorf("commit JCR import transaction: %w", err)
	}
	return receipt, nil
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

type storedPostgresMetric struct {
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
