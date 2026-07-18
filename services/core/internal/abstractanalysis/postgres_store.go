package abstractanalysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/openairesponses"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New(
			"abstract analysis PostgreSQL pool is required",
		)
	}
	return &PostgresStore{pool: pool}, nil
}

func (store *PostgresStore) Candidates(
	ctx context.Context,
	selection Selection,
) ([]Candidate, error) {
	if store == nil || store.pool == nil {
		return nil, errors.New("abstract analysis PostgreSQL store is nil")
	}
	if err := selection.Validate(); err != nil {
		return nil, err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf(
			"begin abstract analysis candidate selection: %w",
			err,
		)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `
		UPDATE abstract_route_analysis_runs
		SET
			status = 'failed',
			failure_code = $1,
			completed_at = lease_expires_at
		WHERE status = 'running'
		  AND lease_expires_at <= now()
	`, FailureLeaseExpired); err != nil {
		return nil, fmt.Errorf(
			"reclaim expired abstract analysis leases: %w",
			err,
		)
	}
	rows, err := tx.Query(ctx, `
		SELECT
			state.work_id::text,
			assertion.id::text,
			state.normalized_assertion_id::text,
			state.source_record_uuid::text,
			source_record.source,
			source_record.source_record_id,
			normalized.normalized_payload->>'parser_version',
			state.source_time,
			normalized.normalized_payload->>'title',
			normalized.normalized_payload->>'abstract'
		FROM work_projection_states AS state
		JOIN ingestion_projection_assertions AS assertion
		  ON assertion.work_id = state.work_id
		 AND assertion.normalized_assertion_id =
		     state.normalized_assertion_id
		 AND assertion.source_record_uuid = state.source_record_uuid
		 AND assertion.scope_policy_version =
		     state.scope_policy_version
		 AND assertion.projection_policy_version =
		     state.projection_policy_version
		JOIN ingestion_normalized_records AS normalized
		  ON normalized.id = state.normalized_assertion_id
		 AND normalized.source_record_uuid = state.source_record_uuid
		JOIN source_records AS source_record
		  ON source_record.id = state.source_record_uuid
		WHERE state.source_time <= $1
		  AND normalized.payload_schema_version = 'normalized-record/v4'
		  AND btrim(
		        COALESCE(normalized.normalized_payload->>'parser_version', '')
		      ) <> ''
		  AND btrim(
		        COALESCE(normalized.normalized_payload->>'title', '')
		      ) <> ''
		  AND btrim(
		        COALESCE(normalized.normalized_payload->>'abstract', '')
		      ) <> ''
		  AND NOT EXISTS (
		      SELECT 1
		      FROM abstract_route_analysis_runs AS existing
		      WHERE existing.normalized_assertion_id =
		            state.normalized_assertion_id
		        AND existing.prompt_version = $2
		        AND existing.schema_version = $3
		        AND existing.api_mode = $4
		        AND existing.requested_model = $5
		        AND existing.status IN ('running', 'succeeded')
		  )
		ORDER BY
			state.source_time DESC,
			state.work_id,
			assertion.id
		LIMIT $6
	`,
		selection.Cutoff,
		selection.PromptVersion,
		selection.SchemaVersion,
		selection.APIMode,
		selection.RequestedModel,
		selection.Limit,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"query abstract analysis candidates: %w",
			err,
		)
	}
	defer rows.Close()

	candidates := make([]Candidate, 0)
	for rows.Next() {
		var candidate Candidate
		if err := rows.Scan(
			&candidate.WorkID,
			&candidate.ProjectionAssertionID,
			&candidate.NormalizedAssertionID,
			&candidate.SourceRecordID,
			&candidate.Source,
			&candidate.SourceRecordExternalID,
			&candidate.ParserVersion,
			&candidate.SourceTime,
			&candidate.Title,
			&candidate.Abstract,
		); err != nil {
			return nil, fmt.Errorf(
				"scan abstract analysis candidate: %w",
				err,
			)
		}
		candidate.SourceTime = candidate.SourceTime.UTC()
		if err := candidate.Validate(); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterate abstract analysis candidates: %w",
			err,
		)
	}
	rows.Close()
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf(
			"commit abstract analysis candidate selection: %w",
			err,
		)
	}
	return candidates, nil
}

func (store *PostgresStore) Start(
	ctx context.Context,
	descriptor RunDescriptor,
) (Run, error) {
	if store == nil || store.pool == nil {
		return Run{}, errors.New("abstract analysis PostgreSQL store is nil")
	}
	if err := descriptor.Validate(); err != nil {
		return Run{}, err
	}
	var id string
	if err := store.pool.QueryRow(ctx, `
		INSERT INTO abstract_route_analysis_runs (
			work_id,
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			source_name,
			source_record_external_id,
			parser_version,
			source_time,
			model_provider,
			api_mode,
			requested_model,
			prompt_version,
			prompt_text,
			prompt_sha256,
			schema_name,
			schema_version,
			schema_json,
			schema_sha256,
			input_title,
			title_sha256,
			input_abstract,
			abstract_sha256,
			input_sha256,
			status,
			started_at,
			lease_expires_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14, $15, $16,
			$17::jsonb, $18, $19, $20, $21, $22,
			$23, 'running', $24, $25
		)
		RETURNING id::text
	`,
		descriptor.Candidate.WorkID,
		descriptor.Candidate.ProjectionAssertionID,
		descriptor.Candidate.NormalizedAssertionID,
		descriptor.Candidate.SourceRecordID,
		descriptor.Candidate.Source,
		descriptor.Candidate.SourceRecordExternalID,
		descriptor.Candidate.ParserVersion,
		descriptor.Candidate.SourceTime,
		descriptor.ModelProvider,
		descriptor.APIMode,
		descriptor.RequestedModel,
		descriptor.PromptVersion,
		descriptor.PromptText,
		descriptor.PromptSHA256,
		descriptor.SchemaName,
		descriptor.SchemaVersion,
		string(descriptor.SchemaJSON),
		descriptor.SchemaSHA256,
		descriptor.Title,
		descriptor.TitleSHA256,
		descriptor.Abstract,
		descriptor.AbstractSHA256,
		descriptor.InputSHA256,
		descriptor.StartedAt,
		descriptor.LeaseExpiresAt,
	).Scan(&id); err != nil {
		return Run{}, fmt.Errorf("start abstract analysis run: %w", err)
	}
	return Run{
		ID:         id,
		Descriptor: descriptor,
	}, nil
}

func (store *PostgresStore) Succeed(
	ctx context.Context,
	run Run,
	success Success,
) error {
	if store == nil || store.pool == nil {
		return errors.New("abstract analysis PostgreSQL store is nil")
	}
	if err := validateSuccess(run, success); err != nil {
		return err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin abstract analysis success: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockPersistedRun(ctx, tx, run); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
		UPDATE abstract_route_analysis_runs
		SET
			actual_model = $2,
			response_id = $3,
			usage = $4::jsonb,
			input_tokens = $5,
			output_tokens = $6,
			total_tokens = $7,
			output_payload = $8::jsonb,
			status = 'succeeded',
			completed_at = $9
		WHERE id = $1
		  AND status = 'running'
	`,
		run.ID,
		success.ActualModel,
		success.ResponseID,
		string(success.Usage.Raw),
		success.Usage.InputTokens,
		success.Usage.OutputTokens,
		success.Usage.TotalTokens,
		string(success.OutputJSON),
		success.CompletedAt,
	)
	if err != nil {
		return fmt.Errorf("complete abstract analysis run: %w", err)
	}
	if command.RowsAffected() != 1 {
		return errors.New("abstract analysis run is not running")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit abstract analysis success: %w", err)
	}
	return nil
}

func (store *PostgresStore) Fail(
	ctx context.Context,
	run Run,
	failure Failure,
) error {
	if store == nil || store.pool == nil {
		return errors.New("abstract analysis PostgreSQL store is nil")
	}
	if err := validateFailure(run, failure); err != nil {
		return err
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin abstract analysis failure: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockPersistedRun(ctx, tx, run); err != nil {
		return err
	}

	var usage any
	var inputTokens, outputTokens, totalTokens any
	if len(failure.Usage.Raw) > 0 {
		usage = string(failure.Usage.Raw)
		inputTokens = failure.Usage.InputTokens
		outputTokens = failure.Usage.OutputTokens
		totalTokens = failure.Usage.TotalTokens
	}
	command, err := tx.Exec(ctx, `
		UPDATE abstract_route_analysis_runs
		SET
			actual_model = NULLIF($2, ''),
			response_id = NULLIF($3, ''),
			usage = $4::jsonb,
			input_tokens = $5,
			output_tokens = $6,
			total_tokens = $7,
			status = 'failed',
			failure_code = $8,
			completed_at = $9
		WHERE id = $1
		  AND status = 'running'
	`,
		run.ID,
		failure.ActualModel,
		failure.ResponseID,
		usage,
		inputTokens,
		outputTokens,
		totalTokens,
		failure.Code,
		failure.CompletedAt,
	)
	if err != nil {
		return fmt.Errorf("fail abstract analysis run: %w", err)
	}
	if command.RowsAffected() != 1 {
		return errors.New("abstract analysis run is not running")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit abstract analysis failure: %w", err)
	}
	return nil
}

func (descriptor RunDescriptor) Validate() error {
	expected, err := newRunDescriptor(
		descriptor.Candidate,
		descriptor.APIMode,
		descriptor.RequestedModel,
		descriptor.StartedAt,
	)
	if err != nil {
		return err
	}
	if descriptor.ModelProvider != expected.ModelProvider ||
		descriptor.APIMode != expected.APIMode ||
		descriptor.PromptVersion != expected.PromptVersion ||
		descriptor.PromptText != expected.PromptText ||
		descriptor.PromptSHA256 != expected.PromptSHA256 ||
		descriptor.SchemaName != expected.SchemaName ||
		descriptor.SchemaVersion != expected.SchemaVersion ||
		!bytes.Equal(descriptor.SchemaJSON, expected.SchemaJSON) ||
		descriptor.SchemaSHA256 != expected.SchemaSHA256 ||
		descriptor.Title != expected.Title ||
		descriptor.TitleSHA256 != expected.TitleSHA256 ||
		descriptor.Abstract != expected.Abstract ||
		descriptor.AbstractSHA256 != expected.AbstractSHA256 ||
		descriptor.InputSHA256 != expected.InputSHA256 ||
		!descriptor.StartedAt.Equal(expected.StartedAt) {
		return errors.New(
			"abstract analysis run descriptor is not canonical",
		)
	}
	if !descriptor.LeaseExpiresAt.Equal(expected.LeaseExpiresAt) {
		return errors.New(
			"abstract analysis run lease is not canonical",
		)
	}
	return nil
}

func validateSuccess(run Run, success Success) error {
	if _, err := uuid.Parse(run.ID); err != nil {
		return errors.New("abstract analysis run ID must be a UUID")
	}
	if err := run.Descriptor.Validate(); err != nil {
		return err
	}
	if success.ResponseID == "" ||
		success.ResponseID != strings.TrimSpace(success.ResponseID) {
		return errors.New(
			"abstract analysis success response ID is required and trimmed",
		)
	}
	if success.ActualModel == "" ||
		success.ActualModel != strings.TrimSpace(success.ActualModel) {
		return errors.New(
			"abstract analysis success actual model is required and trimmed",
		)
	}
	if err := validateUsage(success.Usage); err != nil {
		return err
	}
	decoded, err := DecodeResult(success.OutputJSON)
	if err != nil {
		return err
	}
	if err := ValidateEvidence(run.Descriptor.Abstract, decoded); err != nil {
		return err
	}
	if !success.CompletedAt.After(run.Descriptor.StartedAt) &&
		!success.CompletedAt.Equal(run.Descriptor.StartedAt) {
		return errors.New(
			"abstract analysis completion precedes start",
		)
	}
	return nil
}

func validateFailure(run Run, failure Failure) error {
	if _, err := uuid.Parse(run.ID); err != nil {
		return errors.New("abstract analysis run ID must be a UUID")
	}
	if err := run.Descriptor.Validate(); err != nil {
		return err
	}
	if failure.Code == "" ||
		failure.Code != strings.TrimSpace(failure.Code) {
		return errors.New(
			"abstract analysis failure code is required and trimmed",
		)
	}
	for name, value := range map[string]string{
		"response ID":  failure.ResponseID,
		"actual model": failure.ActualModel,
	} {
		if value != "" && value != strings.TrimSpace(value) {
			return fmt.Errorf(
				"abstract analysis failure %s must be trimmed",
				name,
			)
		}
	}
	if len(failure.Usage.Raw) > 0 {
		if err := validateUsage(failure.Usage); err != nil {
			return err
		}
	}
	if failure.CompletedAt.Before(run.Descriptor.StartedAt) {
		return errors.New(
			"abstract analysis failure completion precedes start",
		)
	}
	return nil
}

func validateUsage(usage openairesponses.Usage) error {
	if usage.InputTokens < 0 ||
		usage.OutputTokens < 0 ||
		usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		return errors.New("abstract analysis usage totals are invalid")
	}
	if len(usage.Raw) == 0 {
		return errors.New("abstract analysis usage JSON is required")
	}
	var object map[string]any
	if err := json.Unmarshal(usage.Raw, &object); err != nil ||
		object == nil {
		return errors.New(
			"abstract analysis usage must be a JSON object",
		)
	}
	return nil
}

func lockPersistedRun(
	ctx context.Context,
	tx pgx.Tx,
	run Run,
) error {
	var (
		normalizedAssertionID string
		apiMode               openairesponses.Mode
		requestedModel        string
		promptVersion         string
		schemaVersion         string
		startedAt             time.Time
		leaseExpiresAt        time.Time
		status                string
	)
	if err := tx.QueryRow(ctx, `
			SELECT
				normalized_assertion_id::text,
				api_mode,
				requested_model,
			prompt_version,
			schema_version,
			started_at,
			lease_expires_at,
			status
		FROM abstract_route_analysis_runs
		WHERE id = $1
		FOR UPDATE
		`, run.ID).Scan(
		&normalizedAssertionID,
		&apiMode,
		&requestedModel,
		&promptVersion,
		&schemaVersion,
		&startedAt,
		&leaseExpiresAt,
		&status,
	); err != nil {
		return fmt.Errorf("lock persisted abstract analysis run: %w", err)
	}
	if normalizedAssertionID != run.Descriptor.Candidate.NormalizedAssertionID ||
		apiMode != run.Descriptor.APIMode ||
		requestedModel != run.Descriptor.RequestedModel ||
		promptVersion != run.Descriptor.PromptVersion ||
		schemaVersion != run.Descriptor.SchemaVersion ||
		!startedAt.Equal(run.Descriptor.StartedAt) ||
		!leaseExpiresAt.Equal(run.Descriptor.LeaseExpiresAt) {
		return errors.New(
			"abstract analysis run conflicts with persisted descriptor",
		)
	}
	if status != "running" {
		return fmt.Errorf(
			"persisted abstract analysis run is not running: %s",
			status,
		)
	}
	return nil
}
