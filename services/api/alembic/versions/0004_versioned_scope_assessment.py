"""Add versioned scope assessments.

Revision ID: 0004_versioned_scope_assessment
Revises: 0003_openalex_source_provenance
Create Date: 2026-07-15
"""

from collections.abc import Sequence

from alembic import op
import sqlalchemy as sa
from sqlalchemy.dialects import postgresql


revision: str = "0004_versioned_scope_assessment"
down_revision: str | None = "0003_openalex_source_provenance"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    op.create_table(
        "scope_assessment",
        sa.Column(
            "id",
            postgresql.UUID(as_uuid=True),
            nullable=False,
        ),
        sa.Column(
            "source_record_id",
            postgresql.UUID(as_uuid=True),
            nullable=False,
        ),
        sa.Column(
            "rule_version",
            sa.String(length=128),
            nullable=False,
        ),
        sa.Column("included", sa.Boolean(), nullable=False),
        sa.Column("reason", sa.Text(), nullable=True),
        sa.Column(
            "evidence",
            postgresql.JSONB(astext_type=sa.Text()),
            nullable=False,
        ),
        sa.Column(
            "evaluated_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("now()"),
            nullable=False,
        ),
        sa.Column(
            "work_id",
            postgresql.UUID(as_uuid=True),
            nullable=True,
        ),
        sa.CheckConstraint(
            "("
            "included AND work_id IS NOT NULL AND reason IS NULL"
            ") OR ("
            "NOT included AND work_id IS NULL "
            "AND reason IS NOT NULL AND btrim(reason) <> ''"
            ")",
            name="ck_scope_assessment_inclusion_consistency",
        ),
        sa.CheckConstraint(
            "btrim(rule_version) <> ''",
            name="ck_scope_assessment_rule_version_nonempty",
        ),
        sa.CheckConstraint(
            "jsonb_typeof(evidence) = 'array'",
            name="ck_scope_assessment_evidence_array",
        ),
        sa.ForeignKeyConstraint(
            ["source_record_id"],
            ["source_record.id"],
            name=(
                "fk_scope_assessment_source_record_id_source_record"
            ),
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["work_id"],
            ["work.id"],
            name="fk_scope_assessment_work_id_work",
        ),
        sa.PrimaryKeyConstraint(
            "id",
            name="pk_scope_assessment",
        ),
        sa.UniqueConstraint(
            "source_record_id",
            "rule_version",
            name="uq_scope_assessment_record_rule",
        ),
    )
    op.create_index(
        "ix_scope_assessment_record_evaluated_at",
        "scope_assessment",
        ["source_record_id", "evaluated_at"],
        unique=False,
    )

    op.execute(
        """
        DO $$
        BEGIN
            IF EXISTS (
                SELECT 1
                FROM field_assertion AS assertion
                JOIN source_record AS source
                  ON source.id = assertion.source_record_id
                WHERE assertion.field_name = 'scope'
                  AND (
                    jsonb_typeof(assertion.value) IS DISTINCT FROM 'object'
                    OR jsonb_typeof(
                        assertion.value -> 'included'
                    ) IS DISTINCT FROM 'boolean'
                    OR NULLIF(
                        btrim(assertion.value ->> 'rule_version'),
                        ''
                    ) IS NULL
                    OR jsonb_typeof(
                        assertion.value -> 'evidence'
                    ) IS DISTINCT FROM 'array'
                    OR (
                        (assertion.value ->> 'included')::boolean
                        AND source.work_id IS NULL
                    )
                    OR (
                        (assertion.value ->> 'included')::boolean
                        AND assertion.value ->> 'reason' IS NOT NULL
                    )
                    OR (
                        NOT (assertion.value ->> 'included')::boolean
                        AND NULLIF(
                            btrim(assertion.value ->> 'reason'),
                            ''
                        ) IS NULL
                    )
                  )
            ) THEN
                RAISE EXCEPTION
                    'Cannot backfill invalid legacy scope assertions';
            END IF;

            IF EXISTS (
                SELECT 1
                FROM field_assertion
                WHERE field_name = 'scope'
                GROUP BY
                    source_record_id,
                    value ->> 'rule_version'
                HAVING count(*) > 1
            ) THEN
                RAISE EXCEPTION
                    'Cannot backfill duplicate legacy scope rule versions';
            END IF;
        END;
        $$;
        """
    )
    op.execute(
        """
        INSERT INTO scope_assessment (
            id,
            source_record_id,
            rule_version,
            included,
            reason,
            evidence,
            evaluated_at,
            work_id
        )
        SELECT
            assertion.id,
            assertion.source_record_id,
            assertion.value ->> 'rule_version',
            (assertion.value ->> 'included')::boolean,
            assertion.value ->> 'reason',
            assertion.value -> 'evidence',
            assertion.created_at,
            CASE
                WHEN (assertion.value ->> 'included')::boolean
                THEN source.work_id
                ELSE NULL
            END
        FROM field_assertion AS assertion
        JOIN source_record AS source
          ON source.id = assertion.source_record_id
        WHERE assertion.field_name = 'scope'
        """
    )

    op.execute(
        """
        CREATE FUNCTION enforce_scope_assessment_work_match()
        RETURNS trigger
        LANGUAGE plpgsql
        AS $$
        DECLARE
            source_work_id uuid;
        BEGIN
            SELECT work_id
            INTO source_work_id
            FROM source_record
            WHERE id = NEW.source_record_id;

            IF NEW.included
               AND source_work_id IS DISTINCT FROM NEW.work_id
            THEN
                RAISE EXCEPTION
                    'scope assessment work must match source record work'
                    USING ERRCODE = '23514';
            END IF;
            RETURN NEW;
        END;
        $$;
        """
    )
    op.execute(
        """
        CREATE TRIGGER trg_scope_assessment_work_match
        BEFORE INSERT OR UPDATE OF
            source_record_id,
            included,
            work_id
        ON scope_assessment
        FOR EACH ROW
        EXECUTE FUNCTION enforce_scope_assessment_work_match()
        """
    )
    op.execute(
        """
        CREATE FUNCTION prevent_source_record_scope_work_mismatch()
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
                      AND assessment.work_id IS DISTINCT FROM NEW.work_id
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
    op.execute(
        """
        CREATE TRIGGER trg_source_record_scope_work_match
        BEFORE UPDATE OF work_id
        ON source_record
        FOR EACH ROW
        EXECUTE FUNCTION prevent_source_record_scope_work_mismatch()
        """
    )
    op.execute(
        """
        CREATE FUNCTION prevent_scope_assessment_mutation()
        RETURNS trigger
        LANGUAGE plpgsql
        AS $$
        BEGIN
            RAISE EXCEPTION
                'scope assessments are immutable'
                USING ERRCODE = '55000';
        END;
        $$;
        """
    )
    op.execute(
        """
        CREATE TRIGGER trg_scope_assessment_immutable
        BEFORE UPDATE
        ON scope_assessment
        FOR EACH ROW
        EXECUTE FUNCTION prevent_scope_assessment_mutation()
        """
    )


def downgrade() -> None:
    op.execute(
        """
        DO $$
        BEGIN
            IF EXISTS (
                SELECT 1
                FROM scope_assessment
                WHERE length(rule_version) > 64
            ) THEN
                RAISE EXCEPTION
                    'Cannot backfill scope rule_version longer than 64 characters';
            END IF;
        END;
        $$;
        """
    )
    op.execute(
        """
        INSERT INTO field_assertion (
            id,
            source_record_id,
            field_name,
            value,
            parser_version,
            confidence,
            source,
            source_url,
            retrieved_at,
            source_license,
            content_license,
            status,
            created_at,
            updated_at
        )
        SELECT
            assessment.id,
            assessment.source_record_id,
            'scope',
            jsonb_build_object(
                'included', assessment.included,
                'rule_version', assessment.rule_version,
                'reason', assessment.reason,
                'evidence', assessment.evidence
            ),
            assessment.rule_version,
            NULL,
            source.source,
            source.source_url,
            source.retrieved_at,
            source.source_license,
            source.content_license,
            source.status,
            assessment.evaluated_at,
            assessment.evaluated_at
        FROM scope_assessment AS assessment
        JOIN source_record AS source
          ON source.id = assessment.source_record_id
        WHERE NOT EXISTS (
            SELECT 1
            FROM field_assertion AS assertion
            WHERE assertion.source_record_id = assessment.source_record_id
              AND assertion.field_name = 'scope'
              AND assertion.value ->> 'rule_version'
                  = assessment.rule_version
        )
        """
    )
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_source_record_scope_work_match
        ON source_record
        """
    )
    op.execute(
        """
        DROP FUNCTION IF EXISTS
            prevent_source_record_scope_work_mismatch()
        """
    )
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_scope_assessment_immutable
        ON scope_assessment
        """
    )
    op.execute(
        """
        DROP FUNCTION IF EXISTS
            prevent_scope_assessment_mutation()
        """
    )
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_scope_assessment_work_match
        ON scope_assessment
        """
    )
    op.execute(
        """
        DROP FUNCTION IF EXISTS
            enforce_scope_assessment_work_match()
        """
    )
    op.drop_index(
        "ix_scope_assessment_record_evaluated_at",
        table_name="scope_assessment",
    )
    op.drop_table("scope_assessment")
