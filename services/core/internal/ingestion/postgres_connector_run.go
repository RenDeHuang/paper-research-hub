package ingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresConnectorRunStore struct {
	pool *pgxpool.Pool
}

func NewPostgresConnectorRunStore(
	pool *pgxpool.Pool,
) (*PostgresConnectorRunStore, error) {
	if pool == nil {
		return nil, errors.New("connector run PostgreSQL pool is required")
	}
	return &PostgresConnectorRunStore{pool: pool}, nil
}

func (store *PostgresConnectorRunStore) Start(
	ctx context.Context,
	claim ConnectorClaim,
) (ConnectorRun, error) {
	if store == nil || store.pool == nil {
		return ConnectorRun{}, errors.New("connector run PostgreSQL store is nil")
	}
	if err := claim.Validate(); err != nil {
		return ConnectorRun{}, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ConnectorRun{}, fmt.Errorf("begin connector run: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	lockKey := claim.Source + "/" + claim.Stream
	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		lockKey,
	); err != nil {
		return ConnectorRun{}, fmt.Errorf("lock connector watermark: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO connector_watermarks (
			source, stream, watermark_kind, watermark_value, version
		) VALUES ($1, $2, $3, $4, 0)
		ON CONFLICT (source, stream) DO NOTHING
	`, claim.Source, claim.Stream, claim.WatermarkKind, claim.From); err != nil {
		return ConnectorRun{}, fmt.Errorf("initialize connector watermark: %w", err)
	}

	var kind WatermarkKind
	var watermark time.Time
	var version int64
	if err := tx.QueryRow(ctx, `
		SELECT watermark_kind, watermark_value, version
		FROM connector_watermarks
		WHERE source = $1 AND stream = $2
		FOR UPDATE
	`, claim.Source, claim.Stream).Scan(
		&kind,
		&watermark,
		&version,
	); err != nil {
		return ConnectorRun{}, fmt.Errorf("read connector watermark: %w", err)
	}
	if kind != claim.WatermarkKind || !watermark.Equal(claim.From) {
		return ConnectorRun{}, fmt.Errorf(
			"%w: persisted %s/%s v%d, claim %s/%s",
			ErrWatermarkConflict,
			kind,
			watermark.UTC().Format(time.RFC3339Nano),
			version,
			claim.WatermarkKind,
			claim.From.Format(time.RFC3339Nano),
		)
	}

	var runID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO connector_runs (
			source,
			stream,
			watermark_kind,
			claimed_from,
			claimed_until,
			expected_watermark_version,
			idempotency_key,
			status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, 'running')
		RETURNING id::text
	`,
		claim.Source,
		claim.Stream,
		claim.WatermarkKind,
		claim.From,
		claim.Until,
		version,
		claim.IdempotencyKey,
	).Scan(&runID); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) &&
			postgresError.Code == "23505" &&
			(postgresError.ConstraintName == "connector_runs_active_stream" ||
				postgresError.ConstraintName == "connector_runs_active_idempotency") {
			return ConnectorRun{}, errors.New(
				"connector stream already has an active run",
			)
		}
		return ConnectorRun{}, fmt.Errorf("insert connector run: %w", err)
	}
	run, err := RestoreConnectorRun(
		runID,
		claim,
		version,
		ConnectorRunRunning,
	)
	if err != nil {
		return ConnectorRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ConnectorRun{}, fmt.Errorf("commit connector run start: %w", err)
	}
	return run, nil
}

func (store *PostgresConnectorRunStore) RecordPage(
	ctx context.Context,
	run ConnectorRun,
	receipt ConnectorPageReceipt,
) error {
	if store == nil || store.pool == nil {
		return errors.New("connector run PostgreSQL store is nil")
	}
	if err := run.Validate(); err != nil {
		return err
	}
	if run.Status != ConnectorRunRunning {
		return errors.New("connector page receipt requires a running run")
	}
	if err := receipt.Validate(); err != nil {
		return err
	}
	if receipt.RunID != run.ID {
		return errors.New("connector page receipt run ID conflicts with run")
	}

	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin connector page receipt: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockPersistedConnectorRun(ctx, tx, run); err != nil {
		return err
	}

	var cursorIn, cursorOut, hash string
	var count int
	err = tx.QueryRow(ctx, `
		SELECT cursor_in, cursor_out, content_sha256, record_count
		FROM connector_run_pages
		WHERE run_id = $1 AND page_ordinal = $2
	`, receipt.RunID, receipt.PageOrdinal).Scan(
		&cursorIn,
		&cursorOut,
		&hash,
		&count,
	)
	if err == nil {
		if cursorIn != receipt.CursorIn ||
			cursorOut != receipt.CursorOut ||
			hash != receipt.ContentSHA256 ||
			count != receipt.RecordCount {
			return errors.New(
				"connector page receipt conflicts with immutable receipt",
			)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit idempotent connector page receipt: %w", err)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read connector page receipt: %w", err)
	}

	var previousOrdinal int
	var previousCursorOut string
	err = tx.QueryRow(ctx, `
		SELECT page_ordinal, cursor_out
		FROM connector_run_pages
		WHERE run_id = $1
		ORDER BY page_ordinal DESC
		LIMIT 1
	`, receipt.RunID).Scan(&previousOrdinal, &previousCursorOut)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if receipt.PageOrdinal != 1 {
			return errors.New(
				"connector page receipt ordinal must start at 1",
			)
		}
	case err != nil:
		return fmt.Errorf("read previous connector page receipt: %w", err)
	default:
		if receipt.PageOrdinal != previousOrdinal+1 {
			return fmt.Errorf(
				"connector page receipt ordinal %d must follow %d",
				receipt.PageOrdinal,
				previousOrdinal,
			)
		}
		if previousCursorOut == "" {
			return errors.New(
				"connector page receipt cannot follow a terminal page",
			)
		}
		if receipt.CursorIn != previousCursorOut {
			return errors.New(
				"connector page receipt cursor does not continue previous page",
			)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO connector_run_pages (
			run_id,
			page_ordinal,
			cursor_in,
			cursor_out,
			content_sha256,
			record_count
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (run_id, page_ordinal) DO NOTHING
	`,
		receipt.RunID,
		receipt.PageOrdinal,
		receipt.CursorIn,
		receipt.CursorOut,
		receipt.ContentSHA256,
		receipt.RecordCount,
	); err != nil {
		return fmt.Errorf("insert connector page receipt: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit connector page receipt: %w", err)
	}
	return nil
}

func (store *PostgresConnectorRunStore) Succeed(
	ctx context.Context,
	run ConnectorRun,
) error {
	if store == nil || store.pool == nil {
		return errors.New("connector run PostgreSQL store is nil")
	}
	if err := run.Validate(); err != nil {
		return err
	}
	if run.Status != ConnectorRunSucceeded {
		return errors.New("connector run completion requires succeeded status")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin connector run completion: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if err := lockPersistedConnectorRun(ctx, tx, run); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT page_ordinal, cursor_in, cursor_out
		FROM connector_run_pages
		WHERE run_id = $1
		ORDER BY page_ordinal
	`, run.ID)
	if err != nil {
		return fmt.Errorf("read connector run page chain: %w", err)
	}
	defer rows.Close()
	pageCount := 0
	previousCursorOut := ""
	for rows.Next() {
		var ordinal int
		var cursorIn, cursorOut string
		if err := rows.Scan(&ordinal, &cursorIn, &cursorOut); err != nil {
			return fmt.Errorf("scan connector run page chain: %w", err)
		}
		pageCount++
		if ordinal != pageCount {
			return errors.New(
				"connector run page chain contains a missing ordinal",
			)
		}
		if pageCount > 1 && cursorIn != previousCursorOut {
			return errors.New(
				"connector run page chain contains a cursor gap",
			)
		}
		if pageCount > 1 && previousCursorOut == "" {
			return errors.New(
				"connector run page chain continues after terminal page",
			)
		}
		previousCursorOut = cursorOut
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate connector run page chain: %w", err)
	}
	if pageCount == 0 {
		return errors.New("connector run cannot advance watermark without page receipts")
	}
	if previousCursorOut != "" {
		return errors.New(
			"connector run cannot advance watermark without a terminal page",
		)
	}
	command, err := tx.Exec(ctx, `
		UPDATE connector_watermarks
		SET
			watermark_value = $5,
			version = version + 1,
			updated_at = now()
		WHERE source = $1
		  AND stream = $2
		  AND watermark_kind = $3
		  AND version = $4
		  AND watermark_value = $6
	`,
		run.Claim.Source,
		run.Claim.Stream,
		run.Claim.WatermarkKind,
		run.ExpectedWatermarkVersion,
		run.Claim.Until,
		run.Claim.From,
	)
	if err != nil {
		return fmt.Errorf("advance connector watermark: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrWatermarkConflict
	}
	command, err = tx.Exec(ctx, `
		UPDATE connector_runs
		SET status = 'succeeded', completed_at = now()
		WHERE id = $1 AND status = 'running'
	`, run.ID)
	if err != nil {
		return fmt.Errorf("complete connector run: %w", err)
	}
	if command.RowsAffected() != 1 {
		return errors.New("connector run is not running")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit connector run completion: %w", err)
	}
	return nil
}

func (store *PostgresConnectorRunStore) Fail(
	ctx context.Context,
	run ConnectorRun,
) error {
	if store == nil || store.pool == nil {
		return errors.New("connector run PostgreSQL store is nil")
	}
	if err := run.Validate(); err != nil {
		return err
	}
	if run.Status != ConnectorRunFailed {
		return errors.New("connector run failure requires failed status")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin connector run failure: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockPersistedConnectorRun(ctx, tx, run); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
		UPDATE connector_runs
		SET
			status = 'failed',
			failure_stage = $2,
			failure_code = $3,
			completed_at = now()
		WHERE id = $1 AND status = 'running'
	`,
		run.ID,
		run.FailureStage,
		run.FailureCode,
	)
	if err != nil {
		return fmt.Errorf("fail connector run: %w", err)
	}
	if command.RowsAffected() != 1 {
		return errors.New("connector run is not running")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit connector run failure: %w", err)
	}
	return nil
}

func lockPersistedConnectorRun(
	ctx context.Context,
	tx pgx.Tx,
	run ConnectorRun,
) error {
	var (
		source                   string
		stream                   string
		watermarkKind            WatermarkKind
		claimedFrom              time.Time
		claimedUntil             time.Time
		expectedWatermarkVersion int64
		idempotencyKey           string
		status                   ConnectorRunStatus
	)
	if err := tx.QueryRow(ctx, `
		SELECT
			source,
			stream,
			watermark_kind,
			claimed_from,
			claimed_until,
			expected_watermark_version,
			idempotency_key,
			status
		FROM connector_runs
		WHERE id = $1
		FOR UPDATE
	`, run.ID).Scan(
		&source,
		&stream,
		&watermarkKind,
		&claimedFrom,
		&claimedUntil,
		&expectedWatermarkVersion,
		&idempotencyKey,
		&status,
	); err != nil {
		return fmt.Errorf("lock persisted connector run: %w", err)
	}
	if source != run.Claim.Source ||
		stream != run.Claim.Stream ||
		watermarkKind != run.Claim.WatermarkKind ||
		!claimedFrom.Equal(run.Claim.From) ||
		!claimedUntil.Equal(run.Claim.Until) ||
		expectedWatermarkVersion != run.ExpectedWatermarkVersion ||
		idempotencyKey != run.Claim.IdempotencyKey {
		return errors.New(
			"connector run conflicts with persisted claim",
		)
	}
	if status != ConnectorRunRunning {
		return fmt.Errorf(
			"persisted connector run is not running: %s",
			status,
		)
	}
	return nil
}

func (store *PostgresConnectorRunStore) Watermark(
	ctx context.Context,
	source string,
	stream string,
) (time.Time, int64, error) {
	var value time.Time
	var version int64
	if err := store.pool.QueryRow(ctx, `
		SELECT watermark_value, version
		FROM connector_watermarks
		WHERE source = $1 AND stream = $2
	`, strings.TrimSpace(source), strings.TrimSpace(stream)).Scan(
		&value,
		&version,
	); err != nil {
		return time.Time{}, 0, fmt.Errorf("read connector watermark: %w", err)
	}
	return value.UTC(), version, nil
}
