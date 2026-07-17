ALTER TABLE ingestion_normalized_records
    ADD COLUMN id uuid DEFAULT gen_random_uuid(),
    ADD COLUMN payload_schema_version text DEFAULT 'normalized-record/v1';

ALTER TABLE ingestion_normalized_records
    ALTER COLUMN id SET NOT NULL,
    ALTER COLUMN id SET DEFAULT gen_random_uuid(),
    ALTER COLUMN payload_schema_version SET NOT NULL,
    ALTER COLUMN payload_schema_version DROP DEFAULT,
    DROP CONSTRAINT ingestion_normalized_records_pkey,
    DROP CONSTRAINT ingestion_normalized_records_source_record_uuid_key,
    ADD CONSTRAINT ingestion_normalized_records_pkey
        PRIMARY KEY (id),
    ADD CONSTRAINT ingestion_normalized_records_payload_schema_check
        CHECK (btrim(payload_schema_version) <> ''),
    ADD CONSTRAINT ingestion_normalized_records_raw_policy_schema_key
        UNIQUE (
            raw_event_id,
            normalization_policy_version,
            payload_schema_version
        ),
    ADD CONSTRAINT ingestion_normalized_records_id_raw_source_key
        UNIQUE (id, raw_event_id, source_record_uuid);

ALTER TABLE ingestion_projection_assertions
    ADD COLUMN normalized_assertion_id uuid;

DROP TRIGGER ingestion_projection_assertions_immutable
    ON ingestion_projection_assertions;

UPDATE ingestion_projection_assertions AS assertion
SET normalized_assertion_id = normalized.id
FROM ingestion_normalized_records AS normalized
WHERE normalized.raw_event_id = assertion.raw_event_id
  AND normalized.source_record_uuid = assertion.source_record_uuid;

DO $$
DECLARE
    legacy_constraint text;
BEGIN
    SELECT constraint_row.conname
    INTO legacy_constraint
    FROM pg_constraint AS constraint_row
    WHERE constraint_row.conrelid = 'ingestion_projection_assertions'::regclass
      AND constraint_row.contype = 'u'
      AND pg_get_constraintdef(constraint_row.oid) =
          'UNIQUE (raw_event_id, scope_policy_version, projection_policy_version)';

    IF legacy_constraint IS NULL THEN
        RAISE EXCEPTION
            'legacy ingestion projection assertion uniqueness constraint is missing';
    END IF;

    EXECUTE format(
        'ALTER TABLE ingestion_projection_assertions DROP CONSTRAINT %I',
        legacy_constraint
    );
END;
$$;

ALTER TABLE ingestion_projection_assertions
    ALTER COLUMN normalized_assertion_id SET NOT NULL,
    ADD CONSTRAINT ingestion_projection_assertions_normalized_policy_key
        UNIQUE (
            normalized_assertion_id,
            scope_policy_version,
            projection_policy_version
        ),
    ADD CONSTRAINT ingestion_projection_assertions_normalized_assertion_fkey
        FOREIGN KEY (
            normalized_assertion_id,
            raw_event_id,
            source_record_uuid
        )
        REFERENCES ingestion_normalized_records(
            id,
            raw_event_id,
            source_record_uuid
        )
        ON DELETE RESTRICT;

CREATE TRIGGER ingestion_projection_assertions_immutable
BEFORE UPDATE OR DELETE ON ingestion_projection_assertions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE INDEX idx_ingestion_projection_assertions_normalized
    ON ingestion_projection_assertions(
        normalized_assertion_id,
        projected_at DESC,
        id DESC
    );

ALTER TABLE ingestion_source_states
    ADD COLUMN normalized_assertion_id uuid;

UPDATE ingestion_source_states AS state
SET normalized_assertion_id = normalized.id
FROM ingestion_normalized_records AS normalized
WHERE NOT state.is_deleted
  AND normalized.raw_event_id = state.raw_event_id
  AND normalized.source_record_uuid = state.source_record_uuid;

ALTER TABLE ingestion_source_states
    DROP CONSTRAINT ingestion_source_states_shape_check,
    ADD CONSTRAINT ingestion_source_states_shape_check
        CHECK (
            (
                is_deleted
                AND source_record_uuid IS NULL
                AND normalized_assertion_id IS NULL
            )
            OR
            (
                NOT is_deleted
                AND source_record_uuid IS NOT NULL
                AND normalized_assertion_id IS NOT NULL
            )
        ),
    ADD CONSTRAINT ingestion_source_states_normalized_assertion_fkey
        FOREIGN KEY (
            normalized_assertion_id,
            raw_event_id,
            source_record_uuid
        )
        REFERENCES ingestion_normalized_records(
            id,
            raw_event_id,
            source_record_uuid
        )
        ON DELETE RESTRICT;

ALTER TABLE work_projection_states
    ADD COLUMN normalized_assertion_id uuid;

UPDATE work_projection_states AS state
SET normalized_assertion_id = normalized.id
FROM ingestion_normalized_records AS normalized
WHERE normalized.raw_event_id = state.raw_event_id
  AND normalized.source_record_uuid = state.source_record_uuid;

ALTER TABLE work_projection_states
    ALTER COLUMN normalized_assertion_id SET NOT NULL,
    ADD CONSTRAINT work_projection_states_normalized_assertion_fkey
        FOREIGN KEY (
            normalized_assertion_id,
            raw_event_id,
            source_record_uuid
        )
        REFERENCES ingestion_normalized_records(
            id,
            raw_event_id,
            source_record_uuid
        )
        ON DELETE RESTRICT;
