CREATE TABLE connector_watermarks (
    source text NOT NULL,
    stream text NOT NULL,
    watermark_kind text NOT NULL,
    watermark_value timestamptz NOT NULL,
    version bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source, stream),
    CONSTRAINT connector_watermarks_source_check
        CHECK (btrim(source) <> '' AND source = btrim(source)),
    CONSTRAINT connector_watermarks_stream_check
        CHECK (btrim(stream) <> '' AND stream = btrim(stream)),
    CONSTRAINT connector_watermarks_kind_check
        CHECK (watermark_kind = 'timestamp'),
    CONSTRAINT connector_watermarks_version_check
        CHECK (version >= 0)
);

CREATE TABLE connector_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source text NOT NULL,
    stream text NOT NULL,
    watermark_kind text NOT NULL,
    claimed_from timestamptz NOT NULL,
    claimed_until timestamptz NOT NULL,
    expected_watermark_version bigint NOT NULL,
    idempotency_key text NOT NULL,
    status text NOT NULL,
    failure_stage text,
    failure_code text,
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT connector_runs_source_check
        CHECK (btrim(source) <> '' AND source = btrim(source)),
    CONSTRAINT connector_runs_stream_check
        CHECK (btrim(stream) <> '' AND stream = btrim(stream)),
    CONSTRAINT connector_runs_kind_check
        CHECK (watermark_kind = 'timestamp'),
    CONSTRAINT connector_runs_interval_check
        CHECK (claimed_from < claimed_until),
    CONSTRAINT connector_runs_expected_version_check
        CHECK (expected_watermark_version >= 0),
    CONSTRAINT connector_runs_idempotency_check
        CHECK (
            btrim(idempotency_key) <> ''
            AND idempotency_key = btrim(idempotency_key)
        ),
    CONSTRAINT connector_runs_status_check
        CHECK (status IN ('running', 'succeeded', 'failed')),
    CONSTRAINT connector_runs_terminal_shape_check
        CHECK (
            (
                status = 'running'
                AND completed_at IS NULL
                AND failure_stage IS NULL
                AND failure_code IS NULL
            )
            OR
            (
                status = 'succeeded'
                AND completed_at IS NOT NULL
                AND completed_at >= started_at
                AND failure_stage IS NULL
                AND failure_code IS NULL
            )
            OR
            (
                status = 'failed'
                AND completed_at IS NOT NULL
                AND completed_at >= started_at
                AND btrim(failure_stage) <> ''
                AND failure_stage = btrim(failure_stage)
                AND btrim(failure_code) <> ''
                AND failure_code = btrim(failure_code)
            )
        )
);

CREATE UNIQUE INDEX connector_runs_active_idempotency
    ON connector_runs(idempotency_key)
    WHERE status = 'running';

CREATE UNIQUE INDEX connector_runs_active_stream
    ON connector_runs(source, stream)
    WHERE status = 'running';

CREATE INDEX connector_runs_stream_history
    ON connector_runs(source, stream, started_at DESC, id DESC);

CREATE TABLE connector_run_pages (
    run_id uuid NOT NULL
        REFERENCES connector_runs(id)
        ON DELETE RESTRICT,
    page_ordinal integer NOT NULL,
    cursor_in text NOT NULL,
    cursor_out text NOT NULL,
    content_sha256 char(64) NOT NULL,
    record_count integer NOT NULL,
    fetched_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, page_ordinal),
    CONSTRAINT connector_run_pages_ordinal_check CHECK (page_ordinal > 0),
    CONSTRAINT connector_run_pages_cursor_in_check
        CHECK (cursor_in <> ''),
    CONSTRAINT connector_run_pages_sha256_check
        CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT connector_run_pages_record_count_check
        CHECK (record_count >= 0)
);

CREATE TRIGGER connector_run_pages_immutable
BEFORE UPDATE OR DELETE ON connector_run_pages
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();
