CREATE TABLE public_catalog_home (
    generation_id uuid PRIMARY KEY
        REFERENCES public_catalog_generations(id)
        ON DELETE RESTRICT,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT public_catalog_home_payload_check
        CHECK (jsonb_typeof(payload) = 'object')
);

CREATE TRIGGER public_catalog_home_immutable
BEFORE INSERT OR UPDATE OR DELETE ON public_catalog_home
FOR EACH ROW
EXECUTE FUNCTION enforce_public_catalog_child_immutability();

CREATE TABLE public_catalog_biomedical_coverage (
    generation_id uuid PRIMARY KEY
        REFERENCES public_catalog_generations(id)
        ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER public_catalog_biomedical_coverage_immutable
BEFORE INSERT OR UPDATE OR DELETE ON public_catalog_biomedical_coverage
FOR EACH ROW
EXECUTE FUNCTION enforce_public_catalog_child_immutability();

CREATE TABLE public_catalog_biomedical_manifest (
    generation_id uuid PRIMARY KEY
        REFERENCES public_catalog_generations(id)
        ON DELETE RESTRICT,
    subject_count bigint NOT NULL,
    subject_list_payload jsonb NOT NULL,
    journal_count bigint NOT NULL,
    journal_list_payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT public_catalog_biomedical_manifest_subject_count_check
        CHECK (subject_count >= 0),
    CONSTRAINT public_catalog_biomedical_manifest_subject_payload_check
        CHECK (jsonb_typeof(subject_list_payload) = 'object'),
    CONSTRAINT public_catalog_biomedical_manifest_journal_count_check
        CHECK (journal_count >= 0),
    CONSTRAINT public_catalog_biomedical_manifest_journal_payload_check
        CHECK (jsonb_typeof(journal_list_payload) = 'object')
);

CREATE TRIGGER public_catalog_biomedical_manifest_immutable
BEFORE INSERT OR UPDATE OR DELETE ON public_catalog_biomedical_manifest
FOR EACH ROW
EXECUTE FUNCTION enforce_public_catalog_child_immutability();

CREATE TABLE public_catalog_subjects (
    generation_id uuid NOT NULL
        REFERENCES public_catalog_generations(id)
        ON DELETE RESTRICT,
    subject_id uuid NOT NULL,
    slug text NOT NULL,
    paper_count bigint NOT NULL,
    summary_payload jsonb NOT NULL,
    detail_payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (generation_id, subject_id),
    CONSTRAINT public_catalog_subjects_slug_key
        UNIQUE (generation_id, slug),
    CONSTRAINT public_catalog_subjects_slug_check
        CHECK (
            slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
            AND length(slug) <= 120
        ),
    CONSTRAINT public_catalog_subjects_paper_count_check
        CHECK (paper_count >= 0),
    CONSTRAINT public_catalog_subjects_summary_payload_check
        CHECK (jsonb_typeof(summary_payload) = 'object'),
    CONSTRAINT public_catalog_subjects_detail_payload_check
        CHECK (jsonb_typeof(detail_payload) = 'object')
);

CREATE INDEX public_catalog_subjects_order
    ON public_catalog_subjects(generation_id, paper_count DESC, slug);

CREATE TRIGGER public_catalog_subjects_immutable
BEFORE INSERT OR UPDATE OR DELETE ON public_catalog_subjects
FOR EACH ROW
EXECUTE FUNCTION enforce_public_catalog_child_immutability();

CREATE TABLE public_catalog_journals (
    generation_id uuid NOT NULL
        REFERENCES public_catalog_generations(id)
        ON DELETE RESTRICT,
    journal_id uuid NOT NULL,
    slug text NOT NULL,
    paper_count bigint NOT NULL,
    summary_payload jsonb NOT NULL,
    detail_payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (generation_id, journal_id),
    CONSTRAINT public_catalog_journals_slug_key
        UNIQUE (generation_id, slug),
    CONSTRAINT public_catalog_journals_slug_check
        CHECK (
            slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'
            AND length(slug) <= 120
        ),
    CONSTRAINT public_catalog_journals_paper_count_check
        CHECK (paper_count >= 0),
    CONSTRAINT public_catalog_journals_summary_payload_check
        CHECK (jsonb_typeof(summary_payload) = 'object'),
    CONSTRAINT public_catalog_journals_detail_payload_check
        CHECK (jsonb_typeof(detail_payload) = 'object')
);

CREATE INDEX public_catalog_journals_order
    ON public_catalog_journals(generation_id, paper_count DESC, slug);

CREATE TRIGGER public_catalog_journals_immutable
BEFORE INSERT OR UPDATE OR DELETE ON public_catalog_journals
FOR EACH ROW
EXECUTE FUNCTION enforce_public_catalog_child_immutability();

CREATE OR REPLACE FUNCTION validate_public_catalog_publication()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    home_snapshot_count bigint;
    declared_subject_count bigint;
    declared_journal_count bigint;
    actual_subject_count bigint;
    actual_journal_count bigint;
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM public_catalog_stats
        WHERE generation_id = NEW.generation_id
    ) THEN
        RAISE EXCEPTION 'public catalog generation % requires a stats snapshot before publication',
            NEW.generation_id
            USING
                ERRCODE = '23514',
                CONSTRAINT = 'public_catalog_publications_requires_stats';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM public_catalog_biomedical_coverage
        WHERE generation_id = NEW.generation_id
    ) THEN
        RETURN NEW;
    END IF;

    SELECT count(*)
    INTO home_snapshot_count
    FROM public_catalog_home
    WHERE generation_id = NEW.generation_id;

    SELECT subject_count, journal_count
    INTO declared_subject_count, declared_journal_count
    FROM public_catalog_biomedical_manifest
    WHERE generation_id = NEW.generation_id;

    IF home_snapshot_count <> 1 OR NOT FOUND THEN
        RAISE EXCEPTION
            'biomedical public catalog generation % requires one Home snapshot and one manifest before publication',
            NEW.generation_id
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'public_catalog_publications_requires_biomedical_snapshots';
    END IF;

    SELECT count(*)
    INTO actual_subject_count
    FROM public_catalog_subjects
    WHERE generation_id = NEW.generation_id;

    SELECT count(*)
    INTO actual_journal_count
    FROM public_catalog_journals
    WHERE generation_id = NEW.generation_id;

    IF actual_subject_count <> declared_subject_count
       OR actual_journal_count <> declared_journal_count THEN
        RAISE EXCEPTION
            'biomedical public catalog generation % coverage mismatch: Subjects %/%, Journals %/%',
            NEW.generation_id,
            actual_subject_count,
            declared_subject_count,
            actual_journal_count,
            declared_journal_count
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'public_catalog_publications_biomedical_coverage_matches';
    END IF;

    RETURN NEW;
END;
$$;
