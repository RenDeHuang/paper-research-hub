-- Fixed fixture for the physical schema created by c74476a while its
-- Alembic revision still identified itself as 0001_initial_schema.
ALTER TABLE work
    ADD CONSTRAINT ck_work_canonical_key_approved_prefix
    CHECK (
        canonical_key ~
        '^(doi|arxiv|openreview|openalex|s2):[^[:space:]]+$'
    );

ALTER TABLE ranking_snapshot
    DROP CONSTRAINT uq_ranking_snapshot_subject_window_time;
ALTER TABLE ranking_snapshot DROP COLUMN subject_id;
ALTER TABLE ranking_snapshot DROP COLUMN subject_type;
ALTER TABLE ranking_snapshot ADD COLUMN work_id UUID;
ALTER TABLE ranking_snapshot ADD COLUMN topic_id UUID;
ALTER TABLE ranking_snapshot ADD COLUMN method_id UUID;

ALTER TABLE ranking_snapshot
    ADD CONSTRAINT fk_ranking_snapshot_work_id_work
    FOREIGN KEY (work_id) REFERENCES work (id) ON DELETE CASCADE;
ALTER TABLE ranking_snapshot
    ADD CONSTRAINT fk_ranking_snapshot_topic_id_topic
    FOREIGN KEY (topic_id) REFERENCES topic (id) ON DELETE CASCADE;
ALTER TABLE ranking_snapshot
    ADD CONSTRAINT fk_ranking_snapshot_method_id_method
    FOREIGN KEY (method_id) REFERENCES method (id) ON DELETE CASCADE;
ALTER TABLE ranking_snapshot
    ADD CONSTRAINT ck_ranking_snapshot_exactly_one_subject
    CHECK (
        (
            CASE WHEN work_id IS NOT NULL THEN 1 ELSE 0 END
            + CASE WHEN topic_id IS NOT NULL THEN 1 ELSE 0 END
            + CASE WHEN method_id IS NOT NULL THEN 1 ELSE 0 END
        ) = 1
    );

ALTER TABLE ranking_snapshot
    ADD CONSTRAINT uq_ranking_snapshot_work_window_time
    UNIQUE (ranking_name, work_id, window_days, computed_at);
ALTER TABLE ranking_snapshot
    ADD CONSTRAINT uq_ranking_snapshot_topic_window_time
    UNIQUE (ranking_name, topic_id, window_days, computed_at);
ALTER TABLE ranking_snapshot
    ADD CONSTRAINT uq_ranking_snapshot_method_window_time
    UNIQUE (ranking_name, method_id, window_days, computed_at);
