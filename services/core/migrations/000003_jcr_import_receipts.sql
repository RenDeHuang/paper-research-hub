ALTER TABLE venue_policy_assessments
    DROP CONSTRAINT venue_policy_assessments_decision_check,
    DROP CONSTRAINT venue_policy_assessments_decision_semantics_check;

ALTER TABLE venue_policy_assessments
    ADD CONSTRAINT venue_policy_assessments_decision_check
        CHECK (decision IN ('accepted', 'rejected', 'unknown', 'not_applicable')),
    ADD CONSTRAINT venue_policy_assessments_decision_semantics_check
        CHECK (
            (
                (decision = 'accepted' AND jsonb_array_length(matched_rules) > 0)
                OR
                (
                    decision IN ('rejected', 'unknown', 'not_applicable')
                    AND jsonb_array_length(matched_rules) = 0
                )
            )
            AND
            (
                decision <> 'not_applicable'
                OR
                (
                    evidence ? 'reason'
                    AND jsonb_typeof(evidence -> 'reason') = 'string'
                    AND btrim(evidence ->> 'reason') <> ''
                    AND evidence ? 'venue_type'
                    AND jsonb_typeof(evidence -> 'venue_type') = 'string'
                    AND btrim(evidence ->> 'venue_type') <> ''
                )
            )
        );

CREATE TABLE jcr_import_receipts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    file_sha256 text NOT NULL UNIQUE,
    source text NOT NULL,
    imported_at timestamptz NOT NULL,
    input_rows integer NOT NULL,
    inserted_rows integer NOT NULL,
    unchanged_rows integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT jcr_import_receipts_file_sha256_check
        CHECK (file_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT jcr_import_receipts_source_check
        CHECK (btrim(source) <> ''),
    CONSTRAINT jcr_import_receipts_counts_check
        CHECK (
            input_rows >= 0
            AND inserted_rows >= 0
            AND unchanged_rows >= 0
            AND input_rows = inserted_rows + unchanged_rows
        )
);

CREATE TRIGGER jcr_import_receipts_immutable
BEFORE UPDATE OR DELETE ON jcr_import_receipts
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

ALTER TABLE venue_metric_snapshots
    ADD COLUMN jcr_import_receipt_id uuid,
    ADD CONSTRAINT venue_metric_snapshots_jcr_import_receipt_id_fkey
        FOREIGN KEY (jcr_import_receipt_id)
        REFERENCES jcr_import_receipts(id)
        ON DELETE RESTRICT;

CREATE INDEX idx_venue_metric_snapshots_import_receipt
    ON venue_metric_snapshots(jcr_import_receipt_id, venue_id, metric_year, category)
    WHERE jcr_import_receipt_id IS NOT NULL;

CREATE TABLE jcr_import_receipt_aliases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    import_receipt_id uuid NOT NULL,
    venue_alias_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT jcr_import_receipt_aliases_import_receipt_id_fkey
        FOREIGN KEY (import_receipt_id)
        REFERENCES jcr_import_receipts(id)
        ON DELETE RESTRICT,
    CONSTRAINT jcr_import_receipt_aliases_venue_alias_id_fkey
        FOREIGN KEY (venue_alias_id)
        REFERENCES venue_aliases(id)
        ON DELETE RESTRICT,
    UNIQUE (import_receipt_id, venue_alias_id)
);

CREATE INDEX idx_jcr_import_receipt_aliases_alias
    ON jcr_import_receipt_aliases(venue_alias_id, import_receipt_id);

CREATE TRIGGER jcr_import_receipt_aliases_immutable
BEFORE UPDATE OR DELETE ON jcr_import_receipt_aliases
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();
