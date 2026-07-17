CREATE FUNCTION biomedical_analysis_numeric_is_finite(value numeric)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT value::text NOT IN ('NaN', 'Infinity', '-Infinity')
$$;

CREATE FUNCTION biomedical_analysis_text_array_is_nonempty_trimmed(items text[])
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT
        cardinality(items) > 0
        AND NOT EXISTS (
            SELECT 1
            FROM unnest(items) AS item
            WHERE item IS NULL
               OR btrim(item) = ''
               OR item <> btrim(item)
        )
$$;

CREATE FUNCTION biomedical_analysis_estimate_is_valid(
    metric_name text,
    estimate numeric,
    lower_bound numeric,
    upper_bound numeric
)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT
        biomedical_analysis_numeric_is_finite(estimate)
        AND biomedical_analysis_numeric_is_finite(lower_bound)
        AND biomedical_analysis_numeric_is_finite(upper_bound)
        AND lower_bound <= estimate
        AND estimate <= upper_bound
        AND CASE metric_name
            WHEN 'trend_rate_ratio' THEN lower_bound > 0
            WHEN 'rct_share' THEN lower_bound >= 0 AND upper_bound <= 1
            WHEN 'single_center_share' THEN lower_bound >= 0 AND upper_bound <= 1
            WHEN 'external_validation_share' THEN lower_bound >= 0 AND upper_bound <= 1
            WHEN 'citation_percentile' THEN lower_bound >= 0 AND upper_bound <= 1
            WHEN 'open_data_share' THEN lower_bound >= 0 AND upper_bound <= 1
            WHEN 'observational_share' THEN lower_bound >= 0 AND upper_bound <= 1
            WHEN 'independent_team_count' THEN lower_bound >= 0
            ELSE false
        END
$$;

CREATE TABLE publication_trend_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    analysis_run_id uuid NOT NULL,
    entity_type text NOT NULL,
    entity_id text NOT NULL,
    state text NOT NULL,
    model text NOT NULL,
    model_selection_rule text NOT NULL,
    dispersion_threshold numeric NOT NULL,
    predeclared_dispersion_alpha numeric NOT NULL,
    recent_window_start timestamptz NOT NULL,
    recent_window_end timestamptz NOT NULL,
    recent_paper_count bigint NOT NULL,
    recent_rate_per_day numeric NOT NULL,
    baseline_window_start timestamptz NOT NULL,
    baseline_window_end timestamptz NOT NULL,
    baseline_paper_count bigint NOT NULL,
    baseline_rate_per_day numeric NOT NULL,
    independent_journal_count integer NOT NULL,
    independent_team_count integer NOT NULL,
    rate_ratio numeric,
    confidence_level numeric,
    confidence_interval_lower numeric,
    confidence_interval_upper numeric,
    p_value numeric,
    adjusted_p_value numeric,
    cohort_revision char(64) NOT NULL,
    formula_version text NOT NULL,
    payload jsonb NOT NULL,
    evidence jsonb NOT NULL,
    generated_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT publication_trend_snapshots_run_fkey
        FOREIGN KEY (analysis_run_id)
        REFERENCES analysis_runs(id)
        ON DELETE RESTRICT,
    CONSTRAINT publication_trend_snapshots_entity_type_check
        CHECK (
            entity_type IN (
                'paper',
                'subject',
                'mesh_descriptor',
                'publication_type',
                'method',
                'journal'
            )
        ),
    CONSTRAINT publication_trend_snapshots_entity_id_check
        CHECK (btrim(entity_id) <> '' AND entity_id = btrim(entity_id)),
    CONSTRAINT publication_trend_snapshots_state_check
        CHECK (state IN ('sufficient_evidence', 'insufficient_evidence')),
    CONSTRAINT publication_trend_snapshots_model_check
        CHECK (model IN ('poisson', 'negative_binomial')),
    CONSTRAINT publication_trend_snapshots_model_selection_rule_check
        CHECK (
            model_selection_rule IN (
                'fixed_poisson',
                'fixed_negative_binomial',
                'predeclared_dispersion_threshold'
            )
        ),
    CONSTRAINT publication_trend_snapshots_dispersion_check
        CHECK (
            biomedical_analysis_numeric_is_finite(dispersion_threshold)
            AND dispersion_threshold >= 0
            AND biomedical_analysis_numeric_is_finite(
                predeclared_dispersion_alpha
            )
            AND predeclared_dispersion_alpha >= 0
        ),
    CONSTRAINT publication_trend_snapshots_model_selection_shape_check
        CHECK (
            (
                model_selection_rule = 'fixed_poisson'
                AND model = 'poisson'
                AND dispersion_threshold = 0
            )
            OR
            (
                model_selection_rule = 'fixed_negative_binomial'
                AND model = 'negative_binomial'
                AND dispersion_threshold = 0
            )
            OR
            (
                model_selection_rule = 'predeclared_dispersion_threshold'
                AND dispersion_threshold > 0
                AND (
                    (
                        predeclared_dispersion_alpha >= dispersion_threshold
                        AND model = 'negative_binomial'
                    )
                    OR
                    (
                        predeclared_dispersion_alpha < dispersion_threshold
                        AND model = 'poisson'
                    )
                )
            )
        ),
    CONSTRAINT publication_trend_snapshots_window_check
        CHECK (
            baseline_window_start < baseline_window_end
            AND baseline_window_end <= recent_window_start
            AND recent_window_start < recent_window_end
            AND generated_at >= recent_window_end
        ),
    CONSTRAINT publication_trend_snapshots_count_shape_check
        CHECK (
            recent_paper_count >= 0
            AND baseline_paper_count >= 0
            AND independent_journal_count >= 0
            AND independent_team_count >= 0
        ),
    CONSTRAINT publication_trend_snapshots_rate_shape_check
        CHECK (
            biomedical_analysis_numeric_is_finite(recent_rate_per_day)
            AND recent_rate_per_day >= 0
            AND biomedical_analysis_numeric_is_finite(baseline_rate_per_day)
            AND baseline_rate_per_day >= 0
            AND (recent_paper_count = 0) = (recent_rate_per_day = 0)
            AND (baseline_paper_count = 0) = (baseline_rate_per_day = 0)
        ),
    CONSTRAINT publication_trend_snapshots_estimate_shape_check
        CHECK (
            (
                state = 'sufficient_evidence'
                AND recent_paper_count > 0
                AND baseline_paper_count > 0
                AND rate_ratio IS NOT NULL
                AND biomedical_analysis_numeric_is_finite(rate_ratio)
                AND rate_ratio > 0
                AND confidence_level = 0.95
                AND confidence_interval_lower IS NOT NULL
                AND biomedical_analysis_numeric_is_finite(
                    confidence_interval_lower
                )
                AND confidence_interval_lower > 0
                AND confidence_interval_lower <= rate_ratio
                AND confidence_interval_upper IS NOT NULL
                AND biomedical_analysis_numeric_is_finite(
                    confidence_interval_upper
                )
                AND rate_ratio <= confidence_interval_upper
                AND p_value IS NOT NULL
                AND biomedical_analysis_numeric_is_finite(p_value)
                AND p_value BETWEEN 0 AND 1
                AND adjusted_p_value IS NOT NULL
                AND biomedical_analysis_numeric_is_finite(adjusted_p_value)
                AND adjusted_p_value BETWEEN 0 AND 1
            )
            OR
            (
                state = 'insufficient_evidence'
                AND rate_ratio IS NULL
                AND confidence_level IS NULL
                AND confidence_interval_lower IS NULL
                AND confidence_interval_upper IS NULL
                AND p_value IS NULL
                AND adjusted_p_value IS NULL
            )
        ),
    CONSTRAINT publication_trend_snapshots_cohort_revision_check
        CHECK (cohort_revision ~ '^[0-9a-f]{64}$'),
    CONSTRAINT publication_trend_snapshots_formula_check
        CHECK (
            btrim(formula_version) <> ''
            AND formula_version = btrim(formula_version)
        ),
    CONSTRAINT publication_trend_snapshots_payload_check
        CHECK (
            jsonb_typeof(payload) = 'object'
            AND payload <> '{}'::jsonb
        ),
    CONSTRAINT publication_trend_snapshots_evidence_check
        CHECK (
            jsonb_typeof(evidence) = 'object'
            AND evidence <> '{}'::jsonb
        ),
    CONSTRAINT publication_trend_snapshots_identity_key
        UNIQUE (analysis_run_id, entity_type, entity_id)
);

CREATE INDEX idx_publication_trend_snapshots_lookup
    ON publication_trend_snapshots(
        analysis_run_id,
        entity_type,
        state,
        adjusted_p_value,
        entity_id
    );

CREATE TABLE journal_pattern_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    analysis_run_id uuid NOT NULL,
    pattern_key text NOT NULL,
    journal_id uuid NOT NULL,
    subject_version_id uuid NOT NULL,
    subject_id uuid NOT NULL,
    pattern_kind text NOT NULL,
    feature_type text NOT NULL,
    feature_value text NOT NULL,
    state text NOT NULL,
    measure text NOT NULL,
    journal_feature_paper_count bigint NOT NULL,
    journal_paper_count bigint NOT NULL,
    field_feature_paper_count bigint NOT NULL,
    field_baseline_paper_count bigint NOT NULL,
    journal_exposure numeric,
    field_baseline_exposure numeric,
    covered_paper_count bigint NOT NULL,
    eligible_paper_count bigint NOT NULL,
    coverage numeric NOT NULL,
    effect_value numeric,
    confidence_level numeric,
    confidence_interval_lower numeric,
    confidence_interval_upper numeric,
    p_value numeric,
    adjusted_p_value numeric,
    cohort_revision char(64) NOT NULL,
    formula_version text NOT NULL,
    payload jsonb NOT NULL,
    evidence jsonb NOT NULL,
    generated_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT journal_pattern_snapshots_run_fkey
        FOREIGN KEY (analysis_run_id)
        REFERENCES analysis_runs(id)
        ON DELETE RESTRICT,
    CONSTRAINT journal_pattern_snapshots_journal_fkey
        FOREIGN KEY (journal_id)
        REFERENCES venues(id)
        ON DELETE RESTRICT,
    CONSTRAINT journal_pattern_snapshots_subject_fkey
        FOREIGN KEY (subject_id, subject_version_id)
        REFERENCES subjects(id, subject_version_id)
        ON DELETE RESTRICT,
    CONSTRAINT journal_pattern_snapshots_pattern_key_check
        CHECK (
            btrim(pattern_key) <> ''
            AND pattern_key = btrim(pattern_key)
        ),
    CONSTRAINT journal_pattern_snapshots_kind_check
        CHECK (pattern_kind = 'editorial_pattern'),
    CONSTRAINT journal_pattern_snapshots_feature_type_check
        CHECK (feature_type IN ('mesh', 'publication_type', 'method')),
    CONSTRAINT journal_pattern_snapshots_feature_value_check
        CHECK (
            btrim(feature_value) <> ''
            AND feature_value = btrim(feature_value)
        ),
    CONSTRAINT journal_pattern_snapshots_state_check
        CHECK (state IN ('sufficient_evidence', 'insufficient_evidence')),
    CONSTRAINT journal_pattern_snapshots_measure_check
        CHECK (measure IN ('odds_ratio', 'rate_ratio')),
    CONSTRAINT journal_pattern_snapshots_count_shape_check
        CHECK (
            journal_paper_count > 0
            AND journal_feature_paper_count >= 0
            AND journal_feature_paper_count <= journal_paper_count
            AND field_baseline_paper_count > 0
            AND field_feature_paper_count >= 0
            AND field_feature_paper_count <= field_baseline_paper_count
            AND eligible_paper_count > 0
            AND covered_paper_count >= 0
            AND covered_paper_count <= eligible_paper_count
            AND biomedical_analysis_numeric_is_finite(coverage)
            AND coverage BETWEEN 0 AND 1
            AND coverage * eligible_paper_count = covered_paper_count
        ),
    CONSTRAINT journal_pattern_snapshots_exposure_shape_check
        CHECK (
            (
                measure = 'odds_ratio'
                AND journal_exposure IS NULL
                AND field_baseline_exposure IS NULL
            )
            OR
            (
                measure = 'rate_ratio'
                AND journal_exposure IS NOT NULL
                AND biomedical_analysis_numeric_is_finite(journal_exposure)
                AND journal_exposure > 0
                AND field_baseline_exposure IS NOT NULL
                AND biomedical_analysis_numeric_is_finite(
                    field_baseline_exposure
                )
                AND field_baseline_exposure > 0
            )
        ),
    CONSTRAINT journal_pattern_snapshots_estimate_shape_check
        CHECK (
            (
                state = 'sufficient_evidence'
                AND journal_feature_paper_count > 0
                AND field_feature_paper_count > 0
                AND (
                    measure = 'rate_ratio'
                    OR (
                        journal_feature_paper_count < journal_paper_count
                        AND field_feature_paper_count <
                            field_baseline_paper_count
                    )
                )
                AND effect_value IS NOT NULL
                AND biomedical_analysis_numeric_is_finite(effect_value)
                AND effect_value > 0
                AND confidence_level = 0.95
                AND confidence_interval_lower IS NOT NULL
                AND biomedical_analysis_numeric_is_finite(
                    confidence_interval_lower
                )
                AND confidence_interval_lower > 0
                AND confidence_interval_lower <= effect_value
                AND confidence_interval_upper IS NOT NULL
                AND biomedical_analysis_numeric_is_finite(
                    confidence_interval_upper
                )
                AND effect_value <= confidence_interval_upper
                AND p_value IS NOT NULL
                AND biomedical_analysis_numeric_is_finite(p_value)
                AND p_value BETWEEN 0 AND 1
                AND adjusted_p_value IS NOT NULL
                AND biomedical_analysis_numeric_is_finite(adjusted_p_value)
                AND adjusted_p_value BETWEEN 0 AND 1
            )
            OR
            (
                state = 'insufficient_evidence'
                AND effect_value IS NULL
                AND confidence_level IS NULL
                AND confidence_interval_lower IS NULL
                AND confidence_interval_upper IS NULL
                AND p_value IS NULL
                AND adjusted_p_value IS NULL
            )
        ),
    CONSTRAINT journal_pattern_snapshots_cohort_revision_check
        CHECK (cohort_revision ~ '^[0-9a-f]{64}$'),
    CONSTRAINT journal_pattern_snapshots_formula_check
        CHECK (
            btrim(formula_version) <> ''
            AND formula_version = btrim(formula_version)
        ),
    CONSTRAINT journal_pattern_snapshots_payload_check
        CHECK (
            jsonb_typeof(payload) = 'object'
            AND payload <> '{}'::jsonb
        ),
    CONSTRAINT journal_pattern_snapshots_evidence_check
        CHECK (
            jsonb_typeof(evidence) = 'object'
            AND evidence <> '{}'::jsonb
        ),
    CONSTRAINT journal_pattern_snapshots_identity_key
        UNIQUE (analysis_run_id, pattern_key)
);

CREATE INDEX idx_journal_pattern_snapshots_lookup
    ON journal_pattern_snapshots(
        analysis_run_id,
        journal_id,
        subject_id,
        state,
        adjusted_p_value,
        pattern_key
    );

CREATE TABLE research_opportunity_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    analysis_run_id uuid NOT NULL,
    rule text NOT NULL,
    rule_version text NOT NULL,
    entity_type text NOT NULL,
    entity_id text NOT NULL,
    state text NOT NULL,
    supporting_work_count integer NOT NULL,
    covered_paper_count bigint NOT NULL,
    eligible_paper_count bigint NOT NULL,
    coverage numeric NOT NULL,
    confidence_level numeric NOT NULL,
    primary_metric text NOT NULL,
    primary_estimate numeric NOT NULL,
    primary_confidence_interval_lower numeric NOT NULL,
    primary_confidence_interval_upper numeric NOT NULL,
    secondary_metric text,
    secondary_estimate numeric,
    secondary_confidence_interval_lower numeric,
    secondary_confidence_interval_upper numeric,
    limitations text[] NOT NULL,
    cohort_revision char(64) NOT NULL,
    payload jsonb NOT NULL,
    evidence jsonb NOT NULL,
    generated_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT research_opportunity_snapshots_run_fkey
        FOREIGN KEY (analysis_run_id)
        REFERENCES analysis_runs(id)
        ON DELETE RESTRICT,
    CONSTRAINT research_opportunity_snapshots_rule_check
        CHECK (
            rule IN (
                'rapid_growth_low_rct_share',
                'single_center_external_validation_gap',
                'high_citation_low_open_data',
                'observational_dominance',
                'emerging_method_low_independent_team_count'
            )
        ),
    CONSTRAINT research_opportunity_snapshots_rule_version_check
        CHECK (
            btrim(rule_version) <> ''
            AND rule_version = btrim(rule_version)
        ),
    CONSTRAINT research_opportunity_snapshots_entity_type_check
        CHECK (
            entity_type IN (
                'paper',
                'subject',
                'mesh_descriptor',
                'publication_type',
                'method',
                'journal'
            )
        ),
    CONSTRAINT research_opportunity_snapshots_entity_id_check
        CHECK (btrim(entity_id) <> '' AND entity_id = btrim(entity_id)),
    CONSTRAINT research_opportunity_snapshots_state_check
        CHECK (
            state IN (
                'triggered',
                'not_triggered',
                'insufficient_evidence'
            )
        ),
    CONSTRAINT research_opportunity_snapshots_supporting_work_shape_check
        CHECK (
            supporting_work_count >= 0
        ),
    CONSTRAINT research_opportunity_snapshots_coverage_shape_check
        CHECK (
            eligible_paper_count > 0
            AND covered_paper_count >= 0
            AND covered_paper_count <= eligible_paper_count
            AND biomedical_analysis_numeric_is_finite(coverage)
            AND coverage BETWEEN 0 AND 1
            AND coverage * eligible_paper_count = covered_paper_count
            AND confidence_level = 0.95
        ),
    CONSTRAINT research_opportunity_snapshots_rule_metric_shape_check
        CHECK (
            (
                rule = 'rapid_growth_low_rct_share'
                AND primary_metric = 'trend_rate_ratio'
                AND secondary_metric = 'rct_share'
            )
            OR
            (
                rule = 'single_center_external_validation_gap'
                AND primary_metric = 'single_center_share'
                AND secondary_metric = 'external_validation_share'
            )
            OR
            (
                rule = 'high_citation_low_open_data'
                AND primary_metric = 'citation_percentile'
                AND secondary_metric = 'open_data_share'
            )
            OR
            (
                rule = 'observational_dominance'
                AND primary_metric = 'observational_share'
                AND secondary_metric IS NULL
            )
            OR
            (
                rule = 'emerging_method_low_independent_team_count'
                AND primary_metric = 'trend_rate_ratio'
                AND secondary_metric = 'independent_team_count'
            )
        ),
    CONSTRAINT research_opportunity_snapshots_estimate_shape_check
        CHECK (
            biomedical_analysis_estimate_is_valid(
                primary_metric,
                primary_estimate,
                primary_confidence_interval_lower,
                primary_confidence_interval_upper
            )
            AND (
                (
                    secondary_metric IS NULL
                    AND secondary_estimate IS NULL
                    AND secondary_confidence_interval_lower IS NULL
                    AND secondary_confidence_interval_upper IS NULL
                )
                OR
                (
                    secondary_metric IS NOT NULL
                    AND secondary_estimate IS NOT NULL
                    AND secondary_confidence_interval_lower IS NOT NULL
                    AND secondary_confidence_interval_upper IS NOT NULL
                    AND biomedical_analysis_estimate_is_valid(
                        secondary_metric,
                        secondary_estimate,
                        secondary_confidence_interval_lower,
                        secondary_confidence_interval_upper
                    )
                )
            )
        ),
    CONSTRAINT research_opportunity_snapshots_state_shape_check
        CHECK (
            (
                state = 'insufficient_evidence'
                AND (
                    supporting_work_count < 5
                    OR coverage < 0.8
                )
            )
            OR
            (
                state = 'triggered'
                AND supporting_work_count >= 5
                AND coverage >= 0.8
                AND CASE rule
                    WHEN 'rapid_growth_low_rct_share' THEN
                        primary_confidence_interval_lower > 1
                        AND secondary_confidence_interval_upper <= 0.2
                    WHEN 'single_center_external_validation_gap' THEN
                        primary_confidence_interval_lower >= 0.6
                        AND secondary_confidence_interval_upper <= 0.2
                    WHEN 'high_citation_low_open_data' THEN
                        primary_confidence_interval_lower >= 0.75
                        AND secondary_confidence_interval_upper <= 0.25
                    WHEN 'observational_dominance' THEN
                        primary_confidence_interval_lower >= 0.7
                    WHEN 'emerging_method_low_independent_team_count' THEN
                        primary_confidence_interval_lower > 1
                        AND secondary_confidence_interval_upper < 3
                    ELSE false
                END
            )
            OR
            (
                state = 'not_triggered'
                AND supporting_work_count >= 5
                AND coverage >= 0.8
                AND NOT CASE rule
                    WHEN 'rapid_growth_low_rct_share' THEN
                        primary_confidence_interval_lower > 1
                        AND secondary_confidence_interval_upper <= 0.2
                    WHEN 'single_center_external_validation_gap' THEN
                        primary_confidence_interval_lower >= 0.6
                        AND secondary_confidence_interval_upper <= 0.2
                    WHEN 'high_citation_low_open_data' THEN
                        primary_confidence_interval_lower >= 0.75
                        AND secondary_confidence_interval_upper <= 0.25
                    WHEN 'observational_dominance' THEN
                        primary_confidence_interval_lower >= 0.7
                    WHEN 'emerging_method_low_independent_team_count' THEN
                        primary_confidence_interval_lower > 1
                        AND secondary_confidence_interval_upper < 3
                    ELSE false
                END
            )
        ),
    CONSTRAINT research_opportunity_snapshots_limitations_check
        CHECK (
            biomedical_analysis_text_array_is_nonempty_trimmed(limitations)
        ),
    CONSTRAINT research_opportunity_snapshots_cohort_revision_check
        CHECK (cohort_revision ~ '^[0-9a-f]{64}$'),
    CONSTRAINT research_opportunity_snapshots_payload_check
        CHECK (
            jsonb_typeof(payload) = 'object'
            AND payload <> '{}'::jsonb
        ),
    CONSTRAINT research_opportunity_snapshots_evidence_check
        CHECK (
            jsonb_typeof(evidence) = 'object'
            AND evidence <> '{}'::jsonb
        ),
    CONSTRAINT research_opportunity_snapshots_identity_key
        UNIQUE (analysis_run_id, rule, entity_type, entity_id),
    CONSTRAINT research_opportunity_snapshots_id_run_key
        UNIQUE (id, analysis_run_id)
);

CREATE INDEX idx_research_opportunity_snapshots_lookup
    ON research_opportunity_snapshots(
        analysis_run_id,
        state,
        rule,
        coverage DESC,
        entity_type,
        entity_id
    );

CREATE TABLE research_opportunity_supporting_works (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    analysis_run_id uuid NOT NULL,
    research_opportunity_snapshot_id uuid NOT NULL,
    work_id uuid NOT NULL,
    ordinal integer NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT research_opportunity_supporting_works_run_fkey
        FOREIGN KEY (analysis_run_id)
        REFERENCES analysis_runs(id)
        ON DELETE RESTRICT,
    CONSTRAINT research_opportunity_supporting_works_snapshot_fkey
        FOREIGN KEY (
            research_opportunity_snapshot_id,
            analysis_run_id
        )
        REFERENCES research_opportunity_snapshots(id, analysis_run_id)
        ON DELETE RESTRICT,
    CONSTRAINT research_opportunity_supporting_works_work_fkey
        FOREIGN KEY (work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT research_opportunity_supporting_works_ordinal_check
        CHECK (ordinal > 0),
    CONSTRAINT research_opportunity_supporting_works_snapshot_work_key
        UNIQUE (research_opportunity_snapshot_id, work_id),
    CONSTRAINT research_opportunity_supporting_works_snapshot_ordinal_key
        UNIQUE (research_opportunity_snapshot_id, ordinal)
);

CREATE INDEX idx_research_opportunity_supporting_works_lookup
    ON research_opportunity_supporting_works(
        analysis_run_id,
        research_opportunity_snapshot_id,
        ordinal
    );

CREATE INDEX idx_research_opportunity_supporting_works_work
    ON research_opportunity_supporting_works(
        work_id,
        analysis_run_id
    );

CREATE FUNCTION enforce_biomedical_analysis_snapshot_run()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    expected_analysis_type text;
    run_type text;
    run_status text;
    run_started_at timestamptz;
BEGIN
    expected_analysis_type := CASE TG_TABLE_NAME
        WHEN 'publication_trend_snapshots' THEN 'publication_trends'
        WHEN 'journal_pattern_snapshots' THEN 'journal_editorial_patterns'
        WHEN 'research_opportunity_snapshots' THEN 'research_opportunities'
        ELSE NULL
    END;

    SELECT analysis_type, status, started_at
    INTO run_type, run_status, run_started_at
    FROM analysis_runs
    WHERE id = NEW.analysis_run_id;

    IF NOT FOUND THEN
        RETURN NEW;
    END IF;

    IF expected_analysis_type IS NULL
       OR run_type IS DISTINCT FROM expected_analysis_type
       OR run_status IS DISTINCT FROM 'running'
       OR run_started_at IS NULL THEN
        RAISE EXCEPTION
            '% rows require a matching running analysis run with started_at',
            TG_TABLE_NAME
            USING
                ERRCODE = '23514',
                CONSTRAINT = 'biomedical_analysis_running_run';
    END IF;

    RETURN NEW;
END;
$$;

CREATE FUNCTION enforce_journal_pattern_snapshot_semantics()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM venues
        WHERE id = NEW.journal_id
          AND venue_type = 'journal'
    ) THEN
        RAISE EXCEPTION
            'journal editorial patterns require a journal Venue'
            USING
                ERRCODE = '23514',
                CONSTRAINT = 'journal_pattern_snapshots_journal_type';
    END IF;

    RETURN NEW;
END;
$$;

CREATE FUNCTION enforce_research_opportunity_supporting_work_run()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_type text;
    run_status text;
    run_started_at timestamptz;
BEGIN
    SELECT analysis_type, status, started_at
    INTO run_type, run_status, run_started_at
    FROM analysis_runs
    WHERE id = NEW.analysis_run_id;

    IF NOT FOUND THEN
        RETURN NEW;
    END IF;

    IF run_type IS DISTINCT FROM 'research_opportunities'
       OR run_status IS DISTINCT FROM 'running'
       OR run_started_at IS NULL THEN
        RAISE EXCEPTION
            'research opportunity supporting Works require a matching running research_opportunities analysis run'
            USING
                ERRCODE = '23514',
                CONSTRAINT = 'biomedical_analysis_running_run';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER publication_trend_snapshots_run_guard
BEFORE INSERT ON publication_trend_snapshots
FOR EACH ROW
EXECUTE FUNCTION enforce_biomedical_analysis_snapshot_run();

CREATE TRIGGER journal_pattern_snapshots_run_guard
BEFORE INSERT ON journal_pattern_snapshots
FOR EACH ROW
EXECUTE FUNCTION enforce_biomedical_analysis_snapshot_run();

CREATE TRIGGER journal_pattern_snapshots_journal_guard
BEFORE INSERT ON journal_pattern_snapshots
FOR EACH ROW
EXECUTE FUNCTION enforce_journal_pattern_snapshot_semantics();

CREATE TRIGGER research_opportunity_snapshots_run_guard
BEFORE INSERT ON research_opportunity_snapshots
FOR EACH ROW
EXECUTE FUNCTION enforce_biomedical_analysis_snapshot_run();

CREATE TRIGGER research_opportunity_supporting_works_run_guard
BEFORE INSERT ON research_opportunity_supporting_works
FOR EACH ROW
EXECUTE FUNCTION enforce_research_opportunity_supporting_work_run();

CREATE TRIGGER publication_trend_snapshots_immutable
BEFORE UPDATE OR DELETE ON publication_trend_snapshots
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TRIGGER journal_pattern_snapshots_immutable
BEFORE UPDATE OR DELETE ON journal_pattern_snapshots
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TRIGGER research_opportunity_snapshots_immutable
BEFORE UPDATE OR DELETE ON research_opportunity_snapshots
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TRIGGER research_opportunity_supporting_works_immutable
BEFORE UPDATE OR DELETE ON research_opportunity_supporting_works
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE FUNCTION enforce_biomedical_analysis_run_snapshot_completion()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    snapshot_count bigint;
    latest_generated_at timestamptz;
    has_bound_snapshots boolean;
BEGIN
    IF TG_OP = 'UPDATE'
       AND NEW.analysis_type IS DISTINCT FROM OLD.analysis_type THEN
        SELECT EXISTS (
            SELECT 1
            FROM publication_trend_snapshots
            WHERE analysis_run_id = OLD.id
            UNION ALL
            SELECT 1
            FROM journal_pattern_snapshots
            WHERE analysis_run_id = OLD.id
            UNION ALL
            SELECT 1
            FROM research_opportunity_snapshots
            WHERE analysis_run_id = OLD.id
        )
        INTO has_bound_snapshots;

        IF has_bound_snapshots THEN
            RAISE EXCEPTION
                'analysis run % type cannot change after snapshot materialization',
                OLD.id
                USING
                    ERRCODE = '23514',
                    CONSTRAINT = 'biomedical_analysis_run_binding';
        END IF;
    END IF;

    IF NEW.status <> 'succeeded'
       OR NEW.analysis_type NOT IN (
           'publication_trends',
           'journal_editorial_patterns',
           'research_opportunities'
       ) THEN
        RETURN NEW;
    END IF;

    CASE NEW.analysis_type
    WHEN 'publication_trends' THEN
        SELECT count(*), max(generated_at)
        INTO snapshot_count, latest_generated_at
        FROM publication_trend_snapshots
        WHERE analysis_run_id = NEW.id;
    WHEN 'journal_editorial_patterns' THEN
        SELECT count(*), max(generated_at)
        INTO snapshot_count, latest_generated_at
        FROM journal_pattern_snapshots
        WHERE analysis_run_id = NEW.id;
    WHEN 'research_opportunities' THEN
        SELECT count(*), max(generated_at)
        INTO snapshot_count, latest_generated_at
        FROM research_opportunity_snapshots
        WHERE analysis_run_id = NEW.id;

        IF EXISTS (
            SELECT 1
            FROM research_opportunity_snapshots AS opportunity
            WHERE opportunity.analysis_run_id = NEW.id
              AND opportunity.supporting_work_count <> (
                  SELECT count(*)
                  FROM research_opportunity_supporting_works AS supporting
                  WHERE supporting.research_opportunity_snapshot_id =
                        opportunity.id
                    AND supporting.analysis_run_id = opportunity.analysis_run_id
              )
        ) THEN
            RAISE EXCEPTION
                'completed research opportunity run % has a supporting Work count mismatch',
                NEW.id
                USING
                    ERRCODE = '23514',
                    CONSTRAINT = 'biomedical_analysis_run_completion';
        END IF;
    END CASE;

    IF snapshot_count = 0
       OR NEW.started_at IS NULL
       OR NEW.completed_at IS NULL
       OR NEW.completed_at < NEW.started_at
       OR latest_generated_at > NEW.completed_at THEN
        RAISE EXCEPTION
            'completed biomedical analysis run % must contain snapshots generated no later than completion',
            NEW.id
            USING
                ERRCODE = '23514',
                CONSTRAINT = 'biomedical_analysis_run_completion';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER analysis_runs_biomedical_snapshot_completion
BEFORE INSERT OR UPDATE ON analysis_runs
FOR EACH ROW
EXECUTE FUNCTION enforce_biomedical_analysis_run_snapshot_completion();
