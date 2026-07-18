ALTER TABLE domain_versions
    ADD COLUMN sealed_at timestamptz,
    ADD CONSTRAINT domain_versions_sealed_at_check
        CHECK (sealed_at IS NULL OR sealed_at >= imported_at);

ALTER TABLE preprint_source_versions
    ADD COLUMN sealed_at timestamptz,
    ADD CONSTRAINT preprint_source_versions_sealed_at_check
        CHECK (sealed_at IS NULL OR sealed_at >= imported_at);

ALTER TABLE conference_registry_versions
    ADD COLUMN sealed_at timestamptz,
    ADD CONSTRAINT conference_registry_versions_sealed_at_check
        CHECK (sealed_at IS NULL OR sealed_at >= imported_at);

DROP TRIGGER domain_versions_content_integrity ON domain_versions;
DROP TRIGGER preprint_source_versions_content_integrity
    ON preprint_source_versions;
DROP TRIGGER conference_registry_versions_content_integrity
    ON conference_registry_versions;

DROP FUNCTION enforce_domain_registry_receipt_integrity();
DROP FUNCTION enforce_preprint_registry_receipt_integrity();
DROP FUNCTION enforce_conference_registry_receipt_integrity();

DROP TRIGGER domain_versions_immutable ON domain_versions;
DROP TRIGGER preprint_source_versions_immutable ON preprint_source_versions;
DROP TRIGGER conference_registry_versions_immutable
    ON conference_registry_versions;

CREATE FUNCTION require_registry_receipt_insert_unsealed()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.sealed_at IS NOT NULL THEN
        RAISE EXCEPTION
            '% receipt must be inserted unsealed',
            TG_TABLE_NAME
            USING ERRCODE = '55000',
                  CONSTRAINT = TG_ARGV[0];
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION require_registry_receipt_sealed()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    current_sealed_at timestamptz;
BEGIN
    EXECUTE format(
        'SELECT sealed_at FROM %I WHERE id = $1',
        TG_TABLE_NAME
    )
    INTO current_sealed_at
    USING NEW.id;

    IF current_sealed_at IS NULL THEN
        RAISE EXCEPTION
            '% receipt % must be sealed before commit',
            TG_TABLE_NAME,
            NEW.id
            USING ERRCODE = '23514',
                  CONSTRAINT = TG_ARGV[0];
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION seal_domain_registry_receipt()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_domain_count integer;
    actual_rule_count integer;
BEGIN
    IF OLD.sealed_at IS NOT NULL
       OR NEW.sealed_at IS NULL
       OR (to_jsonb(NEW) - 'sealed_at') <>
          (to_jsonb(OLD) - 'sealed_at') THEN
        RAISE EXCEPTION
            'domain Registry receipt % only permits one NULL-to-non-NULL seal',
            OLD.id
            USING ERRCODE = '55000',
                  CONSTRAINT = 'domain_versions_seal';
    END IF;

    SELECT count(*)
    INTO actual_domain_count
    FROM research_domains
    WHERE domain_version_id = OLD.id;

    SELECT count(*)
    INTO actual_rule_count
    FROM domain_category_rules
    WHERE domain_version_id = OLD.id;

    IF actual_domain_count <> NEW.domain_count
       OR actual_rule_count <> NEW.rule_count THEN
        RAISE EXCEPTION
            'domain Registry receipt % content mismatch: domains %/%, rules %/%',
            NEW.id,
            actual_domain_count,
            NEW.domain_count,
            actual_rule_count,
            NEW.rule_count
            USING ERRCODE = '23514',
                  CONSTRAINT = 'domain_versions_seal';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION seal_preprint_registry_receipt()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_source_count integer;
    actual_rule_count integer;
BEGIN
    IF OLD.sealed_at IS NOT NULL
       OR NEW.sealed_at IS NULL
       OR (to_jsonb(NEW) - 'sealed_at') <>
          (to_jsonb(OLD) - 'sealed_at') THEN
        RAISE EXCEPTION
            'preprint Registry receipt % only permits one NULL-to-non-NULL seal',
            OLD.id
            USING ERRCODE = '55000',
                  CONSTRAINT = 'preprint_source_versions_seal';
    END IF;

    SELECT
        count(*),
        COALESCE(sum(cardinality(allowed_domains)), 0)
    INTO actual_source_count, actual_rule_count
    FROM trusted_preprint_sources
    WHERE preprint_source_version_id = OLD.id;

    IF actual_source_count <> NEW.source_count
       OR actual_rule_count <> NEW.rule_count THEN
        RAISE EXCEPTION
            'preprint Registry receipt % content mismatch: sources %/%, rules %/%',
            NEW.id,
            actual_source_count,
            NEW.source_count,
            actual_rule_count,
            NEW.rule_count
            USING ERRCODE = '23514',
                  CONSTRAINT = 'preprint_source_versions_seal';
    END IF;
    RETURN NEW;
END;
$$;

CREATE FUNCTION seal_conference_registry_receipt()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_series_count integer;
    actual_event_count integer;
    actual_entry_count integer;
    actual_identifier_count integer;
    actual_host_count integer;
    actual_rule_count integer;
BEGIN
    IF OLD.sealed_at IS NOT NULL
       OR NEW.sealed_at IS NULL
       OR (to_jsonb(NEW) - 'sealed_at') <>
          (to_jsonb(OLD) - 'sealed_at') THEN
        RAISE EXCEPTION
            'conference Registry receipt % only permits one NULL-to-non-NULL seal',
            OLD.id
            USING ERRCODE = '55000',
                  CONSTRAINT = 'conference_registry_versions_seal';
    END IF;

    SELECT count(*)
    INTO actual_series_count
    FROM conference_series
    WHERE conference_registry_version_id = OLD.id;

    SELECT count(*)
    INTO actual_event_count
    FROM conference_events AS event
    JOIN conference_series AS series
      ON series.id = event.conference_series_id
    WHERE series.conference_registry_version_id = OLD.id;

    SELECT
        count(*),
        COALESCE(sum(cardinality(entry.allowed_domains)), 0)
    INTO actual_entry_count, actual_rule_count
    FROM conference_registry_entries AS entry
    WHERE entry.conference_registry_version_id = OLD.id;

    SELECT count(*)
    INTO actual_identifier_count
    FROM conference_identifiers AS identifier
    JOIN conference_registry_entries AS entry
      ON entry.id = identifier.conference_registry_entry_id
    WHERE entry.conference_registry_version_id = OLD.id;

    SELECT count(*)
    INTO actual_host_count
    FROM conference_official_hosts AS host
    JOIN conference_registry_entries AS entry
      ON entry.id = host.conference_registry_entry_id
    WHERE entry.conference_registry_version_id = OLD.id;

    IF actual_series_count <> NEW.series_count
       OR actual_event_count <> NEW.event_count
       OR actual_entry_count <> NEW.event_count
       OR actual_identifier_count <> NEW.event_count
       OR actual_host_count <> NEW.event_count
       OR actual_rule_count <> NEW.rule_count THEN
        RAISE EXCEPTION
            'conference Registry receipt % content mismatch',
            NEW.id
            USING ERRCODE = '23514',
                  CONSTRAINT = 'conference_registry_versions_seal';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER domain_versions_insert_unsealed
BEFORE INSERT ON domain_versions
FOR EACH ROW
EXECUTE FUNCTION require_registry_receipt_insert_unsealed(
    'domain_versions_insert_unsealed'
);

CREATE TRIGGER domain_versions_seal
BEFORE UPDATE ON domain_versions
FOR EACH ROW
EXECUTE FUNCTION seal_domain_registry_receipt();

CREATE TRIGGER domain_versions_delete_immutable
BEFORE DELETE ON domain_versions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE CONSTRAINT TRIGGER domain_versions_must_be_sealed
AFTER INSERT ON domain_versions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION require_registry_receipt_sealed(
    'domain_versions_must_be_sealed'
);

CREATE TRIGGER preprint_source_versions_insert_unsealed
BEFORE INSERT ON preprint_source_versions
FOR EACH ROW
EXECUTE FUNCTION require_registry_receipt_insert_unsealed(
    'preprint_source_versions_insert_unsealed'
);

CREATE TRIGGER preprint_source_versions_seal
BEFORE UPDATE ON preprint_source_versions
FOR EACH ROW
EXECUTE FUNCTION seal_preprint_registry_receipt();

CREATE TRIGGER preprint_source_versions_delete_immutable
BEFORE DELETE ON preprint_source_versions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE CONSTRAINT TRIGGER preprint_source_versions_must_be_sealed
AFTER INSERT ON preprint_source_versions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION require_registry_receipt_sealed(
    'preprint_source_versions_must_be_sealed'
);

CREATE TRIGGER conference_registry_versions_insert_unsealed
BEFORE INSERT ON conference_registry_versions
FOR EACH ROW
EXECUTE FUNCTION require_registry_receipt_insert_unsealed(
    'conference_registry_versions_insert_unsealed'
);

CREATE TRIGGER conference_registry_versions_seal
BEFORE UPDATE ON conference_registry_versions
FOR EACH ROW
EXECUTE FUNCTION seal_conference_registry_receipt();

CREATE TRIGGER conference_registry_versions_delete_immutable
BEFORE DELETE ON conference_registry_versions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE CONSTRAINT TRIGGER conference_registry_versions_must_be_sealed
AFTER INSERT ON conference_registry_versions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION require_registry_receipt_sealed(
    'conference_registry_versions_must_be_sealed'
);

CREATE FUNCTION require_unsealed_registry_parent()
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
        'SELECT sealed_at, true FROM %I WHERE id = $1',
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

CREATE TRIGGER research_domains_parent_unsealed
BEFORE INSERT ON research_domains
FOR EACH ROW
EXECUTE FUNCTION require_unsealed_registry_parent(
    'domain_versions',
    'domain_version_id',
    'research_domains_parent_unsealed'
);

CREATE TRIGGER domain_category_rules_parent_unsealed
BEFORE INSERT ON domain_category_rules
FOR EACH ROW
EXECUTE FUNCTION require_unsealed_registry_parent(
    'domain_versions',
    'domain_version_id',
    'domain_category_rules_parent_unsealed'
);

CREATE TRIGGER trusted_preprint_sources_parent_unsealed
BEFORE INSERT ON trusted_preprint_sources
FOR EACH ROW
EXECUTE FUNCTION require_unsealed_registry_parent(
    'preprint_source_versions',
    'preprint_source_version_id',
    'trusted_preprint_sources_parent_unsealed'
);

CREATE TRIGGER conference_series_parent_unsealed
BEFORE INSERT ON conference_series
FOR EACH ROW
EXECUTE FUNCTION require_unsealed_registry_parent(
    'conference_registry_versions',
    'conference_registry_version_id',
    'conference_series_parent_unsealed'
);

CREATE FUNCTION require_unsealed_conference_event_parent()
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
    WHERE series.id = NEW.conference_series_id;

    IF NOT FOUND OR parent_sealed_at IS NOT NULL THEN
        RAISE EXCEPTION
            'conference event cannot be inserted after its Registry receipt is sealed'
            USING ERRCODE = '55000',
                  CONSTRAINT = 'conference_events_parent_unsealed';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER conference_events_parent_unsealed
BEFORE INSERT ON conference_events
FOR EACH ROW
EXECUTE FUNCTION require_unsealed_conference_event_parent();

CREATE FUNCTION require_unsealed_conference_entry_parent()
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
    WHERE event.id = NEW.conference_event_id;

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

CREATE TRIGGER conference_registry_entries_parent_unsealed
BEFORE INSERT ON conference_registry_entries
FOR EACH ROW
EXECUTE FUNCTION require_unsealed_conference_entry_parent();

CREATE FUNCTION require_unsealed_conference_entry_child_parent()
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
    WHERE entry.id = NEW.conference_registry_entry_id;

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

CREATE TRIGGER conference_identifiers_parent_unsealed
BEFORE INSERT ON conference_identifiers
FOR EACH ROW
EXECUTE FUNCTION require_unsealed_conference_entry_child_parent(
    'conference_identifiers_parent_unsealed'
);

CREATE TRIGGER conference_official_hosts_parent_unsealed
BEFORE INSERT ON conference_official_hosts
FOR EACH ROW
EXECUTE FUNCTION require_unsealed_conference_entry_child_parent(
    'conference_official_hosts_parent_unsealed'
);

UPDATE domain_versions
SET sealed_at = imported_at
WHERE sealed_at IS NULL;

UPDATE preprint_source_versions
SET sealed_at = imported_at
WHERE sealed_at IS NULL;

UPDATE conference_registry_versions
SET sealed_at = imported_at
WHERE sealed_at IS NULL;

ALTER TABLE domain_category_rules
    ADD CONSTRAINT domain_category_rules_assertion_binding_key
        UNIQUE (id, domain_version_id, domain_id);

CREATE TABLE work_domain_assertions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    projection_assertion_id uuid NOT NULL,
    normalized_assertion_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    work_id uuid NOT NULL,
    domain_version_id uuid NOT NULL,
    domain_category_rule_id uuid NOT NULL,
    domain_id uuid NOT NULL,
    source_path text NOT NULL,
    asserted_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_domain_assertions_registry_fkey
        FOREIGN KEY (domain_version_id)
        REFERENCES domain_versions(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_domain_assertions_rule_binding_fkey
        FOREIGN KEY (
            domain_category_rule_id,
            domain_version_id,
            domain_id
        )
        REFERENCES domain_category_rules(
            id,
            domain_version_id,
            domain_id
        )
        ON DELETE RESTRICT,
    CONSTRAINT work_domain_assertions_domain_binding_fkey
        FOREIGN KEY (domain_id, domain_version_id)
        REFERENCES research_domains(id, domain_version_id)
        ON DELETE RESTRICT,
    CONSTRAINT work_domain_assertions_source_path_check
        CHECK (
            btrim(source_path) <> ''
            AND source_path = btrim(source_path)
        ),
    CONSTRAINT work_domain_assertions_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_domain_assertions_projection_provenance_fkey
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
    CONSTRAINT work_domain_assertions_projection_path_key
        UNIQUE (
            projection_assertion_id,
            source_path,
            domain_category_rule_id
        )
);

CREATE INDEX idx_work_domain_assertions_work
    ON work_domain_assertions(
        work_id,
        domain_version_id,
        domain_category_rule_id,
        asserted_at DESC,
        id DESC
    );

CREATE TRIGGER work_domain_assertions_immutable
BEFORE UPDATE OR DELETE ON work_domain_assertions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

DROP TRIGGER work_channel_admission_decisions_immutable
    ON work_channel_admission_decisions;

ALTER TABLE work_channel_admission_decisions
    DROP CONSTRAINT work_channel_admission_decisions_policy_check,
    DROP CONSTRAINT work_channel_admission_decisions_registry_check,
    DROP CONSTRAINT work_channel_admission_decisions_identity_key;

ALTER TABLE work_channel_admission_decisions
    RENAME COLUMN policy_version TO admission_policy_version;

ALTER TABLE work_channel_admission_decisions
    ADD COLUMN domain_registry_version text,
    ADD COLUMN journal_policy_version text,
    ADD COLUMN channel_registry_version text;

UPDATE work_channel_admission_decisions
SET
    domain_registry_version = 'research-domains-jcr-subjects/v2',
    journal_policy_version = CASE
        WHEN channel IN ('journal_published', 'accepted_early')
            THEN COALESCE(registry_version, 'journal-all-q1/v2')
        ELSE NULL
    END,
    channel_registry_version = CASE
        WHEN channel = 'preprint'
            THEN COALESCE(registry_version, 'preprint-sources/v1')
        WHEN channel = 'conference_proceeding'
            THEN COALESCE(registry_version, 'conference-venues/v1')
        ELSE NULL
    END;

ALTER TABLE work_channel_admission_decisions
    DROP COLUMN registry_version,
    ALTER COLUMN domain_registry_version SET NOT NULL,
    ADD CONSTRAINT work_channel_admission_decisions_admission_policy_check
        CHECK (admission_policy_version = 'channel-admission/v1'),
    ADD CONSTRAINT work_channel_admission_decisions_domain_registry_check
        CHECK (
            domain_registry_version =
                'research-domains-jcr-subjects/v2'
        ),
    ADD CONSTRAINT work_channel_admission_decisions_journal_policy_check
        CHECK (
            journal_policy_version IS NULL
            OR journal_policy_version = 'journal-all-q1/v2'
        ),
    ADD CONSTRAINT work_channel_admission_decisions_channel_registry_check
        CHECK (
            channel_registry_version IS NULL
            OR channel_registry_version IN (
                'preprint-sources/v1',
                'conference-venues/v1'
            )
        ),
    ADD CONSTRAINT work_channel_admission_decisions_version_shape_check
        CHECK (
            (
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
        UNIQUE (
            work_id,
            channel,
            admission_policy_version,
            source_record_id
        );

CREATE TRIGGER work_channel_admission_decisions_immutable
BEFORE UPDATE OR DELETE ON work_channel_admission_decisions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();
