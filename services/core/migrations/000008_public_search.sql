CREATE TABLE public_catalog_papers (
    generation_id uuid NOT NULL
        REFERENCES public_catalog_generations(id)
        ON DELETE RESTRICT,
    paper_id uuid NOT NULL,
    canonical_key text NOT NULL,
    title text NOT NULL,
    search_text text NOT NULL,
    search_document tsvector
        GENERATED ALWAYS AS (to_tsvector('english'::regconfig, search_text)) STORED,
    published_at_state text NOT NULL,
    published_at timestamptz,
    paper_type_state text NOT NULL,
    paper_type text,
    lifecycle_status text NOT NULL,
    topic_slugs text[] NOT NULL DEFAULT '{}',
    method_slugs text[] NOT NULL DEFAULT '{}',
    source_names text[] NOT NULL DEFAULT '{}',
    has_code_state text NOT NULL,
    has_code_value boolean,
    has_data_state text NOT NULL,
    has_data_value boolean,
    has_benchmark_state text NOT NULL,
    has_benchmark_value boolean,
    citation_count_state text NOT NULL,
    citation_count_value bigint,
    trend_score_state text NOT NULL,
    trend_score_value numeric,
    summary_payload jsonb NOT NULL,
    detail_payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (generation_id, paper_id),
    CONSTRAINT public_catalog_papers_canonical_key_key
        UNIQUE (generation_id, canonical_key),
    CONSTRAINT public_catalog_papers_canonical_key_check
        CHECK (btrim(canonical_key) <> '' AND canonical_key = btrim(canonical_key)),
    CONSTRAINT public_catalog_papers_title_check
        CHECK (btrim(title) <> '' AND title = btrim(title)),
    CONSTRAINT public_catalog_papers_search_text_check
        CHECK (btrim(search_text) <> ''),
    CONSTRAINT public_catalog_papers_published_at_state_check
        CHECK (published_at_state IN ('known', 'unknown', 'missing')),
    CONSTRAINT public_catalog_papers_published_at_consistency_check
        CHECK (
            (published_at_state = 'known' AND published_at IS NOT NULL)
            OR
            (published_at_state IN ('unknown', 'missing') AND published_at IS NULL)
        ),
    CONSTRAINT public_catalog_papers_paper_type_state_check
        CHECK (paper_type_state IN ('known', 'unknown', 'missing')),
    CONSTRAINT public_catalog_papers_paper_type_value_check
        CHECK (
            paper_type IS NULL
            OR paper_type IN ('research_article', 'review', 'preprint', 'dataset', 'benchmark')
        ),
    CONSTRAINT public_catalog_papers_paper_type_consistency_check
        CHECK (
            (paper_type_state = 'known' AND paper_type IS NOT NULL)
            OR
            (paper_type_state IN ('unknown', 'missing') AND paper_type IS NULL)
        ),
    CONSTRAINT public_catalog_papers_lifecycle_status_check
        CHECK (
            lifecycle_status IN (
                'active',
                'withdrawn',
                'retracted',
                'rejected',
                'superseded'
            )
        ),
    CONSTRAINT public_catalog_papers_topic_slugs_check
        CHECK (array_position(topic_slugs, NULL) IS NULL),
    CONSTRAINT public_catalog_papers_method_slugs_check
        CHECK (array_position(method_slugs, NULL) IS NULL),
    CONSTRAINT public_catalog_papers_source_names_check
        CHECK (array_position(source_names, NULL) IS NULL),
    CONSTRAINT public_catalog_papers_has_code_state_check
        CHECK (has_code_state IN ('known', 'unknown', 'missing')),
    CONSTRAINT public_catalog_papers_has_code_consistency_check
        CHECK (
            (has_code_state = 'known' AND has_code_value IS NOT NULL)
            OR
            (has_code_state IN ('unknown', 'missing') AND has_code_value IS NULL)
        ),
    CONSTRAINT public_catalog_papers_has_data_state_check
        CHECK (has_data_state IN ('known', 'unknown', 'missing')),
    CONSTRAINT public_catalog_papers_has_data_consistency_check
        CHECK (
            (has_data_state = 'known' AND has_data_value IS NOT NULL)
            OR
            (has_data_state IN ('unknown', 'missing') AND has_data_value IS NULL)
        ),
    CONSTRAINT public_catalog_papers_has_benchmark_state_check
        CHECK (has_benchmark_state IN ('known', 'unknown', 'missing')),
    CONSTRAINT public_catalog_papers_has_benchmark_consistency_check
        CHECK (
            (has_benchmark_state = 'known' AND has_benchmark_value IS NOT NULL)
            OR
            (has_benchmark_state IN ('unknown', 'missing') AND has_benchmark_value IS NULL)
        ),
    CONSTRAINT public_catalog_papers_citation_count_state_check
        CHECK (citation_count_state IN ('known', 'unknown', 'missing')),
    CONSTRAINT public_catalog_papers_citation_count_value_check
        CHECK (citation_count_value IS NULL OR citation_count_value >= 0),
    CONSTRAINT public_catalog_papers_citation_count_consistency_check
        CHECK (
            (citation_count_state = 'known' AND citation_count_value IS NOT NULL)
            OR
            (citation_count_state IN ('unknown', 'missing') AND citation_count_value IS NULL)
        ),
    CONSTRAINT public_catalog_papers_trend_score_state_check
        CHECK (trend_score_state IN ('known', 'unknown', 'missing')),
    CONSTRAINT public_catalog_papers_trend_score_value_check
        CHECK (
            trend_score_value IS NULL
            OR (trend_score_value >= 0 AND trend_score_value <= 1)
        ),
    CONSTRAINT public_catalog_papers_trend_score_consistency_check
        CHECK (
            (trend_score_state = 'known' AND trend_score_value IS NOT NULL)
            OR
            (trend_score_state IN ('unknown', 'missing') AND trend_score_value IS NULL)
        ),
    CONSTRAINT public_catalog_papers_summary_payload_check
        CHECK (jsonb_typeof(summary_payload) = 'object'),
    CONSTRAINT public_catalog_papers_detail_payload_check
        CHECK (jsonb_typeof(detail_payload) = 'object')
);

CREATE INDEX public_catalog_papers_search_document
    ON public_catalog_papers USING gin(search_document);

CREATE INDEX public_catalog_papers_published_order
    ON public_catalog_papers(
        generation_id,
        published_at DESC NULLS LAST,
        canonical_key
    );

CREATE INDEX public_catalog_papers_citation_order
    ON public_catalog_papers(
        generation_id,
        citation_count_state,
        citation_count_value DESC NULLS LAST,
        canonical_key
    );

CREATE INDEX public_catalog_papers_trend_order
    ON public_catalog_papers(
        generation_id,
        trend_score_state,
        trend_score_value DESC NULLS LAST,
        canonical_key
    );

CREATE INDEX public_catalog_papers_topic_slugs
    ON public_catalog_papers USING gin(topic_slugs);

CREATE INDEX public_catalog_papers_method_slugs
    ON public_catalog_papers USING gin(method_slugs);

CREATE INDEX public_catalog_papers_source_names
    ON public_catalog_papers USING gin(source_names);

CREATE INDEX public_catalog_papers_filter_columns
    ON public_catalog_papers(
        generation_id,
        lifecycle_status,
        paper_type,
        published_at
    );

CREATE TRIGGER public_catalog_papers_immutable
BEFORE INSERT OR UPDATE OR DELETE ON public_catalog_papers
FOR EACH ROW
EXECUTE FUNCTION enforce_public_catalog_child_immutability();
