ALTER TABLE ingestion_normalized_records
    ADD CONSTRAINT ingestion_normalized_records_id_source_record_key
        UNIQUE (id, source_record_uuid);

ALTER TABLE ingestion_projection_assertions
    ADD CONSTRAINT ingestion_projection_assertions_publication_provenance_key
        UNIQUE (
            id,
            normalized_assertion_id,
            source_record_uuid,
            work_id
        );

CREATE TABLE work_publication_event_assertions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    projection_assertion_id uuid NOT NULL,
    normalized_assertion_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    work_id uuid NOT NULL,
    event_kind text NOT NULL,
    event_date date,
    date_precision text NOT NULL,
    source_date jsonb NOT NULL,
    status_raw text NOT NULL,
    publication_model_raw text,
    source_path text NOT NULL,
    ordinal integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_publication_event_assertions_event_kind_check
        CHECK (
            event_kind IN (
                'print_published',
                'electronic_published',
                'ahead_of_print',
                'accepted'
            )
        ),
    CONSTRAINT work_publication_event_assertions_date_precision_check
        CHECK (date_precision IN ('year', 'month', 'day')),
    CONSTRAINT work_publication_event_assertions_date_shape_check
        CHECK (
            (
                date_precision = 'day'
                AND event_date IS NOT NULL
            )
            OR
            (
                date_precision IN ('year', 'month')
                AND event_date IS NULL
            )
        ),
    CONSTRAINT work_publication_event_assertions_source_date_check
        CHECK (
            jsonb_typeof(source_date) = 'object'
            AND source_date <> '{}'::jsonb
        ),
    CONSTRAINT work_publication_event_assertions_status_raw_check
        CHECK (
            btrim(status_raw) <> ''
            AND status_raw = btrim(status_raw)
        ),
    CONSTRAINT work_publication_event_assertions_publication_model_raw_check
        CHECK (
            publication_model_raw IS NULL
            OR (
                btrim(publication_model_raw) <> ''
                AND publication_model_raw = btrim(publication_model_raw)
            )
        ),
    CONSTRAINT work_publication_event_assertions_source_path_check
        CHECK (
            btrim(source_path) <> ''
            AND source_path = btrim(source_path)
        ),
    CONSTRAINT work_publication_event_assertions_ordinal_check
        CHECK (ordinal > 0),
    CONSTRAINT work_publication_event_assertions_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_publication_event_assertions_work_id_fkey
        FOREIGN KEY (work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_publication_event_assertions_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_publication_event_assertions_normalized_source_fkey
        FOREIGN KEY (normalized_assertion_id, source_record_id)
        REFERENCES ingestion_normalized_records(id, source_record_uuid)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_publication_event_assertions_projection_provenance_fkey
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
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_publication_event_assertions_projection_ordinal_key
        UNIQUE (projection_assertion_id, ordinal)
);

CREATE INDEX idx_work_publication_event_assertions_event_date
    ON work_publication_event_assertions(
        event_kind,
        event_date DESC,
        work_id
    );

CREATE INDEX idx_work_publication_event_assertions_work_projection
    ON work_publication_event_assertions(work_id, projection_assertion_id);

CREATE INDEX idx_work_publication_event_assertions_source_ordinal
    ON work_publication_event_assertions(source_record_id, ordinal);

CREATE TRIGGER work_publication_event_assertions_immutable
BEFORE UPDATE OR DELETE ON work_publication_event_assertions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_publication_states (
    work_id uuid PRIMARY KEY,
    projection_assertion_id uuid NOT NULL,
    normalized_assertion_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    print_published_on date,
    print_published_state text NOT NULL,
    electronic_published_on date,
    electronic_published_state text NOT NULL,
    ahead_of_print_on date,
    ahead_of_print_state text NOT NULL,
    accepted_on date,
    accepted_state text NOT NULL,
    publication_model_raw text,
    publication_status_raw text,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_publication_states_print_state_check
        CHECK (
            (
                print_published_state = 'known'
                AND print_published_on IS NOT NULL
            )
            OR
            (
                print_published_state IN ('missing', 'conflict')
                AND print_published_on IS NULL
            )
        ),
    CONSTRAINT work_publication_states_electronic_state_check
        CHECK (
            (
                electronic_published_state = 'known'
                AND electronic_published_on IS NOT NULL
            )
            OR
            (
                electronic_published_state IN ('missing', 'conflict')
                AND electronic_published_on IS NULL
            )
        ),
    CONSTRAINT work_publication_states_ahead_of_print_state_check
        CHECK (
            (
                ahead_of_print_state = 'known'
                AND ahead_of_print_on IS NOT NULL
            )
            OR
            (
                ahead_of_print_state IN ('missing', 'conflict')
                AND ahead_of_print_on IS NULL
            )
        ),
    CONSTRAINT work_publication_states_accepted_state_check
        CHECK (
            (
                accepted_state = 'known'
                AND accepted_on IS NOT NULL
            )
            OR
            (
                accepted_state IN ('missing', 'conflict')
                AND accepted_on IS NULL
            )
        ),
    CONSTRAINT work_publication_states_publication_model_raw_check
        CHECK (
            publication_model_raw IS NULL
            OR (
                btrim(publication_model_raw) <> ''
                AND publication_model_raw = btrim(publication_model_raw)
            )
        ),
    CONSTRAINT work_publication_states_publication_status_raw_check
        CHECK (
            publication_status_raw IS NULL
            OR (
                btrim(publication_status_raw) <> ''
                AND publication_status_raw = btrim(publication_status_raw)
            )
        ),
    CONSTRAINT work_publication_states_work_id_fkey
        FOREIGN KEY (work_id)
        REFERENCES works(id)
        ON DELETE CASCADE,
    CONSTRAINT work_publication_states_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_publication_states_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_publication_states_normalized_source_fkey
        FOREIGN KEY (normalized_assertion_id, source_record_id)
        REFERENCES ingestion_normalized_records(id, source_record_uuid)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_publication_states_projection_provenance_fkey
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
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED
);
