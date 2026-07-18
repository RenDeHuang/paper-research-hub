ALTER TABLE venue_metric_snapshots
    ADD COLUMN registry_version text NOT NULL DEFAULT 'legacy/v1',
    ADD COLUMN edition_year integer,
    ADD COLUMN jif_rank integer,
    ADD COLUMN category_journal_count integer,
    ADD COLUMN jif_percentile numeric;

ALTER TABLE venue_metric_snapshots
    ADD CONSTRAINT venue_metric_snapshots_registry_version_check
        CHECK (registry_version IN ('legacy/v1', 'jcr-registry/v2')),
    ADD CONSTRAINT venue_metric_snapshots_edition_year_check
        CHECK (edition_year IS NULL OR edition_year BETWEEN 1900 AND 3000),
    ADD CONSTRAINT venue_metric_snapshots_jif_rank_check
        CHECK (jif_rank IS NULL OR jif_rank > 0),
    ADD CONSTRAINT venue_metric_snapshots_category_journal_count_check
        CHECK (category_journal_count IS NULL OR category_journal_count > 0),
    ADD CONSTRAINT venue_metric_snapshots_rank_within_category_check
        CHECK (
            jif_rank IS NULL
            OR category_journal_count IS NULL
            OR jif_rank <= category_journal_count
        ),
    ADD CONSTRAINT venue_metric_snapshots_jif_percentile_check
        CHECK (
            jif_percentile IS NULL
            OR (jif_percentile >= 0 AND jif_percentile <= 100)
        ),
    ADD CONSTRAINT venue_metric_snapshots_registry_v2_shape_check
        CHECK (
            registry_version = 'legacy/v1'
            OR (
                metric_status = 'known'
                AND edition_year IS NOT NULL
                AND jif IS NOT NULL
                AND jif_rank IS NOT NULL
                AND category_journal_count IS NOT NULL
                AND jif_percentile IS NOT NULL
                AND quartile IS NOT NULL
            )
            OR (
                metric_status = 'unknown'
                AND edition_year IS NULL
                AND jif IS NULL
                AND jif_rank IS NULL
                AND category_journal_count IS NULL
                AND jif_percentile IS NULL
                AND quartile IS NULL
            )
        );

CREATE INDEX idx_venue_metric_snapshots_registry_v2_q1
    ON venue_metric_snapshots(metric_year DESC, edition_year DESC, category)
    WHERE registry_version = 'jcr-registry/v2'
      AND metric_status = 'known'
      AND quartile = 'Q1';

INSERT INTO venue_policy_versions (
    policy_name,
    version_number,
    definition,
    effective_at
) VALUES (
    'journal-all-q1',
    2,
    '{
        "logic": "ANY",
        "accept": ["jcr_q1"],
        "policy_version": "journal-all-q1/v2"
    }'::jsonb,
    TIMESTAMPTZ '2026-07-18 00:00:00+00'
);
