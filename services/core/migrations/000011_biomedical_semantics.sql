ALTER TABLE ingestion_projection_assertions
    ADD CONSTRAINT ingestion_projection_assertions_id_source_record_work_key
        UNIQUE (id, source_record_uuid, work_id);

ALTER TABLE venue_metric_snapshots
    ADD CONSTRAINT venue_metric_snapshots_id_category_key
        UNIQUE (id, category);

CREATE TABLE mesh_descriptors (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    descriptor_ui text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mesh_descriptors_descriptor_ui_check
        CHECK (btrim(descriptor_ui) <> '' AND descriptor_ui = btrim(descriptor_ui)),
    CONSTRAINT mesh_descriptors_descriptor_ui_key
        UNIQUE (descriptor_ui)
);

CREATE TRIGGER mesh_descriptors_immutable
BEFORE UPDATE OR DELETE ON mesh_descriptors
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE mesh_qualifiers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    qualifier_ui text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT mesh_qualifiers_qualifier_ui_check
        CHECK (btrim(qualifier_ui) <> '' AND qualifier_ui = btrim(qualifier_ui)),
    CONSTRAINT mesh_qualifiers_qualifier_ui_key
        UNIQUE (qualifier_ui)
);

CREATE TRIGGER mesh_qualifiers_immutable
BEFORE UPDATE OR DELETE ON mesh_qualifiers
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE publication_types (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    publication_type_ui text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT publication_types_publication_type_ui_check
        CHECK (
            btrim(publication_type_ui) <> ''
            AND publication_type_ui = btrim(publication_type_ui)
        ),
    CONSTRAINT publication_types_publication_type_ui_key
        UNIQUE (publication_type_ui)
);

CREATE TRIGGER publication_types_immutable
BEFORE UPDATE OR DELETE ON publication_types
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_mesh_headings (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    projection_assertion_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    work_id uuid NOT NULL,
    descriptor_id uuid NOT NULL,
    source_path text NOT NULL,
    descriptor_label text NOT NULL,
    is_major_topic boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_mesh_headings_source_path_check
        CHECK (btrim(source_path) <> '' AND source_path = btrim(source_path)),
    CONSTRAINT work_mesh_headings_descriptor_label_check
        CHECK (
            btrim(descriptor_label) <> ''
            AND descriptor_label = btrim(descriptor_label)
        ),
    CONSTRAINT work_mesh_headings_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_mesh_headings_work_id_fkey
        FOREIGN KEY (work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_mesh_headings_descriptor_id_fkey
        FOREIGN KEY (descriptor_id)
        REFERENCES mesh_descriptors(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_mesh_headings_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_mesh_headings_projection_assertion_fkey
        FOREIGN KEY (projection_assertion_id, source_record_id, work_id)
        REFERENCES ingestion_projection_assertions(id, source_record_uuid, work_id)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_mesh_headings_projection_descriptor_key
        UNIQUE (projection_assertion_id, descriptor_id),
    CONSTRAINT work_mesh_headings_assertion_identity_key
        UNIQUE (id, projection_assertion_id, source_record_id, work_id)
);

CREATE INDEX idx_work_mesh_headings_work
    ON work_mesh_headings(work_id, projection_assertion_id, id);
CREATE INDEX idx_work_mesh_headings_source_record
    ON work_mesh_headings(source_record_id, projection_assertion_id, id);
CREATE INDEX idx_work_mesh_headings_descriptor
    ON work_mesh_headings(descriptor_id, work_id, id);

CREATE TRIGGER work_mesh_headings_immutable
BEFORE UPDATE OR DELETE ON work_mesh_headings
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_mesh_qualifiers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    projection_assertion_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    work_id uuid NOT NULL,
    work_mesh_heading_id uuid NOT NULL,
    qualifier_id uuid NOT NULL,
    source_path text NOT NULL,
    qualifier_label text NOT NULL,
    is_major_topic boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_mesh_qualifiers_source_path_check
        CHECK (btrim(source_path) <> '' AND source_path = btrim(source_path)),
    CONSTRAINT work_mesh_qualifiers_qualifier_label_check
        CHECK (
            btrim(qualifier_label) <> ''
            AND qualifier_label = btrim(qualifier_label)
        ),
    CONSTRAINT work_mesh_qualifiers_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_mesh_qualifiers_work_id_fkey
        FOREIGN KEY (work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_mesh_qualifiers_qualifier_id_fkey
        FOREIGN KEY (qualifier_id)
        REFERENCES mesh_qualifiers(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_mesh_qualifiers_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_mesh_qualifiers_projection_assertion_fkey
        FOREIGN KEY (projection_assertion_id, source_record_id, work_id)
        REFERENCES ingestion_projection_assertions(id, source_record_uuid, work_id)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_mesh_qualifiers_heading_assertion_fkey
        FOREIGN KEY (
            work_mesh_heading_id,
            projection_assertion_id,
            source_record_id,
            work_id
        )
        REFERENCES work_mesh_headings(
            id,
            projection_assertion_id,
            source_record_id,
            work_id
        )
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_mesh_qualifiers_heading_qualifier_key
        UNIQUE (work_mesh_heading_id, qualifier_id)
);

CREATE INDEX idx_work_mesh_qualifiers_work
    ON work_mesh_qualifiers(work_id, projection_assertion_id, id);
CREATE INDEX idx_work_mesh_qualifiers_source_record
    ON work_mesh_qualifiers(source_record_id, projection_assertion_id, id);
CREATE INDEX idx_work_mesh_qualifiers_qualifier
    ON work_mesh_qualifiers(qualifier_id, work_mesh_heading_id, id);

CREATE TRIGGER work_mesh_qualifiers_immutable
BEFORE UPDATE OR DELETE ON work_mesh_qualifiers
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_publication_types (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    projection_assertion_id uuid NOT NULL,
    source_record_id uuid NOT NULL,
    work_id uuid NOT NULL,
    publication_type_id uuid NOT NULL,
    source_path text NOT NULL,
    publication_type_label text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_publication_types_source_path_check
        CHECK (btrim(source_path) <> '' AND source_path = btrim(source_path)),
    CONSTRAINT work_publication_types_label_check
        CHECK (
            btrim(publication_type_label) <> ''
            AND publication_type_label = btrim(publication_type_label)
        ),
    CONSTRAINT work_publication_types_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_publication_types_work_id_fkey
        FOREIGN KEY (work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_publication_types_publication_type_id_fkey
        FOREIGN KEY (publication_type_id)
        REFERENCES publication_types(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_publication_types_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_publication_types_projection_assertion_fkey
        FOREIGN KEY (projection_assertion_id, source_record_id, work_id)
        REFERENCES ingestion_projection_assertions(id, source_record_uuid, work_id)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_publication_types_projection_type_key
        UNIQUE (projection_assertion_id, publication_type_id)
);

CREATE INDEX idx_work_publication_types_work
    ON work_publication_types(work_id, projection_assertion_id, id);
CREATE INDEX idx_work_publication_types_source_record
    ON work_publication_types(source_record_id, projection_assertion_id, id);
CREATE INDEX idx_work_publication_types_type
    ON work_publication_types(publication_type_id, work_id, id);

CREATE TRIGGER work_publication_types_immutable
BEFORE UPDATE OR DELETE ON work_publication_types
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE subject_import_receipts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source text NOT NULL,
    registry_version text NOT NULL,
    file_sha256 text NOT NULL,
    subject_count integer NOT NULL,
    rule_count integer NOT NULL,
    imported_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT subject_import_receipts_source_check
        CHECK (btrim(source) <> '' AND source = btrim(source)),
    CONSTRAINT subject_import_receipts_registry_version_check
        CHECK (
            btrim(registry_version) <> ''
            AND registry_version = btrim(registry_version)
        ),
    CONSTRAINT subject_import_receipts_file_sha256_check
        CHECK (file_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT subject_import_receipts_counts_check
        CHECK (subject_count >= 0 AND rule_count >= 0),
    CONSTRAINT subject_import_receipts_source_registry_key
        UNIQUE (source, registry_version),
    CONSTRAINT subject_import_receipts_file_sha256_key
        UNIQUE (file_sha256)
);

CREATE TRIGGER subject_import_receipts_immutable
BEFORE UPDATE OR DELETE ON subject_import_receipts
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE subject_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_import_receipt_id uuid NOT NULL,
    version_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT subject_versions_version_key_check
        CHECK (btrim(version_key) <> '' AND version_key = btrim(version_key)),
    CONSTRAINT subject_versions_subject_import_receipt_id_fkey
        FOREIGN KEY (subject_import_receipt_id)
        REFERENCES subject_import_receipts(id)
        ON DELETE RESTRICT,
    CONSTRAINT subject_versions_subject_import_receipt_id_key
        UNIQUE (subject_import_receipt_id),
    CONSTRAINT subject_versions_version_key_key
        UNIQUE (version_key)
);

CREATE TRIGGER subject_versions_immutable
BEFORE UPDATE OR DELETE ON subject_versions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE subjects (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_version_id uuid NOT NULL,
    slug text NOT NULL,
    display_label text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT subjects_slug_check
        CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$' AND length(slug) <= 120),
    CONSTRAINT subjects_display_label_check
        CHECK (btrim(display_label) <> '' AND display_label = btrim(display_label)),
    CONSTRAINT subjects_subject_version_id_fkey
        FOREIGN KEY (subject_version_id)
        REFERENCES subject_versions(id)
        ON DELETE RESTRICT,
    CONSTRAINT subjects_version_slug_key
        UNIQUE (subject_version_id, slug),
    CONSTRAINT subjects_id_subject_version_key
        UNIQUE (id, subject_version_id)
);

CREATE INDEX idx_subjects_version_display_label
    ON subjects(subject_version_id, display_label, id);

CREATE TRIGGER subjects_immutable
BEFORE UPDATE OR DELETE ON subjects
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE biomedical_subject_rules (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_version_id uuid NOT NULL,
    subject_id uuid NOT NULL,
    jcr_category text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT biomedical_subject_rules_category_check
        CHECK (btrim(jcr_category) <> '' AND jcr_category = btrim(jcr_category)),
    CONSTRAINT biomedical_subject_rules_subject_version_id_fkey
        FOREIGN KEY (subject_version_id)
        REFERENCES subject_versions(id)
        ON DELETE RESTRICT,
    CONSTRAINT biomedical_subject_rules_subject_version_fkey
        FOREIGN KEY (subject_id, subject_version_id)
        REFERENCES subjects(id, subject_version_id)
        ON DELETE RESTRICT,
    CONSTRAINT biomedical_subject_rules_version_category_key
        UNIQUE (subject_version_id, jcr_category),
    CONSTRAINT biomedical_subject_rules_id_category_key
        UNIQUE (id, jcr_category)
);

CREATE INDEX idx_biomedical_subject_rules_subject
    ON biomedical_subject_rules(subject_id, subject_version_id, id);

CREATE TRIGGER biomedical_subject_rules_immutable
BEFORE UPDATE OR DELETE ON biomedical_subject_rules
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE FUNCTION enforce_subject_import_receipt_integrity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_rule_count integer;
    actual_subject_count integer;
    actual_version_count integer;
    declared_rule_count integer;
    declared_subject_count integer;
BEGIN
    SELECT subject_count, rule_count
    INTO declared_subject_count, declared_rule_count
    FROM subject_import_receipts
    WHERE id = NEW.id;

    IF NOT FOUND THEN
        RETURN NULL;
    END IF;

    SELECT count(*)
    INTO actual_version_count
    FROM subject_versions
    WHERE subject_import_receipt_id = NEW.id;

    SELECT count(*)
    INTO actual_subject_count
    FROM subjects AS subject
    JOIN subject_versions AS version
      ON version.id = subject.subject_version_id
    WHERE version.subject_import_receipt_id = NEW.id;

    SELECT count(*)
    INTO actual_rule_count
    FROM biomedical_subject_rules AS rule
    JOIN subject_versions AS version
      ON version.id = rule.subject_version_id
    WHERE version.subject_import_receipt_id = NEW.id;

    IF actual_version_count <> 1
       OR actual_subject_count <> declared_subject_count
       OR actual_rule_count <> declared_rule_count THEN
        RAISE EXCEPTION
            'Subject receipt % content mismatch: versions %/1, subjects %/%, rules %/%',
            NEW.id,
            actual_version_count,
            actual_subject_count,
            declared_subject_count,
            actual_rule_count,
            declared_rule_count
            USING ERRCODE = '23514',
                  CONSTRAINT = 'subject_import_receipts_content_integrity';
    END IF;

    RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER subject_import_receipts_content_integrity
AFTER INSERT ON subject_import_receipts
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_subject_import_receipt_integrity();

CREATE FUNCTION enforce_subject_import_receipt_child_transaction()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    receipt_created_in_current_transaction boolean;
    target_receipt_id uuid;
BEGIN
    IF TG_TABLE_NAME = 'subject_versions' THEN
        target_receipt_id := NEW.subject_import_receipt_id;
    ELSIF TG_TABLE_NAME = 'subjects' THEN
        SELECT subject_import_receipt_id
        INTO target_receipt_id
        FROM subject_versions
        WHERE id = NEW.subject_version_id;
    ELSE
        SELECT subject_import_receipt_id
        INTO target_receipt_id
        FROM subject_versions
        WHERE id = NEW.subject_version_id;
    END IF;

    IF target_receipt_id IS NULL THEN
        RETURN NEW;
    END IF;

    SELECT receipt.xmin = pg_current_xact_id()::text::xid
    INTO receipt_created_in_current_transaction
    FROM subject_import_receipts AS receipt
    WHERE receipt.id = target_receipt_id;

    IF NOT FOUND THEN
        RETURN NEW;
    END IF;

    IF NOT receipt_created_in_current_transaction THEN
        RAISE EXCEPTION
            'Subject receipt % is sealed against post-commit child rows',
            target_receipt_id
            USING ERRCODE = '23514',
                  CONSTRAINT = 'subject_import_receipt_children_current_transaction';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER subject_versions_receipt_transaction
BEFORE INSERT ON subject_versions
FOR EACH ROW
EXECUTE FUNCTION enforce_subject_import_receipt_child_transaction();

CREATE TRIGGER subjects_receipt_transaction
BEFORE INSERT ON subjects
FOR EACH ROW
EXECUTE FUNCTION enforce_subject_import_receipt_child_transaction();

CREATE TRIGGER biomedical_subject_rules_receipt_transaction
BEFORE INSERT ON biomedical_subject_rules
FOR EACH ROW
EXECUTE FUNCTION enforce_subject_import_receipt_child_transaction();

CREATE TABLE journal_subject_metrics (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    venue_metric_snapshot_id uuid NOT NULL,
    subject_rule_id uuid NOT NULL,
    jcr_category text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT journal_subject_metrics_category_check
        CHECK (btrim(jcr_category) <> '' AND jcr_category = btrim(jcr_category)),
    CONSTRAINT journal_subject_metrics_metric_category_fkey
        FOREIGN KEY (venue_metric_snapshot_id, jcr_category)
        REFERENCES venue_metric_snapshots(id, category)
        ON DELETE RESTRICT,
    CONSTRAINT journal_subject_metrics_rule_category_fkey
        FOREIGN KEY (subject_rule_id, jcr_category)
        REFERENCES biomedical_subject_rules(id, jcr_category)
        ON DELETE RESTRICT,
    CONSTRAINT journal_subject_metrics_metric_rule_key
        UNIQUE (venue_metric_snapshot_id, subject_rule_id)
);

CREATE INDEX idx_journal_subject_metrics_rule
    ON journal_subject_metrics(subject_rule_id, venue_metric_snapshot_id, id);

CREATE TRIGGER journal_subject_metrics_immutable
BEFORE UPDATE OR DELETE ON journal_subject_metrics
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();
