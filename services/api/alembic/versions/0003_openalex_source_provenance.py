"""Add source update provenance and immutable source snapshots.

Revision ID: 0003_openalex_source_provenance
Revises: 0002_enforce_canonical_integrity
Create Date: 2026-07-15
"""

from collections.abc import Sequence

from alembic import op
import sqlalchemy as sa


revision: str = "0003_openalex_source_provenance"
down_revision: str | None = "0002_enforce_canonical_integrity"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


IMMUTABLE_TRIGGER_FUNCTION = """
CREATE FUNCTION prevent_source_record_snapshot_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.source IS DISTINCT FROM OLD.source
       OR NEW.source_record_id IS DISTINCT FROM OLD.source_record_id
       OR NEW.retrieved_at IS DISTINCT FROM OLD.retrieved_at
       OR NEW.content_hash IS DISTINCT FROM OLD.content_hash
       OR NEW.raw_payload IS DISTINCT FROM OLD.raw_payload
       OR NEW.source_updated_at IS DISTINCT FROM OLD.source_updated_at
    THEN
        RAISE EXCEPTION
            'source_record snapshot columns are immutable'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$
"""


def upgrade() -> None:
    op.add_column(
        "source_record",
        sa.Column(
            "source_updated_at",
            sa.DateTime(timezone=True),
            nullable=True,
        ),
    )
    op.execute(IMMUTABLE_TRIGGER_FUNCTION)
    op.execute(
        """
        CREATE TRIGGER trg_source_record_snapshot_immutable
        BEFORE UPDATE OF
            source,
            source_record_id,
            retrieved_at,
            content_hash,
            raw_payload,
            source_updated_at
        ON source_record
        FOR EACH ROW
        EXECUTE FUNCTION prevent_source_record_snapshot_mutation()
        """
    )


def downgrade() -> None:
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_source_record_snapshot_immutable
        ON source_record
        """
    )
    op.execute(
        "DROP FUNCTION IF EXISTS prevent_source_record_snapshot_mutation()"
    )
    op.drop_column("source_record", "source_updated_at")
