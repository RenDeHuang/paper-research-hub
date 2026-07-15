"""Reset projection metadata that 0005 could not verify.

Revision ID: 0006_reset_work_projections
Revises: 0005_work_projection_integrity
Create Date: 2026-07-16
"""

from collections.abc import Sequence

from alembic import op


revision: str = "0006_reset_work_projections"
down_revision: str | None = "0005_work_projection_integrity"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.execute(
        """
        UPDATE work
        SET
            projection_source = NULL,
            projection_source_record_id = NULL,
            projection_source_updated_at = NULL
        WHERE projection_source IS NOT NULL
           OR projection_source_record_id IS NOT NULL
           OR projection_source_updated_at IS NOT NULL
        """
    )


def downgrade() -> None:
    # Recreating 0005's metadata-only backfill would restore an unverified
    # projection claim. Keeping these nullable fields empty is schema
    # compatible with 0005 and preserves the corrected audit semantics.
    pass
