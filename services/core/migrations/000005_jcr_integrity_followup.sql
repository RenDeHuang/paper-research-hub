ALTER TABLE jcr_import_receipt_metrics
    DROP CONSTRAINT jcr_import_receipt_metrics_receipt_metric_key;

DO $$
DECLARE
    receipt record;
    existing_links integer;
    missing_links integer;
    candidate_links integer;
    invalid_existing_links integer;
    backfilled_links integer;
    candidate record;
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
           OR (
               candidate_links <> 1
               AND candidate_links <> receipt.input_rows
           ) THEN
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

        backfilled_links := 0;
        FOR candidate IN
            SELECT DISTINCT
                metric.id,
                metric.venue_id,
                metric.metric_year,
                metric.category
            FROM jcr_import_receipt_aliases AS receipt_alias
            JOIN venue_aliases AS alias
              ON alias.id = receipt_alias.venue_alias_id
            JOIN venue_metric_snapshots AS metric
              ON metric.venue_id = alias.venue_id
            WHERE receipt_alias.import_receipt_id = receipt.id
              AND alias.source = receipt.source
              AND metric.source_name = receipt.source
              AND metric.captured_at <= receipt.imported_at
              AND (
                  candidate_links = 1
                  OR NOT EXISTS (
                      SELECT 1
                      FROM jcr_import_receipt_metrics AS existing
                      WHERE existing.import_receipt_id = receipt.id
                        AND existing.metric_snapshot_id = metric.id
                  )
              )
            ORDER BY
                metric.venue_id,
                metric.metric_year,
                metric.category,
                metric.id
        LOOP
            INSERT INTO jcr_import_receipt_metrics (
                import_receipt_id,
                metric_snapshot_id
            ) VALUES (
                receipt.id,
                candidate.id
            );
            backfilled_links := backfilled_links + 1;

            IF candidate_links = 1 THEN
                WHILE backfilled_links < missing_links LOOP
                    INSERT INTO jcr_import_receipt_metrics (
                        import_receipt_id,
                        metric_snapshot_id
                    ) VALUES (
                        receipt.id,
                        candidate.id
                    );
                    backfilled_links := backfilled_links + 1;
                END LOOP;
            END IF;
        END LOOP;

        IF backfilled_links <> missing_links THEN
            RAISE EXCEPTION
                'cannot deterministically associate legacy JCR receipt % (%): backfilled rows % do not equal missing rows %',
                receipt.id,
                receipt.file_sha256,
                backfilled_links,
                missing_links
                USING ERRCODE = '23514',
                      CONSTRAINT = 'jcr_import_receipts_metric_association_integrity';
        END IF;
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
        receipt.inserted_rows
    HAVING
        count(receipt_metric.id) <> receipt.input_rows
        OR (
            SELECT count(*)
            FROM venue_metric_snapshots AS inserted_metric
            WHERE inserted_metric.jcr_import_receipt_id = receipt.id
        ) <> receipt.inserted_rows
    ORDER BY receipt.id
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION
            'invalid historical JCR receipt % (%): associated rows %/%; creator rows %/%',
            invalid_receipt.id,
            invalid_receipt.file_sha256,
            invalid_receipt.associated_rows,
            invalid_receipt.input_rows,
            invalid_receipt.creator_rows,
            invalid_receipt.inserted_rows
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
    associated_rows integer;
    creator_rows integer;
BEGIN
    IF TG_TABLE_NAME = 'jcr_import_receipts' THEN
        target_receipt_id := NEW.id;
    ELSIF TG_TABLE_NAME = 'venue_metric_snapshots' THEN
        IF TG_OP = 'DELETE' THEN
            target_receipt_id := OLD.jcr_import_receipt_id;
        ELSE
            target_receipt_id := NEW.jcr_import_receipt_id;
        END IF;
    ELSE
        IF TG_OP = 'DELETE' THEN
            target_receipt_id := OLD.import_receipt_id;
        ELSE
            target_receipt_id := NEW.import_receipt_id;
        END IF;
    END IF;

    IF target_receipt_id IS NULL THEN
        RETURN NULL;
    END IF;

    SELECT input_rows, inserted_rows
    INTO declared_input_rows, declared_inserted_rows
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
       OR creator_rows <> declared_inserted_rows THEN
        RAISE EXCEPTION
            'JCR receipt % integrity mismatch: associated rows %/%; creator rows %/%',
            target_receipt_id,
            associated_rows,
            declared_input_rows,
            creator_rows,
            declared_inserted_rows
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

CREATE CONSTRAINT TRIGGER jcr_import_receipt_metrics_integrity
AFTER INSERT OR UPDATE OR DELETE ON jcr_import_receipt_metrics
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_jcr_import_receipt_metric_integrity();

CREATE CONSTRAINT TRIGGER venue_metric_snapshots_receipt_integrity
AFTER INSERT OR UPDATE OR DELETE ON venue_metric_snapshots
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_jcr_import_receipt_metric_integrity();

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
