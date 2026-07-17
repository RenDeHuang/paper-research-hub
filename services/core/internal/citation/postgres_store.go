package citation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrEvidenceConflict = errors.New(
	"citation evidence identity already exists with different immutable values",
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	return &PostgresStore{pool: pool}, nil
}

func (store *PostgresStore) AppendSnapshot(
	ctx context.Context,
	snapshot Snapshot,
) (uuid.UUID, error) {
	if err := snapshot.Validate(); err != nil {
		return uuid.Nil, err
	}
	if err := store.validate(ctx); err != nil {
		return uuid.Nil, err
	}

	var insertedID uuid.UUID
	err := store.pool.QueryRow(ctx, `
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
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		)
		ON CONFLICT (work_id, source, observed_at)
		DO NOTHING
		RETURNING id
	`,
		snapshot.WorkID,
		snapshot.Source,
		snapshot.ObservedAt.UTC(),
		snapshot.Count,
		snapshot.SourceRecordID,
		snapshot.IngestionJobID,
		snapshot.RetrievedAt.UTC(),
		snapshot.Coverage,
		snapshot.DefinitionVersion,
		snapshot.DatasetVersion,
	).Scan(&insertedID)
	if err == nil {
		return insertedID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("append citation snapshot: %w", err)
	}

	existing, existingID, err := store.loadSnapshotIdentity(
		ctx,
		snapshot.WorkID,
		snapshot.Source,
		snapshot.ObservedAt,
	)
	if err != nil {
		return uuid.Nil, err
	}
	if !snapshotsEqual(existing, snapshot) {
		return uuid.Nil, ErrEvidenceConflict
	}
	return existingID, nil
}

func (store *PostgresStore) AppendCitationEdge(
	ctx context.Context,
	edge CitationEdge,
) (uuid.UUID, error) {
	return store.appendEdge(ctx, "citation_edges", edge)
}

func (store *PostgresStore) AppendReferenceEdge(
	ctx context.Context,
	edge CitationEdge,
) (uuid.UUID, error) {
	return store.appendEdge(ctx, "reference_edges", edge)
}

func (store *PostgresStore) appendEdge(
	ctx context.Context,
	table string,
	edge CitationEdge,
) (uuid.UUID, error) {
	if err := edge.Validate(); err != nil {
		return uuid.Nil, err
	}
	if err := store.validate(ctx); err != nil {
		return uuid.Nil, err
	}
	if table != "citation_edges" && table != "reference_edges" {
		return uuid.Nil, errors.New("unsupported citation evidence table")
	}

	var insertedID uuid.UUID
	err := store.pool.QueryRow(
		ctx,
		fmt.Sprintf(`
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
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
			)
			ON CONFLICT (
				source,
				citing_identifier,
				cited_identifier,
				definition_version,
				dataset_version
			)
			DO NOTHING
			RETURNING id
		`, table),
		nullableUUID(edge.CitingWorkID),
		nullableUUID(edge.CitedWorkID),
		edge.CitingIdentifier,
		edge.CitedIdentifier,
		edge.Source,
		edge.SourceRecordID,
		edge.IngestionJobID,
		edge.RetrievedAt.UTC(),
		edge.DefinitionVersion,
		edge.DatasetVersion,
	).Scan(&insertedID)
	if err == nil {
		return insertedID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("append %s evidence: %w", table, err)
	}

	existing, existingID, err := store.loadEdgeIdentity(
		ctx,
		table,
		edge,
	)
	if err != nil {
		return uuid.Nil, err
	}
	if !edgesEqual(existing, edge) {
		return uuid.Nil, ErrEvidenceConflict
	}
	return existingID, nil
}

func (store *PostgresStore) loadSnapshotIdentity(
	ctx context.Context,
	workID uuid.UUID,
	source string,
	observedAt time.Time,
) (Snapshot, uuid.UUID, error) {
	var (
		snapshot Snapshot
		id       uuid.UUID
	)
	err := store.pool.QueryRow(ctx, `
		SELECT
			id,
			work_id,
			source,
			observed_at,
			count,
			source_record_id,
			ingestion_job_id,
			retrieved_at,
			coverage::double precision,
			definition_version,
			dataset_version
		FROM citation_snapshots
		WHERE work_id = $1
		  AND source = $2
		  AND observed_at = $3
	`, workID, source, observedAt.UTC()).Scan(
		&id,
		&snapshot.WorkID,
		&snapshot.Source,
		&snapshot.ObservedAt,
		&snapshot.Count,
		&snapshot.SourceRecordID,
		&snapshot.IngestionJobID,
		&snapshot.RetrievedAt,
		&snapshot.Coverage,
		&snapshot.DefinitionVersion,
		&snapshot.DatasetVersion,
	)
	if err != nil {
		return Snapshot{}, uuid.Nil, fmt.Errorf(
			"load existing citation snapshot identity: %w",
			err,
		)
	}
	return snapshot, id, nil
}

func (store *PostgresStore) loadEdgeIdentity(
	ctx context.Context,
	table string,
	edge CitationEdge,
) (CitationEdge, uuid.UUID, error) {
	var (
		existing                  CitationEdge
		id                        uuid.UUID
		citingWorkID, citedWorkID pgtype.UUID
	)
	err := store.pool.QueryRow(
		ctx,
		fmt.Sprintf(`
			SELECT
				id,
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
			FROM %s
			WHERE source = $1
			  AND citing_identifier = $2
			  AND cited_identifier = $3
			  AND definition_version = $4
			  AND dataset_version = $5
		`, table),
		edge.Source,
		edge.CitingIdentifier,
		edge.CitedIdentifier,
		edge.DefinitionVersion,
		edge.DatasetVersion,
	).Scan(
		&id,
		&citingWorkID,
		&citedWorkID,
		&existing.CitingIdentifier,
		&existing.CitedIdentifier,
		&existing.Source,
		&existing.SourceRecordID,
		&existing.IngestionJobID,
		&existing.RetrievedAt,
		&existing.DefinitionVersion,
		&existing.DatasetVersion,
	)
	if err != nil {
		return CitationEdge{}, uuid.Nil, fmt.Errorf(
			"load existing %s identity: %w",
			table,
			err,
		)
	}
	existing.CitingWorkID = pgUUID(citingWorkID)
	existing.CitedWorkID = pgUUID(citedWorkID)
	return existing, id, nil
}

func (store *PostgresStore) validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil || store.pool == nil {
		return errors.New("PostgresStore is not initialized")
	}
	return nil
}

func snapshotsEqual(left Snapshot, right Snapshot) bool {
	return left.WorkID == right.WorkID &&
		left.Source == right.Source &&
		left.ObservedAt.Equal(right.ObservedAt) &&
		left.Count == right.Count &&
		left.SourceRecordID == right.SourceRecordID &&
		left.IngestionJobID == right.IngestionJobID &&
		left.RetrievedAt.Equal(right.RetrievedAt) &&
		left.Coverage == right.Coverage &&
		left.DefinitionVersion == right.DefinitionVersion &&
		left.DatasetVersion == right.DatasetVersion
}

func edgesEqual(left CitationEdge, right CitationEdge) bool {
	return left.CitingWorkID == right.CitingWorkID &&
		left.CitedWorkID == right.CitedWorkID &&
		left.CitingIdentifier == right.CitingIdentifier &&
		left.CitedIdentifier == right.CitedIdentifier &&
		left.Source == right.Source &&
		left.SourceRecordID == right.SourceRecordID &&
		left.IngestionJobID == right.IngestionJobID &&
		left.RetrievedAt.Equal(right.RetrievedAt) &&
		left.DatasetVersion == right.DatasetVersion &&
		left.DefinitionVersion == right.DefinitionVersion
}

func nullableUUID(value uuid.UUID) any {
	if value == uuid.Nil {
		return nil
	}
	return value
}

func pgUUID(value pgtype.UUID) uuid.UUID {
	if !value.Valid {
		return uuid.Nil
	}
	return uuid.UUID(value.Bytes)
}
