CREATE OR REPLACE FUNCTION require_unsealed_registry_parent()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    parent_id uuid;
    parent_sealed_at timestamptz;
    parent_found boolean := false;
BEGIN
    parent_id := (to_jsonb(NEW) ->> TG_ARGV[1])::uuid;
    EXECUTE format(
        'SELECT sealed_at, true
         FROM %I
         WHERE id = $1
         FOR SHARE',
        TG_ARGV[0]
    )
    INTO parent_sealed_at, parent_found
    USING parent_id;

    IF NOT parent_found OR parent_sealed_at IS NOT NULL THEN
        RAISE EXCEPTION
            '% cannot be inserted after its Registry receipt is sealed',
            TG_TABLE_NAME
            USING ERRCODE = '55000',
                  CONSTRAINT = TG_ARGV[2];
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION require_unsealed_conference_event_parent()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    parent_sealed_at timestamptz;
BEGIN
    SELECT version.sealed_at
    INTO parent_sealed_at
    FROM conference_series AS series
    JOIN conference_registry_versions AS version
      ON version.id = series.conference_registry_version_id
    WHERE series.id = NEW.conference_series_id
    FOR SHARE OF version;

    IF NOT FOUND OR parent_sealed_at IS NOT NULL THEN
        RAISE EXCEPTION
            'conference event cannot be inserted after its Registry receipt is sealed'
            USING ERRCODE = '55000',
                  CONSTRAINT = 'conference_events_parent_unsealed';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION require_unsealed_conference_entry_parent()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    event_version_id uuid;
    parent_sealed_at timestamptz;
BEGIN
    SELECT
        series.conference_registry_version_id,
        version.sealed_at
    INTO event_version_id, parent_sealed_at
    FROM conference_events AS event
    JOIN conference_series AS series
      ON series.id = event.conference_series_id
    JOIN conference_registry_versions AS version
      ON version.id = series.conference_registry_version_id
    WHERE event.id = NEW.conference_event_id
    FOR SHARE OF version;

    IF NOT FOUND
       OR event_version_id <> NEW.conference_registry_version_id
       OR parent_sealed_at IS NOT NULL THEN
        RAISE EXCEPTION
            'conference entry requires one matching unsealed Registry receipt'
            USING ERRCODE = '55000',
                  CONSTRAINT = 'conference_registry_entries_parent_unsealed';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION require_unsealed_conference_entry_child_parent()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    parent_sealed_at timestamptz;
BEGIN
    SELECT version.sealed_at
    INTO parent_sealed_at
    FROM conference_registry_entries AS entry
    JOIN conference_registry_versions AS version
      ON version.id = entry.conference_registry_version_id
    WHERE entry.id = NEW.conference_registry_entry_id
    FOR SHARE OF version;

    IF NOT FOUND OR parent_sealed_at IS NOT NULL THEN
        RAISE EXCEPTION
            '% cannot be inserted after its Registry receipt is sealed',
            TG_TABLE_NAME
            USING ERRCODE = '55000',
                  CONSTRAINT = TG_ARGV[0];
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER work_channel_admission_decisions_immutable
    ON work_channel_admission_decisions;

ALTER TABLE work_channel_admission_decisions
    DROP CONSTRAINT work_channel_admission_decisions_version_shape_check,
    DROP CONSTRAINT work_channel_admission_decisions_identity_key,
    ALTER COLUMN channel DROP NOT NULL;

ALTER TABLE work_channel_admission_decisions
    ADD CONSTRAINT work_channel_admission_decisions_channel_shape_check
        CHECK (
            (
                decision = 'missing'
                AND reason = 'channel_unresolved'
                AND channel IS NULL
            )
            OR (
                decision IN ('accepted', 'rejected')
                AND channel IS NOT NULL
            )
        ),
    ADD CONSTRAINT work_channel_admission_decisions_version_shape_check
        CHECK (
            (
                channel IS NULL
                AND journal_policy_version IS NULL
                AND channel_registry_version IS NULL
            )
            OR (
                channel IN ('journal_published', 'accepted_early')
                AND journal_policy_version = 'journal-all-q1/v2'
                AND channel_registry_version IS NULL
            )
            OR (
                channel = 'preprint'
                AND journal_policy_version IS NULL
                AND channel_registry_version = 'preprint-sources/v1'
            )
            OR (
                channel = 'conference_proceeding'
                AND journal_policy_version IS NULL
                AND channel_registry_version = 'conference-venues/v1'
            )
        ),
    ADD CONSTRAINT work_channel_admission_decisions_identity_key
        UNIQUE NULLS NOT DISTINCT (
            work_id,
            channel,
            admission_policy_version,
            source_record_id
        );

CREATE TRIGGER work_channel_admission_decisions_immutable
BEFORE UPDATE OR DELETE ON work_channel_admission_decisions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();
