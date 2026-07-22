CREATE TABLE work_visibility_assessments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    policy_version text NOT NULL,
    publicly_visible boolean NOT NULL,
    analysis_ready boolean NOT NULL,
    analysis_cutoff timestamptz,
    reasons text[] NOT NULL DEFAULT '{}'::text[],
    evaluated_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_visibility_assessments_policy_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT work_visibility_assessments_reasons_check
        CHECK (
            array_position(reasons, NULL) IS NULL
            AND reasons <@ ARRAY[
                'admission_rejected',
                'stable_identity_missing',
                'work_inactive',
                'lifecycle_ineligible',
                'official_link_missing',
                'official_link_invalid',
                'official_link_expired',
                'domain_classification_missing',
                'required_taxonomy_missing',
                'abstract_route_missing',
                'decisive_source_conflict'
            ]::text[]
        ),
    CONSTRAINT work_visibility_assessments_cutoff_check
        CHECK (
            analysis_cutoff IS NULL
            OR analysis_cutoff <= evaluated_at
        ),
    CONSTRAINT work_visibility_assessments_public_shape_check
        CHECK (
            publicly_visible
            OR cardinality(reasons) > 0
        ),
    CONSTRAINT work_visibility_assessments_analysis_shape_check
        CHECK (
            NOT analysis_ready
            OR (
                publicly_visible
                AND analysis_cutoff IS NOT NULL
                AND cardinality(reasons) = 0
            )
        ),
    CONSTRAINT work_visibility_assessments_identity_key
        UNIQUE (work_id, policy_version, evaluated_at)
);

CREATE INDEX idx_work_visibility_assessments_current
    ON work_visibility_assessments(
        work_id,
        policy_version,
        evaluated_at DESC,
        id DESC
    );

CREATE TRIGGER work_visibility_assessments_immutable
BEFORE UPDATE OR DELETE ON work_visibility_assessments
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE VIEW current_work_visibility_states AS
SELECT DISTINCT ON (work_id, policy_version)
    id AS assessment_id,
    work_id,
    policy_version,
    publicly_visible,
    analysis_ready,
    analysis_cutoff,
    reasons,
    evaluated_at,
    created_at
FROM work_visibility_assessments
ORDER BY
    work_id,
    policy_version,
    evaluated_at DESC,
    id DESC;
