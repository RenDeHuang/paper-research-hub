"""Create the canonical paper data model.

Revision ID: 0001_initial_schema
Revises:
Create Date: 2026-07-15
"""

from collections.abc import Sequence

from alembic import op
import sqlalchemy as sa
from sqlalchemy.dialects import postgresql


revision: str = "0001_initial_schema"
down_revision: str | None = None
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


record_status = postgresql.ENUM(
    "active",
    "corrected",
    "expression_of_concern",
    "retracted",
    "withdrawn",
    "desk_rejected",
    "superseded",
    name="record_status",
    create_type=False,
)


def _id_column() -> sa.Column:
    return sa.Column("id", postgresql.UUID(as_uuid=True), nullable=False)


def _provenance_columns() -> list[sa.Column]:
    return [
        sa.Column("source", sa.String(length=64), nullable=False),
        sa.Column("source_url", sa.Text(), nullable=True),
        sa.Column(
            "retrieved_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.Column("source_license", sa.String(length=128), nullable=True),
        sa.Column("content_license", sa.String(length=128), nullable=True),
        sa.Column(
            "status",
            record_status,
            server_default=sa.text("'active'"),
            nullable=False,
        ),
    ]


def _timestamp_columns() -> list[sa.Column]:
    return [
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
    ]


def upgrade() -> None:
    record_status.create(op.get_bind(), checkfirst=True)

    op.create_table(
        "work",
        _id_column(),
        sa.Column("canonical_key", sa.String(length=512), nullable=False),
        sa.Column("title", sa.Text(), nullable=False),
        sa.Column("abstract", sa.Text(), nullable=True),
        sa.Column("publication_date", sa.Date(), nullable=True),
        *_provenance_columns(),
        *_timestamp_columns(),
        sa.CheckConstraint(
            "canonical_key ~ "
            "'^(doi|arxiv|openreview|openalex|s2):[^[:space:]]+$'",
            name="canonical_key_approved_prefix",
        ),
        sa.PrimaryKeyConstraint("id", name="pk_work"),
        sa.UniqueConstraint("canonical_key", name="uq_work_canonical_key"),
    )
    op.create_table(
        "paper_version",
        _id_column(),
        sa.Column(
            "work_id",
            postgresql.UUID(as_uuid=True),
            nullable=False,
        ),
        sa.Column("version_label", sa.String(length=64), nullable=False),
        sa.Column("version_number", sa.Integer(), nullable=True),
        sa.Column("version_type", sa.String(length=32), nullable=False),
        sa.Column("title", sa.Text(), nullable=True),
        sa.Column("abstract", sa.Text(), nullable=True),
        sa.Column("submitted_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("published_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("version_url", sa.Text(), nullable=True),
        *_provenance_columns(),
        *_timestamp_columns(),
        sa.ForeignKeyConstraint(
            ["work_id"],
            ["work.id"],
            name="fk_paper_version_work_id_work",
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id", name="pk_paper_version"),
        sa.UniqueConstraint(
            "work_id",
            "version_label",
            name="uq_paper_version_work_label",
        ),
    )
    op.create_table(
        "source_record",
        _id_column(),
        sa.Column("work_id", postgresql.UUID(as_uuid=True), nullable=True),
        sa.Column(
            "paper_version_id",
            postgresql.UUID(as_uuid=True),
            nullable=True,
        ),
        sa.Column("source_record_id", sa.String(length=255), nullable=False),
        sa.Column("content_hash", sa.String(length=64), nullable=False),
        sa.Column(
            "raw_payload",
            postgresql.JSONB(astext_type=sa.Text()),
            nullable=False,
        ),
        sa.Column("http_status", sa.Integer(), nullable=True),
        *_provenance_columns(),
        *_timestamp_columns(),
        sa.ForeignKeyConstraint(
            ["paper_version_id"],
            ["paper_version.id"],
            name="fk_source_record_paper_version_id_paper_version",
            ondelete="SET NULL",
        ),
        sa.ForeignKeyConstraint(
            ["work_id"],
            ["work.id"],
            name="fk_source_record_work_id_work",
            ondelete="SET NULL",
        ),
        sa.PrimaryKeyConstraint("id", name="pk_source_record"),
        sa.UniqueConstraint(
            "source",
            "source_record_id",
            "content_hash",
            name="uq_source_record_source_record_hash",
        ),
    )
    op.create_table(
        "external_identifier",
        _id_column(),
        sa.Column("work_id", postgresql.UUID(as_uuid=True), nullable=False),
        sa.Column(
            "paper_version_id",
            postgresql.UUID(as_uuid=True),
            nullable=True,
        ),
        sa.Column(
            "source_record_id",
            postgresql.UUID(as_uuid=True),
            nullable=True,
        ),
        sa.Column("scheme", sa.String(length=32), nullable=False),
        sa.Column("normalized_value", sa.String(length=512), nullable=False),
        sa.Column("raw_value", sa.String(length=512), nullable=False),
        *_provenance_columns(),
        *_timestamp_columns(),
        sa.ForeignKeyConstraint(
            ["paper_version_id"],
            ["paper_version.id"],
            name="fk_external_identifier_paper_version_id_paper_version",
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["source_record_id"],
            ["source_record.id"],
            name="fk_external_identifier_source_record_id_source_record",
            ondelete="SET NULL",
        ),
        sa.ForeignKeyConstraint(
            ["work_id"],
            ["work.id"],
            name="fk_external_identifier_work_id_work",
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id", name="pk_external_identifier"),
        sa.UniqueConstraint(
            "scheme",
            "normalized_value",
            name="uq_external_identifier_scheme_value",
        ),
    )
    op.create_table(
        "field_assertion",
        _id_column(),
        sa.Column("work_id", postgresql.UUID(as_uuid=True), nullable=False),
        sa.Column(
            "paper_version_id",
            postgresql.UUID(as_uuid=True),
            nullable=True,
        ),
        sa.Column(
            "source_record_id",
            postgresql.UUID(as_uuid=True),
            nullable=False,
        ),
        sa.Column("field_name", sa.String(length=128), nullable=False),
        sa.Column(
            "value",
            postgresql.JSONB(astext_type=sa.Text()),
            nullable=False,
        ),
        sa.Column("parser_version", sa.String(length=64), nullable=False),
        sa.Column("confidence", sa.Numeric(precision=5, scale=4), nullable=True),
        *_provenance_columns(),
        *_timestamp_columns(),
        sa.ForeignKeyConstraint(
            ["paper_version_id"],
            ["paper_version.id"],
            name="fk_field_assertion_paper_version_id_paper_version",
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["source_record_id"],
            ["source_record.id"],
            name="fk_field_assertion_source_record_id_source_record",
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["work_id"],
            ["work.id"],
            name="fk_field_assertion_work_id_work",
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id", name="pk_field_assertion"),
        sa.UniqueConstraint(
            "source_record_id",
            "field_name",
            "parser_version",
            name="uq_field_assertion_record_field_parser",
        ),
    )

    for table_name in ("topic", "method"):
        op.create_table(
            table_name,
            _id_column(),
            sa.Column("name", sa.String(length=255), nullable=False),
            sa.Column("normalized_name", sa.String(length=255), nullable=False),
            sa.Column("description", sa.Text(), nullable=True),
            *_provenance_columns(),
            *_timestamp_columns(),
            sa.PrimaryKeyConstraint("id", name=f"pk_{table_name}"),
            sa.UniqueConstraint(
                "normalized_name",
                name=f"uq_{table_name}_normalized_name",
            ),
        )

    for table_name in ("dataset", "benchmark"):
        op.create_table(
            table_name,
            _id_column(),
            sa.Column("name", sa.String(length=255), nullable=False),
            sa.Column("normalized_name", sa.String(length=255), nullable=False),
            sa.Column("description", sa.Text(), nullable=True),
            sa.Column("homepage_url", sa.Text(), nullable=True),
            *_provenance_columns(),
            *_timestamp_columns(),
            sa.PrimaryKeyConstraint("id", name=f"pk_{table_name}"),
            sa.UniqueConstraint(
                "normalized_name",
                name=f"uq_{table_name}_normalized_name",
            ),
        )

    for entity_name in ("topic", "method", "dataset", "benchmark"):
        association_name = f"work_{entity_name}"
        op.create_table(
            association_name,
            sa.Column(
                "work_id",
                postgresql.UUID(as_uuid=True),
                nullable=False,
            ),
            sa.Column(
                f"{entity_name}_id",
                postgresql.UUID(as_uuid=True),
                nullable=False,
            ),
            sa.ForeignKeyConstraint(
                [f"{entity_name}_id"],
                [f"{entity_name}.id"],
                name=f"fk_{association_name}_{entity_name}_id_{entity_name}",
                ondelete="CASCADE",
            ),
            sa.ForeignKeyConstraint(
                ["work_id"],
                ["work.id"],
                name=f"fk_{association_name}_work_id_work",
                ondelete="CASCADE",
            ),
            sa.PrimaryKeyConstraint(
                "work_id",
                f"{entity_name}_id",
                name=f"pk_{association_name}",
            ),
        )

    op.create_table(
        "code_repository",
        _id_column(),
        sa.Column("work_id", postgresql.UUID(as_uuid=True), nullable=False),
        sa.Column("provider", sa.String(length=32), nullable=False),
        sa.Column("repository_name", sa.String(length=255), nullable=False),
        sa.Column("repository_url", sa.Text(), nullable=False),
        sa.Column("normalized_url", sa.String(length=512), nullable=False),
        sa.Column(
            "is_official",
            sa.Boolean(),
            server_default=sa.text("false"),
            nullable=False,
        ),
        *_provenance_columns(),
        *_timestamp_columns(),
        sa.ForeignKeyConstraint(
            ["work_id"],
            ["work.id"],
            name="fk_code_repository_work_id_work",
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id", name="pk_code_repository"),
        sa.UniqueConstraint(
            "normalized_url",
            name="uq_code_repository_normalized_url",
        ),
    )
    op.create_table(
        "metric_snapshot",
        _id_column(),
        sa.Column("work_id", postgresql.UUID(as_uuid=True), nullable=True),
        sa.Column(
            "code_repository_id",
            postgresql.UUID(as_uuid=True),
            nullable=True,
        ),
        sa.Column("metric_name", sa.String(length=64), nullable=False),
        sa.Column(
            "metric_value",
            sa.Numeric(precision=20, scale=6),
            nullable=False,
        ),
        sa.Column(
            "measured_at",
            sa.DateTime(timezone=True),
            nullable=False,
        ),
        sa.Column("window_days", sa.Integer(), nullable=True),
        sa.Column(
            "metadata",
            postgresql.JSONB(astext_type=sa.Text()),
            nullable=True,
        ),
        *_provenance_columns(),
        *_timestamp_columns(),
        sa.CheckConstraint(
            "(work_id IS NOT NULL AND code_repository_id IS NULL) OR "
            "(work_id IS NULL AND code_repository_id IS NOT NULL)",
            name="ck_metric_snapshot_single_target",
        ),
        sa.ForeignKeyConstraint(
            ["code_repository_id"],
            ["code_repository.id"],
            name="fk_metric_snapshot_code_repository_id_code_repository",
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["work_id"],
            ["work.id"],
            name="fk_metric_snapshot_work_id_work",
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id", name="pk_metric_snapshot"),
        sa.UniqueConstraint(
            "code_repository_id",
            "metric_name",
            "measured_at",
            "source",
            name="uq_metric_snapshot_repo_metric_time_source",
        ),
        sa.UniqueConstraint(
            "work_id",
            "metric_name",
            "measured_at",
            "source",
            name="uq_metric_snapshot_work_metric_time_source",
        ),
    )
    op.create_index(
        "ix_metric_snapshot_work_metric_time",
        "metric_snapshot",
        ["work_id", "metric_name", "measured_at"],
        unique=False,
    )
    op.create_table(
        "ranking_snapshot",
        _id_column(),
        sa.Column("ranking_name", sa.String(length=64), nullable=False),
        sa.Column("work_id", postgresql.UUID(as_uuid=True), nullable=True),
        sa.Column("topic_id", postgresql.UUID(as_uuid=True), nullable=True),
        sa.Column("method_id", postgresql.UUID(as_uuid=True), nullable=True),
        sa.Column("rank_position", sa.Integer(), nullable=False),
        sa.Column(
            "score",
            sa.Numeric(precision=20, scale=6),
            nullable=False,
        ),
        sa.Column("window_days", sa.Integer(), nullable=False),
        sa.Column(
            "computed_at",
            sa.DateTime(timezone=True),
            nullable=False,
        ),
        sa.Column("formula_version", sa.String(length=64), nullable=False),
        sa.Column(
            "coverage",
            postgresql.JSONB(astext_type=sa.Text()),
            nullable=False,
        ),
        sa.Column("explanation", sa.Text(), nullable=True),
        *_provenance_columns(),
        *_timestamp_columns(),
        sa.CheckConstraint(
            "(CASE WHEN work_id IS NOT NULL THEN 1 ELSE 0 END + "
            "CASE WHEN topic_id IS NOT NULL THEN 1 ELSE 0 END + "
            "CASE WHEN method_id IS NOT NULL THEN 1 ELSE 0 END) = 1",
            name="exactly_one_subject",
        ),
        sa.ForeignKeyConstraint(
            ["method_id"],
            ["method.id"],
            name="fk_ranking_snapshot_method_id_method",
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["topic_id"],
            ["topic.id"],
            name="fk_ranking_snapshot_topic_id_topic",
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["work_id"],
            ["work.id"],
            name="fk_ranking_snapshot_work_id_work",
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id", name="pk_ranking_snapshot"),
        sa.UniqueConstraint(
            "ranking_name",
            "method_id",
            "window_days",
            "computed_at",
            name="uq_ranking_snapshot_method_window_time",
        ),
        sa.UniqueConstraint(
            "ranking_name",
            "window_days",
            "computed_at",
            "rank_position",
            name="uq_ranking_snapshot_position",
        ),
        sa.UniqueConstraint(
            "ranking_name",
            "topic_id",
            "window_days",
            "computed_at",
            name="uq_ranking_snapshot_topic_window_time",
        ),
        sa.UniqueConstraint(
            "ranking_name",
            "work_id",
            "window_days",
            "computed_at",
            name="uq_ranking_snapshot_work_window_time",
        ),
    )


def downgrade() -> None:
    op.drop_table("ranking_snapshot")
    op.drop_index(
        "ix_metric_snapshot_work_metric_time",
        table_name="metric_snapshot",
    )
    op.drop_table("metric_snapshot")
    op.drop_table("code_repository")
    op.drop_table("work_benchmark")
    op.drop_table("work_dataset")
    op.drop_table("work_method")
    op.drop_table("work_topic")
    op.drop_table("benchmark")
    op.drop_table("dataset")
    op.drop_table("method")
    op.drop_table("topic")
    op.drop_table("field_assertion")
    op.drop_table("external_identifier")
    op.drop_table("source_record")
    op.drop_table("paper_version")
    op.drop_table("work")
    record_status.drop(op.get_bind(), checkfirst=True)
