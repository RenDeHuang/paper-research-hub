"""Enforce canonical identities and relational ownership.

Revision ID: 0002_enforce_canonical_integrity
Revises: 0001_initial_schema
Create Date: 2026-07-15

Ranking snapshots are derived data. The upgrade intentionally deletes existing
ranking rows before replacing the legacy polymorphic subject columns. Their
historical values cannot be restored by downgrade; the old schema is restored
and rankings must be recomputed from metric snapshots.

Legacy source records may redundantly reference both a work and one of its
versions. The upgrade verifies those references agree, then keeps only the
paper-version reference for version-level records. Downgrade reconstructs the
legacy work reference from PaperVersion.work_id. If later parent deletion has
orphaned a source that still has field assertions, downgrade aborts explicitly
because the 0001 schema requires every assertion to have a non-null work owner.
"""

from collections.abc import Sequence

from alembic import op
import sqlalchemy as sa
from sqlalchemy.dialects import postgresql


revision: str = "0002_enforce_canonical_integrity"
down_revision: str | None = "0001_initial_schema"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


CANONICAL_KEY_CHECK = (
    "("
    "(canonical_key ~ '^doi:10[.][0-9]{4,9}/[^[:space:]]+$' "
    "AND canonical_key = lower(canonical_key)) OR "
    "(canonical_key ~ "
    "'^arxiv:([0-9]{4}[.][0-9]{4,5}|[a-z][a-z0-9.-]*/[0-9]{7})$') OR "
    "(canonical_key ~ "
    "'^openreview:[A-Za-z0-9][A-Za-z0-9._~-]{0,254}$') OR "
    "(canonical_key ~ '^openalex:W[0-9]+$') OR "
    "(canonical_key ~ '^s2:[0-9a-f]{40}$')"
    ")"
)


def upgrade() -> None:
    op.execute(
        sa.text(
            f"""
            DO $$
            BEGIN
                IF EXISTS (
                    SELECT 1 FROM work
                    WHERE NOT ({CANONICAL_KEY_CHECK})
                ) THEN
                    RAISE EXCEPTION
                        'Cannot enforce canonical key format: invalid work rows exist';
                END IF;
            END
            $$;
            """
        )
    )
    op.create_check_constraint(
        "ck_work_canonical_key_approved_prefix",
        "work",
        CANONICAL_KEY_CHECK,
    )

    op.create_check_constraint(
        "ck_paper_version_version_number_positive",
        "paper_version",
        "version_number IS NULL OR version_number >= 1",
    )
    op.execute(
        sa.text(
            """
            DO $$
            BEGIN
                IF EXISTS (
                    SELECT 1
                    FROM source_record AS source
                    JOIN paper_version AS version
                        ON version.id = source.paper_version_id
                    WHERE source.work_id IS NOT NULL
                      AND source.work_id <> version.work_id
                ) THEN
                    RAISE EXCEPTION
                        'Cannot enforce source ownership: inconsistent source record ownership exists';
                END IF;
            END
            $$;
            """
        )
    )
    op.execute(
        sa.text(
            """
            UPDATE source_record
            SET work_id = NULL
            WHERE paper_version_id IS NOT NULL
            """
        )
    )
    op.create_check_constraint(
        "ck_source_record_single_owner",
        "source_record",
        "work_id IS NULL OR paper_version_id IS NULL",
    )

    op.drop_constraint(
        "fk_field_assertion_paper_version_id_paper_version",
        "field_assertion",
        type_="foreignkey",
    )
    op.drop_constraint(
        "fk_field_assertion_work_id_work",
        "field_assertion",
        type_="foreignkey",
    )
    op.drop_column("field_assertion", "paper_version_id")
    op.drop_column("field_assertion", "work_id")
    op.create_check_constraint(
        "ck_field_assertion_confidence_range",
        "field_assertion",
        "confidence IS NULL OR (confidence >= 0 AND confidence <= 1)",
    )

    op.create_check_constraint(
        "ck_metric_snapshot_window_days_positive",
        "metric_snapshot",
        "window_days IS NULL OR window_days > 0",
    )

    op.execute(sa.text("DELETE FROM ranking_snapshot"))
    op.drop_constraint(
        "uq_ranking_snapshot_subject_window_time",
        "ranking_snapshot",
        type_="unique",
    )
    op.drop_column("ranking_snapshot", "subject_id")
    op.drop_column("ranking_snapshot", "subject_type")
    op.add_column(
        "ranking_snapshot",
        sa.Column("work_id", postgresql.UUID(as_uuid=True), nullable=True),
    )
    op.add_column(
        "ranking_snapshot",
        sa.Column("topic_id", postgresql.UUID(as_uuid=True), nullable=True),
    )
    op.add_column(
        "ranking_snapshot",
        sa.Column("method_id", postgresql.UUID(as_uuid=True), nullable=True),
    )
    op.create_foreign_key(
        "fk_ranking_snapshot_work_id_work",
        "ranking_snapshot",
        "work",
        ["work_id"],
        ["id"],
        ondelete="CASCADE",
    )
    op.create_foreign_key(
        "fk_ranking_snapshot_topic_id_topic",
        "ranking_snapshot",
        "topic",
        ["topic_id"],
        ["id"],
        ondelete="CASCADE",
    )
    op.create_foreign_key(
        "fk_ranking_snapshot_method_id_method",
        "ranking_snapshot",
        "method",
        ["method_id"],
        ["id"],
        ondelete="CASCADE",
    )
    op.create_check_constraint(
        "ck_ranking_snapshot_exactly_one_subject",
        "ranking_snapshot",
        "(CASE WHEN work_id IS NOT NULL THEN 1 ELSE 0 END + "
        "CASE WHEN topic_id IS NOT NULL THEN 1 ELSE 0 END + "
        "CASE WHEN method_id IS NOT NULL THEN 1 ELSE 0 END) = 1",
    )
    op.create_check_constraint(
        "ck_ranking_snapshot_rank_position_positive",
        "ranking_snapshot",
        "rank_position >= 1",
    )
    op.create_check_constraint(
        "ck_ranking_snapshot_window_days_positive",
        "ranking_snapshot",
        "window_days > 0",
    )
    op.create_unique_constraint(
        "uq_ranking_snapshot_work_window_time",
        "ranking_snapshot",
        ["ranking_name", "work_id", "window_days", "computed_at"],
    )
    op.create_unique_constraint(
        "uq_ranking_snapshot_topic_window_time",
        "ranking_snapshot",
        ["ranking_name", "topic_id", "window_days", "computed_at"],
    )
    op.create_unique_constraint(
        "uq_ranking_snapshot_method_window_time",
        "ranking_snapshot",
        ["ranking_name", "method_id", "window_days", "computed_at"],
    )


def downgrade() -> None:
    op.drop_constraint(
        "uq_ranking_snapshot_method_window_time",
        "ranking_snapshot",
        type_="unique",
    )
    op.drop_constraint(
        "uq_ranking_snapshot_topic_window_time",
        "ranking_snapshot",
        type_="unique",
    )
    op.drop_constraint(
        "uq_ranking_snapshot_work_window_time",
        "ranking_snapshot",
        type_="unique",
    )
    op.drop_constraint(
        "ck_ranking_snapshot_window_days_positive",
        "ranking_snapshot",
        type_="check",
    )
    op.drop_constraint(
        "ck_ranking_snapshot_rank_position_positive",
        "ranking_snapshot",
        type_="check",
    )
    op.drop_constraint(
        "ck_ranking_snapshot_exactly_one_subject",
        "ranking_snapshot",
        type_="check",
    )
    op.drop_constraint(
        "fk_ranking_snapshot_method_id_method",
        "ranking_snapshot",
        type_="foreignkey",
    )
    op.drop_constraint(
        "fk_ranking_snapshot_topic_id_topic",
        "ranking_snapshot",
        type_="foreignkey",
    )
    op.drop_constraint(
        "fk_ranking_snapshot_work_id_work",
        "ranking_snapshot",
        type_="foreignkey",
    )
    op.add_column(
        "ranking_snapshot",
        sa.Column("subject_type", sa.String(length=32), nullable=True),
    )
    op.add_column(
        "ranking_snapshot",
        sa.Column("subject_id", postgresql.UUID(as_uuid=True), nullable=True),
    )
    op.execute(
        sa.text(
            """
            UPDATE ranking_snapshot
            SET subject_type = CASE
                    WHEN work_id IS NOT NULL THEN 'work'
                    WHEN topic_id IS NOT NULL THEN 'topic'
                    ELSE 'method'
                END,
                subject_id = COALESCE(work_id, topic_id, method_id)
            """
        )
    )
    op.alter_column("ranking_snapshot", "subject_type", nullable=False)
    op.alter_column("ranking_snapshot", "subject_id", nullable=False)
    op.drop_column("ranking_snapshot", "method_id")
    op.drop_column("ranking_snapshot", "topic_id")
    op.drop_column("ranking_snapshot", "work_id")
    op.create_unique_constraint(
        "uq_ranking_snapshot_subject_window_time",
        "ranking_snapshot",
        [
            "ranking_name",
            "subject_type",
            "subject_id",
            "window_days",
            "computed_at",
        ],
    )

    op.drop_constraint(
        "ck_metric_snapshot_window_days_positive",
        "metric_snapshot",
        type_="check",
    )

    op.drop_constraint(
        "ck_source_record_single_owner",
        "source_record",
        type_="check",
    )
    op.execute(
        sa.text(
            """
            UPDATE source_record AS source
            SET work_id = version.work_id
            FROM paper_version AS version
            WHERE version.id = source.paper_version_id
            """
        )
    )

    op.drop_constraint(
        "ck_field_assertion_confidence_range",
        "field_assertion",
        type_="check",
    )
    op.add_column(
        "field_assertion",
        sa.Column("work_id", postgresql.UUID(as_uuid=True), nullable=True),
    )
    op.add_column(
        "field_assertion",
        sa.Column(
            "paper_version_id",
            postgresql.UUID(as_uuid=True),
            nullable=True,
        ),
    )
    op.execute(
        sa.text(
            """
            UPDATE field_assertion AS assertion
            SET work_id = source.work_id,
                paper_version_id = source.paper_version_id
            FROM source_record AS source
            WHERE source.id = assertion.source_record_id
            """
        )
    )
    op.execute(
        sa.text(
            """
            DO $$
            BEGIN
                IF EXISTS (
                    SELECT 1 FROM field_assertion WHERE work_id IS NULL
                ) THEN
                    RAISE EXCEPTION
                        'Cannot downgrade: field assertions have no work owner';
                END IF;
            END
            $$;
            """
        )
    )
    op.alter_column("field_assertion", "work_id", nullable=False)
    op.create_foreign_key(
        "fk_field_assertion_work_id_work",
        "field_assertion",
        "work",
        ["work_id"],
        ["id"],
        ondelete="CASCADE",
    )
    op.create_foreign_key(
        "fk_field_assertion_paper_version_id_paper_version",
        "field_assertion",
        "paper_version",
        ["paper_version_id"],
        ["id"],
        ondelete="CASCADE",
    )

    op.drop_constraint(
        "ck_paper_version_version_number_positive",
        "paper_version",
        type_="check",
    )
    op.drop_constraint(
        "ck_work_canonical_key_approved_prefix",
        "work",
        type_="check",
    )
