ALTER TABLE citation_snapshots
    ADD CONSTRAINT citation_snapshots_id_work_source_key
        UNIQUE (id, work_id, source),
    ADD CONSTRAINT citation_snapshots_id_work_source_count_key
        UNIQUE (id, work_id, source, count);

CREATE TABLE citation_analysis_work_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    analysis_run_id uuid NOT NULL,
    work_id uuid NOT NULL,
    source text NOT NULL,
    as_of timestamptz NOT NULL,
    velocity_window_days integer NOT NULL,
    citation_count_state text NOT NULL,
    current_snapshot_id uuid,
    citation_count bigint,
    citation_velocity_state text NOT NULL,
    baseline_snapshot_id uuid,
    citation_velocity numeric,
    source_revision char(64) NOT NULL,
    formula_version text NOT NULL,
    evidence jsonb NOT NULL,
    generated_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT citation_analysis_work_run_fkey
        FOREIGN KEY (analysis_run_id)
        REFERENCES analysis_runs(id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_analysis_work_work_fkey
        FOREIGN KEY (work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_analysis_work_current_snapshot_fkey
        FOREIGN KEY (
            current_snapshot_id,
            work_id,
            source,
            citation_count
        )
        REFERENCES citation_snapshots(id, work_id, source, count)
        ON DELETE RESTRICT,
    CONSTRAINT citation_analysis_work_baseline_snapshot_fkey
        FOREIGN KEY (baseline_snapshot_id, work_id, source)
        REFERENCES citation_snapshots(id, work_id, source)
        ON DELETE RESTRICT,
    CONSTRAINT citation_analysis_work_source_check
        CHECK (btrim(source) <> '' AND source = btrim(source)),
    CONSTRAINT citation_analysis_work_window_check
        CHECK (velocity_window_days BETWEEN 1 AND 3650),
    CONSTRAINT citation_analysis_work_count_state_check
        CHECK (citation_count_state IN ('known', 'missing')),
    CONSTRAINT citation_analysis_work_count_shape_check
        CHECK (
            (
                citation_count_state = 'known'
                AND current_snapshot_id IS NOT NULL
                AND citation_count IS NOT NULL
                AND citation_count >= 0
            )
            OR
            (
                citation_count_state = 'missing'
                AND current_snapshot_id IS NULL
                AND citation_count IS NULL
            )
        ),
    CONSTRAINT citation_analysis_work_velocity_state_check
        CHECK (
            citation_velocity_state IN (
                'known',
                'insufficient_evidence'
            )
        ),
    CONSTRAINT citation_analysis_work_velocity_shape_check
        CHECK (
            (
                citation_velocity_state = 'known'
                AND citation_count_state = 'known'
                AND baseline_snapshot_id IS NOT NULL
                AND baseline_snapshot_id <> current_snapshot_id
                AND citation_velocity IS NOT NULL
            )
            OR
            (
                citation_velocity_state = 'insufficient_evidence'
                AND citation_velocity IS NULL
            )
        ),
    CONSTRAINT citation_analysis_work_source_revision_check
        CHECK (source_revision ~ '^[0-9a-f]{64}$'),
    CONSTRAINT citation_analysis_work_formula_check
        CHECK (
            btrim(formula_version) <> ''
            AND formula_version = btrim(formula_version)
        ),
    CONSTRAINT citation_analysis_work_evidence_check
        CHECK (
            jsonb_typeof(evidence) = 'object'
            AND evidence <> '{}'::jsonb
        ),
    CONSTRAINT citation_analysis_work_run_work_key
        UNIQUE (analysis_run_id, work_id)
);

CREATE INDEX idx_citation_analysis_work_lookup
    ON citation_analysis_work_snapshots(
        analysis_run_id,
        source,
        work_id
    );

CREATE TABLE citation_analysis_percentiles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    analysis_run_id uuid NOT NULL,
    work_id uuid NOT NULL,
    subject_version_id uuid NOT NULL,
    subject_id uuid NOT NULL,
    publication_year integer NOT NULL,
    publication_type_id uuid NOT NULL,
    source text NOT NULL,
    citation_snapshot_id uuid NOT NULL,
    citation_count bigint NOT NULL,
    cohort_key text NOT NULL,
    cohort_size integer NOT NULL,
    minimum_cohort_size integer NOT NULL,
    percentile_state text NOT NULL,
    midrank numeric,
    citation_percentile numeric,
    source_revision char(64) NOT NULL,
    formula_version text NOT NULL,
    evidence jsonb NOT NULL,
    generated_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT citation_analysis_percentiles_run_fkey
        FOREIGN KEY (analysis_run_id)
        REFERENCES analysis_runs(id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_analysis_percentiles_work_fkey
        FOREIGN KEY (work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_analysis_percentiles_subject_fkey
        FOREIGN KEY (subject_id, subject_version_id)
        REFERENCES subjects(id, subject_version_id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_analysis_percentiles_publication_type_fkey
        FOREIGN KEY (publication_type_id)
        REFERENCES publication_types(id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_analysis_percentiles_snapshot_fkey
        FOREIGN KEY (
            citation_snapshot_id,
            work_id,
            source,
            citation_count
        )
        REFERENCES citation_snapshots(id, work_id, source, count)
        ON DELETE RESTRICT,
    CONSTRAINT citation_analysis_percentiles_year_check
        CHECK (publication_year BETWEEN 1900 AND 3000),
    CONSTRAINT citation_analysis_percentiles_source_check
        CHECK (btrim(source) <> '' AND source = btrim(source)),
    CONSTRAINT citation_analysis_percentiles_count_check
        CHECK (citation_count >= 0),
    CONSTRAINT citation_analysis_percentiles_cohort_key_check
        CHECK (
            btrim(cohort_key) <> ''
            AND cohort_key = btrim(cohort_key)
        ),
    CONSTRAINT citation_analysis_percentiles_cohort_size_check
        CHECK (
            cohort_size >= 1
            AND minimum_cohort_size >= 2
        ),
    CONSTRAINT citation_analysis_percentiles_state_check
        CHECK (
            percentile_state IN (
                'known',
                'insufficient_evidence'
            )
        ),
    CONSTRAINT citation_analysis_percentiles_shape_check
        CHECK (
            (
                percentile_state = 'known'
                AND cohort_size >= minimum_cohort_size
                AND midrank IS NOT NULL
                AND midrank >= 1
                AND midrank <= cohort_size
                AND citation_percentile IS NOT NULL
                AND citation_percentile >= 0
                AND citation_percentile <= 100
            )
            OR
            (
                percentile_state = 'insufficient_evidence'
                AND midrank IS NULL
                AND citation_percentile IS NULL
            )
        ),
    CONSTRAINT citation_analysis_percentiles_source_revision_check
        CHECK (source_revision ~ '^[0-9a-f]{64}$'),
    CONSTRAINT citation_analysis_percentiles_formula_check
        CHECK (
            btrim(formula_version) <> ''
            AND formula_version = btrim(formula_version)
        ),
    CONSTRAINT citation_analysis_percentiles_evidence_check
        CHECK (
            jsonb_typeof(evidence) = 'object'
            AND evidence <> '{}'::jsonb
        ),
    CONSTRAINT citation_analysis_percentiles_identity_key
        UNIQUE (
            analysis_run_id,
            work_id,
            subject_id,
            publication_type_id
        )
);

CREATE INDEX idx_citation_analysis_percentiles_cohort
    ON citation_analysis_percentiles(
        analysis_run_id,
        subject_id,
        publication_year,
        publication_type_id,
        citation_percentile DESC
    );

CREATE FUNCTION enforce_citation_analysis_run_and_boundaries()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    run_type text;
    run_status text;
    current_observed_at timestamptz;
    baseline_observed_at timestamptz;
BEGIN
    SELECT analysis_type, status
    INTO run_type, run_status
    FROM analysis_runs
    WHERE id = NEW.analysis_run_id;

    IF run_type IS DISTINCT FROM 'citation_intelligence'
       OR run_status IS DISTINCT FROM 'running' THEN
        RAISE EXCEPTION
            'citation analysis snapshots require a running citation_intelligence analysis run'
            USING
                ERRCODE = '23514',
                CONSTRAINT = 'citation_analysis_running_run';
    END IF;

    IF TG_TABLE_NAME = 'citation_analysis_work_snapshots' THEN
        IF NEW.current_snapshot_id IS NOT NULL THEN
            SELECT observed_at
            INTO current_observed_at
            FROM citation_snapshots
            WHERE id = NEW.current_snapshot_id;

            IF current_observed_at > NEW.as_of THEN
                RAISE EXCEPTION
                    'citation analysis current snapshot is after as_of'
                    USING
                        ERRCODE = '23514',
                        CONSTRAINT = 'citation_analysis_boundary_semantics';
            END IF;
        END IF;

        IF NEW.baseline_snapshot_id IS NOT NULL THEN
            SELECT observed_at
            INTO baseline_observed_at
            FROM citation_snapshots
            WHERE id = NEW.baseline_snapshot_id;

            IF current_observed_at IS NULL
               OR baseline_observed_at >= current_observed_at
               OR baseline_observed_at >
                    NEW.as_of - make_interval(days => NEW.velocity_window_days) THEN
                RAISE EXCEPTION
                    'citation analysis baseline snapshot does not satisfy the declared boundary rule'
                    USING
                        ERRCODE = '23514',
                        CONSTRAINT = 'citation_analysis_boundary_semantics';
            END IF;
        END IF;
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER citation_analysis_work_semantics
BEFORE INSERT ON citation_analysis_work_snapshots
FOR EACH ROW
EXECUTE FUNCTION enforce_citation_analysis_run_and_boundaries();

CREATE TRIGGER citation_analysis_percentiles_semantics
BEFORE INSERT ON citation_analysis_percentiles
FOR EACH ROW
EXECUTE FUNCTION enforce_citation_analysis_run_and_boundaries();

CREATE TRIGGER citation_analysis_work_immutable
BEFORE UPDATE OR DELETE ON citation_analysis_work_snapshots
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TRIGGER citation_analysis_percentiles_immutable
BEFORE UPDATE OR DELETE ON citation_analysis_percentiles
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();
