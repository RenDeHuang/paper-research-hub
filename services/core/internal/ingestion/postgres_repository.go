package ingestion

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

type PostgresRepository struct {
	pool *pgxpool.Pool
}

const (
	postgresJobIdempotencyAdvisorySeed int64 = 0x49444D50
	normalizedPayloadSchemaVersion           = "normalized-record/v2"
)

func NewPostgresRepository(pool *pgxpool.Pool) (*PostgresRepository, error) {
	if pool == nil {
		return nil, errors.New("ingestion PostgreSQL pool is required")
	}
	return &PostgresRepository{pool: pool}, nil
}

type postgresBatchLease struct {
	connection *pgxpool.Conn
	batchKey   string
	released   bool
}

func (repository *PostgresRepository) Acquire(
	ctx context.Context,
	batchKey string,
) (BatchLease, error) {
	if repository == nil || repository.pool == nil {
		return nil, errors.New("ingestion PostgreSQL repository is nil")
	}
	normalized := strings.TrimSpace(batchKey)
	if normalized == "" {
		return nil, errors.New("ingestion batch key is required")
	}
	connection, err := repository.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire ingestion batch lock connection: %w", err)
	}
	if _, err := connection.Exec(
		ctx,
		"SELECT pg_advisory_lock(hashtextextended($1, 0))",
		normalized,
	); err != nil {
		connection.Release()
		return nil, fmt.Errorf("acquire ingestion batch advisory lock: %w", err)
	}
	return &postgresBatchLease{
		connection: connection,
		batchKey:   normalized,
	}, nil
}

func (lease *postgresBatchLease) Release(ctx context.Context) error {
	if lease == nil || lease.connection == nil {
		return errors.New("ingestion batch lease is nil")
	}
	if lease.released {
		return errors.New("ingestion batch lease was already released")
	}
	lease.released = true
	_, err := lease.connection.Exec(
		ctx,
		"SELECT pg_advisory_unlock(hashtextextended($1, 0))",
		lease.batchKey,
	)
	lease.connection.Release()
	if err != nil {
		return fmt.Errorf("release ingestion batch advisory lock: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) Start(
	ctx context.Context,
	pending Job,
) (Job, error) {
	if repository == nil || repository.pool == nil {
		return Job{}, errors.New("ingestion PostgreSQL repository is nil")
	}
	if err := pending.Validate(); err != nil {
		return Job{}, err
	}
	if pending.Status != JobStatusPending {
		return Job{}, fmt.Errorf("cannot persist ingestion job in status %q", pending.Status)
	}
	payload, err := json.Marshal(pending.Payload)
	if err != nil {
		return Job{}, fmt.Errorf("encode ingestion job payload: %w", err)
	}

	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Job{}, fmt.Errorf("begin ingestion job start transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	if _, err := tx.Exec(
		ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1, $2))",
		pending.IdempotencyKey,
		postgresJobIdempotencyAdvisorySeed,
	); err != nil {
		return Job{}, fmt.Errorf("lock ingestion job idempotency key: %w", err)
	}

	var existingID string
	var existingSource, existingBatch string
	var existingPayload []byte
	err = tx.QueryRow(ctx, `
		SELECT id::text, source, batch_key, payload
		FROM ingestion_jobs
		WHERE idempotency_key = $1
		  AND status IN ('pending', 'running')
		ORDER BY created_at DESC, id DESC
		LIMIT 1
		FOR UPDATE
	`, pending.IdempotencyKey).Scan(
		&existingID,
		&existingSource,
		&existingBatch,
		&existingPayload,
	)
	switch {
	case err == nil:
		return Job{}, &ErrIdempotencyConflict{
			IdempotencyKey: pending.IdempotencyKey,
			ExistingJobID:  existingID,
			Cause: fmt.Errorf(
				"persisted job source/batch/payload = %q/%q/%s; incoming = %q/%q/%s",
				existingSource,
				existingBatch,
				string(existingPayload),
				pending.LogicalSource,
				pending.BatchKey,
				string(payload),
			),
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return Job{}, fmt.Errorf("query active ingestion job: %w", err)
	}

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO ingestion_jobs (
			source,
			job_type,
			idempotency_key,
			status,
			payload,
			attempts,
			max_attempts,
			started_at,
			batch_key,
			stage
		) VALUES (
			$1, $2, $3, 'running', $4, 1, 1, now(), $5, $6
		)
		RETURNING id::text
	`,
		pending.LogicalSource,
		pending.BatchKey,
		pending.IdempotencyKey,
		payload,
		pending.BatchKey,
		pending.Stage,
	).Scan(&id)
	if err != nil {
		return Job{}, fmt.Errorf("insert ingestion job: %w", err)
	}
	started, err := pending.Start(id)
	if err != nil {
		return Job{}, fmt.Errorf("restore started ingestion job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Job{}, fmt.Errorf("commit ingestion job start: %w", err)
	}
	return started, nil
}

func (repository *PostgresRepository) SetStage(
	ctx context.Context,
	job Job,
) error {
	if err := job.Validate(); err != nil {
		return err
	}
	command, err := repository.pool.Exec(ctx, `
		UPDATE ingestion_jobs
		SET stage = $2, updated_at = now()
		WHERE id = $1
		  AND status = 'running'
	`, job.ID, job.Stage)
	if err != nil {
		return fmt.Errorf("update ingestion job stage: %w", err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("ingestion job %s is not running", job.ID)
	}
	return nil
}

func (repository *PostgresRepository) Complete(
	ctx context.Context,
	job Job,
) error {
	return repository.finishJob(ctx, job, nil)
}

func (repository *PostgresRepository) Fail(
	ctx context.Context,
	job Job,
	cause error,
) error {
	return repository.finishJob(ctx, job, cause)
}

func (repository *PostgresRepository) finishJob(
	ctx context.Context,
	job Job,
	cause error,
) error {
	if err := job.Validate(); err != nil {
		return err
	}
	if !job.Status.terminal() {
		return fmt.Errorf("ingestion job status %q is not terminal", job.Status)
	}
	var encodedError []byte
	if cause != nil {
		lastError := map[string]any{
			"message": cause.Error(),
			"stage":   job.Stage.String(),
		}
		var err error
		encodedError, err = json.Marshal(lastError)
		if err != nil {
			return fmt.Errorf("encode ingestion job failure: %w", err)
		}
	}
	command, err := repository.pool.Exec(ctx, `
		UPDATE ingestion_jobs
		SET
			status = $2,
			stage = $3,
			finished_at = now(),
			last_error = $4,
			failed = failed + CASE WHEN $2 = 'failed' THEN 1 ELSE 0 END,
			updated_at = now()
		WHERE id = $1
		  AND status = 'running'
	`, job.ID, job.Status, job.Stage, encodedError)
	if err != nil {
		return fmt.Errorf("finish ingestion job: %w", err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("ingestion job %s is not running", job.ID)
	}
	return nil
}

func (repository *PostgresRepository) Summary(
	ctx context.Context,
	jobID string,
) (JobSummary, error) {
	var status JobStatus
	var counts JobSummaryCounts
	err := repository.pool.QueryRow(ctx, `
		SELECT
			status,
			raw_inserted,
			raw_reused,
			projected,
			excluded,
			deleted,
			unchanged,
			failed
		FROM ingestion_jobs
		WHERE id = $1
	`, strings.TrimSpace(jobID)).Scan(
		&status,
		&counts.RawInserted,
		&counts.RawReused,
		&counts.Projected,
		&counts.Excluded,
		&counts.Deleted,
		&counts.Unchanged,
		&counts.Failed,
	)
	if err != nil {
		return JobSummary{}, fmt.Errorf("read ingestion job summary: %w", err)
	}
	return NewJobSummary(jobID, status, counts)
}

func (repository *PostgresRepository) RecordSideEffectFailure(
	ctx context.Context,
	jobID string,
	failure SideEffectFailure,
) error {
	if err := failure.Validate(); err != nil {
		return err
	}
	_, err := repository.pool.Exec(ctx, `
		INSERT INTO ingestion_side_effect_failures (
			job_id, hook, logical_source, event_key, stage, message
		) VALUES ($1, $2, $3, $4, $5, $6)
	`,
		jobID,
		failure.Hook,
		failure.LogicalSource,
		failure.EventKey,
		failure.Stage,
		failure.Message,
	)
	if err != nil {
		return fmt.Errorf("record ingestion side-effect failure: %w", err)
	}
	return nil
}

func (repository *PostgresRepository) PersistRaw(
	ctx context.Context,
	jobID string,
	envelope Envelope,
) (PersistedRaw, error) {
	if err := envelope.Validate(); err != nil {
		return PersistedRaw{}, err
	}
	id, disposition, err := repository.persistRawEvent(
		ctx,
		jobID,
		envelope.LogicalSource,
		envelope.EventKey,
		EventKindUpsert,
		envelope.Record.SourceRecordID,
		envelope.SourceTime,
		envelope.TieBreakKey,
		envelope.Position,
		envelope.Raw,
	)
	if err != nil {
		return PersistedRaw{}, err
	}
	return NewPersistedRaw(id, jobID, envelope, disposition)
}

func (repository *PostgresRepository) PersistDeletion(
	ctx context.Context,
	jobID string,
	envelope DeletionEnvelope,
) (PersistedDeletion, error) {
	if err := envelope.Validate(); err != nil {
		return PersistedDeletion{}, err
	}
	id, disposition, err := repository.persistRawEvent(
		ctx,
		jobID,
		envelope.LogicalSource,
		envelope.EventKey,
		EventKindDelete,
		"",
		envelope.SourceTime,
		envelope.TieBreakKey,
		envelope.Position,
		envelope.Raw,
	)
	if err != nil {
		return PersistedDeletion{}, err
	}
	return NewPersistedDeletion(id, jobID, envelope, disposition)
}

func (repository *PostgresRepository) persistRawEvent(
	ctx context.Context,
	jobID string,
	logicalSource string,
	eventKey string,
	kind EventKind,
	sourceRecordID string,
	sourceTime time.Time,
	tieBreakKey string,
	position int64,
	raw source.RawRecord,
) (string, RawDisposition, error) {
	format := "json"
	if _, err := source.NewRawRecord(raw.Payload); err != nil {
		if _, xmlErr := source.NewRawXMLRecord(raw.Payload); xmlErr != nil {
			return "", "", fmt.Errorf(
				"raw event is neither one JSON object nor one XML element: JSON: %v; XML: %w",
				err,
				xmlErr,
			)
		}
		format = "xml"
	}

	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return "", "", fmt.Errorf("begin raw event transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	var insertedID string
	err = tx.QueryRow(ctx, `
			INSERT INTO ingestion_raw_events (
			job_id,
			logical_source,
			event_key,
			event_kind,
			source_record_id,
			source_time,
			tie_break_key,
			position,
			content_hash,
			raw_format,
			raw_payload
		) VALUES (
			$1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9, $10, $11
		)
		ON CONFLICT (logical_source, event_key, event_kind, content_hash)
		DO NOTHING
		RETURNING id::text
	`,
		jobID,
		logicalSource,
		eventKey,
		kind,
		sourceRecordID,
		sourceTime.UTC(),
		tieBreakKey,
		position,
		raw.SHA256,
		format,
		raw.Payload,
	).Scan(&insertedID)

	disposition := RawDispositionInserted
	switch {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows):
		disposition = RawDispositionReused
		if err := tx.QueryRow(ctx, `
				SELECT id::text
			FROM ingestion_raw_events
			WHERE logical_source = $1
			  AND event_key = $2
			  AND event_kind = $3
			  AND content_hash = $4
		`, logicalSource, eventKey, kind, raw.SHA256).Scan(&insertedID); err != nil {
			return "", "", fmt.Errorf("read reused ingestion raw event: %w", err)
		}
	default:
		return "", "", fmt.Errorf("insert ingestion raw event: %w", err)
	}

	counterColumn := "raw_inserted"
	if disposition == RawDispositionReused {
		counterColumn = "raw_reused"
	}
	command, err := tx.Exec(ctx, `
			UPDATE ingestion_jobs
			SET `+counterColumn+` = `+counterColumn+` + 1, updated_at = now()
			WHERE id = $1
			  AND status = 'running'
		`, jobID)
	if err != nil {
		return "", "", fmt.Errorf("increment ingestion raw counter: %w", err)
	}
	if command.RowsAffected() != 1 {
		return "", "", fmt.Errorf("ingestion job %s is not running", jobID)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", "", fmt.Errorf("commit raw event transaction: %w", err)
	}
	return insertedID, disposition, nil
}

type persistedRecordPayload struct {
	Source           string                   `json:"source"`
	SourceRecordID   string                   `json:"source_record_id"`
	CanonicalKey     string                   `json:"canonical_key,omitempty"`
	Identifiers      []source.Identifier      `json:"identifiers"`
	Title            string                   `json:"title"`
	Abstract         string                   `json:"abstract,omitempty"`
	AbstractSections []source.AbstractSection `json:"abstract_sections"`
	PublishedAt      *time.Time               `json:"published_at,omitempty"`
	Authors          []source.Author          `json:"authors"`
	MeSHHeadings     []source.MeSHHeading     `json:"mesh_headings"`
	PublicationTypes []source.PublicationType `json:"publication_types"`
	Relations        []source.Relation        `json:"relations"`
	Topics           []source.Topic           `json:"topics"`
	Keywords         []source.Keyword         `json:"keywords"`
	CitedByCount     *int                     `json:"cited_by_count,omitempty"`
	Venue            *source.Venue            `json:"venue,omitempty"`
	Retracted        *bool                    `json:"retracted,omitempty"`
	CodeURLs         []string                 `json:"code_urls"`
	Evidence         []source.FieldEvidence   `json:"evidence"`
	AuthorsTruncated *bool                    `json:"authors_truncated,omitempty"`
}

func recordPayload(record source.Record) persistedRecordPayload {
	canonicalKey := ""
	if record.Identity.Valid() {
		canonicalKey = record.Identity.CanonicalKey()
	}
	return persistedRecordPayload{
		Source:           record.Source,
		SourceRecordID:   record.SourceRecordID,
		CanonicalKey:     canonicalKey,
		Identifiers:      slices.Clone(record.Identifiers),
		Title:            record.Title,
		Abstract:         record.Abstract,
		AbstractSections: slices.Clone(record.AbstractSections),
		PublishedAt:      cloneRepositoryTime(record.PublishedAt),
		Authors:          cloneAuthors(record.Authors),
		MeSHHeadings:     cloneMeSHHeadings(record.MeSHHeadings),
		PublicationTypes: slices.Clone(record.PublicationTypes),
		Relations:        slices.Clone(record.Relations),
		Topics:           cloneTopics(record.Topics),
		Keywords:         cloneKeywords(record.Keywords),
		CitedByCount:     cloneRepositoryInt(record.CitedByCount),
		Venue:            cloneRepositoryVenue(record.Venue),
		Retracted:        cloneRepositoryBool(record.Retracted),
		CodeURLs:         slices.Clone(record.CodeURLs),
		Evidence:         slices.Clone(record.Evidence),
		AuthorsTruncated: cloneRepositoryBool(record.AuthorsTruncated),
	}
}

func (repository *PostgresRepository) Normalize(
	ctx context.Context,
	jobID string,
	raw PersistedRaw,
	policyVersion string,
) (NormalizedRecord, error) {
	if err := raw.Validate(); err != nil {
		return NormalizedRecord{}, err
	}
	if raw.JobID != jobID {
		return NormalizedRecord{}, errors.New("normalized raw job ID is inconsistent")
	}
	normalizedPolicy := strings.TrimSpace(policyVersion)
	if normalizedPolicy == "" {
		return NormalizedRecord{}, errors.New("normalization policy version is required")
	}
	payload := recordPayload(raw.Envelope.Record)
	normalizedJSON, err := json.Marshal(payload)
	if err != nil {
		return NormalizedRecord{}, fmt.Errorf("encode normalized record: %w", err)
	}
	identityJSON, err := json.Marshal(map[string]any{
		"canonical_key": payload.CanonicalKey,
		"identifiers":   payload.Identifiers,
	})
	if err != nil {
		return NormalizedRecord{}, fmt.Errorf("encode source identity: %w", err)
	}
	rawJSON, err := sourceRecordJSON(raw.Envelope.Raw)
	if err != nil {
		return NormalizedRecord{}, err
	}

	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return NormalizedRecord{}, fmt.Errorf("begin normalization transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	var sourceRecordUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO source_records (
			source,
			source_record_id,
			source_identity,
			source_time,
			content_hash,
			raw_payload
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (source, source_record_id, content_hash)
		DO NOTHING
		RETURNING id::text
	`,
		raw.Envelope.LogicalSource,
		raw.Envelope.Record.SourceRecordID,
		identityJSON,
		raw.Envelope.SourceTime,
		raw.Envelope.Raw.SHA256,
		rawJSON,
	).Scan(&sourceRecordUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `
			SELECT id::text
			FROM source_records
			WHERE source = $1
			  AND source_record_id = $2
			  AND content_hash = $3
		`,
			raw.Envelope.LogicalSource,
			raw.Envelope.Record.SourceRecordID,
			raw.Envelope.Raw.SHA256,
		).Scan(&sourceRecordUUID)
	}
	if err != nil {
		return NormalizedRecord{}, fmt.Errorf("persist normalized source record: %w", err)
	}

	var normalizedAssertionID string
	err = tx.QueryRow(ctx, `
		INSERT INTO ingestion_normalized_records (
			raw_event_id,
			source_record_uuid,
			normalization_policy_version,
			payload_schema_version,
			normalized_payload
		) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (
			raw_event_id,
			normalization_policy_version,
			payload_schema_version
		) DO NOTHING
		RETURNING id::text
	`,
		raw.ID,
		sourceRecordUUID,
		normalizedPolicy,
		normalizedPayloadSchemaVersion,
		normalizedJSON,
	).Scan(&normalizedAssertionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return NormalizedRecord{}, fmt.Errorf("link normalized ingestion record: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		var existingSourceRecord, existingPolicy string
		var payloadMatches bool
		if err := tx.QueryRow(ctx, `
			SELECT
				id::text,
				source_record_uuid::text,
				normalization_policy_version,
				normalized_payload = $4::jsonb
			FROM ingestion_normalized_records
			WHERE raw_event_id = $1
			  AND normalization_policy_version = $2
			  AND payload_schema_version = $3
		`,
			raw.ID,
			normalizedPolicy,
			normalizedPayloadSchemaVersion,
			normalizedJSON,
		).Scan(
			&normalizedAssertionID,
			&existingSourceRecord,
			&existingPolicy,
			&payloadMatches,
		); err != nil {
			return NormalizedRecord{}, fmt.Errorf("read existing normalized ingestion record: %w", err)
		}
		if existingSourceRecord != sourceRecordUUID ||
			existingPolicy != normalizedPolicy ||
			!payloadMatches {
			return NormalizedRecord{}, errors.New(
				"raw event was already normalized with conflicting policy or payload",
			)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return NormalizedRecord{}, fmt.Errorf("commit normalization transaction: %w", err)
	}
	return NewNormalizedRecord(
		raw,
		raw.Envelope.Record,
		normalizedAssertionID,
		normalizedPayloadSchemaVersion,
	)
}

func (repository *PostgresRepository) Exclude(
	ctx context.Context,
	jobID string,
	record NormalizedRecord,
	decision source.ScopeDecision,
	scopePolicyVersion string,
) (ProjectionResult, error) {
	if err := record.Validate(); err != nil {
		return ProjectionResult{}, err
	}
	if decision.Status != source.ScopeExcluded {
		return ProjectionResult{}, fmt.Errorf(
			"exclude requires excluded decision, got %q",
			decision.Status,
		)
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectionResult{}, fmt.Errorf("begin exclusion transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	sourceRecordUUID, err := normalizedSourceRecordID(
		ctx,
		tx,
		record.AssertionID,
		record.RawID,
	)
	if err != nil {
		return ProjectionResult{}, err
	}
	if err := persistScopeDecision(
		ctx,
		tx,
		record.RawID,
		jobID,
		scopePolicyVersion,
		decision,
	); err != nil {
		return ProjectionResult{}, err
	}
	changed, err := upsertSourceState(
		ctx,
		tx,
		sourceState{
			LogicalSource:           record.LogicalSource,
			EventKey:                record.EventKey,
			NormalizedAssertionID:   record.AssertionID,
			RawEventID:              record.RawID,
			SourceRecordUUID:        sourceRecordUUID,
			SourceTime:              record.SourceTime,
			TieBreakKey:             record.TieBreakKey,
			Position:                record.Position,
			ScopeStatus:             decision.Status,
			ScopePolicyVersion:      scopePolicyVersion,
			ProjectionPolicyVersion: "not_applicable",
		},
	)
	if err != nil {
		return ProjectionResult{}, err
	}
	counter := ProjectionStatusUnchanged
	if changed {
		counter = ProjectionStatusExcluded
	}
	if err := incrementProjectionCounter(ctx, tx, jobID, counter); err != nil {
		return ProjectionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectionResult{}, fmt.Errorf("commit exclusion transaction: %w", err)
	}
	return NewProjectionResult(record.EventKey, counter, "")
}

func (repository *PostgresRepository) Project(
	ctx context.Context,
	jobID string,
	candidate ProjectionCandidate,
	scopePolicyVersion string,
	projectionPolicyVersion string,
) (ProjectionResult, error) {
	if err := candidate.Validate(); err != nil {
		return ProjectionResult{}, err
	}
	if strings.TrimSpace(scopePolicyVersion) == "" ||
		strings.TrimSpace(projectionPolicyVersion) == "" {
		return ProjectionResult{}, errors.New("projection policy versions are required")
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectionResult{}, fmt.Errorf("begin projection transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if _, err := lockSourceState(
		ctx,
		tx,
		candidate.LogicalSource,
		candidate.EventKey,
	); err != nil {
		return ProjectionResult{}, err
	}
	sourceRecordUUID, normalizedRecord, candidatePayload, err :=
		immutableNormalizedProjectionPayload(
			ctx,
			tx,
			candidate.NormalizedAssertionID,
			candidate.RawID,
			candidate.PayloadSchemaVersion,
			candidate.Record,
		)
	if err != nil {
		return ProjectionResult{}, err
	}

	identifiers, identity, err := controlledIdentifiers(candidate.Record)
	if err != nil {
		return ProjectionResult{}, err
	}
	for _, identifier := range identifiers {
		if _, err := tx.Exec(
			ctx,
			"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
			identifier.Scheme+":"+identifier.Value,
		); err != nil {
			return ProjectionResult{}, fmt.Errorf("lock canonical identifier: %w", err)
		}
	}
	workID, canonicalKey, err := resolveWork(
		ctx,
		tx,
		candidate.Record,
		identifiers,
		identity,
	)
	if err != nil {
		return ProjectionResult{}, err
	}
	if err := associateSourceRecord(ctx, tx, sourceRecordUUID, workID); err != nil {
		return ProjectionResult{}, err
	}
	if err := persistExternalIdentifiers(
		ctx,
		tx,
		workID,
		sourceRecordUUID,
		identifiers,
	); err != nil {
		return ProjectionResult{}, err
	}
	if err := persistScopeDecision(
		ctx,
		tx,
		candidate.RawID,
		jobID,
		scopePolicyVersion,
		candidate.Scope,
	); err != nil {
		return ProjectionResult{}, err
	}
	var projectionAssertionID string
	err = tx.QueryRow(ctx, `
			INSERT INTO ingestion_projection_assertions (
				normalized_assertion_id,
				raw_event_id,
				source_record_uuid,
				work_id,
				job_id,
				scope_policy_version,
				projection_policy_version,
				record_payload
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (
				normalized_assertion_id,
				scope_policy_version,
				projection_policy_version
			) DO NOTHING
			RETURNING id::text
	`,
		candidate.NormalizedAssertionID,
		candidate.RawID,
		sourceRecordUUID,
		workID,
		jobID,
		scopePolicyVersion,
		projectionPolicyVersion,
		candidatePayload,
	).Scan(&projectionAssertionID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ProjectionResult{}, fmt.Errorf("persist projection assertion: %w", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		var existingRawEventID, existingSourceRecordUUID, existingWorkID string
		var payloadMatches bool
		if err := tx.QueryRow(ctx, `
			SELECT
				id::text,
				raw_event_id::text,
				source_record_uuid::text,
				work_id::text,
				record_payload = $4::jsonb
			FROM ingestion_projection_assertions
			WHERE normalized_assertion_id = $1
			  AND scope_policy_version = $2
			  AND projection_policy_version = $3
		`,
			candidate.NormalizedAssertionID,
			scopePolicyVersion,
			projectionPolicyVersion,
			candidatePayload,
		).Scan(
			&projectionAssertionID,
			&existingRawEventID,
			&existingSourceRecordUUID,
			&existingWorkID,
			&payloadMatches,
		); err != nil {
			return ProjectionResult{}, fmt.Errorf(
				"read existing projection assertion: %w",
				err,
			)
		}
		if existingRawEventID != candidate.RawID ||
			existingSourceRecordUUID != sourceRecordUUID ||
			existingWorkID != workID ||
			!payloadMatches {
			return ProjectionResult{}, errors.New(
				"projection assertion replay conflicts with immutable assertion",
			)
		}
	}
	if err := persistBiomedicalSemanticAssertions(
		ctx,
		tx,
		projectionAssertionID,
		sourceRecordUUID,
		workID,
		normalizedRecord,
	); err != nil {
		return ProjectionResult{}, err
	}
	stateChanged, err := upsertSourceState(
		ctx,
		tx,
		sourceState{
			LogicalSource:           candidate.LogicalSource,
			EventKey:                candidate.EventKey,
			NormalizedAssertionID:   candidate.NormalizedAssertionID,
			RawEventID:              candidate.RawID,
			SourceRecordUUID:        sourceRecordUUID,
			WorkID:                  workID,
			SourceTime:              candidate.SourceTime,
			TieBreakKey:             candidate.TieBreakKey,
			Position:                candidate.Position,
			ScopeStatus:             candidate.Scope.Status,
			ScopePolicyVersion:      scopePolicyVersion,
			ProjectionPolicyVersion: projectionPolicyVersion,
		},
	)
	if err != nil {
		return ProjectionResult{}, err
	}

	status := ProjectionStatusUnchanged
	if stateChanged {
		applied, err := applyWinningProjection(ctx, tx, workID)
		if err != nil {
			return ProjectionResult{}, err
		}
		if applied {
			status = ProjectionStatusProjected
		}
	}
	if err := incrementProjectionCounter(ctx, tx, jobID, status); err != nil {
		return ProjectionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectionResult{}, fmt.Errorf("commit projection transaction: %w", err)
	}
	return NewProjectionResult(candidate.EventKey, status, canonicalKey)
}

func (repository *PostgresRepository) ApplyDeletion(
	ctx context.Context,
	jobID string,
	deletion PersistedDeletion,
	scopePolicyVersion string,
	projectionPolicyVersion string,
) (ProjectionResult, error) {
	if err := deletion.Validate(); err != nil {
		return ProjectionResult{}, err
	}
	tx, err := repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ProjectionResult{}, fmt.Errorf("begin deletion projection transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	var workID *string
	currentWorkID, err := lockSourceState(
		ctx,
		tx,
		deletion.Envelope.LogicalSource,
		deletion.Envelope.EventKey,
	)
	if err != nil {
		return ProjectionResult{}, err
	}
	if currentWorkID != "" {
		workID = &currentWorkID
	}
	changed, err := upsertSourceState(
		ctx,
		tx,
		sourceState{
			LogicalSource:           deletion.Envelope.LogicalSource,
			EventKey:                deletion.Envelope.EventKey,
			RawEventID:              deletion.ID,
			WorkID:                  dereferenceString(workID),
			SourceTime:              deletion.Envelope.SourceTime,
			TieBreakKey:             deletion.Envelope.TieBreakKey,
			Position:                deletion.Envelope.Position,
			ScopeStatus:             source.ScopeExcluded,
			ScopePolicyVersion:      scopePolicyVersion,
			ProjectionPolicyVersion: projectionPolicyVersion,
			IsDeleted:               true,
		},
	)
	if err != nil {
		return ProjectionResult{}, err
	}
	status := ProjectionStatusUnchanged
	if changed {
		status = ProjectionStatusDeleted
		if workID != nil {
			if _, err := applyWinningProjection(ctx, tx, *workID); err != nil {
				return ProjectionResult{}, err
			}
		}
	}
	if err := incrementProjectionCounter(ctx, tx, jobID, status); err != nil {
		return ProjectionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProjectionResult{}, fmt.Errorf("commit deletion projection transaction: %w", err)
	}
	return NewProjectionResult(deletion.Envelope.EventKey, status, "")
}

type controlledIdentifier struct {
	Scheme string
	Value  string
}

func controlledIdentifiers(
	record source.Record,
) ([]controlledIdentifier, paper.Identifier, error) {
	byKey := make(map[string]controlledIdentifier)
	for _, identifier := range record.Identifiers {
		scheme := strings.TrimSpace(string(identifier.Scheme))
		value := strings.TrimSpace(identifier.Value)
		if scheme == "" || value == "" {
			continue
		}
		key := scheme + ":" + value
		byKey[key] = controlledIdentifier{Scheme: scheme, Value: value}
	}
	var identity paper.Identifier
	if record.Identity.Valid() {
		identity = record.Identity
		key := string(identity.Scheme()) + ":" + identity.Value()
		byKey[key] = controlledIdentifier{
			Scheme: string(identity.Scheme()),
			Value:  identity.Value(),
		}
	} else {
		identifiers := paper.Identifiers{}
		for _, identifier := range byKey {
			switch identifier.Scheme {
			case string(paper.SchemeDOI):
				identifiers.DOI = append(identifiers.DOI, identifier.Value)
			case string(paper.SchemeArXiv):
				identifiers.ArXiv = append(identifiers.ArXiv, identifier.Value)
			case string(paper.SchemeOpenAlex):
				identifiers.OpenAlex = append(identifiers.OpenAlex, identifier.Value)
			}
		}
		var err error
		identity, err = paper.CanonicalIdentity(identifiers, "")
		if err != nil {
			return nil, paper.Identifier{}, fmt.Errorf(
				"resolve projection canonical identity: %w",
				err,
			)
		}
	}
	result := make([]controlledIdentifier, 0, len(byKey))
	for _, identifier := range byKey {
		result = append(result, identifier)
	}
	slices.SortFunc(result, func(left, right controlledIdentifier) int {
		return strings.Compare(
			left.Scheme+":"+left.Value,
			right.Scheme+":"+right.Value,
		)
	})
	return result, identity, nil
}

func resolveWork(
	ctx context.Context,
	tx pgx.Tx,
	record source.Record,
	identifiers []controlledIdentifier,
	identity paper.Identifier,
) (string, string, error) {
	workIDs := make(map[string]struct{})
	for _, identifier := range identifiers {
		var workID string
		err := tx.QueryRow(ctx, `
			SELECT work_id::text
			FROM external_identifiers
			WHERE scheme = $1 AND normalized_value = $2
		`, identifier.Scheme, identifier.Value).Scan(&workID)
		if err == nil {
			workIDs[workID] = struct{}{}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", fmt.Errorf("resolve external identifier: %w", err)
		}
	}
	if len(workIDs) > 1 {
		values := make([]string, 0, len(workIDs))
		for workID := range workIDs {
			values = append(values, workID)
		}
		slices.Sort(values)
		return "", "", &CanonicalConflictError{
			LogicalSource:        record.Source,
			EventKey:             record.SourceRecordID,
			ExistingCanonicalKey: strings.Join(values, ","),
			IncomingCanonicalKey: identity.CanonicalKey(),
		}
	}

	incomingKey := identity.CanonicalKey()
	if len(workIDs) == 0 {
		var workID string
		status := paper.WorkStatusActive
		if record.Retracted != nil && *record.Retracted {
			status = paper.WorkStatusRetracted
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO works (
				canonical_key, status, title, abstract, published_at
			) VALUES ($1, $2, $3, NULLIF($4, ''), $5)
			RETURNING id::text
		`,
			incomingKey,
			status,
			strings.TrimSpace(record.Title),
			record.Abstract,
			record.PublishedAt,
		).Scan(&workID); err != nil {
			return "", "", fmt.Errorf("insert canonical work: %w", err)
		}
		return workID, incomingKey, nil
	}

	var workID string
	for value := range workIDs {
		workID = value
	}
	var existingKey string
	if err := tx.QueryRow(ctx, `
		SELECT canonical_key
		FROM works
		WHERE id = $1
		FOR UPDATE
	`, workID).Scan(&existingKey); err != nil {
		return "", "", fmt.Errorf("lock canonical work: %w", err)
	}
	if identityPriority(incomingKey) < identityPriority(existingKey) {
		if _, err := tx.Exec(ctx, `
			UPDATE works
			SET canonical_key = $2, updated_at = now()
			WHERE id = $1
		`, workID, incomingKey); err != nil {
			return "", "", fmt.Errorf("upgrade canonical work identity: %w", err)
		}
		existingKey = incomingKey
	}
	return workID, existingKey, nil
}

func identityPriority(canonicalKey string) int {
	scheme, _, found := strings.Cut(canonicalKey, ":")
	if !found {
		return 100
	}
	switch scheme {
	case string(paper.SchemeDOI):
		return 0
	case string(paper.SchemeArXiv):
		return 1
	case string(paper.SchemeOpenReview):
		return 2
	case string(paper.SchemeSemanticScholar):
		return 3
	case string(paper.SchemeOpenAlex):
		return 4
	default:
		return 100
	}
}

func normalizedSourceRecordID(
	ctx context.Context,
	tx pgx.Tx,
	normalizedAssertionID string,
	rawEventID string,
) (string, error) {
	var id string
	if err := tx.QueryRow(ctx, `
		SELECT source_record_uuid::text
		FROM ingestion_normalized_records
		WHERE id = $1
		  AND raw_event_id = $2
	`, normalizedAssertionID, rawEventID).Scan(&id); err != nil {
		return "", fmt.Errorf("read normalized source record ID: %w", err)
	}
	return id, nil
}

func immutableNormalizedProjectionPayload(
	ctx context.Context,
	tx pgx.Tx,
	normalizedAssertionID string,
	rawEventID string,
	payloadSchemaVersion string,
	candidate source.Record,
) (string, persistedRecordPayload, []byte, error) {
	candidatePayload, err := json.Marshal(recordPayload(candidate))
	if err != nil {
		return "", persistedRecordPayload{}, nil, fmt.Errorf(
			"encode projection candidate normalized payload: %w",
			err,
		)
	}

	var sourceRecordID string
	var persistedPayload []byte
	if err := tx.QueryRow(ctx, `
		SELECT
			source_record_uuid::text,
			normalized_payload
		FROM ingestion_normalized_records
		WHERE id = $1
		  AND raw_event_id = $2
		  AND payload_schema_version = $3
	`,
		normalizedAssertionID,
		rawEventID,
		payloadSchemaVersion,
	).Scan(
		&sourceRecordID,
		&persistedPayload,
	); err != nil {
		return "", persistedRecordPayload{}, nil, fmt.Errorf(
			"read immutable normalized projection payload: %w",
			err,
		)
	}

	var persisted persistedRecordPayload
	if err := json.Unmarshal(persistedPayload, &persisted); err != nil {
		return "", persistedRecordPayload{}, nil, fmt.Errorf(
			"decode immutable normalized projection payload: %w",
			err,
		)
	}
	if err := validateImmutableProjectionIdentity(candidate, persisted); err != nil {
		return "", persistedRecordPayload{}, nil, err
	}
	return sourceRecordID, persisted, candidatePayload, nil
}

func validateImmutableProjectionIdentity(
	candidate source.Record,
	normalized persistedRecordPayload,
) error {
	candidateCanonicalKey := ""
	if candidate.Identity.Valid() {
		candidateCanonicalKey = candidate.Identity.CanonicalKey()
	}
	if candidate.Source != normalized.Source ||
		candidate.SourceRecordID != normalized.SourceRecordID ||
		candidateCanonicalKey != normalized.CanonicalKey ||
		!slices.Equal(candidate.Identifiers, normalized.Identifiers) {
		return errors.New(
			"projection candidate changed immutable normalized identity",
		)
	}
	return nil
}

func associateSourceRecord(
	ctx context.Context,
	tx pgx.Tx,
	sourceRecordUUID string,
	workID string,
) error {
	command, err := tx.Exec(ctx, `
		INSERT INTO source_record_works (source_record_id, work_id)
		VALUES ($1, $2)
		ON CONFLICT (source_record_id) DO NOTHING
	`, sourceRecordUUID, workID)
	if err != nil {
		return fmt.Errorf("associate source record with work: %w", err)
	}
	if command.RowsAffected() == 0 {
		var existingWorkID string
		if err := tx.QueryRow(ctx, `
			SELECT work_id::text
			FROM source_record_works
			WHERE source_record_id = $1
		`, sourceRecordUUID).Scan(&existingWorkID); err != nil {
			return fmt.Errorf("read source record work association: %w", err)
		}
		if existingWorkID == workID {
			return nil
		}
		return &CanonicalConflictError{
			ExistingCanonicalKey: existingWorkID,
			IncomingCanonicalKey: workID,
			Cause:                errors.New("source record is already associated with another work"),
		}
	}
	return nil
}

func persistExternalIdentifiers(
	ctx context.Context,
	tx pgx.Tx,
	workID string,
	sourceRecordUUID string,
	identifiers []controlledIdentifier,
) error {
	for _, identifier := range identifiers {
		command, err := tx.Exec(ctx, `
			INSERT INTO external_identifiers (
				work_id, source_record_id, scheme, normalized_value
			) VALUES ($1, $2, $3, $4)
			ON CONFLICT (scheme, normalized_value) DO NOTHING
		`, workID, sourceRecordUUID, identifier.Scheme, identifier.Value)
		if err != nil {
			return fmt.Errorf(
				"persist external identifier %s:%s: %w",
				identifier.Scheme,
				identifier.Value,
				err,
			)
		}
		if command.RowsAffected() == 0 {
			var existingWorkID string
			if err := tx.QueryRow(ctx, `
				SELECT work_id::text
				FROM external_identifiers
				WHERE scheme = $1 AND normalized_value = $2
			`, identifier.Scheme, identifier.Value).Scan(&existingWorkID); err != nil {
				return fmt.Errorf("read conflicting external identifier: %w", err)
			}
			if existingWorkID != workID {
				return &CanonicalConflictError{
					ExistingCanonicalKey: existingWorkID,
					IncomingCanonicalKey: workID,
				}
			}
		}
	}
	return nil
}

func persistScopeDecision(
	ctx context.Context,
	tx pgx.Tx,
	rawEventID string,
	jobID string,
	policyVersion string,
	decision source.ScopeDecision,
) error {
	evidenceValues := decision.Evidence
	if evidenceValues == nil {
		evidenceValues = []source.FieldEvidence{}
	}
	evidence, err := json.Marshal(evidenceValues)
	if err != nil {
		return fmt.Errorf("encode scope decision evidence: %w", err)
	}
	command, err := tx.Exec(ctx, `
		INSERT INTO ingestion_scope_decisions (
			raw_event_id,
			job_id,
			policy_version,
			status,
			reason,
			evidence
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (raw_event_id, policy_version) DO NOTHING
	`,
		rawEventID,
		jobID,
		strings.TrimSpace(policyVersion),
		decision.Status,
		strings.TrimSpace(decision.Reason),
		evidence,
	)
	if err != nil {
		return fmt.Errorf("persist scope decision: %w", err)
	}
	if command.RowsAffected() == 0 {
		var existingStatus, existingReason string
		var evidenceMatches bool
		if err := tx.QueryRow(ctx, `
			SELECT status, reason, evidence = $3::jsonb
			FROM ingestion_scope_decisions
			WHERE raw_event_id = $1 AND policy_version = $2
		`, rawEventID, strings.TrimSpace(policyVersion), evidence).Scan(
			&existingStatus,
			&existingReason,
			&evidenceMatches,
		); err != nil {
			return fmt.Errorf("read existing scope decision: %w", err)
		}
		if existingStatus != string(decision.Status) ||
			existingReason != strings.TrimSpace(decision.Reason) ||
			!evidenceMatches {
			return errors.New("scope decision replay conflicts with immutable decision")
		}
	}
	return nil
}

type biomedicalMeSHQualifierAssertion struct {
	UI         string
	Label      string
	SourcePath string
	MajorTopic bool
}

type biomedicalMeSHHeadingAssertion struct {
	UI         string
	Label      string
	SourcePath string
	MajorTopic bool
	Qualifiers []biomedicalMeSHQualifierAssertion
}

type biomedicalPublicationTypeAssertion struct {
	UI         string
	Label      string
	SourcePath string
}

type biomedicalCanonicalIdentity struct {
	Kind string
	UI   string
}

func persistBiomedicalSemanticAssertions(
	ctx context.Context,
	tx pgx.Tx,
	projectionAssertionID string,
	sourceRecordID string,
	workID string,
	record persistedRecordPayload,
) error {
	headings, publicationTypes, err := normalizedBiomedicalSemanticAssertions(record)
	if err != nil {
		return err
	}
	canonicalIDs, err := resolveBiomedicalCanonicalIDs(
		ctx,
		tx,
		headings,
		publicationTypes,
	)
	if err != nil {
		return err
	}
	for _, heading := range headings {
		descriptorID := canonicalIDs[biomedicalCanonicalIdentity{
			Kind: "mesh descriptor",
			UI:   heading.UI,
		}]
		headingID, err := persistMeSHHeadingAssertion(
			ctx,
			tx,
			projectionAssertionID,
			sourceRecordID,
			workID,
			descriptorID,
			heading,
		)
		if err != nil {
			return err
		}
		for _, qualifier := range heading.Qualifiers {
			qualifierID := canonicalIDs[biomedicalCanonicalIdentity{
				Kind: "mesh qualifier",
				UI:   qualifier.UI,
			}]
			if err := persistMeSHQualifierAssertion(
				ctx,
				tx,
				projectionAssertionID,
				sourceRecordID,
				workID,
				headingID,
				qualifierID,
				qualifier,
			); err != nil {
				return err
			}
		}
	}
	for _, publicationType := range publicationTypes {
		publicationTypeID := canonicalIDs[biomedicalCanonicalIdentity{
			Kind: "publication type",
			UI:   publicationType.UI,
		}]
		if err := persistPublicationTypeAssertion(
			ctx,
			tx,
			projectionAssertionID,
			sourceRecordID,
			workID,
			publicationTypeID,
			publicationType,
		); err != nil {
			return err
		}
	}
	return nil
}

func normalizedBiomedicalSemanticAssertions(
	record persistedRecordPayload,
) ([]biomedicalMeSHHeadingAssertion, []biomedicalPublicationTypeAssertion, error) {
	if strings.TrimSpace(record.Source) != source.PubMed {
		return nil, nil, nil
	}
	headings := make([]biomedicalMeSHHeadingAssertion, len(record.MeSHHeadings))
	descriptorUIs := make(map[string]struct{}, len(record.MeSHHeadings))
	for headingIndex, heading := range record.MeSHHeadings {
		descriptorUI, descriptorLabel, err := normalizedBiomedicalIdentity(
			heading.Descriptor.UI,
			heading.Descriptor.Name,
			fmt.Sprintf("PubMed MeSH descriptor %d", headingIndex+1),
		)
		if err != nil {
			return nil, nil, err
		}
		if _, duplicate := descriptorUIs[descriptorUI]; duplicate {
			return nil, nil, fmt.Errorf(
				"duplicate PubMed MeSH descriptor UI %q",
				descriptorUI,
			)
		}
		descriptorUIs[descriptorUI] = struct{}{}
		qualifiers := make(
			[]biomedicalMeSHQualifierAssertion,
			len(heading.Qualifiers),
		)
		qualifierUIs := make(map[string]struct{}, len(heading.Qualifiers))
		for qualifierIndex, qualifier := range heading.Qualifiers {
			qualifierUI, qualifierLabel, err := normalizedBiomedicalIdentity(
				qualifier.UI,
				qualifier.Name,
				fmt.Sprintf(
					"PubMed MeSH qualifier %d.%d",
					headingIndex+1,
					qualifierIndex+1,
				),
			)
			if err != nil {
				return nil, nil, err
			}
			if _, duplicate := qualifierUIs[qualifierUI]; duplicate {
				return nil, nil, fmt.Errorf(
					"duplicate PubMed MeSH qualifier UI %q in heading %d",
					qualifierUI,
					headingIndex+1,
				)
			}
			qualifierUIs[qualifierUI] = struct{}{}
			qualifiers[qualifierIndex] = biomedicalMeSHQualifierAssertion{
				UI:    qualifierUI,
				Label: qualifierLabel,
				SourcePath: fmt.Sprintf(
					"/PubmedArticle/MedlineCitation/MeshHeadingList/"+
						"MeshHeading[%d]/QualifierName[%d]",
					headingIndex+1,
					qualifierIndex+1,
				),
				MajorTopic: qualifier.MajorTopic,
			}
		}
		headings[headingIndex] = biomedicalMeSHHeadingAssertion{
			UI:    descriptorUI,
			Label: descriptorLabel,
			SourcePath: fmt.Sprintf(
				"/PubmedArticle/MedlineCitation/MeshHeadingList/"+
					"MeshHeading[%d]/DescriptorName",
				headingIndex+1,
			),
			MajorTopic: heading.Descriptor.MajorTopic,
			Qualifiers: qualifiers,
		}
	}

	publicationTypes := make(
		[]biomedicalPublicationTypeAssertion,
		len(record.PublicationTypes),
	)
	publicationTypeUIs := make(
		map[string]struct{},
		len(record.PublicationTypes),
	)
	for typeIndex, publicationType := range record.PublicationTypes {
		publicationTypeUI, publicationTypeLabel, err := normalizedBiomedicalIdentity(
			publicationType.UI,
			publicationType.Name,
			fmt.Sprintf("PubMed Publication Type %d", typeIndex+1),
		)
		if err != nil {
			return nil, nil, err
		}
		if _, duplicate := publicationTypeUIs[publicationTypeUI]; duplicate {
			return nil, nil, fmt.Errorf(
				"duplicate PubMed Publication Type UI %q",
				publicationTypeUI,
			)
		}
		publicationTypeUIs[publicationTypeUI] = struct{}{}
		publicationTypes[typeIndex] = biomedicalPublicationTypeAssertion{
			UI:    publicationTypeUI,
			Label: publicationTypeLabel,
			SourcePath: fmt.Sprintf(
				"/PubmedArticle/MedlineCitation/Article/"+
					"PublicationTypeList/PublicationType[%d]",
				typeIndex+1,
			),
		}
	}
	return headings, publicationTypes, nil
}

func resolveBiomedicalCanonicalIDs(
	ctx context.Context,
	tx pgx.Tx,
	headings []biomedicalMeSHHeadingAssertion,
	publicationTypes []biomedicalPublicationTypeAssertion,
) (map[biomedicalCanonicalIdentity]string, error) {
	unique := make(map[biomedicalCanonicalIdentity]struct{})
	for _, heading := range headings {
		unique[biomedicalCanonicalIdentity{
			Kind: "mesh descriptor",
			UI:   heading.UI,
		}] = struct{}{}
		for _, qualifier := range heading.Qualifiers {
			unique[biomedicalCanonicalIdentity{
				Kind: "mesh qualifier",
				UI:   qualifier.UI,
			}] = struct{}{}
		}
	}
	for _, publicationType := range publicationTypes {
		unique[biomedicalCanonicalIdentity{
			Kind: "publication type",
			UI:   publicationType.UI,
		}] = struct{}{}
	}

	ordered := make([]biomedicalCanonicalIdentity, 0, len(unique))
	for identity := range unique {
		ordered = append(ordered, identity)
	}
	slices.SortFunc(ordered, func(left, right biomedicalCanonicalIdentity) int {
		if compared := strings.Compare(left.Kind, right.Kind); compared != 0 {
			return compared
		}
		return strings.Compare(left.UI, right.UI)
	})

	resolved := make(map[biomedicalCanonicalIdentity]string, len(ordered))
	for _, identity := range ordered {
		id, err := resolveBiomedicalCanonicalID(
			ctx,
			tx,
			identity.Kind,
			identity.UI,
		)
		if err != nil {
			return nil, err
		}
		resolved[identity] = id
	}
	return resolved, nil
}

func normalizedBiomedicalIdentity(
	rawUI string,
	rawLabel string,
	identityName string,
) (string, string, error) {
	ui := strings.TrimSpace(rawUI)
	if ui == "" {
		return "", "", fmt.Errorf("%s UI is required", identityName)
	}
	label := strings.TrimSpace(rawLabel)
	if label == "" {
		return "", "", fmt.Errorf("%s source label is required", identityName)
	}
	return ui, label, nil
}

func resolveBiomedicalCanonicalID(
	ctx context.Context,
	tx pgx.Tx,
	kind string,
	ui string,
) (string, error) {
	var insertQuery, selectQuery string
	switch kind {
	case "mesh descriptor":
		insertQuery = `
			INSERT INTO mesh_descriptors (descriptor_ui)
			VALUES ($1)
			ON CONFLICT (descriptor_ui) DO NOTHING
			RETURNING id::text
		`
		selectQuery = `
			SELECT id::text
			FROM mesh_descriptors
			WHERE descriptor_ui = $1
		`
	case "mesh qualifier":
		insertQuery = `
			INSERT INTO mesh_qualifiers (qualifier_ui)
			VALUES ($1)
			ON CONFLICT (qualifier_ui) DO NOTHING
			RETURNING id::text
		`
		selectQuery = `
			SELECT id::text
			FROM mesh_qualifiers
			WHERE qualifier_ui = $1
		`
	case "publication type":
		insertQuery = `
			INSERT INTO publication_types (publication_type_ui)
			VALUES ($1)
			ON CONFLICT (publication_type_ui) DO NOTHING
			RETURNING id::text
		`
		selectQuery = `
			SELECT id::text
			FROM publication_types
			WHERE publication_type_ui = $1
		`
	default:
		return "", fmt.Errorf("unsupported biomedical canonical kind %q", kind)
	}

	var id string
	err := tx.QueryRow(ctx, insertQuery, ui).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("insert canonical %s %q: %w", kind, ui, err)
	}
	if err := tx.QueryRow(ctx, selectQuery, ui).Scan(&id); err != nil {
		return "", fmt.Errorf("read canonical %s %q: %w", kind, ui, err)
	}
	return id, nil
}

func persistMeSHHeadingAssertion(
	ctx context.Context,
	tx pgx.Tx,
	projectionAssertionID string,
	sourceRecordID string,
	workID string,
	descriptorID string,
	heading biomedicalMeSHHeadingAssertion,
) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO work_mesh_headings (
			projection_assertion_id,
			source_record_id,
			work_id,
			descriptor_id,
			source_path,
			descriptor_label,
			is_major_topic
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (projection_assertion_id, descriptor_id) DO NOTHING
		RETURNING id::text
	`,
		projectionAssertionID,
		sourceRecordID,
		workID,
		descriptorID,
		heading.SourcePath,
		heading.Label,
		heading.MajorTopic,
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("persist PubMed MeSH heading %q: %w", heading.UI, err)
	}

	var existingSourceRecordID, existingWorkID, sourcePath, label string
	var majorTopic bool
	if err := tx.QueryRow(ctx, `
		SELECT
			id::text,
			source_record_id::text,
			work_id::text,
			source_path,
			descriptor_label,
			is_major_topic
		FROM work_mesh_headings
		WHERE projection_assertion_id = $1
		  AND descriptor_id = $2
	`,
		projectionAssertionID,
		descriptorID,
	).Scan(
		&id,
		&existingSourceRecordID,
		&existingWorkID,
		&sourcePath,
		&label,
		&majorTopic,
	); err != nil {
		return "", fmt.Errorf("read replayed PubMed MeSH heading %q: %w", heading.UI, err)
	}
	if existingSourceRecordID != sourceRecordID ||
		existingWorkID != workID ||
		sourcePath != heading.SourcePath ||
		label != heading.Label ||
		majorTopic != heading.MajorTopic {
		return "", fmt.Errorf(
			"PubMed MeSH heading %q replay conflicts with immutable assertion",
			heading.UI,
		)
	}
	return id, nil
}

func persistMeSHQualifierAssertion(
	ctx context.Context,
	tx pgx.Tx,
	projectionAssertionID string,
	sourceRecordID string,
	workID string,
	headingID string,
	qualifierID string,
	qualifier biomedicalMeSHQualifierAssertion,
) error {
	command, err := tx.Exec(ctx, `
		INSERT INTO work_mesh_qualifiers (
			projection_assertion_id,
			source_record_id,
			work_id,
			work_mesh_heading_id,
			qualifier_id,
			source_path,
			qualifier_label,
			is_major_topic
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (work_mesh_heading_id, qualifier_id) DO NOTHING
	`,
		projectionAssertionID,
		sourceRecordID,
		workID,
		headingID,
		qualifierID,
		qualifier.SourcePath,
		qualifier.Label,
		qualifier.MajorTopic,
	)
	if err != nil {
		return fmt.Errorf("persist PubMed MeSH qualifier %q: %w", qualifier.UI, err)
	}
	if command.RowsAffected() == 1 {
		return nil
	}

	var existingProjectionAssertionID, existingSourceRecordID, existingWorkID string
	var sourcePath, label string
	var majorTopic bool
	if err := tx.QueryRow(ctx, `
		SELECT
			projection_assertion_id::text,
			source_record_id::text,
			work_id::text,
			source_path,
			qualifier_label,
			is_major_topic
		FROM work_mesh_qualifiers
		WHERE work_mesh_heading_id = $1
		  AND qualifier_id = $2
	`,
		headingID,
		qualifierID,
	).Scan(
		&existingProjectionAssertionID,
		&existingSourceRecordID,
		&existingWorkID,
		&sourcePath,
		&label,
		&majorTopic,
	); err != nil {
		return fmt.Errorf("read replayed PubMed MeSH qualifier %q: %w", qualifier.UI, err)
	}
	if existingProjectionAssertionID != projectionAssertionID ||
		existingSourceRecordID != sourceRecordID ||
		existingWorkID != workID ||
		sourcePath != qualifier.SourcePath ||
		label != qualifier.Label ||
		majorTopic != qualifier.MajorTopic {
		return fmt.Errorf(
			"PubMed MeSH qualifier %q replay conflicts with immutable assertion",
			qualifier.UI,
		)
	}
	return nil
}

func persistPublicationTypeAssertion(
	ctx context.Context,
	tx pgx.Tx,
	projectionAssertionID string,
	sourceRecordID string,
	workID string,
	publicationTypeID string,
	publicationType biomedicalPublicationTypeAssertion,
) error {
	command, err := tx.Exec(ctx, `
		INSERT INTO work_publication_types (
			projection_assertion_id,
			source_record_id,
			work_id,
			publication_type_id,
			source_path,
			publication_type_label
		) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (projection_assertion_id, publication_type_id) DO NOTHING
	`,
		projectionAssertionID,
		sourceRecordID,
		workID,
		publicationTypeID,
		publicationType.SourcePath,
		publicationType.Label,
	)
	if err != nil {
		return fmt.Errorf(
			"persist PubMed Publication Type %q: %w",
			publicationType.UI,
			err,
		)
	}
	if command.RowsAffected() == 1 {
		return nil
	}

	var existingSourceRecordID, existingWorkID, sourcePath, label string
	if err := tx.QueryRow(ctx, `
		SELECT
			source_record_id::text,
			work_id::text,
			source_path,
			publication_type_label
		FROM work_publication_types
		WHERE projection_assertion_id = $1
		  AND publication_type_id = $2
	`,
		projectionAssertionID,
		publicationTypeID,
	).Scan(
		&existingSourceRecordID,
		&existingWorkID,
		&sourcePath,
		&label,
	); err != nil {
		return fmt.Errorf(
			"read replayed PubMed Publication Type %q: %w",
			publicationType.UI,
			err,
		)
	}
	if existingSourceRecordID != sourceRecordID ||
		existingWorkID != workID ||
		sourcePath != publicationType.SourcePath ||
		label != publicationType.Label {
		return fmt.Errorf(
			"PubMed Publication Type %q replay conflicts with immutable assertion",
			publicationType.UI,
		)
	}
	return nil
}

func lockSourceState(
	ctx context.Context,
	tx pgx.Tx,
	logicalSource string,
	eventKey string,
) (string, error) {
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended(
				'ingestion_source_state:' || $1 || ':' || $2,
				0
			)
		)
	`, logicalSource, eventKey); err != nil {
		return "", fmt.Errorf("lock ingestion source state key: %w", err)
	}

	var workID string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(work_id::text, '')
		FROM ingestion_source_states
		WHERE logical_source = $1 AND event_key = $2
		FOR UPDATE
	`, logicalSource, eventKey).Scan(&workID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lock ingestion source state row: %w", err)
	}
	return workID, nil
}

type sourceState struct {
	LogicalSource           string
	EventKey                string
	NormalizedAssertionID   string
	RawEventID              string
	SourceRecordUUID        string
	WorkID                  string
	SourceTime              time.Time
	TieBreakKey             string
	Position                int64
	ScopeStatus             source.ScopeStatus
	ScopePolicyVersion      string
	ProjectionPolicyVersion string
	IsDeleted               bool
}

func upsertSourceState(
	ctx context.Context,
	tx pgx.Tx,
	state sourceState,
) (bool, error) {
	command, err := tx.Exec(ctx, `
		INSERT INTO ingestion_source_states (
			logical_source,
			event_key,
			raw_event_id,
			source_record_uuid,
			work_id,
			source_time,
			tie_break_key,
			position,
			scope_status,
			scope_policy_version,
			projection_policy_version,
			normalized_assertion_id,
			is_deleted
		) VALUES (
			$1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid,
			$6, $7, $8, $9, $10, $11, NULLIF($12, '')::uuid, $13
		)
		ON CONFLICT (logical_source, event_key)
		DO UPDATE SET
			raw_event_id = EXCLUDED.raw_event_id,
			source_record_uuid = EXCLUDED.source_record_uuid,
			work_id = COALESCE(EXCLUDED.work_id, ingestion_source_states.work_id),
			source_time = EXCLUDED.source_time,
			tie_break_key = EXCLUDED.tie_break_key,
			position = EXCLUDED.position,
			scope_status = EXCLUDED.scope_status,
			scope_policy_version = EXCLUDED.scope_policy_version,
			projection_policy_version = EXCLUDED.projection_policy_version,
			normalized_assertion_id = EXCLUDED.normalized_assertion_id,
			is_deleted = EXCLUDED.is_deleted,
			updated_at = now()
		WHERE (
			EXCLUDED.source_time,
			EXCLUDED.tie_break_key,
			EXCLUDED.position,
			EXCLUDED.raw_event_id
		) > (
			ingestion_source_states.source_time,
			ingestion_source_states.tie_break_key,
			ingestion_source_states.position,
			ingestion_source_states.raw_event_id
		)
		OR (
			EXCLUDED.raw_event_id = ingestion_source_states.raw_event_id
			AND (
				EXCLUDED.source_record_uuid,
				COALESCE(EXCLUDED.work_id, ingestion_source_states.work_id),
				EXCLUDED.scope_status,
				EXCLUDED.scope_policy_version,
				EXCLUDED.projection_policy_version,
				EXCLUDED.normalized_assertion_id,
				EXCLUDED.is_deleted
			) IS DISTINCT FROM (
				ingestion_source_states.source_record_uuid,
				ingestion_source_states.work_id,
				ingestion_source_states.scope_status,
				ingestion_source_states.scope_policy_version,
				ingestion_source_states.projection_policy_version,
				ingestion_source_states.normalized_assertion_id,
				ingestion_source_states.is_deleted
			)
		)
	`,
		state.LogicalSource,
		state.EventKey,
		state.RawEventID,
		state.SourceRecordUUID,
		state.WorkID,
		state.SourceTime.UTC(),
		state.TieBreakKey,
		state.Position,
		state.ScopeStatus,
		state.ScopePolicyVersion,
		state.ProjectionPolicyVersion,
		state.NormalizedAssertionID,
		state.IsDeleted,
	)
	if err != nil {
		return false, fmt.Errorf("upsert current ingestion source state: %w", err)
	}
	return command.RowsAffected() == 1, nil
}

func applyWinningProjection(
	ctx context.Context,
	tx pgx.Tx,
	workID string,
) (bool, error) {
	var (
		normalizedAssertionID string
		rawEventID            string
		sourceRecordID        string
		ingestionJobID        string
		sourceTime            time.Time
		tieBreakKey           string
		position              int64
		scopePolicy           string
		projectionPolicy      string
		payload               []byte
	)
	err := tx.QueryRow(ctx, `
		SELECT
			state.normalized_assertion_id::text,
			state.raw_event_id::text,
			state.source_record_uuid::text,
			assertion.job_id::text,
			state.source_time,
			state.tie_break_key,
			state.position,
			state.scope_policy_version,
			state.projection_policy_version,
			assertion.record_payload
		FROM ingestion_source_states AS state
		JOIN ingestion_projection_assertions AS assertion
		  ON assertion.normalized_assertion_id = state.normalized_assertion_id
		 AND assertion.raw_event_id = state.raw_event_id
		 AND assertion.work_id = state.work_id
		 AND assertion.scope_policy_version = state.scope_policy_version
		 AND assertion.projection_policy_version = state.projection_policy_version
		WHERE state.work_id = $1
		  AND state.scope_status = 'included'
		  AND NOT state.is_deleted
		ORDER BY
			state.source_time DESC,
			state.tie_break_key DESC,
			state.position DESC,
			state.raw_event_id DESC
		LIMIT 1
	`, workID).Scan(
		&normalizedAssertionID,
		&rawEventID,
		&sourceRecordID,
		&ingestionJobID,
		&sourceTime,
		&tieBreakKey,
		&position,
		&scopePolicy,
		&projectionPolicy,
		&payload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx, `
			DELETE FROM work_projection_states WHERE work_id = $1
		`, workID); err != nil {
			return false, fmt.Errorf("clear work projection state: %w", err)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("select winning work projection: %w", err)
	}

	var (
		currentNormalizedAssertionID string
		currentRawEventID            string
		currentSourceRecordID        string
		currentScopePolicy           string
		currentProjectionPolicy      string
	)
	err = tx.QueryRow(ctx, `
		SELECT
			normalized_assertion_id::text,
			raw_event_id::text,
			source_record_uuid::text,
			scope_policy_version,
			projection_policy_version
		FROM work_projection_states
		WHERE work_id = $1
	`, workID).Scan(
		&currentNormalizedAssertionID,
		&currentRawEventID,
		&currentSourceRecordID,
		&currentScopePolicy,
		&currentProjectionPolicy,
	)
	if err == nil &&
		currentNormalizedAssertionID == normalizedAssertionID &&
		currentRawEventID == rawEventID &&
		currentSourceRecordID == sourceRecordID &&
		currentScopePolicy == scopePolicy &&
		currentProjectionPolicy == projectionPolicy {
		return false, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("read current work projection state: %w", err)
	}

	var record persistedRecordPayload
	if err := json.Unmarshal(payload, &record); err != nil {
		return false, fmt.Errorf("decode winning work projection: %w", err)
	}
	if err := applyRecordProjection(
		ctx,
		tx,
		workID,
		sourceRecordID,
		ingestionJobID,
		record,
	); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_projection_states (
			work_id,
			normalized_assertion_id,
			raw_event_id,
			source_record_uuid,
			source_time,
			tie_break_key,
			position,
			scope_policy_version,
			projection_policy_version
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (work_id)
		DO UPDATE SET
			normalized_assertion_id = EXCLUDED.normalized_assertion_id,
			raw_event_id = EXCLUDED.raw_event_id,
			source_record_uuid = EXCLUDED.source_record_uuid,
			source_time = EXCLUDED.source_time,
			tie_break_key = EXCLUDED.tie_break_key,
			position = EXCLUDED.position,
			scope_policy_version = EXCLUDED.scope_policy_version,
			projection_policy_version = EXCLUDED.projection_policy_version,
			updated_at = now()
	`,
		workID,
		normalizedAssertionID,
		rawEventID,
		sourceRecordID,
		sourceTime,
		tieBreakKey,
		position,
		scopePolicy,
		projectionPolicy,
	); err != nil {
		return false, fmt.Errorf("persist work projection state: %w", err)
	}
	return true, nil
}

func applyRecordProjection(
	ctx context.Context,
	tx pgx.Tx,
	workID string,
	sourceRecordID string,
	ingestionJobID string,
	record persistedRecordPayload,
) error {
	status := paper.WorkStatusActive
	if record.Retracted != nil && *record.Retracted {
		status = paper.WorkStatusRetracted
	}
	venueID, err := resolveVenue(ctx, tx, record.Venue)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE works
		SET
			status = CASE
				WHEN works.status = 'active' THEN $2
				ELSE works.status
			END,
			title = $3,
			abstract = NULLIF($4, ''),
			published_at = $5,
			venue_id = NULLIF($6, '')::uuid,
			updated_at = now()
		WHERE id = $1
	`, workID, status, strings.TrimSpace(record.Title), record.Abstract, record.PublishedAt, venueID); err != nil {
		return fmt.Errorf("apply canonical work fields: %w", err)
	}
	if err := replaceAuthors(ctx, tx, workID, record.Authors); err != nil {
		return err
	}
	if err := replaceTopics(ctx, tx, workID, sourceRecordID, record.Topics); err != nil {
		return err
	}
	if err := persistCitationSnapshot(
		ctx,
		tx,
		workID,
		sourceRecordID,
		ingestionJobID,
		record.Source,
		record.CitedByCount,
	); err != nil {
		return err
	}
	if err := replaceCodeRepositories(ctx, tx, workID, record.CodeURLs); err != nil {
		return err
	}
	return persistFieldAssertions(ctx, tx, workID, sourceRecordID, record)
}

func resolveVenue(
	ctx context.Context,
	tx pgx.Tx,
	venue *source.Venue,
) (string, error) {
	if venue == nil {
		return "", nil
	}
	venueType := normalizedVenueType(venue.Type)
	if venueType == "" || strings.TrimSpace(venue.DisplayName) == "" {
		return "", nil
	}
	matches := make(map[string]struct{})
	if venue.OpenAlexID != "" {
		var id string
		err := tx.QueryRow(ctx, `
			SELECT id::text
			FROM venues
			WHERE source_scheme = $1
			  AND source_identifier = $2
		`, source.OpenAlex, venue.OpenAlexID).Scan(&id)
		if err == nil {
			matches[id] = struct{}{}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("resolve venue source identifier: %w", err)
		}
	}

	identifiers := make([]string, 0, 1+len(venue.ISSN)+len(venue.ISSNDetails))
	appendISSN := func(value string) {
		normalized := strings.ToUpper(strings.TrimSpace(value))
		if normalized != "" && !slices.Contains(identifiers, normalized) {
			identifiers = append(identifiers, normalized)
		}
	}
	appendISSN(venue.ISSNL)
	for _, issn := range venue.ISSN {
		appendISSN(issn)
	}
	for _, detail := range venue.ISSNDetails {
		appendISSN(detail.Value)
	}
	if len(identifiers) > 0 {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT id::text
			FROM venues
			WHERE
				issn_l = ANY($1::text[])
				OR issn = ANY($1::text[])
				OR eissn = ANY($1::text[])
			ORDER BY id
		`, identifiers)
		if err != nil {
			return "", fmt.Errorf("resolve venue ISSNs: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return "", fmt.Errorf("scan venue ISSN match: %w", err)
			}
			matches[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return "", fmt.Errorf("iterate venue ISSN matches: %w", err)
		}
	}
	if len(matches) > 1 {
		return "", errors.New("venue identifiers resolve to multiple venues")
	}
	if len(matches) == 1 {
		for id := range matches {
			return id, nil
		}
	}
	var id string
	sourceScheme := ""
	sourceIdentifier := ""
	if venue.OpenAlexID != "" {
		sourceScheme = source.OpenAlex
		sourceIdentifier = venue.OpenAlexID
	}
	var issn, eissn string
	for _, detail := range venue.ISSNDetails {
		switch detail.Type {
		case "Print":
			issn = detail.Value
		case "Electronic":
			eissn = detail.Value
		}
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO venues (
			venue_type,
			display_title,
			issn_l,
			issn,
			eissn,
			source_scheme,
			source_identifier
		) VALUES (
			$1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''),
			NULLIF($6, ''), NULLIF($7, '')
		)
		RETURNING id::text
	`,
		venueType,
		strings.TrimSpace(venue.DisplayName),
		strings.ToUpper(venue.ISSNL),
		strings.ToUpper(issn),
		strings.ToUpper(eissn),
		sourceScheme,
		sourceIdentifier,
	).Scan(&id); err != nil {
		return "", fmt.Errorf("insert venue: %w", err)
	}
	return id, nil
}

func normalizedVenueType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "journal":
		return "journal"
	case "conference":
		return "conference"
	case "preprint":
		return "preprint"
	case "repository":
		return "repository"
	default:
		return ""
	}
}

func replaceAuthors(
	ctx context.Context,
	tx pgx.Tx,
	workID string,
	authors []source.Author,
) error {
	if _, err := tx.Exec(ctx, "DELETE FROM work_authors WHERE work_id = $1", workID); err != nil {
		return fmt.Errorf("replace work authors: %w", err)
	}
	for index, author := range authors {
		displayName := strings.TrimSpace(author.DisplayName)
		if displayName == "" {
			displayName = strings.TrimSpace(author.CollectiveName)
		}
		if displayName == "" {
			continue
		}
		var authorID string
		if strings.TrimSpace(author.ORCID) != "" {
			err := tx.QueryRow(ctx, `
				INSERT INTO authors (display_name, orcid)
				VALUES ($1, $2)
				ON CONFLICT (orcid)
				DO UPDATE SET
					display_name = EXCLUDED.display_name,
					updated_at = now()
				RETURNING id::text
			`, displayName, author.ORCID).Scan(&authorID)
			if err != nil {
				return fmt.Errorf("upsert ORCID author: %w", err)
			}
		} else if err := tx.QueryRow(ctx, `
			INSERT INTO authors (display_name)
			VALUES ($1)
			RETURNING id::text
		`, displayName).Scan(&authorID); err != nil {
			return fmt.Errorf("insert author: %w", err)
		}
		position := author.Position
		if position <= 0 {
			position = index + 1
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_authors (
				work_id, author_id, author_position, is_corresponding
			) VALUES ($1, $2, $3, $4)
		`, workID, authorID, position, author.IsCorresponding); err != nil {
			return fmt.Errorf("link work author: %w", err)
		}
	}
	return nil
}

func replaceTopics(
	ctx context.Context,
	tx pgx.Tx,
	workID string,
	sourceRecordID string,
	topics []source.Topic,
) error {
	if _, err := tx.Exec(ctx, "DELETE FROM work_topics WHERE work_id = $1", workID); err != nil {
		return fmt.Errorf("replace work topics: %w", err)
	}
	for _, topic := range topics {
		name := strings.TrimSpace(topic.DisplayName)
		if name == "" {
			continue
		}
		var topicID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO topics (name)
			VALUES ($1)
			ON CONFLICT (name)
			DO UPDATE SET updated_at = topics.updated_at
			RETURNING id::text
		`, name).Scan(&topicID); err != nil {
			return fmt.Errorf("upsert topic: %w", err)
		}
		confidence := 1.0
		if topic.Score != nil {
			confidence = *topic.Score
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_topics (
				work_id, topic_id, confidence, source_record_id
			) VALUES ($1, $2, $3, $4)
		`, workID, topicID, confidence, sourceRecordID); err != nil {
			return fmt.Errorf("link work topic: %w", err)
		}
	}
	return nil
}

func persistCitationSnapshot(
	ctx context.Context,
	tx pgx.Tx,
	workID string,
	sourceRecordID string,
	ingestionJobID string,
	metricSource string,
	value *int,
) error {
	if value == nil {
		return nil
	}
	if *value < 0 {
		return errors.New("citation metric must not be negative")
	}
	definitionVersion, err := citationDefinitionVersion(metricSource)
	if err != nil {
		return err
	}
	datasetVersion := metricSource + "-source-record/" + sourceRecordID

	var insertedID string
	err = tx.QueryRow(ctx, `
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
		)
		SELECT
			$1,
			source_record.source,
			source_record.source_time,
			$2,
			source_record.id,
			$3,
			source_record.retrieved_at,
			1,
			$4,
			$5
		FROM source_records AS source_record
		JOIN ingestion_projection_assertions AS projection
		  ON projection.source_record_uuid = source_record.id
		 AND projection.work_id = $1
		 AND projection.job_id = $3
		WHERE source_record.id = $6
		  AND source_record.source = $7
		ON CONFLICT (work_id, source, observed_at)
		DO NOTHING
		RETURNING id::text
	`,
		workID,
		*value,
		ingestionJobID,
		definitionVersion,
		datasetVersion,
		sourceRecordID,
		metricSource,
	).Scan(&insertedID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("persist citation snapshot: %w", err)
	}

	var replayMatches bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM citation_snapshots
			WHERE work_id = $1
			  AND source = $2
			  AND observed_at = (
				  SELECT source_time
				  FROM source_records
				  WHERE id = $3
			  )
			  AND count = $4
			  AND source_record_id = $3
			  AND ingestion_job_id = $5
			  AND retrieved_at = (
				  SELECT retrieved_at
				  FROM source_records
				  WHERE id = $3
			  )
			  AND coverage = 1
			  AND definition_version = $6
			  AND dataset_version = $7
		)
	`,
		workID,
		metricSource,
		sourceRecordID,
		*value,
		ingestionJobID,
		definitionVersion,
		datasetVersion,
	).Scan(&replayMatches); err != nil {
		return fmt.Errorf("verify citation snapshot replay: %w", err)
	}
	if !replayMatches {
		return errors.New(
			"citation snapshot replay conflicts with immutable evidence",
		)
	}
	return nil
}

func citationDefinitionVersion(metricSource string) (string, error) {
	switch metricSource {
	case source.OpenAlex:
		return "openalex-cited-by-count/v1", nil
	default:
		return "", fmt.Errorf(
			"citation count source %q has no registered evidence definition",
			metricSource,
		)
	}
}

func replaceCodeRepositories(
	ctx context.Context,
	tx pgx.Tx,
	workID string,
	codeURLs []string,
) error {
	if _, err := tx.Exec(
		ctx,
		"DELETE FROM work_code_repositories WHERE work_id = $1",
		workID,
	); err != nil {
		return fmt.Errorf("replace work code repositories: %w", err)
	}
	for _, rawURL := range codeURLs {
		parsed, err := url.Parse(rawURL)
		if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") {
			continue
		}
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		canonicalURL := "https://github.com/" + parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
		var repositoryID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO code_repositories (
				canonical_url, host, owner_name, repository_name
			) VALUES ($1, 'github.com', $2, $3)
			ON CONFLICT (canonical_url)
			DO UPDATE SET updated_at = code_repositories.updated_at
			RETURNING id::text
		`, canonicalURL, parts[0], strings.TrimSuffix(parts[1], ".git")).Scan(&repositoryID); err != nil {
			return fmt.Errorf("upsert code repository: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_code_repositories (
				work_id, repository_id, relation_type
			) VALUES ($1, $2, 'official')
		`, workID, repositoryID); err != nil {
			return fmt.Errorf("link work code repository: %w", err)
		}
	}
	return nil
}

func persistFieldAssertions(
	ctx context.Context,
	tx pgx.Tx,
	workID string,
	sourceRecordID string,
	record persistedRecordPayload,
) error {
	assertions := []struct {
		name  string
		state string
		value any
	}{
		{name: "title", state: knowledgeState(record.Title != ""), value: record.Title},
		{name: "abstract", state: knowledgeState(record.Abstract != ""), value: record.Abstract},
		{name: "published_at", state: knowledgeState(record.PublishedAt != nil), value: record.PublishedAt},
		{name: "authors", state: listKnowledgeState(record.Authors != nil), value: record.Authors},
		{name: "topics", state: listKnowledgeState(record.Topics != nil), value: record.Topics},
		{name: "venue", state: knowledgeState(record.Venue != nil), value: record.Venue},
		{name: "code_urls", state: listKnowledgeState(record.CodeURLs != nil), value: record.CodeURLs},
		{name: "citation_count", state: knowledgeState(record.CitedByCount != nil), value: record.CitedByCount},
	}
	for _, assertion := range assertions {
		encoded, err := json.Marshal(map[string]any{
			"state": assertion.state,
			"value": assertion.value,
		})
		if err != nil {
			return fmt.Errorf("encode %s field assertion: %w", assertion.name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO field_assertions (
				work_id, source_record_id, field_name, asserted_value
			) VALUES ($1, $2, $3, $4)
		`, workID, sourceRecordID, assertion.name, encoded); err != nil {
			return fmt.Errorf("persist %s field assertion: %w", assertion.name, err)
		}
	}
	return nil
}

func knowledgeState(known bool) string {
	if known {
		return "known"
	}
	return "missing"
}

func listKnowledgeState(covered bool) string {
	if covered {
		return "known"
	}
	return "unknown"
}

func incrementProjectionCounter(
	ctx context.Context,
	tx pgx.Tx,
	jobID string,
	status ProjectionStatus,
) error {
	column := ""
	switch status {
	case ProjectionStatusProjected:
		column = "projected"
	case ProjectionStatusExcluded:
		column = "excluded"
	case ProjectionStatusDeleted:
		column = "deleted"
	case ProjectionStatusUnchanged:
		column = "unchanged"
	default:
		return fmt.Errorf("unsupported projection counter status %q", status)
	}
	command, err := tx.Exec(ctx, `
		UPDATE ingestion_jobs
		SET `+column+` = `+column+` + 1, updated_at = now()
		WHERE id = $1
	`, jobID)
	if err != nil {
		return fmt.Errorf("increment ingestion projection counter: %w", err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("ingestion job %s was not found", jobID)
	}
	return nil
}

func sourceRecordJSON(raw source.RawRecord) ([]byte, error) {
	var object map[string]any
	if err := json.Unmarshal(raw.Payload, &object); err == nil && object != nil {
		return json.Marshal(object)
	}
	if _, err := source.NewRawXMLRecord(raw.Payload); err != nil {
		return nil, fmt.Errorf("encode source record raw payload: %w", err)
	}
	return json.Marshal(map[string]any{
		"format":         "xml",
		"payload_base64": base64.StdEncoding.EncodeToString(raw.Payload),
	})
}

func cloneRepositoryTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

func cloneRepositoryInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneRepositoryBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneRepositoryVenue(value *source.Venue) *source.Venue {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.ISSN = append([]string(nil), value.ISSN...)
	cloned.ISSNDetails = append([]source.VenueISSN(nil), value.ISSNDetails...)
	return &cloned
}

func dereferenceString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
