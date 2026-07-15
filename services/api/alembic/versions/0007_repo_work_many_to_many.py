"""Make code repositories global with many-to-many work links.

Revision ID: 0007_repo_work_many_to_many
Revises: 0006_reset_work_projections
Create Date: 2026-07-16
"""

from collections.abc import Sequence

from alembic import op
import sqlalchemy as sa


revision: str = "0007_repo_work_many_to_many"
down_revision: str | None = "0006_reset_work_projections"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.create_table(
        "work_code_repository",
        sa.Column("work_id", sa.Uuid(), nullable=False),
        sa.Column("code_repository_id", sa.Uuid(), nullable=False),
        sa.ForeignKeyConstraint(
            ["work_id"],
            ["work.id"],
            name="fk_work_code_repository_work_id_work",
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["code_repository_id"],
            ["code_repository.id"],
            name=(
                "fk_work_code_repository_code_repository_id_"
                "code_repository"
            ),
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint(
            "work_id",
            "code_repository_id",
            name="pk_work_code_repository",
        ),
    )
    op.create_index(
        "ix_work_code_repository_code_repository_id",
        "work_code_repository",
        ["code_repository_id"],
        unique=False,
    )
    op.execute(
        """
        INSERT INTO work_code_repository (
            work_id,
            code_repository_id
        )
        SELECT work_id, id
        FROM code_repository
        """
    )
    op.drop_constraint(
        "fk_code_repository_work_id_work",
        "code_repository",
        type_="foreignkey",
    )
    op.drop_column("code_repository", "work_id")


def downgrade() -> None:
    op.execute(
        """
        DO $$
        BEGIN
            IF EXISTS (
                SELECT repository.id
                FROM code_repository AS repository
                LEFT JOIN work_code_repository AS association
                  ON association.code_repository_id = repository.id
                GROUP BY repository.id
                HAVING count(association.work_id) <> 1
            )
            THEN
                RAISE EXCEPTION
                    'cannot downgrade code repositories with '
                    'zero or multiple work associations';
            END IF;
        END;
        $$;
        """
    )
    op.add_column(
        "code_repository",
        sa.Column("work_id", sa.Uuid(), nullable=True),
    )
    op.execute(
        """
        UPDATE code_repository AS repository
        SET work_id = association.work_id
        FROM work_code_repository AS association
        WHERE association.code_repository_id = repository.id
        """
    )
    op.alter_column(
        "code_repository",
        "work_id",
        existing_type=sa.Uuid(),
        nullable=False,
    )
    op.create_foreign_key(
        "fk_code_repository_work_id_work",
        "code_repository",
        "work",
        ["work_id"],
        ["id"],
        ondelete="CASCADE",
    )
    op.drop_index(
        "ix_work_code_repository_code_repository_id",
        table_name="work_code_repository",
    )
    op.drop_table("work_code_repository")
