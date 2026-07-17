CREATE TABLE biomedical_publication_eligibility_decisions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL,
    policy_version text NOT NULL,
    metric_year integer NOT NULL,
    subject_version_id uuid NOT NULL,
    decision text NOT NULL,
    venue_id uuid,
    journal_subject_metric_id uuid,
    evidence jsonb NOT NULL,
    assessed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT biomedical_publication_eligibility_decisions_work_fkey
        FOREIGN KEY (work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT biomedical_publication_eligibility_decisions_subject_version_fkey
        FOREIGN KEY (subject_version_id)
        REFERENCES subject_versions(id)
        ON DELETE RESTRICT,
    CONSTRAINT biomedical_publication_eligibility_decisions_venue_fkey
        FOREIGN KEY (venue_id)
        REFERENCES venues(id)
        ON DELETE RESTRICT,
    CONSTRAINT biomedical_publication_eligibility_decisions_subject_metric_fkey
        FOREIGN KEY (journal_subject_metric_id)
        REFERENCES journal_subject_metrics(id)
        ON DELETE RESTRICT,
    CONSTRAINT biomedical_publication_eligibility_decisions_policy_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT biomedical_publication_eligibility_decisions_year_check
        CHECK (metric_year BETWEEN 1900 AND 3000),
    CONSTRAINT biomedical_publication_eligibility_decisions_decision_check
        CHECK (decision IN ('accepted', 'rejected', 'missing')),
    CONSTRAINT biomedical_publication_eligibility_decisions_evidence_check
        CHECK (
            jsonb_typeof(evidence) = 'object'
            AND evidence <> '{}'::jsonb
        ),
    CONSTRAINT biomedical_publication_eligibility_decisions_shape_check
        CHECK (
            (
                decision = 'accepted'
                AND venue_id IS NOT NULL
                AND journal_subject_metric_id IS NOT NULL
            )
            OR
            (
                decision = 'rejected'
                AND venue_id IS NOT NULL
                AND journal_subject_metric_id IS NULL
            )
            OR
            (
                decision = 'missing'
                AND journal_subject_metric_id IS NULL
            )
        ),
    CONSTRAINT biomedical_publication_eligibility_decisions_identity_key
        UNIQUE (
            work_id,
            policy_version,
            metric_year,
            subject_version_id
        )
);

CREATE INDEX idx_biomedical_publication_eligibility_decision
    ON biomedical_publication_eligibility_decisions(
        decision,
        metric_year DESC,
        subject_version_id,
        work_id
    );

CREATE INDEX idx_biomedical_publication_eligibility_venue
    ON biomedical_publication_eligibility_decisions(
        venue_id,
        metric_year,
        subject_version_id,
        decision
    )
    WHERE venue_id IS NOT NULL;

CREATE FUNCTION enforce_biomedical_publication_eligibility_semantics()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_subject_version_key text;
    actual_venue_id uuid;
    actual_venue_type text;
    actual_issn_l text;
    actual_issn text;
    actual_eissn text;
    decisive_metric_id uuid;
    decisive_subject_rule_id uuid;
    decisive_subject_id uuid;
    decisive_category text;
    exact_metric_count integer;
    exact_subject_match_count integer;
    verified_journal boolean;
BEGIN
    SELECT version.version_key
    INTO actual_subject_version_key
    FROM subject_versions AS version
    WHERE version.id = NEW.subject_version_id;

    SELECT
        work.venue_id,
        venue.venue_type,
        venue.issn_l,
        venue.issn,
        venue.eissn
    INTO
        actual_venue_id,
        actual_venue_type,
        actual_issn_l,
        actual_issn,
        actual_eissn
    FROM works AS work
    LEFT JOIN venues AS venue
      ON venue.id = work.venue_id
    WHERE work.id = NEW.work_id;

    verified_journal :=
        actual_venue_type = 'journal'
        AND num_nonnulls(actual_issn_l, actual_issn, actual_eissn) > 0;

    IF jsonb_typeof(NEW.evidence -> 'policy_version')
            IS DISTINCT FROM 'string'
       OR NEW.evidence ->> 'policy_version'
            IS DISTINCT FROM NEW.policy_version
       OR jsonb_typeof(NEW.evidence -> 'metric_year')
            IS DISTINCT FROM 'number'
       OR (NEW.evidence ->> 'metric_year')::integer
            IS DISTINCT FROM NEW.metric_year
       OR jsonb_typeof(NEW.evidence -> 'subject_version_id')
            IS DISTINCT FROM 'string'
       OR NEW.evidence ->> 'subject_version_id'
            IS DISTINCT FROM NEW.subject_version_id::text
       OR jsonb_typeof(NEW.evidence -> 'subject_version_key')
            IS DISTINCT FROM 'string'
       OR NEW.evidence ->> 'subject_version_key'
            IS DISTINCT FROM actual_subject_version_key
       OR jsonb_typeof(NEW.evidence -> 'metrics')
            IS DISTINCT FROM 'array'
       OR jsonb_typeof(NEW.evidence -> 'matches')
            IS DISTINCT FROM 'array'
    THEN
        RAISE EXCEPTION
            'biomedical public eligibility evidence metadata does not match the immutable decision identity'
            USING ERRCODE = '23514',
                  CONSTRAINT =
                    'biomedical_publication_eligibility_decisions_semantics';
    END IF;

    IF NEW.venue_id IS NOT NULL
       AND NEW.venue_id IS DISTINCT FROM actual_venue_id THEN
        RAISE EXCEPTION
            'biomedical public eligibility Venue does not match the Work Venue'
            USING ERRCODE = '23514',
                  CONSTRAINT =
                    'biomedical_publication_eligibility_decisions_semantics';
    END IF;

    SELECT count(*)
    INTO exact_metric_count
    FROM venue_metric_snapshots AS metric
    WHERE metric.venue_id = actual_venue_id
      AND metric.metric_year = NEW.metric_year;

    SELECT count(*)
    INTO exact_subject_match_count
    FROM journal_subject_metrics AS link
    JOIN venue_metric_snapshots AS metric
      ON metric.id = link.venue_metric_snapshot_id
    JOIN biomedical_subject_rules AS rule
      ON rule.id = link.subject_rule_id
    JOIN subjects AS subject
      ON subject.id = rule.subject_id
     AND subject.subject_version_id = rule.subject_version_id
    WHERE metric.venue_id = actual_venue_id
      AND metric.metric_year = NEW.metric_year
      AND rule.subject_version_id = NEW.subject_version_id;

    IF NEW.decision = 'accepted' THEN
        IF NOT verified_journal
           OR NEW.venue_id IS DISTINCT FROM actual_venue_id
           OR exact_subject_match_count = 0
           OR NEW.evidence ? 'reason'
           OR jsonb_array_length(NEW.evidence -> 'matches') = 0 THEN
            RAISE EXCEPTION
                'accepted biomedical public eligibility requires a verified journal Venue and exact Subject metric evidence'
                USING ERRCODE = '23514',
                      CONSTRAINT =
                        'biomedical_publication_eligibility_decisions_semantics';
        END IF;

        SELECT
            metric.id,
            rule.id,
            subject.id,
            link.jcr_category
        INTO
            decisive_metric_id,
            decisive_subject_rule_id,
            decisive_subject_id,
            decisive_category
        FROM journal_subject_metrics AS link
        JOIN venue_metric_snapshots AS metric
          ON metric.id = link.venue_metric_snapshot_id
        JOIN biomedical_subject_rules AS rule
          ON rule.id = link.subject_rule_id
        JOIN subjects AS subject
          ON subject.id = rule.subject_id
         AND subject.subject_version_id = rule.subject_version_id
        WHERE link.id = NEW.journal_subject_metric_id
          AND metric.venue_id = actual_venue_id
          AND metric.metric_year = NEW.metric_year
          AND rule.subject_version_id = NEW.subject_version_id;

        IF NOT FOUND OR NOT EXISTS (
            SELECT 1
            FROM jsonb_array_elements(NEW.evidence -> 'matches') AS item
            WHERE jsonb_typeof(item) = 'object'
              AND item ->> 'journal_subject_metric_id' =
                    NEW.journal_subject_metric_id::text
              AND item ->> 'venue_metric_snapshot_id' =
                    decisive_metric_id::text
              AND item ->> 'venue_id' = actual_venue_id::text
              AND (item ->> 'metric_year')::integer = NEW.metric_year
              AND item ->> 'subject_version_id' =
                    NEW.subject_version_id::text
              AND item ->> 'subject_rule_id' =
                    decisive_subject_rule_id::text
              AND item ->> 'subject_id' = decisive_subject_id::text
              AND item ->> 'jcr_category' = decisive_category
        ) THEN
            RAISE EXCEPTION
                'accepted biomedical public eligibility decisive evidence is not an exact stored Subject metric link'
                USING ERRCODE = '23514',
                      CONSTRAINT =
                        'biomedical_publication_eligibility_decisions_semantics';
        END IF;
    ELSIF NEW.decision = 'rejected' THEN
        IF NOT verified_journal
           OR NEW.venue_id IS DISTINCT FROM actual_venue_id
           OR exact_metric_count = 0
           OR exact_subject_match_count <> 0
           OR NEW.evidence ->> 'reason'
                IS DISTINCT FROM 'no_exact_subject_metric_link'
           OR jsonb_array_length(NEW.evidence -> 'matches') <> 0 THEN
            RAISE EXCEPTION
                'rejected biomedical public eligibility requires verified journal-year evidence with no exact Subject link'
                USING ERRCODE = '23514',
                      CONSTRAINT =
                        'biomedical_publication_eligibility_decisions_semantics';
        END IF;
    ELSE
        IF exact_subject_match_count <> 0
           OR jsonb_array_length(NEW.evidence -> 'matches') <> 0 THEN
            RAISE EXCEPTION
                'missing biomedical public eligibility cannot hide exact Subject metric evidence'
                USING ERRCODE = '23514',
                      CONSTRAINT =
                        'biomedical_publication_eligibility_decisions_semantics';
        END IF;

        CASE NEW.evidence ->> 'reason'
        WHEN 'venue_not_bound' THEN
            IF actual_venue_id IS NOT NULL OR NEW.venue_id IS NOT NULL THEN
                RAISE EXCEPTION
                    'venue_not_bound requires a Work without a Venue'
                    USING ERRCODE = '23514',
                          CONSTRAINT =
                            'biomedical_publication_eligibility_decisions_semantics';
            END IF;
        WHEN 'venue_not_verified_journal' THEN
            IF actual_venue_id IS NULL
               OR verified_journal
               OR NEW.venue_id IS NOT NULL THEN
                RAISE EXCEPTION
                    'venue_not_verified_journal requires a bound non-verified Venue'
                    USING ERRCODE = '23514',
                          CONSTRAINT =
                            'biomedical_publication_eligibility_decisions_semantics';
            END IF;
        WHEN 'metric_year_evidence_missing' THEN
            IF NOT verified_journal
               OR NEW.venue_id IS DISTINCT FROM actual_venue_id
               OR exact_metric_count <> 0 THEN
                RAISE EXCEPTION
                    'metric_year_evidence_missing requires a verified journal Venue without declared-year metrics'
                    USING ERRCODE = '23514',
                          CONSTRAINT =
                            'biomedical_publication_eligibility_decisions_semantics';
            END IF;
        ELSE
            RAISE EXCEPTION
                'missing biomedical public eligibility requires an exact missing evidence reason'
                USING ERRCODE = '23514',
                      CONSTRAINT =
                        'biomedical_publication_eligibility_decisions_semantics';
        END CASE;
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER biomedical_publication_eligibility_decisions_semantics
BEFORE INSERT ON biomedical_publication_eligibility_decisions
FOR EACH ROW
EXECUTE FUNCTION enforce_biomedical_publication_eligibility_semantics();

CREATE TRIGGER biomedical_publication_eligibility_decisions_immutable
BEFORE UPDATE OR DELETE ON biomedical_publication_eligibility_decisions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();
