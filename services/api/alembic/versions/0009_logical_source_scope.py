"""Associate exclusions with existing works across source snapshots.

Revision ID: 0009_logical_source_scope
Revises: 0008_search_api_indexes
Create Date: 2026-07-16
"""

from collections.abc import Sequence

from alembic import op


revision: str = "0009_logical_source_scope"
down_revision: str | None = "0008_search_api_indexes"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


NEW_CONSISTENCY_CHECK = """
(
    included
    AND work_id IS NOT NULL
    AND reason IS NULL
)
OR
(
    NOT included
    AND reason IS NOT NULL
    AND btrim(reason) <> ''
)
"""

OLD_CONSISTENCY_CHECK = """
(
    included
    AND work_id IS NOT NULL
    AND reason IS NULL
)
OR
(
    NOT included
    AND work_id IS NULL
    AND reason IS NOT NULL
    AND btrim(reason) <> ''
)
"""

WORK_MATCH_FUNCTION = """
CREATE OR REPLACE FUNCTION enforce_scope_assessment_work_match()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    source_work_id uuid;
    logical_work_id uuid;
    logical_owner_count bigint;
BEGIN
    SELECT work_id
    INTO source_work_id
    FROM source_record
    WHERE id = NEW.source_record_id;

    SELECT
        count(DISTINCT owner.work_id),
        min(owner.work_id::text)::uuid
    INTO logical_owner_count, logical_work_id
    FROM (
        SELECT owned_snapshot.work_id
        FROM source_record AS snapshot
        JOIN source_record AS owned_snapshot
          ON owned_snapshot.source = snapshot.source
         AND owned_snapshot.source_record_id =
             snapshot.source_record_id
         AND owned_snapshot.work_id IS NOT NULL
        WHERE snapshot.id = NEW.source_record_id

        UNION ALL

        SELECT assessment.work_id
        FROM source_record AS snapshot
        JOIN source_record AS assessed_snapshot
          ON assessed_snapshot.source = snapshot.source
         AND assessed_snapshot.source_record_id =
             snapshot.source_record_id
        JOIN scope_assessment AS assessment
          ON assessment.source_record_id = assessed_snapshot.id
         AND assessment.work_id IS NOT NULL
        WHERE snapshot.id = NEW.source_record_id
    ) AS owner;

    IF logical_owner_count > 1
    THEN
        RAISE EXCEPTION
            'logical source identity maps to multiple works'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.included
       AND source_work_id IS DISTINCT FROM NEW.work_id
    THEN
        RAISE EXCEPTION
            'scope assessment work must match source record work'
            USING ERRCODE = '23514';
    END IF;

    IF NOT NEW.included
       AND NEW.work_id IS NOT NULL
       AND source_work_id IS NOT NULL
       AND source_work_id IS DISTINCT FROM NEW.work_id
    THEN
        RAISE EXCEPTION
            'scope assessment work must match source record work'
            USING ERRCODE = '23514';
    END IF;

    IF NOT NEW.included
       AND NEW.work_id IS NOT NULL
       AND logical_owner_count = 1
       AND logical_work_id IS DISTINCT FROM NEW.work_id
    THEN
        RAISE EXCEPTION
            'scope assessment work must match logical source work'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
"""

OLD_WORK_MATCH_FUNCTION = """
CREATE OR REPLACE FUNCTION enforce_scope_assessment_work_match()
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

SOURCE_WORK_MATCH_FUNCTION = """
CREATE OR REPLACE FUNCTION prevent_source_record_scope_work_mismatch()
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
              AND assessment.work_id IS DISTINCT FROM NEW.work_id
       )
    THEN
        RAISE EXCEPTION
            'source record work must match included scope assessments'
            USING ERRCODE = '23514';
    END IF;

    IF NEW.work_id IS DISTINCT FROM OLD.work_id
       AND NEW.work_id IS NOT NULL
       AND EXISTS (
            SELECT 1
            FROM source_record AS assessed_snapshot
            JOIN scope_assessment AS assessment
              ON assessment.source_record_id = assessed_snapshot.id
             AND assessment.work_id IS NOT NULL
            WHERE assessed_snapshot.source = NEW.source
              AND assessed_snapshot.source_record_id =
                  NEW.source_record_id
              AND assessment.work_id IS DISTINCT FROM NEW.work_id
       )
    THEN
        RAISE EXCEPTION
            'source record work must match logical source assessments'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
"""

OLD_SOURCE_WORK_MATCH_FUNCTION = """
CREATE OR REPLACE FUNCTION prevent_source_record_scope_work_mismatch()
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

ASSOCIATION_AWARE_IMMUTABILITY_FUNCTION = """
CREATE OR REPLACE FUNCTION prevent_scope_assessment_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT OLD.included
       AND NOT NEW.included
       AND OLD.work_id IS NULL
       AND NEW.work_id IS NOT NULL
       AND NEW.id IS NOT DISTINCT FROM OLD.id
       AND NEW.source_record_id IS NOT DISTINCT FROM OLD.source_record_id
       AND NEW.rule_version IS NOT DISTINCT FROM OLD.rule_version
       AND NEW.reason IS NOT DISTINCT FROM OLD.reason
       AND NEW.evidence IS NOT DISTINCT FROM OLD.evidence
       AND NEW.evaluated_at IS NOT DISTINCT FROM OLD.evaluated_at
    THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION
        'scope assessments are immutable'
        USING ERRCODE = '55000';
END;
$$;
"""

OLD_IMMUTABILITY_FUNCTION = """
CREATE OR REPLACE FUNCTION prevent_scope_assessment_mutation()
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

IMMUTABLE_TRIGGER = """
CREATE TRIGGER trg_scope_assessment_immutable
BEFORE UPDATE
ON scope_assessment
FOR EACH ROW
EXECUTE FUNCTION prevent_scope_assessment_mutation()
"""


def upgrade() -> None:
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_scope_assessment_immutable
        ON scope_assessment
        """
    )
    op.drop_constraint(
        "ck_scope_assessment_inclusion_consistency",
        "scope_assessment",
        type_="check",
    )
    op.create_check_constraint(
        "ck_scope_assessment_inclusion_consistency",
        "scope_assessment",
        NEW_CONSISTENCY_CHECK,
    )
    op.execute(
        """
        DO $$
        BEGIN
            IF EXISTS (
                SELECT
                    source,
                    source_record_id
                FROM source_record
                WHERE work_id IS NOT NULL
                GROUP BY source, source_record_id
                HAVING count(DISTINCT work_id) > 1
            ) THEN
                RAISE EXCEPTION
                    'cannot associate scope exclusions: '
                    'logical source identity maps to multiple works';
            END IF;
        END;
        $$;
        """
    )
    op.execute(
        """
        UPDATE scope_assessment AS assessment
        SET work_id = logical_owner.work_id
        FROM source_record AS snapshot,
             (
                SELECT
                    source,
                    source_record_id,
                    min(work_id::text)::uuid AS work_id
                FROM source_record
                WHERE work_id IS NOT NULL
                GROUP BY source, source_record_id
             ) AS logical_owner
        WHERE snapshot.id = assessment.source_record_id
          AND logical_owner.source = snapshot.source
          AND logical_owner.source_record_id = snapshot.source_record_id
          AND NOT assessment.included
          AND assessment.work_id IS NULL
        """
    )
    op.execute(WORK_MATCH_FUNCTION)
    op.execute(SOURCE_WORK_MATCH_FUNCTION)
    op.create_index(
        "ix_scope_assessment_work_evaluated_at",
        "scope_assessment",
        ["work_id", "evaluated_at"],
        unique=False,
    )
    op.execute(ASSOCIATION_AWARE_IMMUTABILITY_FUNCTION)
    op.execute(IMMUTABLE_TRIGGER)


def downgrade() -> None:
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_scope_assessment_immutable
        ON scope_assessment
        """
    )
    op.execute(
        """
        UPDATE scope_assessment
        SET work_id = NULL
        WHERE NOT included
          AND work_id IS NOT NULL
        """
    )
    op.drop_index(
        "ix_scope_assessment_work_evaluated_at",
        table_name="scope_assessment",
    )
    op.drop_constraint(
        "ck_scope_assessment_inclusion_consistency",
        "scope_assessment",
        type_="check",
    )
    op.create_check_constraint(
        "ck_scope_assessment_inclusion_consistency",
        "scope_assessment",
        OLD_CONSISTENCY_CHECK,
    )
    op.execute(OLD_WORK_MATCH_FUNCTION)
    op.execute(OLD_SOURCE_WORK_MATCH_FUNCTION)
    op.execute(OLD_IMMUTABILITY_FUNCTION)
    op.execute(IMMUTABLE_TRIGGER)
