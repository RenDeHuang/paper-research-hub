"""Add indexes used by public search and projection queries.

Revision ID: 0008_search_api_indexes
Revises: 0007_repo_work_many_to_many
Create Date: 2026-07-16
"""

from collections.abc import Sequence

from alembic import op
import sqlalchemy as sa


revision: str = "0008_search_api_indexes"
down_revision: str | None = "0007_repo_work_many_to_many"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute("CREATE EXTENSION IF NOT EXISTS pg_trgm")
    op.create_index(
        "ix_work_title_trgm",
        "work",
        ["title"],
        unique=False,
        postgresql_using="gin",
        postgresql_ops={"title": "gin_trgm_ops"},
    )
    op.create_index(
        "ix_work_publication_date_canonical_key",
        "work",
        ["publication_date", "canonical_key"],
        unique=False,
    )
    op.execute(
        """
        CREATE INDEX ix_field_assertion_authors_trgm
        ON field_assertion
        USING gin ((CAST(value AS text)) gin_trgm_ops)
        WHERE field_name = 'authors'
        """
    )


def downgrade() -> None:
    op.drop_index(
        "ix_field_assertion_authors_trgm",
        table_name="field_assertion",
    )
    op.drop_index(
        "ix_work_publication_date_canonical_key",
        table_name="work",
    )
    op.drop_index(
        "ix_work_title_trgm",
        table_name="work",
        postgresql_using="gin",
    )
