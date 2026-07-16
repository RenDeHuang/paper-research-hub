ALTER TABLE ingestion_jobs
    ADD COLUMN batch_key text,
    ADD COLUMN stage text,
    ADD COLUMN raw_inserted bigint NOT NULL DEFAULT 0,
    ADD COLUMN raw_reused bigint NOT NULL DEFAULT 0,
    ADD COLUMN projected bigint NOT NULL DEFAULT 0,
    ADD COLUMN excluded bigint NOT NULL DEFAULT 0,
    ADD COLUMN deleted bigint NOT NULL DEFAULT 0,
    ADD COLUMN unchanged bigint NOT NULL DEFAULT 0,
    ADD COLUMN failed bigint NOT NULL DEFAULT 0;

UPDATE ingestion_jobs
SET
    batch_key = job_type,
    stage = 'batch'
WHERE batch_key IS NULL OR stage IS NULL;

ALTER TABLE ingestion_jobs
    ALTER COLUMN batch_key SET NOT NULL,
    ALTER COLUMN stage SET NOT NULL,
    ADD CONSTRAINT ingestion_jobs_batch_key_check
        CHECK (btrim(batch_key) <> ''),
    ADD CONSTRAINT ingestion_jobs_stage_check
        CHECK (stage IN ('batch', 'raw', 'normalize', 'policy', 'project', 'ranking')),
    ADD CONSTRAINT ingestion_jobs_counters_check
        CHECK (
            raw_inserted >= 0
            AND raw_reused >= 0
            AND projected >= 0
            AND excluded >= 0
            AND deleted >= 0
            AND unchanged >= 0
            AND failed >= 0
        );

CREATE TABLE ingestion_raw_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id uuid NOT NULL REFERENCES ingestion_jobs(id) ON DELETE RESTRICT,
    logical_source text NOT NULL,
    event_key text NOT NULL,
    event_kind text NOT NULL,
    source_record_id text,
    source_time timestamptz NOT NULL,
    tie_break_key text NOT NULL,
    position bigint NOT NULL,
    content_hash char(64) NOT NULL,
    raw_format text NOT NULL,
    raw_payload bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ingestion_raw_events_source_check
        CHECK (btrim(logical_source) <> ''),
    CONSTRAINT ingestion_raw_events_event_key_check
        CHECK (btrim(event_key) <> ''),
    CONSTRAINT ingestion_raw_events_kind_check
        CHECK (event_kind IN ('upsert', 'delete')),
    CONSTRAINT ingestion_raw_events_source_record_check
        CHECK (
            (event_kind = 'upsert' AND source_record_id IS NOT NULL AND btrim(source_record_id) <> '')
            OR
            (event_kind = 'delete' AND source_record_id IS NULL)
        ),
    CONSTRAINT ingestion_raw_events_tie_break_check
        CHECK (btrim(tie_break_key) <> ''),
    CONSTRAINT ingestion_raw_events_position_check
        CHECK (position >= 0),
    CONSTRAINT ingestion_raw_events_hash_check
        CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT ingestion_raw_events_format_check
        CHECK (raw_format IN ('json', 'xml')),
    CONSTRAINT ingestion_raw_events_payload_check
        CHECK (octet_length(raw_payload) > 0),
    UNIQUE (logical_source, event_key, event_kind, content_hash)
);

CREATE INDEX idx_ingestion_raw_events_job
    ON ingestion_raw_events(job_id, position, id);
CREATE INDEX idx_ingestion_raw_events_current_order
    ON ingestion_raw_events(
        logical_source,
        event_key,
        source_time DESC,
        tie_break_key DESC,
        position DESC,
        id DESC
    );

CREATE TRIGGER ingestion_raw_events_immutable
BEFORE UPDATE OR DELETE ON ingestion_raw_events
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE ingestion_normalized_records (
    raw_event_id uuid PRIMARY KEY
        REFERENCES ingestion_raw_events(id) ON DELETE RESTRICT,
    source_record_uuid uuid NOT NULL UNIQUE
        REFERENCES source_records(id) ON DELETE RESTRICT,
    normalization_policy_version text NOT NULL,
    normalized_payload jsonb NOT NULL,
    normalized_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ingestion_normalized_records_policy_check
        CHECK (btrim(normalization_policy_version) <> ''),
    CONSTRAINT ingestion_normalized_records_payload_check
        CHECK (
            jsonb_typeof(normalized_payload) = 'object'
            AND normalized_payload <> '{}'::jsonb
        )
);

CREATE TRIGGER ingestion_normalized_records_immutable
BEFORE UPDATE OR DELETE ON ingestion_normalized_records
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE ingestion_scope_decisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    raw_event_id uuid NOT NULL
        REFERENCES ingestion_raw_events(id) ON DELETE RESTRICT,
    job_id uuid NOT NULL REFERENCES ingestion_jobs(id) ON DELETE RESTRICT,
    policy_version text NOT NULL,
    status text NOT NULL,
    reason text NOT NULL,
    evidence jsonb NOT NULL,
    decided_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ingestion_scope_decisions_policy_check
        CHECK (btrim(policy_version) <> ''),
    CONSTRAINT ingestion_scope_decisions_status_check
        CHECK (status IN ('pending', 'included', 'excluded')),
    CONSTRAINT ingestion_scope_decisions_reason_check
        CHECK (btrim(reason) <> ''),
    CONSTRAINT ingestion_scope_decisions_evidence_check
        CHECK (jsonb_typeof(evidence) = 'array'),
    UNIQUE (raw_event_id, policy_version)
);

CREATE INDEX idx_ingestion_scope_decisions_current
    ON ingestion_scope_decisions(raw_event_id, policy_version, decided_at DESC);

CREATE TRIGGER ingestion_scope_decisions_immutable
BEFORE UPDATE OR DELETE ON ingestion_scope_decisions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE ingestion_projection_assertions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    raw_event_id uuid NOT NULL
        REFERENCES ingestion_raw_events(id) ON DELETE RESTRICT,
    source_record_uuid uuid NOT NULL
        REFERENCES source_records(id) ON DELETE RESTRICT,
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    job_id uuid NOT NULL REFERENCES ingestion_jobs(id) ON DELETE RESTRICT,
    scope_policy_version text NOT NULL,
    projection_policy_version text NOT NULL,
    record_payload jsonb NOT NULL,
    projected_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ingestion_projection_assertions_scope_policy_check
        CHECK (btrim(scope_policy_version) <> ''),
    CONSTRAINT ingestion_projection_assertions_projection_policy_check
        CHECK (btrim(projection_policy_version) <> ''),
    CONSTRAINT ingestion_projection_assertions_payload_check
        CHECK (
            jsonb_typeof(record_payload) = 'object'
            AND record_payload <> '{}'::jsonb
        ),
    UNIQUE (raw_event_id, scope_policy_version, projection_policy_version)
);

CREATE INDEX idx_ingestion_projection_assertions_work
    ON ingestion_projection_assertions(work_id, projected_at DESC, id DESC);

CREATE TRIGGER ingestion_projection_assertions_immutable
BEFORE UPDATE OR DELETE ON ingestion_projection_assertions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE ingestion_source_states (
    logical_source text NOT NULL,
    event_key text NOT NULL,
    raw_event_id uuid NOT NULL
        REFERENCES ingestion_raw_events(id) ON DELETE RESTRICT,
    source_record_uuid uuid REFERENCES source_records(id) ON DELETE RESTRICT,
    work_id uuid REFERENCES works(id) ON DELETE RESTRICT,
    source_time timestamptz NOT NULL,
    tie_break_key text NOT NULL,
    position bigint NOT NULL,
    scope_status text NOT NULL,
    scope_policy_version text NOT NULL,
    projection_policy_version text NOT NULL,
    is_deleted boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (logical_source, event_key),
    CONSTRAINT ingestion_source_states_source_check
        CHECK (btrim(logical_source) <> ''),
    CONSTRAINT ingestion_source_states_event_key_check
        CHECK (btrim(event_key) <> ''),
    CONSTRAINT ingestion_source_states_tie_break_check
        CHECK (btrim(tie_break_key) <> ''),
    CONSTRAINT ingestion_source_states_position_check
        CHECK (position >= 0),
    CONSTRAINT ingestion_source_states_scope_check
        CHECK (scope_status IN ('pending', 'included', 'excluded')),
    CONSTRAINT ingestion_source_states_scope_policy_check
        CHECK (btrim(scope_policy_version) <> ''),
    CONSTRAINT ingestion_source_states_projection_policy_check
        CHECK (btrim(projection_policy_version) <> ''),
    CONSTRAINT ingestion_source_states_shape_check
        CHECK (
            (is_deleted AND source_record_uuid IS NULL)
            OR
            (NOT is_deleted AND source_record_uuid IS NOT NULL)
        )
);

CREATE INDEX idx_ingestion_source_states_work_visibility
    ON ingestion_source_states(work_id, scope_status, is_deleted)
    WHERE work_id IS NOT NULL;

CREATE TABLE work_projection_states (
    work_id uuid PRIMARY KEY REFERENCES works(id) ON DELETE CASCADE,
    raw_event_id uuid NOT NULL
        REFERENCES ingestion_raw_events(id) ON DELETE RESTRICT,
    source_record_uuid uuid NOT NULL
        REFERENCES source_records(id) ON DELETE RESTRICT,
    source_time timestamptz NOT NULL,
    tie_break_key text NOT NULL,
    position bigint NOT NULL,
    scope_policy_version text NOT NULL,
    projection_policy_version text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_projection_states_tie_break_check
        CHECK (btrim(tie_break_key) <> ''),
    CONSTRAINT work_projection_states_position_check
        CHECK (position >= 0),
    CONSTRAINT work_projection_states_scope_policy_check
        CHECK (btrim(scope_policy_version) <> ''),
    CONSTRAINT work_projection_states_projection_policy_check
        CHECK (btrim(projection_policy_version) <> '')
);

CREATE TABLE ingestion_side_effect_failures (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    job_id uuid NOT NULL REFERENCES ingestion_jobs(id) ON DELETE RESTRICT,
    hook text NOT NULL,
    logical_source text NOT NULL,
    event_key text NOT NULL,
    stage text NOT NULL,
    message text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ingestion_side_effect_failures_hook_check
        CHECK (btrim(hook) <> ''),
    CONSTRAINT ingestion_side_effect_failures_source_check
        CHECK (btrim(logical_source) <> ''),
    CONSTRAINT ingestion_side_effect_failures_event_check
        CHECK (btrim(event_key) <> ''),
    CONSTRAINT ingestion_side_effect_failures_stage_check
        CHECK (stage IN ('batch', 'raw', 'normalize', 'policy', 'project', 'ranking')),
    CONSTRAINT ingestion_side_effect_failures_message_check
        CHECK (btrim(message) <> '')
);

CREATE INDEX idx_ingestion_side_effect_failures_job
    ON ingestion_side_effect_failures(job_id, created_at, id);
