CREATE TABLE domain_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    registry_name text NOT NULL,
    registry_version text NOT NULL,
    file_sha256 text NOT NULL,
    domain_count integer NOT NULL,
    rule_count integer NOT NULL,
    imported_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT domain_versions_registry_name_check
        CHECK (
            btrim(registry_name) <> ''
            AND registry_name = btrim(registry_name)
        ),
    CONSTRAINT domain_versions_registry_version_check
        CHECK (registry_version = 'research-domains-jcr-subjects/v2'),
    CONSTRAINT domain_versions_file_sha256_check
        CHECK (file_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT domain_versions_counts_check
        CHECK (domain_count = 3 AND rule_count >= domain_count),
    CONSTRAINT domain_versions_registry_version_key
        UNIQUE (registry_version),
    CONSTRAINT domain_versions_file_sha256_key
        UNIQUE (file_sha256)
);

CREATE TRIGGER domain_versions_immutable
BEFORE UPDATE OR DELETE ON domain_versions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE research_domains (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    domain_version_id uuid NOT NULL,
    domain_key text NOT NULL,
    display_label text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT research_domains_version_fkey
        FOREIGN KEY (domain_version_id)
        REFERENCES domain_versions(id)
        ON DELETE RESTRICT,
    CONSTRAINT research_domains_domain_key_check
        CHECK (domain_key IN ('medicine', 'biology', 'computer_science')),
    CONSTRAINT research_domains_display_label_check
        CHECK (
            btrim(display_label) <> ''
            AND display_label = btrim(display_label)
        ),
    CONSTRAINT research_domains_version_domain_key
        UNIQUE (domain_version_id, domain_key),
    CONSTRAINT research_domains_id_version_key
        UNIQUE (id, domain_version_id)
);

CREATE TRIGGER research_domains_immutable
BEFORE UPDATE OR DELETE ON research_domains
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE domain_category_rules (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    domain_version_id uuid NOT NULL,
    domain_id uuid NOT NULL,
    jcr_category text NOT NULL,
    article_level_required boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT domain_category_rules_version_fkey
        FOREIGN KEY (domain_version_id)
        REFERENCES domain_versions(id)
        ON DELETE RESTRICT,
    CONSTRAINT domain_category_rules_domain_version_fkey
        FOREIGN KEY (domain_id, domain_version_id)
        REFERENCES research_domains(id, domain_version_id)
        ON DELETE RESTRICT,
    CONSTRAINT domain_category_rules_category_check
        CHECK (
            btrim(jcr_category) <> ''
            AND jcr_category = btrim(jcr_category)
        ),
    CONSTRAINT domain_category_rules_version_domain_category_key
        UNIQUE (domain_version_id, domain_id, jcr_category),
    CONSTRAINT domain_category_rules_id_category_key
        UNIQUE (id, jcr_category)
);

CREATE INDEX idx_domain_category_rules_exact_category
    ON domain_category_rules(domain_version_id, jcr_category, domain_id);

CREATE TRIGGER domain_category_rules_immutable
BEFORE UPDATE OR DELETE ON domain_category_rules
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE journal_domain_metrics (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    venue_metric_snapshot_id uuid NOT NULL,
    domain_category_rule_id uuid NOT NULL,
    jcr_category text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT journal_domain_metrics_category_check
        CHECK (
            btrim(jcr_category) <> ''
            AND jcr_category = btrim(jcr_category)
        ),
    CONSTRAINT journal_domain_metrics_metric_category_fkey
        FOREIGN KEY (venue_metric_snapshot_id, jcr_category)
        REFERENCES venue_metric_snapshots(id, category)
        ON DELETE RESTRICT,
    CONSTRAINT journal_domain_metrics_rule_category_fkey
        FOREIGN KEY (domain_category_rule_id, jcr_category)
        REFERENCES domain_category_rules(id, jcr_category)
        ON DELETE RESTRICT,
    CONSTRAINT journal_domain_metrics_metric_rule_key
        UNIQUE (venue_metric_snapshot_id, domain_category_rule_id)
);

CREATE INDEX idx_journal_domain_metrics_rule
    ON journal_domain_metrics(
        domain_category_rule_id,
        venue_metric_snapshot_id,
        id
    );

CREATE TRIGGER journal_domain_metrics_immutable
BEFORE UPDATE OR DELETE ON journal_domain_metrics
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE content_channels (
    channel_key text PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT content_channels_key_check
        CHECK (
            channel_key IN (
                'journal_published',
                'accepted_early',
                'preprint',
                'conference_proceeding'
            )
        )
);

INSERT INTO content_channels (channel_key)
VALUES
    ('journal_published'),
    ('accepted_early'),
    ('preprint'),
    ('conference_proceeding');

CREATE TRIGGER content_channels_immutable
BEFORE UPDATE OR DELETE ON content_channels
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE preprint_source_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    registry_name text NOT NULL,
    registry_version text NOT NULL,
    file_sha256 text NOT NULL,
    source_count integer NOT NULL,
    rule_count integer NOT NULL,
    imported_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT preprint_source_versions_name_check
        CHECK (
            btrim(registry_name) <> ''
            AND registry_name = btrim(registry_name)
        ),
    CONSTRAINT preprint_source_versions_version_check
        CHECK (registry_version = 'preprint-sources/v1'),
    CONSTRAINT preprint_source_versions_sha_check
        CHECK (file_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT preprint_source_versions_counts_check
        CHECK (source_count > 0 AND rule_count >= source_count),
    CONSTRAINT preprint_source_versions_registry_version_key
        UNIQUE (registry_version),
    CONSTRAINT preprint_source_versions_file_sha256_key
        UNIQUE (file_sha256)
);

CREATE TRIGGER preprint_source_versions_immutable
BEFORE UPDATE OR DELETE ON preprint_source_versions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE trusted_preprint_sources (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    preprint_source_version_id uuid NOT NULL,
    source_key text NOT NULL,
    display_name text NOT NULL,
    identifier_scheme text NOT NULL,
    official_host text NOT NULL,
    allowed_domains text[] NOT NULL,
    lifecycle text NOT NULL,
    channel text NOT NULL DEFAULT 'preprint',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT trusted_preprint_sources_version_fkey
        FOREIGN KEY (preprint_source_version_id)
        REFERENCES preprint_source_versions(id)
        ON DELETE RESTRICT,
    CONSTRAINT trusted_preprint_sources_source_key_check
        CHECK (
            source_key ~ '^[a-z0-9]+(?:[-_][a-z0-9]+)*$'
            AND source_key = btrim(source_key)
        ),
    CONSTRAINT trusted_preprint_sources_display_name_check
        CHECK (
            btrim(display_name) <> ''
            AND display_name = btrim(display_name)
        ),
    CONSTRAINT trusted_preprint_sources_identifier_scheme_check
        CHECK (
            identifier_scheme ~ '^[a-z0-9]+(?:[-_][a-z0-9]+)*$'
            AND identifier_scheme = btrim(identifier_scheme)
        ),
    CONSTRAINT trusted_preprint_sources_official_host_check
        CHECK (
            official_host ~ '^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$'
            AND official_host = lower(official_host)
            AND position('.' IN official_host) > 0
        ),
    CONSTRAINT trusted_preprint_sources_domains_check
        CHECK (
            cardinality(allowed_domains) > 0
            AND allowed_domains <@
                ARRAY['medicine', 'biology', 'computer_science']::text[]
            AND array_position(allowed_domains, NULL) IS NULL
        ),
    CONSTRAINT trusted_preprint_sources_lifecycle_check
        CHECK (lifecycle IN ('active', 'retired')),
    CONSTRAINT trusted_preprint_sources_channel_check
        CHECK (channel = 'preprint'),
    CONSTRAINT trusted_preprint_sources_version_source_key
        UNIQUE (preprint_source_version_id, source_key)
);

CREATE INDEX idx_trusted_preprint_sources_lookup
    ON trusted_preprint_sources(
        preprint_source_version_id,
        source_key,
        lifecycle
    );

CREATE TRIGGER trusted_preprint_sources_immutable
BEFORE UPDATE OR DELETE ON trusted_preprint_sources
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE conference_registry_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    registry_name text NOT NULL,
    registry_version text NOT NULL,
    file_sha256 text NOT NULL,
    series_count integer NOT NULL,
    event_count integer NOT NULL,
    rule_count integer NOT NULL,
    imported_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT conference_registry_versions_name_check
        CHECK (
            btrim(registry_name) <> ''
            AND registry_name = btrim(registry_name)
        ),
    CONSTRAINT conference_registry_versions_version_check
        CHECK (registry_version = 'conference-venues/v1'),
    CONSTRAINT conference_registry_versions_sha_check
        CHECK (file_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT conference_registry_versions_counts_check
        CHECK (
            series_count > 0
            AND event_count >= series_count
            AND rule_count >= event_count
        ),
    CONSTRAINT conference_registry_versions_registry_version_key
        UNIQUE (registry_version),
    CONSTRAINT conference_registry_versions_file_sha256_key
        UNIQUE (file_sha256)
);

CREATE TRIGGER conference_registry_versions_immutable
BEFORE UPDATE OR DELETE ON conference_registry_versions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE conference_series (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conference_registry_version_id uuid NOT NULL,
    provider text NOT NULL,
    series_key text NOT NULL,
    series_name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT conference_series_version_fkey
        FOREIGN KEY (conference_registry_version_id)
        REFERENCES conference_registry_versions(id)
        ON DELETE RESTRICT,
    CONSTRAINT conference_series_provider_check
        CHECK (provider IN ('ieee', 'acm', 'reviewed_allowlist')),
    CONSTRAINT conference_series_key_check
        CHECK (
            series_key ~ '^[a-z0-9]+(?:[-_][a-z0-9]+)*$'
            AND series_key = btrim(series_key)
        ),
    CONSTRAINT conference_series_name_check
        CHECK (
            btrim(series_name) <> ''
            AND series_name = btrim(series_name)
        ),
    CONSTRAINT conference_series_version_provider_key
        UNIQUE (
            conference_registry_version_id,
            provider,
            series_key
        )
);

CREATE TRIGGER conference_series_immutable
BEFORE UPDATE OR DELETE ON conference_series
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE conference_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conference_series_id uuid NOT NULL,
    event_key text NOT NULL,
    event_name text NOT NULL,
    event_year integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT conference_events_series_fkey
        FOREIGN KEY (conference_series_id)
        REFERENCES conference_series(id)
        ON DELETE RESTRICT,
    CONSTRAINT conference_events_key_check
        CHECK (
            event_key ~ '^[a-z0-9]+(?:[-_][a-z0-9]+)*$'
            AND event_key = btrim(event_key)
        ),
    CONSTRAINT conference_events_name_check
        CHECK (
            btrim(event_name) <> ''
            AND event_name = btrim(event_name)
        ),
    CONSTRAINT conference_events_year_check
        CHECK (event_year BETWEEN 1900 AND 3000),
    CONSTRAINT conference_events_series_event_key
        UNIQUE (conference_series_id, event_key)
);

CREATE TRIGGER conference_events_immutable
BEFORE UPDATE OR DELETE ON conference_events
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE conference_registry_entries (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conference_registry_version_id uuid NOT NULL,
    conference_event_id uuid NOT NULL,
    allowed_domains text[] NOT NULL,
    lifecycle text NOT NULL,
    reviewed boolean NOT NULL,
    channel text NOT NULL DEFAULT 'conference_proceeding',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT conference_registry_entries_version_fkey
        FOREIGN KEY (conference_registry_version_id)
        REFERENCES conference_registry_versions(id)
        ON DELETE RESTRICT,
    CONSTRAINT conference_registry_entries_event_fkey
        FOREIGN KEY (conference_event_id)
        REFERENCES conference_events(id)
        ON DELETE RESTRICT,
    CONSTRAINT conference_registry_entries_domains_check
        CHECK (
            cardinality(allowed_domains) > 0
            AND allowed_domains <@
                ARRAY['medicine', 'biology', 'computer_science']::text[]
            AND array_position(allowed_domains, NULL) IS NULL
        ),
    CONSTRAINT conference_registry_entries_lifecycle_check
        CHECK (lifecycle IN ('active', 'retired')),
    CONSTRAINT conference_registry_entries_channel_check
        CHECK (channel = 'conference_proceeding'),
    CONSTRAINT conference_registry_entries_version_event_key
        UNIQUE (conference_registry_version_id, conference_event_id)
);

CREATE TRIGGER conference_registry_entries_immutable
BEFORE UPDATE OR DELETE ON conference_registry_entries
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE conference_identifiers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conference_registry_entry_id uuid NOT NULL,
    identifier_scheme text NOT NULL,
    identifier_value text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT conference_identifiers_entry_fkey
        FOREIGN KEY (conference_registry_entry_id)
        REFERENCES conference_registry_entries(id)
        ON DELETE RESTRICT,
    CONSTRAINT conference_identifiers_scheme_check
        CHECK (
            identifier_scheme ~ '^[a-z0-9]+(?:[-_][a-z0-9]+)*$'
            AND identifier_scheme = btrim(identifier_scheme)
        ),
    CONSTRAINT conference_identifiers_value_check
        CHECK (
            btrim(identifier_value) <> ''
            AND identifier_value = btrim(identifier_value)
        ),
    CONSTRAINT conference_identifiers_entry_scheme_value_key
        UNIQUE (
            conference_registry_entry_id,
            identifier_scheme,
            identifier_value
        )
);

CREATE TRIGGER conference_identifiers_immutable
BEFORE UPDATE OR DELETE ON conference_identifiers
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE conference_official_hosts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conference_registry_entry_id uuid NOT NULL,
    official_host text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT conference_official_hosts_entry_fkey
        FOREIGN KEY (conference_registry_entry_id)
        REFERENCES conference_registry_entries(id)
        ON DELETE RESTRICT,
    CONSTRAINT conference_official_hosts_host_check
        CHECK (
            official_host ~ '^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$'
            AND official_host = lower(official_host)
            AND position('.' IN official_host) > 0
        ),
    CONSTRAINT conference_official_hosts_entry_host_key
        UNIQUE (conference_registry_entry_id, official_host)
);

CREATE TRIGGER conference_official_hosts_immutable
BEFORE UPDATE OR DELETE ON conference_official_hosts
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_channel_assertions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    projection_assertion_id uuid NOT NULL,
    normalized_assertion_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    work_id uuid NOT NULL,
    channel text NOT NULL,
    source_path text NOT NULL,
    asserted_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_channel_assertions_channel_fkey
        FOREIGN KEY (channel)
        REFERENCES content_channels(channel_key)
        ON DELETE RESTRICT,
    CONSTRAINT work_channel_assertions_source_path_check
        CHECK (
            btrim(source_path) <> ''
            AND source_path = btrim(source_path)
        ),
    CONSTRAINT work_channel_assertions_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_channel_assertions_projection_provenance_fkey
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
    CONSTRAINT work_channel_assertions_projection_path_key
        UNIQUE (projection_assertion_id, source_path, channel)
);

CREATE INDEX idx_work_channel_assertions_work
    ON work_channel_assertions(work_id, asserted_at DESC, id DESC);

CREATE TRIGGER work_channel_assertions_immutable
BEFORE UPDATE OR DELETE ON work_channel_assertions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_channel_decisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    channel text REFERENCES content_channels(channel_key) ON DELETE RESTRICT,
    state text NOT NULL,
    source_record_id uuid NOT NULL REFERENCES source_records(id) ON DELETE RESTRICT,
    source_path text NOT NULL,
    policy_version text NOT NULL,
    evidence jsonb NOT NULL,
    decided_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_channel_decisions_state_check
        CHECK (state IN ('resolved', 'missing', 'conflict')),
    CONSTRAINT work_channel_decisions_shape_check
        CHECK (
            (state = 'resolved' AND channel IS NOT NULL)
            OR (state IN ('missing', 'conflict') AND channel IS NULL)
        ),
    CONSTRAINT work_channel_decisions_source_path_check
        CHECK (
            btrim(source_path) <> ''
            AND source_path = btrim(source_path)
        ),
    CONSTRAINT work_channel_decisions_policy_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT work_channel_decisions_evidence_check
        CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb),
    CONSTRAINT work_channel_decisions_identity_key
        UNIQUE (work_id, policy_version, source_record_id)
);

CREATE TRIGGER work_channel_decisions_immutable
BEFORE UPDATE OR DELETE ON work_channel_decisions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_lifecycle_assertions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    projection_assertion_id uuid NOT NULL,
    normalized_assertion_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    work_id uuid NOT NULL,
    lifecycle_fact text NOT NULL,
    source_path text NOT NULL,
    asserted_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_lifecycle_assertions_fact_check
        CHECK (
            lifecycle_fact IN (
                'published',
                'accepted',
                'pending',
                'ahead_of_print',
                'online_first',
                'posted',
                'withdrawn',
                'proceeding_published'
            )
        ),
    CONSTRAINT work_lifecycle_assertions_source_path_check
        CHECK (
            btrim(source_path) <> ''
            AND source_path = btrim(source_path)
        ),
    CONSTRAINT work_lifecycle_assertions_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_lifecycle_assertions_projection_provenance_fkey
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
    CONSTRAINT work_lifecycle_assertions_projection_path_key
        UNIQUE (projection_assertion_id, source_path, lifecycle_fact)
);

CREATE INDEX idx_work_lifecycle_assertions_work
    ON work_lifecycle_assertions(work_id, asserted_at DESC, id DESC);

CREATE TRIGGER work_lifecycle_assertions_immutable
BEFORE UPDATE OR DELETE ON work_lifecycle_assertions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_lifecycle_states (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    channel text NOT NULL REFERENCES content_channels(channel_key) ON DELETE RESTRICT,
    lifecycle_state text NOT NULL,
    source_record_id uuid NOT NULL REFERENCES source_records(id) ON DELETE RESTRICT,
    source_path text NOT NULL,
    policy_version text NOT NULL,
    evidence jsonb NOT NULL,
    decided_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_lifecycle_states_state_check
        CHECK (
            lifecycle_state IN (
                'published',
                'accepted_early',
                'preprint_active',
                'withdrawn',
                'conference_published',
                'missing',
                'conflict'
            )
        ),
    CONSTRAINT work_lifecycle_states_source_path_check
        CHECK (
            btrim(source_path) <> ''
            AND source_path = btrim(source_path)
        ),
    CONSTRAINT work_lifecycle_states_policy_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT work_lifecycle_states_evidence_check
        CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb),
    CONSTRAINT work_lifecycle_states_identity_key
        UNIQUE (work_id, channel, policy_version, source_record_id)
);

CREATE TRIGGER work_lifecycle_states_immutable
BEFORE UPDATE OR DELETE ON work_lifecycle_states
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_channel_admission_decisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    channel text NOT NULL REFERENCES content_channels(channel_key) ON DELETE RESTRICT,
    decision text NOT NULL,
    reason text NOT NULL,
    source_record_id uuid NOT NULL REFERENCES source_records(id) ON DELETE RESTRICT,
    source_path text NOT NULL,
    policy_version text NOT NULL,
    registry_version text,
    evidence jsonb NOT NULL,
    decided_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_channel_admission_decisions_decision_check
        CHECK (decision IN ('accepted', 'rejected', 'missing')),
    CONSTRAINT work_channel_admission_decisions_reason_check
        CHECK (btrim(reason) <> '' AND reason = btrim(reason)),
    CONSTRAINT work_channel_admission_decisions_source_path_check
        CHECK (
            btrim(source_path) <> ''
            AND source_path = btrim(source_path)
        ),
    CONSTRAINT work_channel_admission_decisions_policy_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT work_channel_admission_decisions_registry_check
        CHECK (
            registry_version IS NULL
            OR (
                btrim(registry_version) <> ''
                AND registry_version = btrim(registry_version)
            )
        ),
    CONSTRAINT work_channel_admission_decisions_evidence_check
        CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb),
    CONSTRAINT work_channel_admission_decisions_identity_key
        UNIQUE (work_id, channel, policy_version, source_record_id)
);

CREATE INDEX idx_work_channel_admission_decisions_lookup
    ON work_channel_admission_decisions(
        channel,
        decision,
        decided_at DESC,
        work_id
    );

CREATE TRIGGER work_channel_admission_decisions_immutable
BEFORE UPDATE OR DELETE ON work_channel_admission_decisions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE FUNCTION enforce_domain_registry_receipt_integrity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_domain_count integer;
    actual_rule_count integer;
BEGIN
    SELECT count(*)
    INTO actual_domain_count
    FROM research_domains
    WHERE domain_version_id = NEW.id;

    SELECT count(*)
    INTO actual_rule_count
    FROM domain_category_rules
    WHERE domain_version_id = NEW.id;

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
                  CONSTRAINT = 'domain_versions_content_integrity';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER domain_versions_content_integrity
AFTER INSERT ON domain_versions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_domain_registry_receipt_integrity();

CREATE FUNCTION enforce_preprint_registry_receipt_integrity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_source_count integer;
    actual_rule_count integer;
BEGIN
    SELECT
        count(*),
        COALESCE(sum(cardinality(allowed_domains)), 0)
    INTO actual_source_count, actual_rule_count
    FROM trusted_preprint_sources
    WHERE preprint_source_version_id = NEW.id;

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
                  CONSTRAINT = 'preprint_source_versions_content_integrity';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER preprint_source_versions_content_integrity
AFTER INSERT ON preprint_source_versions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_preprint_registry_receipt_integrity();

CREATE FUNCTION enforce_conference_registry_receipt_integrity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_series_count integer;
    actual_event_count integer;
    actual_rule_count integer;
BEGIN
    SELECT count(*)
    INTO actual_series_count
    FROM conference_series
    WHERE conference_registry_version_id = NEW.id;

    SELECT count(*)
    INTO actual_event_count
    FROM conference_events AS event
    JOIN conference_series AS series
      ON series.id = event.conference_series_id
    WHERE series.conference_registry_version_id = NEW.id;

    SELECT COALESCE(sum(cardinality(allowed_domains)), 0)
    INTO actual_rule_count
    FROM conference_registry_entries
    WHERE conference_registry_version_id = NEW.id;

    IF actual_series_count <> NEW.series_count
       OR actual_event_count <> NEW.event_count
       OR actual_rule_count <> NEW.rule_count THEN
        RAISE EXCEPTION
            'conference Registry receipt % content mismatch: series %/%, events %/%, rules %/%',
            NEW.id,
            actual_series_count,
            NEW.series_count,
            actual_event_count,
            NEW.event_count,
            actual_rule_count,
            NEW.rule_count
            USING ERRCODE = '23514',
                  CONSTRAINT = 'conference_registry_versions_content_integrity';
    END IF;
    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER conference_registry_versions_content_integrity
AFTER INSERT ON conference_registry_versions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_conference_registry_receipt_integrity();
