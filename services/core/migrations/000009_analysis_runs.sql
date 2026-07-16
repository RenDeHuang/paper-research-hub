ALTER TABLE analysis_runs
    ADD CONSTRAINT analysis_runs_succeeded_completion_check
    CHECK (
        status <> 'succeeded'
        OR (
            output_payload IS NOT NULL
            AND started_at IS NOT NULL
            AND completed_at IS NOT NULL
            AND started_at <= completed_at
        )
    );

CREATE FUNCTION reject_completed_analysis_run_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status = 'succeeded' THEN
        RAISE EXCEPTION 'completed analysis run % is immutable', OLD.id
            USING ERRCODE = '55000';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER analysis_runs_completed_immutable
BEFORE UPDATE OR DELETE ON analysis_runs
FOR EACH ROW
EXECUTE FUNCTION reject_completed_analysis_run_mutation();

CREATE TABLE public_catalog_trends (
    generation_id uuid NOT NULL
        REFERENCES public_catalog_generations(id)
        ON DELETE RESTRICT,
    trend_kind text NOT NULL,
    window_days integer NOT NULL,
    rank integer NOT NULL,
    subject_id uuid NOT NULL,
    score numeric NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (generation_id, trend_kind, window_days, rank),
    CONSTRAINT public_catalog_trends_subject_key
        UNIQUE (generation_id, trend_kind, window_days, subject_id),
    CONSTRAINT public_catalog_trends_kind_check
        CHECK (trend_kind IN ('papers', 'topics', 'methods')),
    CONSTRAINT public_catalog_trends_window_check
        CHECK (window_days BETWEEN 1 AND 365),
    CONSTRAINT public_catalog_trends_rank_check CHECK (rank > 0),
    CONSTRAINT public_catalog_trends_score_check CHECK (score >= 0 AND score <= 1),
    CONSTRAINT public_catalog_trends_payload_check
        CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX public_catalog_trends_order
    ON public_catalog_trends(generation_id, trend_kind, window_days, rank);

CREATE TRIGGER public_catalog_trends_immutable
BEFORE INSERT OR UPDATE OR DELETE ON public_catalog_trends
FOR EACH ROW
EXECUTE FUNCTION enforce_public_catalog_child_immutability();

CREATE TABLE public_catalog_research_opportunities (
    generation_id uuid NOT NULL
        REFERENCES public_catalog_generations(id)
        ON DELETE RESTRICT,
    opportunity_id uuid NOT NULL,
    status text NOT NULL,
    ordinal integer NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (generation_id, opportunity_id),
    CONSTRAINT public_catalog_research_opportunities_ordinal_key
        UNIQUE (generation_id, ordinal),
    CONSTRAINT public_catalog_research_opportunities_status_check
        CHECK (
            status IN (
                'worth_pursuing',
                'proceed_with_caution',
                'not_recommended_now',
                'insufficient_evidence'
            )
        ),
    CONSTRAINT public_catalog_research_opportunities_ordinal_check
        CHECK (ordinal > 0),
    CONSTRAINT public_catalog_research_opportunities_payload_check
        CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX public_catalog_research_opportunities_order
    ON public_catalog_research_opportunities(generation_id, status, ordinal);

CREATE TRIGGER public_catalog_research_opportunities_immutable
BEFORE INSERT OR UPDATE OR DELETE ON public_catalog_research_opportunities
FOR EACH ROW
EXECUTE FUNCTION enforce_public_catalog_child_immutability();
