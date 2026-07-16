LOCK TABLE jcr_import_receipts IN ACCESS EXCLUSIVE MODE;
LOCK TABLE venue_metric_snapshots IN ACCESS EXCLUSIVE MODE;
LOCK TABLE jcr_import_receipt_metrics IN ACCESS EXCLUSIVE MODE;
LOCK TABLE venue_aliases IN ACCESS EXCLUSIVE MODE;
LOCK TABLE jcr_import_receipt_aliases IN ACCESS EXCLUSIVE MODE;

ALTER TABLE jcr_import_receipt_metrics
    DROP CONSTRAINT jcr_import_receipt_metrics_receipt_metric_key;

CREATE INDEX idx_jcr_import_receipt_metrics_receipt_metric
    ON jcr_import_receipt_metrics(import_receipt_id, metric_snapshot_id);

DO $$
DECLARE
    receipt record;
    existing_links integer;
    missing_links integer;
    candidate_links integer;
    invalid_existing_links integer;
    candidate_metric_id uuid;
    backfilled_link integer;
BEGIN
    FOR receipt IN
        SELECT
            id,
            file_sha256,
            source,
            imported_at,
            input_rows
        FROM jcr_import_receipts
        ORDER BY id
    LOOP
        SELECT count(*)
        INTO existing_links
        FROM jcr_import_receipt_metrics
        WHERE import_receipt_id = receipt.id;

        IF existing_links > receipt.input_rows THEN
            RAISE EXCEPTION
                'legacy JCR receipt % (%): association rows % exceed declared input rows %',
                receipt.id,
                receipt.file_sha256,
                existing_links,
                receipt.input_rows
                USING ERRCODE = '23514',
                      CONSTRAINT = 'jcr_import_receipts_metric_association_integrity';
        END IF;

        missing_links := receipt.input_rows - existing_links;
        IF missing_links = 0 THEN
            CONTINUE;
        END IF;

        SELECT count(*)
        INTO candidate_links
        FROM (
            SELECT DISTINCT metric.id
            FROM jcr_import_receipt_aliases AS receipt_alias
            JOIN venue_aliases AS alias
              ON alias.id = receipt_alias.venue_alias_id
            JOIN venue_metric_snapshots AS metric
              ON metric.venue_id = alias.venue_id
            WHERE receipt_alias.import_receipt_id = receipt.id
              AND alias.source = receipt.source
              AND metric.source_name = receipt.source
              AND metric.captured_at <= receipt.imported_at
        ) AS candidates;

        SELECT count(*)
        INTO invalid_existing_links
        FROM jcr_import_receipt_metrics AS existing
        WHERE existing.import_receipt_id = receipt.id
          AND NOT EXISTS (
              SELECT 1
              FROM jcr_import_receipt_aliases AS receipt_alias
              JOIN venue_aliases AS alias
                ON alias.id = receipt_alias.venue_alias_id
              JOIN venue_metric_snapshots AS metric
                ON metric.venue_id = alias.venue_id
              WHERE receipt_alias.import_receipt_id = receipt.id
                AND alias.source = receipt.source
                AND metric.source_name = receipt.source
                AND metric.captured_at <= receipt.imported_at
                AND metric.id = existing.metric_snapshot_id
          );

        IF invalid_existing_links <> 0
           OR candidate_links <> 1 THEN
            RAISE EXCEPTION
                'cannot deterministically associate legacy JCR receipt % (%): missing rows %, exact candidate metric keys %, invalid existing links %',
                receipt.id,
                receipt.file_sha256,
                missing_links,
                candidate_links,
                invalid_existing_links
                USING ERRCODE = '23514',
                      CONSTRAINT = 'jcr_import_receipts_metric_association_integrity';
        END IF;

        SELECT DISTINCT metric.id
        INTO candidate_metric_id
        FROM jcr_import_receipt_aliases AS receipt_alias
        JOIN venue_aliases AS alias
          ON alias.id = receipt_alias.venue_alias_id
        JOIN venue_metric_snapshots AS metric
          ON metric.venue_id = alias.venue_id
        WHERE receipt_alias.import_receipt_id = receipt.id
          AND alias.source = receipt.source
          AND metric.source_name = receipt.source
          AND metric.captured_at <= receipt.imported_at;

        FOR backfilled_link IN 1..missing_links LOOP
            INSERT INTO jcr_import_receipt_metrics (
                import_receipt_id,
                metric_snapshot_id
            ) VALUES (
                receipt.id,
                candidate_metric_id
            );
        END LOOP;
    END LOOP;
END;
$$;

DO $$
DECLARE
    invalid_receipt record;
BEGIN
    SELECT
        receipt.id,
        receipt.file_sha256,
        receipt.input_rows,
        receipt.inserted_rows,
        receipt.unchanged_rows,
        count(receipt_metric.id) AS associated_rows,
        (
            SELECT count(*)
            FROM venue_metric_snapshots AS inserted_metric
            WHERE inserted_metric.jcr_import_receipt_id = receipt.id
        ) AS creator_rows
    INTO invalid_receipt
    FROM jcr_import_receipts AS receipt
    LEFT JOIN jcr_import_receipt_metrics AS receipt_metric
      ON receipt_metric.import_receipt_id = receipt.id
    GROUP BY
        receipt.id,
        receipt.file_sha256,
        receipt.input_rows,
        receipt.inserted_rows,
        receipt.unchanged_rows
    HAVING
        count(receipt_metric.id) <> receipt.input_rows
        OR (
            SELECT count(*)
            FROM venue_metric_snapshots AS inserted_metric
            WHERE inserted_metric.jcr_import_receipt_id = receipt.id
        ) <> receipt.inserted_rows
        OR count(receipt_metric.id) - (
            SELECT count(*)
            FROM venue_metric_snapshots AS inserted_metric
            WHERE inserted_metric.jcr_import_receipt_id = receipt.id
        ) <> receipt.unchanged_rows
    ORDER BY receipt.id
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION
            'invalid historical JCR receipt % (%): associated rows %/%; creator rows %/%; unchanged rows %/%',
            invalid_receipt.id,
            invalid_receipt.file_sha256,
            invalid_receipt.associated_rows,
            invalid_receipt.input_rows,
            invalid_receipt.creator_rows,
            invalid_receipt.inserted_rows,
            invalid_receipt.associated_rows - invalid_receipt.creator_rows,
            invalid_receipt.unchanged_rows
            USING ERRCODE = '23514',
                  CONSTRAINT = 'jcr_import_receipts_metric_association_integrity';
    END IF;
END;
$$;

CREATE FUNCTION enforce_jcr_import_receipt_metric_integrity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_receipt_id uuid;
    declared_input_rows integer;
    declared_inserted_rows integer;
    declared_unchanged_rows integer;
    associated_rows integer;
    creator_rows integer;
BEGIN
    target_receipt_id := NEW.id;

    SELECT input_rows, inserted_rows, unchanged_rows
    INTO
        declared_input_rows,
        declared_inserted_rows,
        declared_unchanged_rows
    FROM jcr_import_receipts
    WHERE id = target_receipt_id;

    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    SELECT count(*)
    INTO associated_rows
    FROM jcr_import_receipt_metrics
    WHERE import_receipt_id = target_receipt_id;

    SELECT count(*)
    INTO creator_rows
    FROM venue_metric_snapshots
    WHERE jcr_import_receipt_id = target_receipt_id;

    IF associated_rows <> declared_input_rows
       OR creator_rows <> declared_inserted_rows
       OR associated_rows - creator_rows <> declared_unchanged_rows THEN
        RAISE EXCEPTION
            'JCR receipt % integrity mismatch: associated rows %/%; creator rows %/%; unchanged rows %/%',
            target_receipt_id,
            associated_rows,
            declared_input_rows,
            creator_rows,
            declared_inserted_rows,
            associated_rows - creator_rows,
            declared_unchanged_rows
            USING ERRCODE = '23514',
                  CONSTRAINT = 'jcr_import_receipts_metric_association_integrity';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER jcr_import_receipts_metric_integrity
AFTER INSERT ON jcr_import_receipts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_jcr_import_receipt_metric_integrity();

CREATE FUNCTION enforce_jcr_import_receipt_child_transaction()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_receipt_id uuid;
    receipt_created_in_current_transaction boolean;
BEGIN
    IF TG_TABLE_NAME = 'venue_metric_snapshots' THEN
        target_receipt_id := NEW.jcr_import_receipt_id;
        IF target_receipt_id IS NULL THEN
            RETURN NEW;
        END IF;
    ELSE
        target_receipt_id := NEW.import_receipt_id;
    END IF;

    SELECT receipt.xmin = pg_current_xact_id()::text::xid
    INTO receipt_created_in_current_transaction
    FROM jcr_import_receipts AS receipt
    WHERE receipt.id = target_receipt_id;

    IF NOT FOUND THEN
        IF TG_TABLE_NAME = 'venue_metric_snapshots'
           AND TG_WHEN = 'BEFORE' THEN
            RETURN NEW;
        END IF;

        RAISE EXCEPTION
            'JCR receipt % must be created in the current transaction before child rows are committed',
            target_receipt_id
            USING ERRCODE = '23514',
                  CONSTRAINT = 'jcr_import_receipt_children_current_transaction';
    END IF;

    IF NOT receipt_created_in_current_transaction THEN
        RAISE EXCEPTION
            'JCR receipt % is sealed against post-commit child rows',
            target_receipt_id
            USING ERRCODE = '23514',
                  CONSTRAINT = 'jcr_import_receipt_children_current_transaction';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER venue_metric_snapshots_receipt_transaction
BEFORE INSERT ON venue_metric_snapshots
FOR EACH ROW
WHEN (NEW.jcr_import_receipt_id IS NOT NULL)
EXECUTE FUNCTION enforce_jcr_import_receipt_child_transaction();

CREATE CONSTRAINT TRIGGER venue_metric_snapshots_receipt_transaction_deferred
AFTER INSERT ON venue_metric_snapshots
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
WHEN (NEW.jcr_import_receipt_id IS NOT NULL)
EXECUTE FUNCTION enforce_jcr_import_receipt_child_transaction();

CREATE TRIGGER jcr_import_receipt_metrics_receipt_transaction
BEFORE INSERT ON jcr_import_receipt_metrics
FOR EACH ROW
EXECUTE FUNCTION enforce_jcr_import_receipt_child_transaction();

DROP TRIGGER venue_policy_assessments_venue_type_semantics
    ON venue_policy_assessments;

DROP FUNCTION enforce_venue_policy_assessment_venue_type();

DO $$
DECLARE
    invalid_assessment record;
BEGIN
    SELECT
        assessment.id,
        assessment.venue_id,
        assessment.decision,
        venue.venue_type
    INTO invalid_assessment
    FROM venue_policy_assessments AS assessment
    JOIN venues AS venue
      ON venue.id = assessment.venue_id
    WHERE
        jsonb_typeof(assessment.evidence -> 'venue_type') IS DISTINCT FROM 'string'
        OR assessment.evidence ->> 'venue_type' IS DISTINCT FROM venue.venue_type
        OR jsonb_typeof(assessment.matched_rules) IS DISTINCT FROM 'array'
        OR (
            assessment.decision = 'not_applicable'
            AND (
                venue.venue_type = 'journal'
                OR jsonb_array_length(assessment.matched_rules) <> 0
                OR assessment.evidence ->> 'reason'
                   IS DISTINCT FROM 'venue_type_not_journal'
            )
        )
        OR (
            assessment.decision = 'accepted'
            AND (
                venue.venue_type <> 'journal'
                OR jsonb_array_length(assessment.matched_rules) = 0
            )
        )
        OR (
            assessment.decision IN ('rejected', 'unknown')
            AND (
                venue.venue_type <> 'journal'
                OR jsonb_array_length(assessment.matched_rules) <> 0
            )
        )
        OR assessment.decision NOT IN (
            'accepted',
            'rejected',
            'unknown',
            'not_applicable'
        )
    ORDER BY assessment.id
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION
            'invalid historical venue_policy_assessment %: venue % type %, decision %',
            invalid_assessment.id,
            invalid_assessment.venue_id,
            invalid_assessment.venue_type,
            invalid_assessment.decision
            USING ERRCODE = '23514',
                  CONSTRAINT = 'venue_policy_assessments_venue_type_semantics';
    END IF;
END;
$$;

CREATE FUNCTION enforce_venue_policy_assessment_venue_type()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_venue_type text;
BEGIN
    SELECT venue_type
    INTO actual_venue_type
    FROM venues
    WHERE id = NEW.venue_id;

    IF NOT FOUND THEN
        RETURN NEW;
    END IF;

    IF jsonb_typeof(NEW.evidence -> 'venue_type') IS DISTINCT FROM 'string'
       OR NEW.evidence ->> 'venue_type' IS DISTINCT FROM actual_venue_type
       OR jsonb_typeof(NEW.matched_rules) IS DISTINCT FROM 'array'
       OR (
           NEW.decision = 'not_applicable'
           AND (
               actual_venue_type = 'journal'
               OR jsonb_array_length(NEW.matched_rules) <> 0
               OR NEW.evidence ->> 'reason'
                  IS DISTINCT FROM 'venue_type_not_journal'
           )
       )
       OR (
           NEW.decision = 'accepted'
           AND (
               actual_venue_type <> 'journal'
               OR jsonb_array_length(NEW.matched_rules) = 0
           )
       )
       OR (
           NEW.decision IN ('rejected', 'unknown')
           AND (
               actual_venue_type <> 'journal'
               OR jsonb_array_length(NEW.matched_rules) <> 0
           )
       )
       OR NEW.decision NOT IN (
           'accepted',
           'rejected',
           'unknown',
           'not_applicable'
       ) THEN
        RAISE EXCEPTION
            'policy assessment does not match Venue type % and decision semantics',
            actual_venue_type
            USING ERRCODE = '23514',
                  CONSTRAINT = 'venue_policy_assessments_venue_type_semantics';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER venue_policy_assessments_venue_type_semantics
BEFORE INSERT ON venue_policy_assessments
FOR EACH ROW
EXECUTE FUNCTION enforce_venue_policy_assessment_venue_type();
