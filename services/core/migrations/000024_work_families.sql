CREATE TABLE work_families (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE FUNCTION protect_work_family_history()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'work_families rows are immutable'
            USING ERRCODE = '55000';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM work_family_memberships
        WHERE work_family_id = OLD.id
    ) OR EXISTS (
        SELECT 1
        FROM work_family_canonical_states
        WHERE work_family_id = OLD.id
    ) OR EXISTS (
        SELECT 1
        FROM work_family_canonical_decisions
        WHERE work_family_id = OLD.id
    ) THEN
        RAISE EXCEPTION 'non-empty work_families rows are immutable'
            USING ERRCODE = '55000';
    END IF;

    RETURN OLD;
END;
$$;

CREATE TRIGGER work_families_immutable
BEFORE UPDATE OR DELETE ON work_families
FOR EACH ROW
EXECUTE FUNCTION protect_work_family_history();

CREATE TABLE work_relation_assertions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    object_work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    relation_kind text NOT NULL,
    evidence_kind text NOT NULL,
    subject_source_record_id uuid NOT NULL,
    subject_source_path text NOT NULL,
    object_source_record_id uuid,
    object_source_path text,
    identifier_scheme text,
    identifier_value text,
    asserted_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_relation_assertions_subject_source_fkey
        FOREIGN KEY (subject_source_record_id, subject_work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        ON DELETE RESTRICT,
    CONSTRAINT work_relation_assertions_object_source_fkey
        FOREIGN KEY (object_source_record_id, object_work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        ON DELETE RESTRICT,
    CONSTRAINT work_relation_assertions_no_self_loop_check
        CHECK (subject_work_id <> object_work_id),
    CONSTRAINT work_relation_assertions_relation_kind_check
        CHECK (
            relation_kind IN (
                'is_preprint_of',
                'has_preprint',
                'is_version_of',
                'has_version',
                'replaces',
                'is_replaced_by',
                'is_correction_of',
                'is_retraction_of'
            )
        ),
    CONSTRAINT work_relation_assertions_evidence_kind_check
        CHECK (
            evidence_kind IN (
                'explicit_source_relation',
                'stable_shared_identifier'
            )
        ),
    CONSTRAINT work_relation_assertions_subject_source_path_check
        CHECK (
            btrim(subject_source_path) <> ''
            AND subject_source_path = btrim(subject_source_path)
        ),
    CONSTRAINT work_relation_assertions_object_source_path_check
        CHECK (
            object_source_path IS NULL
            OR (
                btrim(object_source_path) <> ''
                AND object_source_path = btrim(object_source_path)
            )
        ),
    CONSTRAINT work_relation_assertions_identifier_scheme_check
        CHECK (
            identifier_scheme IS NULL
            OR identifier_scheme IN (
                'doi',
                'arxiv',
                'openreview',
                'semantic_scholar',
                'openalex',
                'pmid',
                'pmcid'
            )
        ),
    CONSTRAINT work_relation_assertions_identifier_value_check
        CHECK (
            identifier_value IS NULL
            OR (
                btrim(identifier_value) <> ''
                AND identifier_value = btrim(identifier_value)
            )
        ),
    CONSTRAINT work_relation_assertions_evidence_shape_check
        CHECK (
            (
                evidence_kind = 'explicit_source_relation'
                AND object_source_record_id IS NULL
                AND object_source_path IS NULL
                AND identifier_scheme IS NULL
                AND identifier_value IS NULL
            )
            OR
            (
                evidence_kind = 'stable_shared_identifier'
                AND object_source_record_id IS NOT NULL
                AND object_source_path IS NOT NULL
                AND identifier_scheme IS NOT NULL
                AND identifier_value IS NOT NULL
            )
        ),
    CONSTRAINT work_relation_assertions_identity_key
        UNIQUE NULLS NOT DISTINCT (
            subject_work_id,
            object_work_id,
            relation_kind,
            evidence_kind,
            subject_source_record_id,
            subject_source_path,
            object_source_record_id,
            object_source_path,
            identifier_scheme,
            identifier_value
        )
);

CREATE INDEX idx_work_relation_assertions_subject
    ON work_relation_assertions(subject_work_id, asserted_at, id);

CREATE INDEX idx_work_relation_assertions_object
    ON work_relation_assertions(object_work_id, asserted_at, id);

CREATE TRIGGER work_relation_assertions_immutable
BEFORE UPDATE OR DELETE ON work_relation_assertions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_relation_decisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    assertion_id uuid NOT NULL
        REFERENCES work_relation_assertions(id) ON DELETE RESTRICT,
    supersedes_decision_id uuid
        REFERENCES work_relation_decisions(id) ON DELETE RESTRICT,
    outcome text NOT NULL,
    reason text NOT NULL,
    policy_version text NOT NULL,
    decided_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_relation_decisions_outcome_check
        CHECK (outcome IN ('accepted', 'rejected')),
    CONSTRAINT work_relation_decisions_no_self_supersede_check
        CHECK (
            supersedes_decision_id IS NULL
            OR supersedes_decision_id <> id
        ),
    CONSTRAINT work_relation_decisions_reason_check
        CHECK (
            btrim(reason) <> ''
            AND reason = btrim(reason)
        ),
    CONSTRAINT work_relation_decisions_policy_version_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT work_relation_decisions_supersedes_key
        UNIQUE (supersedes_decision_id),
    CONSTRAINT work_relation_decisions_identity_key
        UNIQUE NULLS NOT DISTINCT (
            assertion_id,
            supersedes_decision_id,
            outcome,
            reason,
            policy_version,
            decided_at
        )
);

CREATE UNIQUE INDEX work_relation_decisions_one_root_per_assertion
    ON work_relation_decisions(assertion_id)
    WHERE supersedes_decision_id IS NULL;

CREATE INDEX idx_work_relation_decisions_assertion_history
    ON work_relation_decisions(assertion_id, decided_at, id);

CREATE INDEX idx_work_relation_decisions_decided_at
    ON work_relation_decisions(decided_at, id);

CREATE FUNCTION enforce_work_relation_decision_chain()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    prior_assertion_id uuid;
    prior_decided_at timestamptz;
BEGIN
    IF NEW.supersedes_decision_id IS NULL THEN
        RETURN NEW;
    END IF;

    SELECT assertion_id, decided_at
    INTO prior_assertion_id, prior_decided_at
    FROM work_relation_decisions
    WHERE id = NEW.supersedes_decision_id
    FOR KEY SHARE;

    IF NOT FOUND THEN
        RETURN NEW;
    END IF;

    IF prior_assertion_id <> NEW.assertion_id THEN
        RAISE EXCEPTION
            'relation decision % cannot supersede a different assertion',
            NEW.id
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_relation_decisions_same_assertion_chain';
    END IF;

    IF NEW.decided_at < prior_decided_at THEN
        RAISE EXCEPTION
            'relation decision % predates superseded decision %',
            NEW.id,
            NEW.supersedes_decision_id
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_relation_decisions_monotonic_time';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER work_relation_decisions_chain_guard
BEFORE INSERT ON work_relation_decisions
FOR EACH ROW
EXECUTE FUNCTION enforce_work_relation_decision_chain();

CREATE FUNCTION enforce_one_current_work_relation_decision()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    current_count integer;
BEGIN
    SELECT count(*)
    INTO current_count
    FROM work_relation_decisions AS decision
    WHERE decision.assertion_id = NEW.assertion_id
      AND NOT EXISTS (
          SELECT 1
          FROM work_relation_decisions AS successor
          WHERE successor.supersedes_decision_id = decision.id
      );

    IF current_count <> 1 THEN
        RAISE EXCEPTION
            'relation assertion % must have exactly one current decision, found %',
            NEW.assertion_id,
            current_count
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_relation_decisions_exactly_one_current';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER work_relation_decisions_exactly_one_current
AFTER INSERT ON work_relation_decisions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_one_current_work_relation_decision();

CREATE TRIGGER work_relation_decisions_immutable
BEFORE UPDATE OR DELETE ON work_relation_decisions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_family_memberships (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_family_id uuid NOT NULL
        REFERENCES work_families(id) ON DELETE RESTRICT,
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    origin text NOT NULL,
    relation_decision_id uuid
        REFERENCES work_relation_decisions(id) ON DELETE RESTRICT,
    started_at timestamptz NOT NULL,
    ended_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_family_memberships_origin_check
        CHECK (origin IN ('singleton', 'relation_decision')),
    CONSTRAINT work_family_memberships_origin_shape_check
        CHECK (
            (
                origin = 'singleton'
                AND relation_decision_id IS NULL
            )
            OR
            (
                origin = 'relation_decision'
                AND relation_decision_id IS NOT NULL
            )
        ),
    CONSTRAINT work_family_memberships_interval_check
        CHECK (ended_at IS NULL OR ended_at >= started_at)
);

CREATE UNIQUE INDEX work_family_memberships_one_active_per_work
    ON work_family_memberships(work_id)
    WHERE ended_at IS NULL;

CREATE INDEX idx_work_family_memberships_active_family
    ON work_family_memberships(work_family_id, work_id)
    WHERE ended_at IS NULL;

CREATE INDEX idx_work_family_memberships_work_history
    ON work_family_memberships(work_id, started_at, id);

CREATE INDEX idx_work_family_memberships_family_history
    ON work_family_memberships(work_family_id, started_at, id);

CREATE FUNCTION protect_work_family_membership_history()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF NOT EXISTS (
            SELECT 1
            FROM works
            WHERE id = OLD.work_id
        ) THEN
            RETURN OLD;
        END IF;

        RAISE EXCEPTION 'work_family_memberships rows cannot be deleted'
            USING ERRCODE = '55000';
    END IF;

    IF OLD.ended_at IS NOT NULL
        OR NEW.id IS DISTINCT FROM OLD.id
        OR NEW.work_family_id IS DISTINCT FROM OLD.work_family_id
        OR NEW.work_id IS DISTINCT FROM OLD.work_id
        OR NEW.origin IS DISTINCT FROM OLD.origin
        OR NEW.relation_decision_id IS DISTINCT FROM OLD.relation_decision_id
        OR NEW.started_at IS DISTINCT FROM OLD.started_at
        OR NEW.created_at IS DISTINCT FROM OLD.created_at
        OR NEW.ended_at IS NULL
        OR NEW.ended_at < OLD.started_at
    THEN
        RAISE EXCEPTION 'work_family_memberships history is immutable'
            USING ERRCODE = '55000';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER work_family_memberships_history_guard
BEFORE UPDATE OR DELETE ON work_family_memberships
FOR EACH ROW
EXECUTE FUNCTION protect_work_family_membership_history();

CREATE FUNCTION restrict_work_delete_with_family_membership_history()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    membership_count integer;
    deletable_singleton_count integer;
    singleton_family_id uuid;
    singleton_started_at timestamptz;
    canonical_decision_count integer;
    deletable_canonical_decision_count integer;
    singleton_canonical_decision_id uuid;
    canonical_state_count integer;
    deletable_canonical_state_count integer;
BEGIN
    SELECT
        count(*),
        count(*) FILTER (
            WHERE ended_at IS NULL
              AND origin = 'singleton'
              AND relation_decision_id IS NULL
        )
    INTO membership_count, deletable_singleton_count
    FROM work_family_memberships
    WHERE work_id = OLD.id;

    IF membership_count <> 1
        OR deletable_singleton_count <> 1
    THEN
        RAISE EXCEPTION
            'Work % has Work Family history and cannot be deleted',
            OLD.id
            USING
                ERRCODE = '23001',
                CONSTRAINT = 'work_family_membership_history_restrict';
    END IF;

    SELECT work_family_id, started_at
    INTO singleton_family_id, singleton_started_at
    FROM work_family_memberships
    WHERE work_id = OLD.id;

    SELECT
        count(*),
        count(*) FILTER (
            WHERE work_family_id = singleton_family_id
              AND canonical_work_id = OLD.id
              AND decision_kind = 'singleton'
              AND relation_decision_id IS NULL
              AND prior_canonical_decision_id IS NULL
              AND basis = 'singleton'
              AND precedence = 0
              AND decided_at = singleton_started_at
        )
    INTO canonical_decision_count, deletable_canonical_decision_count
    FROM work_family_canonical_decisions
    WHERE work_family_id = singleton_family_id
       OR canonical_work_id = OLD.id;

    IF canonical_decision_count <> 1
        OR deletable_canonical_decision_count <> 1
    THEN
        RAISE EXCEPTION
            'Work % has Work Family canonical history and cannot be deleted',
            OLD.id
            USING
                ERRCODE = '23001',
                CONSTRAINT = 'work_family_membership_history_restrict';
    END IF;

    SELECT id
    INTO singleton_canonical_decision_id
    FROM work_family_canonical_decisions
    WHERE work_family_id = singleton_family_id
      AND canonical_work_id = OLD.id
      AND decision_kind = 'singleton'
      AND relation_decision_id IS NULL
      AND prior_canonical_decision_id IS NULL
      AND basis = 'singleton'
      AND precedence = 0
      AND decided_at = singleton_started_at;

    SELECT
        count(*),
        count(*) FILTER (
            WHERE work_family_id = singleton_family_id
              AND canonical_work_id = OLD.id
              AND canonical_decision_id = singleton_canonical_decision_id
              AND updated_at = singleton_started_at
        )
    INTO canonical_state_count, deletable_canonical_state_count
    FROM work_family_canonical_states
    WHERE work_family_id = singleton_family_id
       OR canonical_work_id = OLD.id;

    IF canonical_state_count <> 1
        OR deletable_canonical_state_count <> 1
    THEN
        RAISE EXCEPTION
            'Work % has Work Family canonical state history and cannot be deleted',
            OLD.id
            USING
                ERRCODE = '23001',
                CONSTRAINT = 'work_family_membership_history_restrict';
    END IF;

    RETURN OLD;
END;
$$;

CREATE TRIGGER works_family_membership_history_restrict
BEFORE DELETE ON works
FOR EACH ROW
EXECUTE FUNCTION restrict_work_delete_with_family_membership_history();

CREATE FUNCTION cleanup_deleted_singleton_work_family()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM works
        WHERE id = OLD.work_id
    ) THEN
        RETURN NULL;
    END IF;

    DELETE FROM work_family_canonical_states
    WHERE work_family_id = OLD.work_family_id;

    DELETE FROM work_family_canonical_decisions
    WHERE work_family_id = OLD.work_family_id;

    DELETE FROM work_families AS family
    WHERE family.id = OLD.work_family_id
      AND NOT EXISTS (
          SELECT 1
          FROM work_family_memberships AS membership
          WHERE membership.work_family_id = family.id
      );

    RETURN NULL;
END;
$$;

CREATE TRIGGER work_family_memberships_cleanup_deleted_singleton
AFTER DELETE ON work_family_memberships
FOR EACH ROW
EXECUTE FUNCTION cleanup_deleted_singleton_work_family();

CREATE FUNCTION enforce_exactly_one_active_work_family_membership()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    affected_work_id uuid;
    active_count integer;
BEGIN
    affected_work_id := COALESCE(NEW.work_id, OLD.work_id);

    IF NOT EXISTS (
        SELECT 1
        FROM works
        WHERE id = affected_work_id
    ) THEN
        RETURN NULL;
    END IF;

    SELECT count(*)
    INTO active_count
    FROM work_family_memberships
    WHERE work_id = affected_work_id
      AND ended_at IS NULL;

    IF active_count <> 1 THEN
        RAISE EXCEPTION
            'Work % must have exactly one active family membership, found %',
            affected_work_id,
            active_count
            USING
                ERRCODE = '23514',
                CONSTRAINT = 'work_family_memberships_exactly_one_active';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER work_family_memberships_exactly_one_active
AFTER INSERT OR UPDATE OR DELETE ON work_family_memberships
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_exactly_one_active_work_family_membership();

CREATE TABLE work_family_canonical_decisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_family_id uuid NOT NULL
        REFERENCES work_families(id) ON DELETE RESTRICT,
    canonical_work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    relation_decision_id uuid
        REFERENCES work_relation_decisions(id) ON DELETE RESTRICT,
    prior_canonical_decision_id uuid
        REFERENCES work_family_canonical_decisions(id) ON DELETE RESTRICT,
    decision_kind text NOT NULL,
    basis text NOT NULL,
    precedence smallint NOT NULL,
    reason text NOT NULL,
    policy_version text NOT NULL,
    decided_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_family_canonical_decisions_kind_check
        CHECK (
            decision_kind IN (
                'singleton',
                'accepted_relation',
                'carried_forward',
                'component_rebuild'
            )
        ),
    CONSTRAINT work_family_canonical_decisions_shape_check
        CHECK (
            (
                decision_kind = 'singleton'
                AND relation_decision_id IS NULL
                AND prior_canonical_decision_id IS NULL
            )
            OR
            (
                decision_kind = 'accepted_relation'
                AND relation_decision_id IS NOT NULL
                AND prior_canonical_decision_id IS NULL
            )
            OR
            (
                decision_kind = 'carried_forward'
                AND relation_decision_id IS NULL
                AND prior_canonical_decision_id IS NOT NULL
            )
            OR
            (
                decision_kind = 'component_rebuild'
                AND relation_decision_id IS NOT NULL
                AND prior_canonical_decision_id IS NULL
            )
        ),
    CONSTRAINT work_family_canonical_decisions_basis_precedence_check
        CHECK (
            (
                basis = 'singleton'
                AND precedence = 0
            )
            OR
            (
                basis IN ('is_version_of', 'has_version')
                AND precedence = 100
            )
            OR
            (
                basis IN (
                    'replaces',
                    'is_replaced_by',
                    'is_correction_of',
                    'is_retraction_of'
                )
                AND precedence = 200
            )
            OR
            (
                basis IN ('is_preprint_of', 'has_preprint')
                AND precedence = 300
            )
        ),
    CONSTRAINT work_family_canonical_decisions_reason_check
        CHECK (
            btrim(reason) <> ''
            AND reason = btrim(reason)
        ),
    CONSTRAINT work_family_canonical_decisions_policy_version_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT work_family_canonical_decisions_relation_key
        UNIQUE (work_family_id, relation_decision_id),
    CONSTRAINT work_family_canonical_decisions_projection_key
        UNIQUE (id, work_family_id, canonical_work_id)
);

CREATE INDEX idx_work_family_canonical_decisions_family_history
    ON work_family_canonical_decisions(
        work_family_id,
        precedence DESC,
        decided_at,
        id
    );

CREATE INDEX idx_work_family_canonical_decisions_work_history
    ON work_family_canonical_decisions(
        canonical_work_id,
        decided_at,
        id
    );

CREATE FUNCTION protect_work_family_canonical_decision_history()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE'
        AND OLD.decision_kind = 'singleton'
        AND OLD.relation_decision_id IS NULL
        AND OLD.prior_canonical_decision_id IS NULL
        AND OLD.basis = 'singleton'
        AND OLD.precedence = 0
        AND NOT EXISTS (
            SELECT 1
            FROM works
            WHERE id = OLD.canonical_work_id
        )
        AND NOT EXISTS (
            SELECT 1
            FROM work_family_canonical_decisions AS other_decision
            WHERE other_decision.id <> OLD.id
              AND (
                  other_decision.work_family_id = OLD.work_family_id
                  OR other_decision.canonical_work_id = OLD.canonical_work_id
              )
        )
    THEN
        RETURN OLD;
    END IF;

    RAISE EXCEPTION 'work_family_canonical_decisions rows are immutable'
        USING ERRCODE = '55000';
END;
$$;

CREATE TRIGGER work_family_canonical_decisions_immutable
BEFORE UPDATE OR DELETE ON work_family_canonical_decisions
FOR EACH ROW
EXECUTE FUNCTION protect_work_family_canonical_decision_history();

CREATE TABLE work_family_canonical_states (
    work_family_id uuid PRIMARY KEY
        REFERENCES work_families(id) ON DELETE RESTRICT,
    canonical_work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    canonical_decision_id uuid NOT NULL UNIQUE,
    updated_at timestamptz NOT NULL,
    CONSTRAINT work_family_canonical_states_decision_fkey
        FOREIGN KEY (
            canonical_decision_id,
            work_family_id,
            canonical_work_id
        )
        REFERENCES work_family_canonical_decisions(
            id,
            work_family_id,
            canonical_work_id
        )
        ON DELETE CASCADE
);

CREATE INDEX idx_work_family_canonical_states_canonical_work
    ON work_family_canonical_states(canonical_work_id, work_family_id);

CREATE FUNCTION validate_active_work_family_canonical(
    family_id uuid
)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    current_canonical_work_id uuid;
    active_membership_count integer;
    canonical_state_count integer;
    canonical_membership_count integer;
BEGIN
    SELECT count(*)
    INTO active_membership_count
    FROM work_family_memberships
    WHERE work_family_id = family_id
      AND ended_at IS NULL;

    SELECT count(*)
    INTO canonical_state_count
    FROM work_family_canonical_states
    WHERE work_family_id = family_id;

    IF active_membership_count = 0
        AND canonical_state_count = 0
    THEN
        RETURN;
    END IF;

    IF active_membership_count > 0
        AND canonical_state_count <> 1
    THEN
        RAISE EXCEPTION
            'active family % must have exactly one canonical state, found %',
            family_id,
            canonical_state_count
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_family_canonical_states_exactly_one_active_family';
    END IF;

    SELECT canonical_work_id
    INTO current_canonical_work_id
    FROM work_family_canonical_states
    WHERE work_family_id = family_id;

    SELECT count(*)
    INTO canonical_membership_count
    FROM work_family_memberships
    WHERE work_family_id = family_id
      AND work_id = current_canonical_work_id
      AND ended_at IS NULL;

    IF canonical_membership_count <> 1 THEN
        RAISE EXCEPTION
            'canonical Work % is not an active member of family %',
            current_canonical_work_id,
            family_id
            USING
                ERRCODE = '23514',
                CONSTRAINT = 'work_family_canonical_states_active_member';
    END IF;
END;
$$;

CREATE FUNCTION validate_queued_active_work_family_canonical()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM validate_active_work_family_canonical(NEW.work_family_id);
    DELETE FROM pg_temp.work_family_canonical_validation_queue
    WHERE work_family_id = NEW.work_family_id;
    RETURN NULL;
END;
$$;

CREATE FUNCTION queue_active_work_family_canonical_validation(
    family_id uuid
)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF to_regclass(
        'pg_temp.work_family_canonical_validation_queue'
    ) IS NULL THEN
        CREATE TEMPORARY TABLE work_family_canonical_validation_queue (
            work_family_id uuid PRIMARY KEY
        ) ON COMMIT DELETE ROWS;

        CREATE CONSTRAINT TRIGGER
            work_family_canonical_validation_deferred
        AFTER INSERT
        ON work_family_canonical_validation_queue
        DEFERRABLE INITIALLY DEFERRED
        FOR EACH ROW
        EXECUTE FUNCTION validate_queued_active_work_family_canonical();
    END IF;

    INSERT INTO pg_temp.work_family_canonical_validation_queue (
        work_family_id
    ) VALUES (
        family_id
    )
    ON CONFLICT (work_family_id) DO NOTHING;
END;
$$;

CREATE FUNCTION enforce_active_work_family_canonical_state()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE'
        OR (
            TG_OP = 'UPDATE'
            AND OLD.work_family_id IS DISTINCT FROM NEW.work_family_id
        )
    THEN
        PERFORM queue_active_work_family_canonical_validation(
            OLD.work_family_id
        );
    END IF;

    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        PERFORM queue_active_work_family_canonical_validation(
            NEW.work_family_id
        );
    END IF;

    RETURN NULL;
END;
$$;

CREATE TRIGGER work_family_canonical_states_active_member
AFTER INSERT OR UPDATE OR DELETE ON work_family_canonical_states
FOR EACH ROW
EXECUTE FUNCTION enforce_active_work_family_canonical_state();

CREATE FUNCTION enforce_membership_work_family_canonical()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP IN ('UPDATE', 'DELETE') THEN
        PERFORM queue_active_work_family_canonical_validation(
            OLD.work_family_id
        );
    END IF;

    IF TG_OP IN ('INSERT', 'UPDATE')
        AND (
            TG_OP = 'INSERT'
            OR NEW.work_family_id IS DISTINCT FROM OLD.work_family_id
            OR NEW.work_id IS DISTINCT FROM OLD.work_id
            OR NEW.ended_at IS DISTINCT FROM OLD.ended_at
        )
    THEN
        PERFORM queue_active_work_family_canonical_validation(
            NEW.work_family_id
        );
    END IF;

    RETURN NULL;
END;
$$;

CREATE TRIGGER work_family_memberships_canonical_active
AFTER INSERT OR UPDATE OR DELETE ON work_family_memberships
FOR EACH ROW
EXECUTE FUNCTION enforce_membership_work_family_canonical();

CREATE FUNCTION create_singleton_work_family_membership()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    family_id uuid;
    canonical_decision_id uuid;
BEGIN
    INSERT INTO work_families (created_at)
    VALUES (NEW.created_at)
    RETURNING id INTO family_id;

    INSERT INTO work_family_memberships (
        work_family_id,
        work_id,
        origin,
        started_at
    ) VALUES (
        family_id,
        NEW.id,
        'singleton',
        NEW.created_at
    );

    INSERT INTO work_family_canonical_decisions (
        work_family_id,
        canonical_work_id,
        decision_kind,
        basis,
        precedence,
        reason,
        policy_version,
        decided_at
    ) VALUES (
        family_id,
        NEW.id,
        'singleton',
        'singleton',
        0,
        'singleton Work',
        'work-family-canonical/v1',
        NEW.created_at
    )
    RETURNING id INTO canonical_decision_id;

    INSERT INTO work_family_canonical_states (
        work_family_id,
        canonical_work_id,
        canonical_decision_id,
        updated_at
    ) VALUES (
        family_id,
        NEW.id,
        canonical_decision_id,
        NEW.created_at
    );

    RETURN NEW;
END;
$$;

CREATE TRIGGER works_create_singleton_work_family
AFTER INSERT ON works
FOR EACH ROW
EXECUTE FUNCTION create_singleton_work_family_membership();

DO $$
DECLARE
    work_row record;
    family_id uuid;
    canonical_decision_id uuid;
BEGIN
    FOR work_row IN
        SELECT work.id, work.created_at
        FROM works AS work
        WHERE NOT EXISTS (
            SELECT 1
            FROM work_family_memberships AS membership
            WHERE membership.work_id = work.id
              AND membership.ended_at IS NULL
        )
        ORDER BY work.created_at, work.id
    LOOP
        INSERT INTO work_families (created_at)
        VALUES (work_row.created_at)
        RETURNING id INTO family_id;

        INSERT INTO work_family_memberships (
            work_family_id,
            work_id,
            origin,
            started_at
        ) VALUES (
            family_id,
            work_row.id,
            'singleton',
            work_row.created_at
        );

        INSERT INTO work_family_canonical_decisions (
            work_family_id,
            canonical_work_id,
            decision_kind,
            basis,
            precedence,
            reason,
            policy_version,
            decided_at
        ) VALUES (
            family_id,
            work_row.id,
            'singleton',
            'singleton',
            0,
            'singleton Work',
            'work-family-canonical/v1',
            work_row.created_at
        )
        RETURNING id INTO canonical_decision_id;

        INSERT INTO work_family_canonical_states (
            work_family_id,
            canonical_work_id,
            canonical_decision_id,
            updated_at
        ) VALUES (
            family_id,
            work_row.id,
            canonical_decision_id,
            work_row.created_at
        );
    END LOOP;
END;
$$;
