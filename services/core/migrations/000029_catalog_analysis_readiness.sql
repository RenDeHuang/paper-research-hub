ALTER TABLE work_visibility_assessments
    DROP CONSTRAINT work_visibility_assessments_reasons_check,
    ADD CONSTRAINT work_visibility_assessments_reasons_check
        CHECK (
            array_position(reasons, NULL) IS NULL
            AND reasons <@ ARRAY[
                'admission_rejected',
                'stable_identity_missing',
                'work_inactive',
                'lifecycle_ineligible',
                'official_link_missing',
                'official_link_invalid',
                'official_link_expired',
                'canonical_channel_event_missing',
                'canonical_channel_event_after_cutoff',
                'domain_classification_missing',
                'required_taxonomy_missing',
                'abstract_route_missing',
                'decisive_source_conflict'
            ]::text[]
        );

ALTER TABLE work_topics
    ADD CONSTRAINT work_topics_exact_assertion_key
        UNIQUE (id, work_id, source_record_id);

ALTER TABLE work_methods
    ADD CONSTRAINT work_methods_exact_assertion_key
        UNIQUE (id, work_id, source_record_id);

ALTER TABLE abstract_route_analysis_runs
    ADD CONSTRAINT abstract_route_analysis_runs_exact_selection_key
        UNIQUE (
            id,
            work_id,
            projection_assertion_id,
            normalized_assertion_id,
            source_record_id
        );

CREATE TABLE catalog_analysis_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    selection_revision char(64) NOT NULL,
    analysis_cutoff timestamptz NOT NULL,
    generated_at timestamptz NOT NULL,
    classifier_version text NOT NULL,
    classifier_policy_version text NOT NULL,
    channel_event_policy_version text NOT NULL,
    abstract_route_revision char(64) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT catalog_analysis_snapshots_revision_check
        CHECK (selection_revision ~ '^[0-9a-f]{64}$'),
    CONSTRAINT catalog_analysis_snapshots_cutoff_check
        CHECK (analysis_cutoff <= generated_at),
    CONSTRAINT catalog_analysis_snapshots_classifier_version_check
        CHECK (
            btrim(classifier_version) <> ''
            AND classifier_version = btrim(classifier_version)
        ),
    CONSTRAINT catalog_analysis_snapshots_classifier_policy_check
        CHECK (
            btrim(classifier_policy_version) <> ''
            AND classifier_policy_version =
                btrim(classifier_policy_version)
        ),
    CONSTRAINT catalog_analysis_snapshots_channel_event_policy_check
        CHECK (
            btrim(channel_event_policy_version) <> ''
            AND channel_event_policy_version =
                btrim(channel_event_policy_version)
        ),
    CONSTRAINT catalog_analysis_snapshots_abstract_revision_check
        CHECK (abstract_route_revision ~ '^[0-9a-f]{64}$'),
    CONSTRAINT catalog_analysis_snapshots_revision_key
        UNIQUE (selection_revision)
);

CREATE TABLE catalog_analysis_work_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    analysis_snapshot_id uuid NOT NULL
        REFERENCES catalog_analysis_snapshots(id) ON DELETE RESTRICT,
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    channel text NOT NULL
        REFERENCES content_channels(channel_key) ON DELETE RESTRICT,
    projection_assertion_id uuid NOT NULL,
    normalized_assertion_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    source_time timestamptz NOT NULL,
    channel_event_assertion_ids uuid[] NOT NULL DEFAULT '{}'::uuid[],
    canonical_channel_event_at timestamptz,
    classifier_ready boolean NOT NULL,
    classifier_assertion_revision char(64) NOT NULL,
    abstract_route_run_id uuid,
    analysis_ready boolean NOT NULL,
    reasons text[] NOT NULL DEFAULT '{}'::text[],
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT catalog_analysis_work_snapshots_projection_fkey
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
    CONSTRAINT catalog_analysis_work_snapshots_abstract_run_fkey
        FOREIGN KEY (
            abstract_route_run_id,
            work_id,
            projection_assertion_id,
            normalized_assertion_id,
            source_record_id
        )
        REFERENCES abstract_route_analysis_runs(
            id,
            work_id,
            projection_assertion_id,
            normalized_assertion_id,
            source_record_id
        )
        ON DELETE RESTRICT,
    CONSTRAINT catalog_analysis_work_snapshots_reasons_check
        CHECK (
            array_position(reasons, NULL) IS NULL
            AND reasons <@ ARRAY[
                'canonical_channel_event_missing',
                'canonical_channel_event_after_cutoff',
                'domain_classification_missing',
                'required_taxonomy_missing',
                'abstract_route_missing',
                'decisive_source_conflict'
            ]::text[]
        ),
    CONSTRAINT catalog_analysis_work_snapshots_channel_event_assertions_check
        CHECK (array_position(channel_event_assertion_ids, NULL) IS NULL),
    CONSTRAINT catalog_analysis_work_snapshots_classifier_revision_check
        CHECK (classifier_assertion_revision ~ '^[0-9a-f]{64}$'),
    CONSTRAINT catalog_analysis_work_snapshots_shape_check
        CHECK (
            (
                analysis_ready
                AND canonical_channel_event_at IS NOT NULL
                AND cardinality(channel_event_assertion_ids) > 0
                AND classifier_ready
                AND abstract_route_run_id IS NOT NULL
                AND cardinality(reasons) = 0
            )
            OR (
                NOT analysis_ready
                AND cardinality(reasons) > 0
            )
        ),
    CONSTRAINT catalog_analysis_work_snapshots_identity_key
        UNIQUE (analysis_snapshot_id, work_id),
    CONSTRAINT catalog_analysis_work_snapshots_child_binding_key
        UNIQUE (id, work_id, source_record_id)
);

CREATE INDEX idx_catalog_analysis_work_snapshots_work
    ON catalog_analysis_work_snapshots(
        work_id,
        created_at DESC,
        id DESC
    );

CREATE TABLE catalog_analysis_work_topic_assertions (
    work_snapshot_id uuid NOT NULL,
    work_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    work_topic_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (work_snapshot_id, work_topic_id),
    CONSTRAINT catalog_analysis_work_topic_snapshot_fkey
        FOREIGN KEY (work_snapshot_id, work_id, source_record_id)
        REFERENCES catalog_analysis_work_snapshots(
            id,
            work_id,
            source_record_id
        )
        ON DELETE RESTRICT,
    CONSTRAINT catalog_analysis_work_topic_assertion_fkey
        FOREIGN KEY (work_topic_id, work_id, source_record_id)
        REFERENCES work_topics(id, work_id, source_record_id)
        ON DELETE RESTRICT
);

CREATE TABLE catalog_analysis_work_method_assertions (
    work_snapshot_id uuid NOT NULL,
    work_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    work_method_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (work_snapshot_id, work_method_id),
    CONSTRAINT catalog_analysis_work_method_snapshot_fkey
        FOREIGN KEY (work_snapshot_id, work_id, source_record_id)
        REFERENCES catalog_analysis_work_snapshots(
            id,
            work_id,
            source_record_id
        )
        ON DELETE RESTRICT,
    CONSTRAINT catalog_analysis_work_method_assertion_fkey
        FOREIGN KEY (work_method_id, work_id, source_record_id)
        REFERENCES work_methods(id, work_id, source_record_id)
        ON DELETE RESTRICT
);

CREATE TRIGGER catalog_analysis_snapshots_immutable
BEFORE UPDATE OR DELETE ON catalog_analysis_snapshots
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TRIGGER catalog_analysis_work_snapshots_immutable
BEFORE UPDATE OR DELETE ON catalog_analysis_work_snapshots
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TRIGGER catalog_analysis_work_topic_assertions_immutable
BEFORE UPDATE OR DELETE ON catalog_analysis_work_topic_assertions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TRIGGER catalog_analysis_work_method_assertions_immutable
BEFORE UPDATE OR DELETE ON catalog_analysis_work_method_assertions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();
