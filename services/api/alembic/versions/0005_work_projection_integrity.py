"""Add ordered work projections and cascading scope ownership.

Revision ID: 0005_work_projection_integrity
Revises: 0004_versioned_scope_assessment
Create Date: 2026-07-15
"""

from collections.abc import Sequence

from alembic import op
import sqlalchemy as sa


revision: str = "0005_work_projection_integrity"
down_revision: str | None = "0004_versioned_scope_assessment"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.add_column(
        "work",
        sa.Column(
            "projection_source",
            sa.String(length=64),
            nullable=True,
        ),
    )
    op.add_column(
        "work",
        sa.Column(
            "projection_source_record_id",
            sa.String(length=255),
            nullable=True,
        ),
    )
    op.add_column(
        "work",
        sa.Column(
            "projection_source_updated_at",
            sa.DateTime(timezone=True),
            nullable=True,
        ),
    )
    op.execute(
        """
        WITH latest_source AS (
            SELECT DISTINCT ON (work_id)
                work_id,
                source,
                source_record_id,
                source_updated_at
            FROM source_record
            WHERE work_id IS NOT NULL
            ORDER BY
                work_id,
                source_updated_at DESC NULLS LAST,
                retrieved_at DESC,
                id DESC
        )
        UPDATE work AS target
        SET
            projection_source = latest_source.source,
            projection_source_record_id =
                latest_source.source_record_id,
            projection_source_updated_at =
                latest_source.source_updated_at
        FROM latest_source
        WHERE target.id = latest_source.work_id
        """
    )

    op.drop_constraint(
        "fk_scope_assessment_work_id_work",
        "scope_assessment",
        type_="foreignkey",
    )
    op.create_foreign_key(
        "fk_scope_assessment_work_id_work",
        "scope_assessment",
        "work",
        ["work_id"],
        ["id"],
        ondelete="CASCADE",
    )

    op.execute(
        """
        CREATE OR REPLACE FUNCTION
            prevent_source_record_scope_work_mismatch()
        RETURNS trigger
        LANGUAGE plpgsql
        AS $$
        BEGIN
            IF NEW.work_id IS DISTINCT FROM OLD.work_id
               AND EXISTS (
                    SELECT 1
                    FROM scope_assessment AS assessment
                    JOIN work AS assessment_work
                      ON assessment_work.id = assessment.work_id
                    WHERE assessment.source_record_id = OLD.id
                      AND assessment.included
                      AND assessment.work_id
                          IS DISTINCT FROM NEW.work_id
               )
            THEN
                RAISE EXCEPTION
                    'source record work must match included scope assessments'
                    USING ERRCODE = '23514';
            END IF;
            RETURN NEW;
        END;
        $$;
        """
    )


def downgrade() -> None:
    op.execute(
        """
        CREATE OR REPLACE FUNCTION
            prevent_source_record_scope_work_mismatch()
        RETURNS trigger
        LANGUAGE plpgsql
        AS $$
        BEGIN
            IF NEW.work_id IS DISTINCT FROM OLD.work_id
               AND EXISTS (
                    SELECT 1
                    FROM scope_assessment AS assessment
                    WHERE assessment.source_record_id = OLD.id
                      AND assessment.included
                      AND assessment.work_id
                          IS DISTINCT FROM NEW.work_id
               )
            THEN
                RAISE EXCEPTION
                    'source record work must match included scope assessments'
                    USING ERRCODE = '23514';
            END IF;
            RETURN NEW;
        END;
        $$;
        """
    )

    op.drop_constraint(
        "fk_scope_assessment_work_id_work",
        "scope_assessment",
        type_="foreignkey",
    )
    op.create_foreign_key(
        "fk_scope_assessment_work_id_work",
        "scope_assessment",
        "work",
        ["work_id"],
        ["id"],
    )
    op.drop_column("work", "projection_source_updated_at")
    op.drop_column("work", "projection_source_record_id")
    op.drop_column("work", "projection_source")
