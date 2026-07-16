ALTER TABLE venue_metric_snapshots
    DROP CONSTRAINT venue_metric_snapshots_jcr_import_receipt_id_fkey,
    ADD CONSTRAINT venue_metric_snapshots_jcr_import_receipt_id_fkey
        FOREIGN KEY (jcr_import_receipt_id)
        REFERENCES jcr_import_receipts(id)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE jcr_import_receipt_metrics (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    import_receipt_id uuid NOT NULL,
    metric_snapshot_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT jcr_import_receipt_metrics_import_receipt_id_fkey
        FOREIGN KEY (import_receipt_id)
        REFERENCES jcr_import_receipts(id)
        ON DELETE RESTRICT,
    CONSTRAINT jcr_import_receipt_metrics_metric_snapshot_id_fkey
        FOREIGN KEY (metric_snapshot_id)
        REFERENCES venue_metric_snapshots(id)
        ON DELETE RESTRICT,
    CONSTRAINT jcr_import_receipt_metrics_receipt_metric_key
        UNIQUE (import_receipt_id, metric_snapshot_id)
);

CREATE INDEX idx_jcr_import_receipt_metrics_metric
    ON jcr_import_receipt_metrics(metric_snapshot_id, import_receipt_id);

INSERT INTO jcr_import_receipt_metrics (
    import_receipt_id,
    metric_snapshot_id
)
SELECT
    jcr_import_receipt_id,
    id
FROM venue_metric_snapshots
WHERE jcr_import_receipt_id IS NOT NULL
ON CONFLICT (import_receipt_id, metric_snapshot_id) DO NOTHING;

CREATE TRIGGER jcr_import_receipt_metrics_immutable
BEFORE UPDATE OR DELETE ON jcr_import_receipt_metrics
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

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
       OR NEW.evidence ->> 'venue_type' IS DISTINCT FROM actual_venue_type THEN
        RAISE EXCEPTION
            'policy evidence venue_type must equal Venue type %',
            actual_venue_type
            USING ERRCODE = '23514',
                  CONSTRAINT = 'venue_policy_assessments_venue_type_semantics';
    END IF;

    IF NEW.decision = 'not_applicable' THEN
        IF actual_venue_type = 'journal'
           OR jsonb_array_length(NEW.matched_rules) <> 0
           OR NEW.evidence ->> 'reason' IS DISTINCT FROM 'venue_type_not_journal' THEN
            RAISE EXCEPTION
                'not_applicable requires a non-journal Venue, no matched rules, and reason venue_type_not_journal'
                USING ERRCODE = '23514',
                      CONSTRAINT = 'venue_policy_assessments_venue_type_semantics';
        END IF;
    ELSIF actual_venue_type <> 'journal' THEN
        RAISE EXCEPTION
            'decision % requires a journal Venue',
            NEW.decision
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
