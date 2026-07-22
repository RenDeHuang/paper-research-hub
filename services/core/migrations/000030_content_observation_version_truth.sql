ALTER TABLE ingestion_raw_events
    ADD CONSTRAINT ingestion_raw_events_id_job_key
        UNIQUE (id, job_id);

CREATE TABLE ingestion_raw_observations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    raw_event_id uuid NOT NULL,
    job_id uuid NOT NULL,
    connector_run_id uuid NOT NULL,
    page_ordinal integer NOT NULL,
    record_ordinal integer NOT NULL,
    observed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ingestion_raw_observations_raw_job_fkey
        FOREIGN KEY (raw_event_id, job_id)
        REFERENCES ingestion_raw_events(id, job_id)
        ON DELETE RESTRICT,
    CONSTRAINT ingestion_raw_observations_page_fkey
        FOREIGN KEY (connector_run_id, page_ordinal)
        REFERENCES connector_run_pages(run_id, page_ordinal)
        ON DELETE RESTRICT,
    CONSTRAINT ingestion_raw_observations_page_ordinal_check
        CHECK (page_ordinal > 0),
    CONSTRAINT ingestion_raw_observations_record_ordinal_check
        CHECK (record_ordinal > 0),
    CONSTRAINT ingestion_raw_observations_coordinates_key
        UNIQUE (connector_run_id, page_ordinal, record_ordinal)
);

CREATE INDEX idx_ingestion_raw_observations_raw_event
    ON ingestion_raw_observations(raw_event_id, observed_at, id);

CREATE INDEX idx_ingestion_raw_observations_observed_at
    ON ingestion_raw_observations(observed_at, raw_event_id);

CREATE FUNCTION validate_ingestion_raw_observation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    raw_source text;
    job_source text;
    run_source text;
    run_started_at timestamptz;
    page_record_count integer;
BEGIN
    SELECT raw_event.logical_source
    INTO raw_source
    FROM ingestion_raw_events AS raw_event
    WHERE raw_event.id = NEW.raw_event_id
      AND raw_event.job_id = NEW.job_id
    FOR KEY SHARE;

    SELECT job.source
    INTO job_source
    FROM ingestion_jobs AS job
    WHERE job.id = NEW.job_id
    FOR KEY SHARE;

    SELECT connector_run.source, connector_run.started_at, page.record_count
    INTO run_source, run_started_at, page_record_count
    FROM connector_runs AS connector_run
    JOIN connector_run_pages AS page
      ON page.run_id = connector_run.id
     AND page.page_ordinal = NEW.page_ordinal
    WHERE connector_run.id = NEW.connector_run_id
    FOR KEY SHARE OF connector_run, page;

    IF raw_source IS NULL
       OR job_source IS NULL
       OR run_source IS NULL THEN
        RETURN NEW;
    END IF;

    IF raw_source <> job_source OR raw_source <> run_source THEN
        RAISE EXCEPTION
            'raw observation source mismatch: raw %, job %, connector %',
            raw_source,
            job_source,
            run_source
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'ingestion_raw_observations_source_consistency';
    END IF;

    IF NEW.record_ordinal > page_record_count THEN
        RAISE EXCEPTION
            'raw observation record ordinal % exceeds page record count %',
            NEW.record_ordinal,
            page_record_count
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'ingestion_raw_observations_page_record_bounds';
    END IF;

    IF NEW.observed_at < run_started_at THEN
        RAISE EXCEPTION
            'raw observation predates connector run start'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'ingestion_raw_observations_run_interval';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER ingestion_raw_observations_binding_guard
BEFORE INSERT ON ingestion_raw_observations
FOR EACH ROW
EXECUTE FUNCTION validate_ingestion_raw_observation();

CREATE TRIGGER ingestion_raw_observations_immutable
BEFORE UPDATE OR DELETE ON ingestion_raw_observations
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE VIEW current_work_first_observed_at AS
SELECT
    source_work.work_id,
    min(observation.observed_at) AS first_observed_at
FROM ingestion_raw_observations AS observation
JOIN ingestion_normalized_records AS normalized
  ON normalized.raw_event_id = observation.raw_event_id
JOIN source_record_works AS source_work
  ON source_work.source_record_id = normalized.source_record_uuid
GROUP BY source_work.work_id;

ALTER TABLE ingestion_normalized_records
    ADD COLUMN explicit_version_number integer,
    ADD COLUMN explicit_version_label_raw text,
    ADD COLUMN explicit_version_source_path text,
    ADD CONSTRAINT ingestion_normalized_records_explicit_version_shape_check
        CHECK (
            (
                explicit_version_number IS NULL
                AND explicit_version_label_raw IS NULL
                AND explicit_version_source_path IS NULL
            )
            OR
            (
                explicit_version_number > 0
                AND btrim(explicit_version_label_raw) <> ''
                AND explicit_version_label_raw =
                    btrim(explicit_version_label_raw)
                AND btrim(explicit_version_source_path) <> ''
                AND explicit_version_source_path =
                    btrim(explicit_version_source_path)
            )
        );

CREATE TABLE work_channel_event_assertions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    projection_assertion_id uuid NOT NULL,
    normalized_assertion_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    work_id uuid NOT NULL,
    channel text NOT NULL,
    event_kind text NOT NULL,
    event_at timestamptz NOT NULL,
    source_path text NOT NULL,
    asserted_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_channel_event_assertions_channel_fkey
        FOREIGN KEY (channel)
        REFERENCES content_channels(channel_key)
        ON DELETE RESTRICT,
    CONSTRAINT work_channel_event_assertions_provenance_fkey
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
    CONSTRAINT work_channel_event_assertions_channel_kind_check
        CHECK (
            (
                channel = 'journal_published'
                AND event_kind IN ('official_online', 'official_print')
            )
            OR
            (
                channel = 'accepted_early'
                AND event_kind IN (
                    'accepted',
                    'ahead_of_print',
                    'online_first'
                )
            )
            OR
            (
                channel = 'preprint'
                AND event_kind = 'preprint_posted'
            )
            OR
            (
                channel = 'conference_proceeding'
                AND event_kind = 'proceeding_published'
            )
        ),
    CONSTRAINT work_channel_event_assertions_source_path_check
        CHECK (
            btrim(source_path) <> ''
            AND source_path = btrim(source_path)
        ),
    CONSTRAINT work_channel_event_assertions_identity_key
        UNIQUE (
            projection_assertion_id,
            source_path,
            channel,
            event_kind,
            event_at
        )
);

CREATE INDEX idx_work_channel_event_assertions_work
    ON work_channel_event_assertions(
        work_id,
        channel,
        event_at DESC,
        asserted_at DESC,
        id DESC
    );

CREATE TRIGGER work_channel_event_assertions_immutable
BEFORE UPDATE OR DELETE ON work_channel_event_assertions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

ALTER TABLE work_channel_decisions
    ADD CONSTRAINT work_channel_decisions_projection_binding_key
        UNIQUE (id, work_id, channel, state);

CREATE TABLE work_channel_event_decisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    channel text NOT NULL
        REFERENCES content_channels(channel_key) ON DELETE RESTRICT,
    state text NOT NULL,
    event_kind text,
    event_at timestamptz,
    policy_version text NOT NULL,
    input_digest char(64) NOT NULL,
    evidence jsonb NOT NULL,
    decided_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_channel_event_decisions_state_check
        CHECK (state IN ('known', 'missing', 'conflict')),
    CONSTRAINT work_channel_event_decisions_shape_check
        CHECK (
            (
                state = 'known'
                AND event_kind IS NOT NULL
                AND event_at IS NOT NULL
                AND jsonb_typeof(evidence) = 'object'
                AND jsonb_array_length(evidence -> 'assertions') > 0
            )
            OR
            (
                state IN ('missing', 'conflict')
                AND event_kind IS NULL
                AND event_at IS NULL
                AND jsonb_typeof(evidence) = 'object'
            )
        ),
    CONSTRAINT work_channel_event_decisions_channel_kind_check
        CHECK (
            event_kind IS NULL
            OR (
                channel = 'journal_published'
                AND event_kind IN ('official_online', 'official_print')
            )
            OR (
                channel = 'accepted_early'
                AND event_kind IN (
                    'accepted',
                    'ahead_of_print',
                    'online_first'
                )
            )
            OR (
                channel = 'preprint'
                AND event_kind = 'preprint_posted'
            )
            OR (
                channel = 'conference_proceeding'
                AND event_kind = 'proceeding_published'
            )
        ),
    CONSTRAINT work_channel_event_decisions_policy_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT work_channel_event_decisions_digest_check
        CHECK (input_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT work_channel_event_decisions_evidence_check
        CHECK (
            jsonb_typeof(evidence) = 'object'
            AND evidence ? 'assertions'
            AND jsonb_typeof(evidence -> 'assertions') = 'array'
        ),
    CONSTRAINT work_channel_event_decisions_identity_key
        UNIQUE (work_id, channel, policy_version, input_digest)
);

CREATE INDEX idx_work_channel_event_decisions_work
    ON work_channel_event_decisions(
        work_id,
        channel,
        decided_at DESC,
        id DESC
    );

CREATE TRIGGER work_channel_event_decisions_immutable
BEFORE UPDATE OR DELETE ON work_channel_event_decisions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_version_number_assertions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    projection_assertion_id uuid NOT NULL,
    normalized_assertion_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    work_id uuid NOT NULL,
    version_number integer NOT NULL,
    raw_label text NOT NULL,
    source_path text NOT NULL,
    asserted_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_version_number_assertions_provenance_fkey
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
    CONSTRAINT work_version_number_assertions_number_check
        CHECK (version_number > 0),
    CONSTRAINT work_version_number_assertions_raw_label_check
        CHECK (
            btrim(raw_label) <> ''
            AND raw_label = btrim(raw_label)
        ),
    CONSTRAINT work_version_number_assertions_source_path_check
        CHECK (
            btrim(source_path) <> ''
            AND source_path = btrim(source_path)
        ),
    CONSTRAINT work_version_number_assertions_identity_key
        UNIQUE (
            projection_assertion_id,
            source_path,
            version_number,
            raw_label
        )
);

CREATE INDEX idx_work_version_number_assertions_work
    ON work_version_number_assertions(
        work_id,
        asserted_at DESC,
        id DESC
    );

CREATE TRIGGER work_version_number_assertions_immutable
BEFORE UPDATE OR DELETE ON work_version_number_assertions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_version_number_assessments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    channel text NOT NULL
        REFERENCES content_channels(channel_key) ON DELETE RESTRICT,
    state text NOT NULL,
    version_number integer,
    checked_source_revisions jsonb NOT NULL,
    assertion_ids jsonb NOT NULL,
    policy_version text NOT NULL,
    input_digest char(64) NOT NULL,
    assessed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_version_number_assessments_state_check
        CHECK (
            state IN ('known', 'missing', 'conflict', 'not_applicable')
        ),
    CONSTRAINT work_version_number_assessments_shape_check
        CHECK (
            (
                state = 'known'
                AND version_number > 0
                AND jsonb_array_length(assertion_ids) > 0
            )
            OR
            (
                state = 'missing'
                AND version_number IS NULL
                AND jsonb_array_length(assertion_ids) = 0
            )
            OR
            (
                state = 'conflict'
                AND version_number IS NULL
                AND jsonb_array_length(assertion_ids) > 1
            )
            OR
            (
                state = 'not_applicable'
                AND version_number IS NULL
                AND jsonb_array_length(assertion_ids) = 0
            )
        ),
    CONSTRAINT work_version_number_assessments_checked_revisions_check
        CHECK (
            jsonb_typeof(checked_source_revisions) = 'array'
            AND jsonb_array_length(checked_source_revisions) > 0
        ),
    CONSTRAINT work_version_number_assessments_assertions_check
        CHECK (jsonb_typeof(assertion_ids) = 'array'),
    CONSTRAINT work_version_number_assessments_policy_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT work_version_number_assessments_digest_check
        CHECK (input_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT work_version_number_assessments_identity_key
        UNIQUE (work_id, channel, policy_version, input_digest)
);

CREATE INDEX idx_work_version_number_assessments_work
    ON work_version_number_assessments(
        work_id,
        channel,
        assessed_at DESC,
        id DESC
    );

CREATE TRIGGER work_version_number_assessments_immutable
BEFORE UPDATE OR DELETE ON work_version_number_assessments
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_version_kind_assessments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    channel text NOT NULL
        REFERENCES content_channels(channel_key) ON DELETE RESTRICT,
    channel_decision_id uuid NOT NULL,
    channel_decision_state text NOT NULL,
    version_kind text NOT NULL,
    policy_version text NOT NULL,
    input_digest char(64) NOT NULL,
    assessed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_version_kind_assessments_channel_state_check
        CHECK (channel_decision_state = 'resolved'),
    CONSTRAINT work_version_kind_assessments_channel_decision_fkey
        FOREIGN KEY (
            channel_decision_id,
            work_id,
            channel,
            channel_decision_state
        )
        REFERENCES work_channel_decisions(
            id,
            work_id,
            channel,
            state
        )
        ON DELETE RESTRICT,
    CONSTRAINT work_version_kind_assessments_kind_check
        CHECK (
            (
                channel = 'journal_published'
                AND version_kind IN (
                    'version_of_record',
                    'correction',
                    'retraction'
                )
            )
            OR
            (
                channel = 'accepted_early'
                AND version_kind IN (
                    'accepted_manuscript',
                    'correction',
                    'retraction'
                )
            )
            OR
            (
                channel = 'preprint'
                AND version_kind IN (
                    'preprint',
                    'revised_preprint',
                    'correction',
                    'retraction'
                )
            )
            OR
            (
                channel = 'conference_proceeding'
                AND version_kind IN (
                    'conference_paper',
                    'correction',
                    'retraction'
                )
            )
        ),
    CONSTRAINT work_version_kind_assessments_policy_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT work_version_kind_assessments_digest_check
        CHECK (input_digest ~ '^[0-9a-f]{64}$'),
    CONSTRAINT work_version_kind_assessments_identity_key
        UNIQUE (work_id, channel, policy_version, input_digest)
);

CREATE INDEX idx_work_version_kind_assessments_work
    ON work_version_kind_assessments(
        work_id,
        channel,
        assessed_at DESC,
        id DESC
    );

CREATE TRIGGER work_version_kind_assessments_immutable
BEFORE UPDATE OR DELETE ON work_version_kind_assessments
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_version_kind_relation_decisions (
    assessment_id uuid NOT NULL
        REFERENCES work_version_kind_assessments(id) ON DELETE RESTRICT,
    relation_decision_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (assessment_id, relation_decision_id),
    CONSTRAINT work_version_kind_relation_decisions_relation_fkey
        FOREIGN KEY (relation_decision_id)
        REFERENCES work_relation_decisions(id)
        ON DELETE RESTRICT
);

CREATE INDEX idx_work_version_kind_relation_decisions_relation
    ON work_version_kind_relation_decisions(
        relation_decision_id,
        assessment_id
    );

CREATE FUNCTION validate_work_version_kind_relation_decision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    assessment_work_id uuid;
    assessment_at timestamptz;
    relation_assertion_id uuid;
    relation_outcome text;
    relation_decided_at timestamptz;
    relation_subject_work_id uuid;
    relation_object_work_id uuid;
    subject_family_id uuid;
    object_family_id uuid;
BEGIN
    SELECT assessment.work_id, assessment.assessed_at
    INTO assessment_work_id, assessment_at
    FROM work_version_kind_assessments AS assessment
    WHERE assessment.id = NEW.assessment_id
    FOR KEY SHARE;

    SELECT
        decision.assertion_id,
        decision.outcome,
        decision.decided_at,
        assertion.subject_work_id,
        assertion.object_work_id
    INTO
        relation_assertion_id,
        relation_outcome,
        relation_decided_at,
        relation_subject_work_id,
        relation_object_work_id
    FROM work_relation_decisions AS decision
    JOIN work_relation_assertions AS assertion
      ON assertion.id = decision.assertion_id
    WHERE decision.id = NEW.relation_decision_id
    FOR KEY SHARE OF decision, assertion;

    IF assessment_work_id IS NULL
       OR relation_assertion_id IS NULL THEN
        RETURN NEW;
    END IF;

    IF relation_outcome <> 'accepted'
       OR relation_decided_at > assessment_at
       OR EXISTS (
            SELECT 1
            FROM work_relation_decisions AS successor
            WHERE successor.supersedes_decision_id =
                    NEW.relation_decision_id
              AND successor.decided_at <= assessment_at
       ) THEN
        RAISE EXCEPTION
            'version kind assessment requires a current accepted relation decision'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_version_kind_relations_current_accepted';
    END IF;

    IF assessment_work_id NOT IN (
        relation_subject_work_id,
        relation_object_work_id
    ) THEN
        RAISE EXCEPTION
            'version kind relation does not involve assessed Work'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_version_kind_relations_work_binding';
    END IF;

    SELECT membership.work_family_id
    INTO subject_family_id
    FROM work_family_memberships AS membership
    WHERE membership.work_id = relation_subject_work_id
      AND membership.started_at <= assessment_at
      AND (
          membership.ended_at IS NULL
          OR membership.ended_at > assessment_at
      );

    SELECT membership.work_family_id
    INTO object_family_id
    FROM work_family_memberships AS membership
    WHERE membership.work_id = relation_object_work_id
      AND membership.started_at <= assessment_at
      AND (
          membership.ended_at IS NULL
          OR membership.ended_at > assessment_at
      );

    IF subject_family_id IS NULL
       OR object_family_id IS NULL
       OR subject_family_id <> object_family_id THEN
        RAISE EXCEPTION
            'version kind relation endpoints are not in one Work Family at assessment time'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_version_kind_relations_family_binding';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER work_version_kind_relation_decisions_binding_guard
BEFORE INSERT ON work_version_kind_relation_decisions
FOR EACH ROW
EXECUTE FUNCTION validate_work_version_kind_relation_decision();

CREATE TRIGGER work_version_kind_relation_decisions_immutable
BEFORE UPDATE OR DELETE ON work_version_kind_relation_decisions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();
