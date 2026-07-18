ALTER TABLE ingestion_projection_assertions
    ADD CONSTRAINT ingestion_projection_assertions_exact_revision_key
        UNIQUE (
            id,
            normalized_assertion_id,
            source_record_uuid,
            work_id
        );

CREATE TABLE abstract_route_analysis_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL,
    projection_assertion_id uuid NOT NULL,
    normalized_assertion_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    source_name text NOT NULL,
    source_record_external_id text NOT NULL,
    parser_version text NOT NULL,
    source_time timestamptz NOT NULL,
    model_provider text NOT NULL,
    api_mode text NOT NULL,
    requested_model text NOT NULL,
    actual_model text,
    prompt_version text NOT NULL,
    prompt_text text NOT NULL,
    prompt_sha256 char(64) NOT NULL,
    schema_name text NOT NULL,
    schema_version text NOT NULL,
    schema_json jsonb NOT NULL,
    schema_sha256 char(64) NOT NULL,
    input_title text NOT NULL,
    title_sha256 char(64) NOT NULL,
    input_abstract text NOT NULL,
    abstract_sha256 char(64) NOT NULL,
    input_sha256 char(64) NOT NULL,
    response_id text,
    usage jsonb,
    input_tokens bigint,
    output_tokens bigint,
    total_tokens bigint,
    output_payload jsonb,
    status text NOT NULL,
    failure_code text,
    started_at timestamptz NOT NULL,
    lease_expires_at timestamptz NOT NULL,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT abstract_route_analysis_runs_exact_revision_fkey
        FOREIGN KEY (
            projection_assertion_id,
            normalized_assertion_id,
            source_record_id,
            work_id
        )
        REFERENCES ingestion_projection_assertions(
            id,
            normalized_assertion_id,
            source_record_uuid,
            work_id
        )
        ON DELETE RESTRICT,
    CONSTRAINT abstract_route_analysis_runs_source_name_check
        CHECK (btrim(source_name) <> '' AND source_name = btrim(source_name)),
    CONSTRAINT abstract_route_analysis_runs_source_external_id_check
        CHECK (
            btrim(source_record_external_id) <> ''
            AND source_record_external_id = btrim(source_record_external_id)
        ),
    CONSTRAINT abstract_route_analysis_runs_parser_version_check
        CHECK (
            btrim(parser_version) <> ''
            AND parser_version = btrim(parser_version)
        ),
    CONSTRAINT abstract_route_analysis_runs_provider_check
        CHECK (
            btrim(model_provider) <> ''
            AND model_provider = btrim(model_provider)
        ),
    CONSTRAINT abstract_route_analysis_runs_api_mode_check
        CHECK (api_mode IN ('responses', 'chat_completions')),
    CONSTRAINT abstract_route_analysis_runs_requested_model_check
        CHECK (
            btrim(requested_model) <> ''
            AND requested_model = btrim(requested_model)
        ),
    CONSTRAINT abstract_route_analysis_runs_actual_model_check
        CHECK (
            actual_model IS NULL
            OR (
                btrim(actual_model) <> ''
                AND actual_model = btrim(actual_model)
            )
        ),
    CONSTRAINT abstract_route_analysis_runs_prompt_version_check
        CHECK (
            btrim(prompt_version) <> ''
            AND prompt_version = btrim(prompt_version)
        ),
    CONSTRAINT abstract_route_analysis_runs_prompt_text_check
        CHECK (btrim(prompt_text) <> ''),
    CONSTRAINT abstract_route_analysis_runs_schema_name_check
        CHECK (
            btrim(schema_name) <> ''
            AND schema_name = btrim(schema_name)
        ),
    CONSTRAINT abstract_route_analysis_runs_schema_version_check
        CHECK (
            btrim(schema_version) <> ''
            AND schema_version = btrim(schema_version)
        ),
    CONSTRAINT abstract_route_analysis_runs_schema_json_check
        CHECK (
            jsonb_typeof(schema_json) = 'object'
            AND schema_json <> '{}'::jsonb
        ),
    CONSTRAINT abstract_route_analysis_runs_input_title_check
        CHECK (btrim(input_title) <> ''),
    CONSTRAINT abstract_route_analysis_runs_input_abstract_check
        CHECK (btrim(input_abstract) <> ''),
    CONSTRAINT abstract_route_analysis_runs_sha256_check
        CHECK (
            prompt_sha256 ~ '^[0-9a-f]{64}$'
            AND schema_sha256 ~ '^[0-9a-f]{64}$'
            AND title_sha256 ~ '^[0-9a-f]{64}$'
            AND abstract_sha256 ~ '^[0-9a-f]{64}$'
            AND input_sha256 ~ '^[0-9a-f]{64}$'
        ),
    CONSTRAINT abstract_route_analysis_runs_response_id_check
        CHECK (
            response_id IS NULL
            OR (
                btrim(response_id) <> ''
                AND response_id = btrim(response_id)
            )
        ),
    CONSTRAINT abstract_route_analysis_runs_usage_check
        CHECK (
            (
                usage IS NULL
                AND input_tokens IS NULL
                AND output_tokens IS NULL
                AND total_tokens IS NULL
            )
            OR
            (
                jsonb_typeof(usage) = 'object'
                AND input_tokens >= 0
                AND output_tokens >= 0
                AND total_tokens = input_tokens + output_tokens
            )
        ),
    CONSTRAINT abstract_route_analysis_runs_output_check
        CHECK (
            output_payload IS NULL
            OR jsonb_typeof(output_payload) = 'object'
        ),
    CONSTRAINT abstract_route_analysis_runs_status_check
        CHECK (status IN ('running', 'succeeded', 'failed')),
    CONSTRAINT abstract_route_analysis_runs_lease_check
        CHECK (lease_expires_at > started_at),
    CONSTRAINT abstract_route_analysis_runs_failure_code_check
        CHECK (
            failure_code IS NULL
            OR (
                btrim(failure_code) <> ''
                AND failure_code = btrim(failure_code)
            )
        ),
    CONSTRAINT abstract_route_analysis_runs_state_shape_check
        CHECK (
            (
                status = 'running'
                AND actual_model IS NULL
                AND response_id IS NULL
                AND usage IS NULL
                AND input_tokens IS NULL
                AND output_tokens IS NULL
                AND total_tokens IS NULL
                AND output_payload IS NULL
                AND failure_code IS NULL
                AND completed_at IS NULL
            )
            OR
            (
                status = 'succeeded'
                AND actual_model IS NOT NULL
                AND response_id IS NOT NULL
                AND usage IS NOT NULL
                AND input_tokens IS NOT NULL
                AND output_tokens IS NOT NULL
                AND total_tokens IS NOT NULL
                AND output_payload IS NOT NULL
                AND failure_code IS NULL
                AND completed_at IS NOT NULL
                AND completed_at >= started_at
            )
            OR
            (
                status = 'failed'
                AND output_payload IS NULL
                AND failure_code IS NOT NULL
                AND completed_at IS NOT NULL
                AND completed_at >= started_at
            )
        )
);

CREATE UNIQUE INDEX abstract_route_analysis_runs_active_or_succeeded
    ON abstract_route_analysis_runs(
        normalized_assertion_id,
        prompt_version,
        schema_version,
        api_mode,
        requested_model
    )
    WHERE status IN ('running', 'succeeded');

CREATE INDEX abstract_route_analysis_runs_work_history
    ON abstract_route_analysis_runs(
        work_id,
        started_at DESC,
        id DESC
    );

CREATE FUNCTION reject_terminal_abstract_route_analysis_run_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status IN ('succeeded', 'failed') THEN
        RAISE EXCEPTION
            'terminal abstract route analysis run % is immutable',
            OLD.id
            USING ERRCODE = '55000';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER abstract_route_analysis_runs_terminal_immutable
BEFORE UPDATE OR DELETE ON abstract_route_analysis_runs
FOR EACH ROW
EXECUTE FUNCTION reject_terminal_abstract_route_analysis_run_mutation();
