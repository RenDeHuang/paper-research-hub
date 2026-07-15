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

The migration also detects the checked-in d1aacd8 schema and the c74476a
physical variant, which used the same revision ID after adding relational
ranking columns. ExternalIdentifier version/source links are intentionally
dropped because their independent foreign keys cannot enforce Work ownership;
downgrade recreates those nullable legacy columns without restoring lost links.
"""

from collections.abc import Sequence

from alembic import context, op
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
METRIC_SINGLE_TARGET_CHECK = (
    "(work_id IS NOT NULL AND code_repository_id IS NULL) OR "
    "(work_id IS NULL AND code_repository_id IS NOT NULL)"
)
RANKING_EXACTLY_ONE_SUBJECT_CHECK = (
    "(CASE WHEN work_id IS NOT NULL THEN 1 ELSE 0 END + "
    "CASE WHEN topic_id IS NOT NULL THEN 1 ELSE 0 END + "
    "CASE WHEN method_id IS NOT NULL THEN 1 ELSE 0 END) = 1"
)


def _column_names(table_name: str) -> set[str]:
    return {
        column["name"]
        for column in sa.inspect(op.get_bind()).get_columns(table_name)
    }


def _check_constraint_names(table_name: str) -> set[str]:
    return {
        constraint["name"]
        for constraint in sa.inspect(op.get_bind()).get_check_constraints(
            table_name
        )
        if constraint["name"] is not None
    }


def _rename_constraint(table_name: str, old_name: str, new_name: str) -> None:
    op.execute(
        sa.text(
            f'ALTER TABLE "{table_name}" '
            f'RENAME CONSTRAINT "{old_name}" TO "{new_name}"'
        )
    )


def _replace_check_constraint(
    table_name: str,
    constraint_name: str,
    condition: str,
    *,
    aliases: tuple[str, ...] = (),
) -> None:
    existing = _check_constraint_names(table_name)
    for name in (constraint_name, *aliases):
        if name in existing:
            op.drop_constraint(name, table_name, type_="check")
    op.create_check_constraint(constraint_name, table_name, condition)


def _ensure_check_constraint(
    table_name: str,
    constraint_name: str,
    condition: str,
    *,
    aliases: tuple[str, ...] = (),
) -> None:
    existing = _check_constraint_names(table_name)
    if constraint_name in existing:
        for alias in aliases:
            if alias in existing:
                op.drop_constraint(alias, table_name, type_="check")
        return

    present_aliases = [alias for alias in aliases if alias in existing]
    if present_aliases:
        _rename_constraint(table_name, present_aliases[0], constraint_name)
        for alias in present_aliases[1:]:
            op.drop_constraint(alias, table_name, type_="check")
        return

    op.create_check_constraint(constraint_name, table_name, condition)


def _drop_foreign_keys_for_column(
    table_name: str,
    column_name: str,
) -> None:
    foreign_keys = sa.inspect(op.get_bind()).get_foreign_keys(table_name)
    for foreign_key in foreign_keys:
        if foreign_key["constrained_columns"] == [column_name]:
            op.drop_constraint(
                foreign_key["name"],
                table_name,
                type_="foreignkey",
            )


def _ensure_foreign_key(
    table_name: str,
    constraint_name: str,
    local_columns: list[str],
    remote_table: str,
    remote_columns: list[str],
    *,
    ondelete: str,
) -> None:
    foreign_keys = sa.inspect(op.get_bind()).get_foreign_keys(table_name)
    named = next(
        (
            foreign_key
            for foreign_key in foreign_keys
            if foreign_key["name"] == constraint_name
        ),
        None,
    )
    if named is not None:
        return

    matching = next(
        (
            foreign_key
            for foreign_key in foreign_keys
            if foreign_key["constrained_columns"] == local_columns
            and foreign_key["referred_table"] == remote_table
            and foreign_key["referred_columns"] == remote_columns
        ),
        None,
    )
    if matching is not None:
        _rename_constraint(table_name, matching["name"], constraint_name)
        return

    op.create_foreign_key(
        constraint_name,
        table_name,
        remote_table,
        local_columns,
        remote_columns,
        ondelete=ondelete,
    )


def _ensure_unique_constraint(
    table_name: str,
    constraint_name: str,
    columns: list[str],
) -> None:
    unique_constraints = sa.inspect(op.get_bind()).get_unique_constraints(
        table_name
    )
    named = next(
        (
            constraint
            for constraint in unique_constraints
            if constraint["name"] == constraint_name
        ),
        None,
    )
    matching = [
        constraint
        for constraint in unique_constraints
        if constraint["column_names"] == columns
    ]
    if named is not None:
        for duplicate in matching:
            if duplicate["name"] != constraint_name:
                op.drop_constraint(
                    duplicate["name"],
                    table_name,
                    type_="unique",
                )
        return

    if matching:
        _rename_constraint(
            table_name,
            matching[0]["name"],
            constraint_name,
        )
        for duplicate in matching[1:]:
            op.drop_constraint(
                duplicate["name"],
                table_name,
                type_="unique",
            )
        return

    op.create_unique_constraint(constraint_name, table_name, columns)


def _validate_canonical_keys() -> None:
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


def _validate_source_ownership() -> None:
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


def _offline_replace_check_constraint(
    table_name: str,
    constraint_name: str,
    condition: str,
    *,
    aliases: tuple[str, ...] = (),
) -> None:
    drop_statements = "\n".join(
        f'ALTER TABLE "{table_name}" DROP CONSTRAINT IF EXISTS "{name}";'
        for name in (constraint_name, *aliases)
    )
    op.execute(
        sa.text(
            f"""
            {drop_statements}
            ALTER TABLE "{table_name}"
                ADD CONSTRAINT "{constraint_name}" CHECK ({condition});
            """
        )
    )


def _offline_ensure_constraint(
    table_name: str,
    constraint_name: str,
    definition: str,
    *,
    aliases: tuple[str, ...] = (),
) -> None:
    target_exists = (
        "EXISTS ("
        "SELECT 1 FROM pg_constraint "
        f"WHERE conname = '{constraint_name}' "
        f"AND conrelid = '{table_name}'::regclass"
        ")"
    )
    if aliases:
        target_cleanup = "\n".join(
            f'ALTER TABLE "{table_name}" '
            f'DROP CONSTRAINT IF EXISTS "{alias}";'
            for alias in aliases
        )
        alias_branches = []
        for index, alias in enumerate(aliases):
            remaining_cleanup = "\n".join(
                f'ALTER TABLE "{table_name}" '
                f'DROP CONSTRAINT IF EXISTS "{other_alias}";'
                for other_alias in aliases[index + 1 :]
            )
            alias_branches.append(
                "ELSIF EXISTS ("
                "SELECT 1 FROM pg_constraint "
                f"WHERE conname = '{alias}' "
                f"AND conrelid = '{table_name}'::regclass"
                ") THEN\n"
                f'ALTER TABLE "{table_name}" '
                f'RENAME CONSTRAINT "{alias}" TO "{constraint_name}";\n'
                f"{remaining_cleanup}"
            )
        branches = "\n".join(alias_branches)
        body = f"""
            IF {target_exists} THEN
                {target_cleanup}
            {branches}
            ELSE
                ALTER TABLE "{table_name}"
                    ADD CONSTRAINT "{constraint_name}" {definition};
            END IF;
        """
    else:
        body = f"""
            IF NOT {target_exists} THEN
                ALTER TABLE "{table_name}"
                    ADD CONSTRAINT "{constraint_name}" {definition};
            END IF;
        """

    op.execute(
        sa.text(
            f"""
            DO $$
            BEGIN
                {body}
            END
            $$;
            """
        )
    )


def _upgrade_online() -> None:
    _validate_canonical_keys()
    _replace_check_constraint(
        "work",
        "ck_work_canonical_key_approved_prefix",
        CANONICAL_KEY_CHECK,
        aliases=("canonical_key_approved_prefix",),
    )

    _ensure_check_constraint(
        "paper_version",
        "ck_paper_version_version_number_positive",
        "version_number IS NULL OR version_number >= 1",
        aliases=(
            "ck_paper_version_ck_paper_version_version_number_positive",
        ),
    )

    source_columns = _column_names("source_record")
    if {"work_id", "paper_version_id"} <= source_columns:
        _validate_source_ownership()
        op.execute(
            sa.text(
                """
                UPDATE source_record
                SET work_id = NULL
                WHERE paper_version_id IS NOT NULL
                """
            )
        )
    _replace_check_constraint(
        "source_record",
        "ck_source_record_single_owner",
        "work_id IS NULL OR paper_version_id IS NULL",
        aliases=("ck_source_record_version_requires_work",),
    )

    for column_name in ("paper_version_id", "work_id"):
        if column_name in _column_names("field_assertion"):
            _drop_foreign_keys_for_column("field_assertion", column_name)
            op.drop_column("field_assertion", column_name)
    _ensure_check_constraint(
        "field_assertion",
        "ck_field_assertion_confidence_range",
        "confidence IS NULL OR (confidence >= 0 AND confidence <= 1)",
        aliases=(
            "ck_field_assertion_ck_field_assertion_confidence_range",
        ),
    )

    for column_name in ("paper_version_id", "source_record_id"):
        if column_name in _column_names("external_identifier"):
            _drop_foreign_keys_for_column(
                "external_identifier",
                column_name,
            )
            op.drop_column("external_identifier", column_name)

    _ensure_check_constraint(
        "metric_snapshot",
        "ck_metric_snapshot_single_target",
        METRIC_SINGLE_TARGET_CHECK,
        aliases=(
            "ck_metric_snapshot_ck_metric_snapshot_single_target",
            "single_target",
        ),
    )
    _ensure_check_constraint(
        "metric_snapshot",
        "ck_metric_snapshot_window_days_positive",
        "window_days IS NULL OR window_days > 0",
        aliases=(
            "ck_metric_snapshot_ck_metric_snapshot_window_days_positive",
        ),
    )

    ranking_columns = _column_names("ranking_snapshot")
    if {"subject_type", "subject_id"} & ranking_columns:
        op.execute(sa.text("DELETE FROM ranking_snapshot"))
        unique_constraints = sa.inspect(
            op.get_bind()
        ).get_unique_constraints("ranking_snapshot")
        for unique_constraint in unique_constraints:
            if {
                "subject_type",
                "subject_id",
            } & set(unique_constraint["column_names"]):
                op.drop_constraint(
                    unique_constraint["name"],
                    "ranking_snapshot",
                    type_="unique",
                )
        for column_name in ("subject_id", "subject_type"):
            if column_name in _column_names("ranking_snapshot"):
                op.drop_column("ranking_snapshot", column_name)

    for column_name in ("work_id", "topic_id", "method_id"):
        if column_name not in _column_names("ranking_snapshot"):
            op.add_column(
                "ranking_snapshot",
                sa.Column(
                    column_name,
                    postgresql.UUID(as_uuid=True),
                    nullable=True,
                ),
            )

    _ensure_foreign_key(
        "ranking_snapshot",
        "fk_ranking_snapshot_work_id_work",
        ["work_id"],
        "work",
        ["id"],
        ondelete="CASCADE",
    )
    _ensure_foreign_key(
        "ranking_snapshot",
        "fk_ranking_snapshot_topic_id_topic",
        ["topic_id"],
        "topic",
        ["id"],
        ondelete="CASCADE",
    )
    _ensure_foreign_key(
        "ranking_snapshot",
        "fk_ranking_snapshot_method_id_method",
        ["method_id"],
        "method",
        ["id"],
        ondelete="CASCADE",
    )
    _ensure_check_constraint(
        "ranking_snapshot",
        "ck_ranking_snapshot_exactly_one_subject",
        RANKING_EXACTLY_ONE_SUBJECT_CHECK,
        aliases=("exactly_one_subject",),
    )
    _ensure_check_constraint(
        "ranking_snapshot",
        "ck_ranking_snapshot_rank_position_positive",
        "rank_position >= 1",
        aliases=(
            "ck_ranking_snapshot_ck_ranking_snapshot_rank_position_positive",
        ),
    )
    _ensure_check_constraint(
        "ranking_snapshot",
        "ck_ranking_snapshot_window_days_positive",
        "window_days > 0",
        aliases=(
            "ck_ranking_snapshot_ck_ranking_snapshot_window_days_positive",
        ),
    )
    _ensure_unique_constraint(
        "ranking_snapshot",
        "uq_ranking_snapshot_work_window_time",
        ["ranking_name", "work_id", "window_days", "computed_at"],
    )
    _ensure_unique_constraint(
        "ranking_snapshot",
        "uq_ranking_snapshot_topic_window_time",
        ["ranking_name", "topic_id", "window_days", "computed_at"],
    )
    _ensure_unique_constraint(
        "ranking_snapshot",
        "uq_ranking_snapshot_method_window_time",
        ["ranking_name", "method_id", "window_days", "computed_at"],
    )


def _upgrade_offline() -> None:
    _validate_canonical_keys()
    _offline_replace_check_constraint(
        "work",
        "ck_work_canonical_key_approved_prefix",
        CANONICAL_KEY_CHECK,
        aliases=("canonical_key_approved_prefix",),
    )
    _offline_ensure_constraint(
        "paper_version",
        "ck_paper_version_version_number_positive",
        "CHECK (version_number IS NULL OR version_number >= 1)",
        aliases=(
            "ck_paper_version_ck_paper_version_version_number_positive",
        ),
    )

    _validate_source_ownership()
    op.execute(
        sa.text(
            """
            UPDATE source_record
            SET work_id = NULL
            WHERE paper_version_id IS NOT NULL
            """
        )
    )
    _offline_replace_check_constraint(
        "source_record",
        "ck_source_record_single_owner",
        "work_id IS NULL OR paper_version_id IS NULL",
        aliases=("ck_source_record_version_requires_work",),
    )

    op.execute(
        sa.text(
            """
            ALTER TABLE field_assertion
                DROP COLUMN IF EXISTS paper_version_id;
            ALTER TABLE field_assertion
                DROP COLUMN IF EXISTS work_id;
            """
        )
    )
    _offline_ensure_constraint(
        "field_assertion",
        "ck_field_assertion_confidence_range",
        "CHECK ("
        "confidence IS NULL OR (confidence >= 0 AND confidence <= 1)"
        ")",
        aliases=(
            "ck_field_assertion_ck_field_assertion_confidence_range",
        ),
    )

    op.execute(
        sa.text(
            """
            ALTER TABLE external_identifier
                DROP COLUMN IF EXISTS paper_version_id;
            ALTER TABLE external_identifier
                DROP COLUMN IF EXISTS source_record_id;
            """
        )
    )
    _offline_ensure_constraint(
        "metric_snapshot",
        "ck_metric_snapshot_single_target",
        f"CHECK ({METRIC_SINGLE_TARGET_CHECK})",
        aliases=(
            "ck_metric_snapshot_ck_metric_snapshot_single_target",
            "single_target",
        ),
    )
    _offline_ensure_constraint(
        "metric_snapshot",
        "ck_metric_snapshot_window_days_positive",
        "CHECK (window_days IS NULL OR window_days > 0)",
        aliases=(
            "ck_metric_snapshot_ck_metric_snapshot_window_days_positive",
        ),
    )

    op.execute(
        sa.text(
            """
            DO $$
            BEGIN
                IF EXISTS (
                    SELECT 1
                    FROM information_schema.columns
                    WHERE table_schema = current_schema()
                      AND table_name = 'ranking_snapshot'
                      AND column_name IN ('subject_type', 'subject_id')
                ) THEN
                    DELETE FROM ranking_snapshot;
                END IF;
            END
            $$;

            ALTER TABLE ranking_snapshot
                DROP COLUMN IF EXISTS subject_id;
            ALTER TABLE ranking_snapshot
                DROP COLUMN IF EXISTS subject_type;
            ALTER TABLE ranking_snapshot
                ADD COLUMN IF NOT EXISTS work_id UUID;
            ALTER TABLE ranking_snapshot
                ADD COLUMN IF NOT EXISTS topic_id UUID;
            ALTER TABLE ranking_snapshot
                ADD COLUMN IF NOT EXISTS method_id UUID;
            """
        )
    )
    _offline_ensure_constraint(
        "ranking_snapshot",
        "fk_ranking_snapshot_work_id_work",
        "FOREIGN KEY (work_id) REFERENCES work (id) ON DELETE CASCADE",
    )
    _offline_ensure_constraint(
        "ranking_snapshot",
        "fk_ranking_snapshot_topic_id_topic",
        "FOREIGN KEY (topic_id) REFERENCES topic (id) ON DELETE CASCADE",
    )
    _offline_ensure_constraint(
        "ranking_snapshot",
        "fk_ranking_snapshot_method_id_method",
        "FOREIGN KEY (method_id) REFERENCES method (id) ON DELETE CASCADE",
    )
    _offline_ensure_constraint(
        "ranking_snapshot",
        "ck_ranking_snapshot_exactly_one_subject",
        f"CHECK ({RANKING_EXACTLY_ONE_SUBJECT_CHECK})",
        aliases=("exactly_one_subject",),
    )
    _offline_ensure_constraint(
        "ranking_snapshot",
        "ck_ranking_snapshot_rank_position_positive",
        "CHECK (rank_position >= 1)",
        aliases=(
            "ck_ranking_snapshot_ck_ranking_snapshot_rank_position_positive",
        ),
    )
    _offline_ensure_constraint(
        "ranking_snapshot",
        "ck_ranking_snapshot_window_days_positive",
        "CHECK (window_days > 0)",
        aliases=(
            "ck_ranking_snapshot_ck_ranking_snapshot_window_days_positive",
        ),
    )
    _offline_ensure_constraint(
        "ranking_snapshot",
        "uq_ranking_snapshot_work_window_time",
        "UNIQUE (ranking_name, work_id, window_days, computed_at)",
    )
    _offline_ensure_constraint(
        "ranking_snapshot",
        "uq_ranking_snapshot_topic_window_time",
        "UNIQUE (ranking_name, topic_id, window_days, computed_at)",
    )
    _offline_ensure_constraint(
        "ranking_snapshot",
        "uq_ranking_snapshot_method_window_time",
        "UNIQUE (ranking_name, method_id, window_days, computed_at)",
    )


def upgrade() -> None:
    if context.is_offline_mode():
        _upgrade_offline()
    else:
        _upgrade_online()


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

    op.add_column(
        "external_identifier",
        sa.Column(
            "paper_version_id",
            postgresql.UUID(as_uuid=True),
            nullable=True,
        ),
    )
    op.add_column(
        "external_identifier",
        sa.Column(
            "source_record_id",
            postgresql.UUID(as_uuid=True),
            nullable=True,
        ),
    )
    op.create_foreign_key(
        "fk_external_identifier_paper_version_id_paper_version",
        "external_identifier",
        "paper_version",
        ["paper_version_id"],
        ["id"],
        ondelete="CASCADE",
    )
    op.create_foreign_key(
        "fk_external_identifier_source_record_id_source_record",
        "external_identifier",
        "source_record",
        ["source_record_id"],
        ["id"],
        ondelete="SET NULL",
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
