CREATE FUNCTION reject_public_catalog_generation_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'public catalog generation rows are immutable'
        USING ERRCODE = '55000';
END;
$$;

CREATE FUNCTION enforce_public_catalog_child_immutability()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_generation_id uuid;
BEGIN
    IF TG_OP = 'INSERT' THEN
        target_generation_id := NEW.generation_id;
        IF EXISTS (
            SELECT 1
            FROM public_catalog_publications
            WHERE generation_id = target_generation_id
        ) THEN
            RAISE EXCEPTION 'published public catalog generation % is immutable',
                target_generation_id
                USING ERRCODE = '55000';
        END IF;
        RETURN NEW;
    END IF;

    target_generation_id := OLD.generation_id;
    RAISE EXCEPTION 'public catalog generation child rows are immutable for generation %',
        target_generation_id
        USING ERRCODE = '55000';
END;
$$;

CREATE TABLE public_catalog_generations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_revision text NOT NULL,
    formula_version text NOT NULL,
    generated_at timestamptz NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT public_catalog_generations_source_revision_check
        CHECK (btrim(source_revision) <> '' AND source_revision = btrim(source_revision)),
    CONSTRAINT public_catalog_generations_formula_version_check
        CHECK (btrim(formula_version) <> '' AND formula_version = btrim(formula_version)),
    CONSTRAINT public_catalog_generations_metadata_check
        CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE UNIQUE INDEX public_catalog_generations_source_revision_key
    ON public_catalog_generations(source_revision);

CREATE TRIGGER public_catalog_generations_immutable
BEFORE UPDATE OR DELETE ON public_catalog_generations
FOR EACH ROW
EXECUTE FUNCTION reject_public_catalog_generation_mutation();

CREATE TABLE public_catalog_publications (
    generation_id uuid PRIMARY KEY
        REFERENCES public_catalog_generations(id)
        ON DELETE RESTRICT,
    published_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER public_catalog_publications_immutable
BEFORE UPDATE OR DELETE ON public_catalog_publications
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE public_catalog_current (
    singleton boolean PRIMARY KEY DEFAULT true,
    generation_id uuid NOT NULL UNIQUE
        REFERENCES public_catalog_publications(generation_id)
        ON DELETE RESTRICT,
    switched_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT public_catalog_current_singleton_check CHECK (singleton)
);

CREATE FUNCTION set_public_catalog_current_switch_time()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.generation_id IS DISTINCT FROM OLD.generation_id THEN
        NEW.switched_at := now();
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER public_catalog_current_switch_time
BEFORE UPDATE ON public_catalog_current
FOR EACH ROW
EXECUTE FUNCTION set_public_catalog_current_switch_time();

CREATE TABLE public_catalog_stats (
    generation_id uuid PRIMARY KEY
        REFERENCES public_catalog_generations(id)
        ON DELETE RESTRICT,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT public_catalog_stats_payload_check
        CHECK (jsonb_typeof(payload) = 'object')
);

CREATE TRIGGER public_catalog_stats_immutable
BEFORE INSERT OR UPDATE OR DELETE ON public_catalog_stats
FOR EACH ROW
EXECUTE FUNCTION enforce_public_catalog_child_immutability();

CREATE FUNCTION validate_public_catalog_publication()
RETURNS trigger
LANGUAGE plpgsql
AS $$
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
    RETURN NEW;
END;
$$;

CREATE TRIGGER public_catalog_publications_validate
BEFORE INSERT ON public_catalog_publications
FOR EACH ROW
EXECUTE FUNCTION validate_public_catalog_publication();

CREATE TABLE public_catalog_topics (
    generation_id uuid NOT NULL
        REFERENCES public_catalog_generations(id)
        ON DELETE RESTRICT,
    taxonomy_id uuid NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    paper_count bigint NOT NULL,
    summary_payload jsonb NOT NULL,
    detail_payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (generation_id, taxonomy_id),
    CONSTRAINT public_catalog_topics_slug_key UNIQUE (generation_id, slug),
    CONSTRAINT public_catalog_topics_slug_check
        CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$' AND length(slug) <= 120),
    CONSTRAINT public_catalog_topics_name_check
        CHECK (btrim(name) <> '' AND name = btrim(name)),
    CONSTRAINT public_catalog_topics_paper_count_check CHECK (paper_count >= 0),
    CONSTRAINT public_catalog_topics_summary_payload_check
        CHECK (jsonb_typeof(summary_payload) = 'object'),
    CONSTRAINT public_catalog_topics_detail_payload_check
        CHECK (jsonb_typeof(detail_payload) = 'object')
);

CREATE INDEX public_catalog_topics_order
    ON public_catalog_topics(generation_id, paper_count DESC, slug);

CREATE TRIGGER public_catalog_topics_immutable
BEFORE INSERT OR UPDATE OR DELETE ON public_catalog_topics
FOR EACH ROW
EXECUTE FUNCTION enforce_public_catalog_child_immutability();

CREATE TABLE public_catalog_methods (
    generation_id uuid NOT NULL
        REFERENCES public_catalog_generations(id)
        ON DELETE RESTRICT,
    taxonomy_id uuid NOT NULL,
    slug text NOT NULL,
    name text NOT NULL,
    paper_count bigint NOT NULL,
    summary_payload jsonb NOT NULL,
    detail_payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (generation_id, taxonomy_id),
    CONSTRAINT public_catalog_methods_slug_key UNIQUE (generation_id, slug),
    CONSTRAINT public_catalog_methods_slug_check
        CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$' AND length(slug) <= 120),
    CONSTRAINT public_catalog_methods_name_check
        CHECK (btrim(name) <> '' AND name = btrim(name)),
    CONSTRAINT public_catalog_methods_paper_count_check CHECK (paper_count >= 0),
    CONSTRAINT public_catalog_methods_summary_payload_check
        CHECK (jsonb_typeof(summary_payload) = 'object'),
    CONSTRAINT public_catalog_methods_detail_payload_check
        CHECK (jsonb_typeof(detail_payload) = 'object')
);

CREATE INDEX public_catalog_methods_order
    ON public_catalog_methods(generation_id, paper_count DESC, slug);

CREATE TRIGGER public_catalog_methods_immutable
BEFORE INSERT OR UPDATE OR DELETE ON public_catalog_methods
FOR EACH ROW
EXECUTE FUNCTION enforce_public_catalog_child_immutability();
