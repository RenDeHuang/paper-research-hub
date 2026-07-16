"""Use exact projection snapshots and audit delayed scope ownership.

Revision ID: 0010_exact_projection_scope
Revises: 0009_logical_source_scope
Create Date: 2026-07-16
"""

from collections.abc import Sequence

from alembic import op
import sqlalchemy as sa
from sqlalchemy.dialects import postgresql


revision: str = "0010_exact_projection_scope"
down_revision: str | None = "0009_logical_source_scope"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


ASSOCIATION_AWARE_IMMUTABILITY_FUNCTION = """
CREATE OR REPLACE FUNCTION prevent_scope_assessment_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    logical_source text;
    logical_source_record_id text;
BEGIN
    IF NOT OLD.included
       AND NOT NEW.included
       AND OLD.work_id IS NULL
       AND NEW.work_id IS NOT NULL
       AND OLD.work_linked_at IS NULL
       AND OLD.work_link_reason IS NULL
       AND NEW.work_linked_at IS NOT NULL
       AND NEW.work_link_reason IS NOT NULL
       AND btrim(NEW.work_link_reason) <> ''
       AND NEW.id IS NOT DISTINCT FROM OLD.id
       AND NEW.source_record_id IS NOT DISTINCT FROM OLD.source_record_id
       AND NEW.rule_version IS NOT DISTINCT FROM OLD.rule_version
       AND NEW.reason IS NOT DISTINCT FROM OLD.reason
       AND NEW.evidence IS NOT DISTINCT FROM OLD.evidence
       AND NEW.evaluated_at IS NOT DISTINCT FROM OLD.evaluated_at
    THEN
        SELECT snapshot.source, snapshot.source_record_id
        INTO logical_source, logical_source_record_id
        FROM source_record AS snapshot
        WHERE snapshot.id = NEW.source_record_id;

        IF FOUND THEN
            PERFORM pg_advisory_xact_lock(
                hashtextextended(
                    logical_source || chr(31) || logical_source_record_id,
                    0
                )
            );
        END IF;

        RETURN NEW;
    END IF;

    RAISE EXCEPTION
        'scope assessments are immutable'
        USING ERRCODE = '55000';
END;
$$;
"""


OLD_ASSOCIATION_AWARE_IMMUTABILITY_FUNCTION = """
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


LOGICAL_SOURCE_LINK_FUNCTION = """
CREATE OR REPLACE FUNCTION link_logical_source_scope_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.work_id IS NULL THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE'
       AND NEW.work_id IS NOT DISTINCT FROM OLD.work_id
    THEN
        RETURN NEW;
    END IF;

    PERFORM pg_advisory_xact_lock(
        hashtextextended(
            NEW.source || chr(31) || NEW.source_record_id,
            0
        )
    );

    UPDATE scope_assessment AS assessment
    SET
        work_id = NEW.work_id,
        work_linked_at = clock_timestamp(),
        work_link_reason = 'logical_source_owner_trigger'
    FROM source_record AS assessed_snapshot
    WHERE assessment.source_record_id = assessed_snapshot.id
      AND assessed_snapshot.source = NEW.source
      AND assessed_snapshot.source_record_id = NEW.source_record_id
      AND NOT assessment.included
      AND assessment.work_id IS NULL;

    RETURN NEW;
END;
$$;
"""


LATE_SCOPE_OWNER_FUNCTION = """
CREATE OR REPLACE FUNCTION attach_scope_assessment_logical_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    logical_work_id uuid;
    logical_owner_count bigint;
    logical_source text;
    logical_source_record_id text;
BEGIN
    SELECT snapshot.source, snapshot.source_record_id
    INTO logical_source, logical_source_record_id
    FROM source_record AS snapshot
    WHERE snapshot.id = NEW.source_record_id;

    IF FOUND THEN
        PERFORM pg_advisory_xact_lock(
            hashtextextended(
                logical_source || chr(31) || logical_source_record_id,
                0
            )
        );
    END IF;

    IF NEW.included THEN
        RETURN NEW;
    END IF;

    IF NEW.work_id IS NOT NULL THEN
        IF NEW.work_linked_at IS NULL
           AND NEW.work_link_reason IS NULL
        THEN
            NEW.work_linked_at = clock_timestamp();
            NEW.work_link_reason = 'explicit_scope_owner_insert';
        END IF;
        RETURN NEW;
    END IF;

    SELECT
        count(DISTINCT owned_snapshot.work_id),
        min(owned_snapshot.work_id::text)::uuid
    INTO logical_owner_count, logical_work_id
    FROM source_record AS snapshot
    JOIN source_record AS owned_snapshot
      ON owned_snapshot.source = snapshot.source
     AND owned_snapshot.source_record_id = snapshot.source_record_id
     AND owned_snapshot.work_id IS NOT NULL
    WHERE snapshot.id = NEW.source_record_id;

    IF logical_owner_count > 1 THEN
        RAISE EXCEPTION
            'logical source identity maps to multiple works'
            USING ERRCODE = '23514';
    END IF;

    IF logical_owner_count = 1 THEN
        NEW.work_id = logical_work_id;
        NEW.work_linked_at = clock_timestamp();
        NEW.work_link_reason = 'logical_source_owner_trigger';
    END IF;
    RETURN NEW;
END;
$$;
"""


LOGICAL_SOURCE_OWNER_CONSISTENCY_FUNCTION = """
CREATE OR REPLACE FUNCTION enforce_logical_source_owner_consistency()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    owner_is_being_deleted boolean := false;
BEGIN
    IF TG_OP = 'UPDATE'
       AND OLD.work_id IS NOT NULL
       AND NEW.work_id IS NULL
    THEN
        owner_is_being_deleted = NOT EXISTS (
            SELECT 1
            FROM work
            WHERE work.id = OLD.work_id
        );
    END IF;

    IF NEW.work_id IS NOT NULL THEN
        PERFORM pg_advisory_xact_lock(
            hashtextextended(
                NEW.source || chr(31) || NEW.source_record_id,
                0
            )
        );

        IF EXISTS (
            SELECT 1
            FROM (
                SELECT existing.work_id
                FROM source_record AS existing
                WHERE existing.source = NEW.source
                  AND existing.source_record_id = NEW.source_record_id
                  AND existing.work_id IS NOT NULL
                  AND existing.id IS DISTINCT FROM NEW.id

                UNION ALL

                SELECT assessment.work_id
                FROM source_record AS assessed_snapshot
                JOIN scope_assessment AS assessment
                  ON assessment.source_record_id = assessed_snapshot.id
                 AND assessment.work_id IS NOT NULL
                WHERE TG_OP = 'INSERT'
                  AND assessed_snapshot.source = NEW.source
                  AND assessed_snapshot.source_record_id =
                      NEW.source_record_id
            ) AS logical_owner
            WHERE logical_owner.work_id IS DISTINCT FROM NEW.work_id
        )
        THEN
            RAISE EXCEPTION
                'logical source identity maps to multiple works'
                USING ERRCODE = '23514';
        END IF;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM work AS projected_work
        WHERE projected_work.projection_source_record_id = NEW.id
          AND projected_work.id IS DISTINCT FROM NEW.work_id
    )
    THEN
        RAISE EXCEPTION
            'projection source record must belong to projected work'
            USING ERRCODE = '23514';
    END IF;

    IF NOT owner_is_being_deleted
       AND EXISTS (
        SELECT 1
        FROM metric_snapshot AS metric
        WHERE metric.source_record_id = NEW.id
          AND (
              metric.work_id IS DISTINCT FROM NEW.work_id
              OR metric.source IS DISTINCT FROM NEW.source
              OR (
                  metric.metric_name = 'citation_count'
                  AND metric.measured_at IS DISTINCT FROM
                      NEW.source_updated_at
              )
          )
    )
    THEN
        RAISE EXCEPTION
            'metric source record provenance is inconsistent'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
"""


PROJECTION_OWNER_FUNCTION = """
CREATE OR REPLACE FUNCTION enforce_work_projection_source_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.projection_source_record_id IS NOT NULL
       AND NOT EXISTS (
            SELECT 1
            FROM source_record AS source
            WHERE source.id = NEW.projection_source_record_id
              AND source.work_id = NEW.id
       )
    THEN
        RAISE EXCEPTION
            'projection source record must belong to projected work'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
"""


METRIC_SOURCE_OWNER_FUNCTION = """
CREATE OR REPLACE FUNCTION enforce_metric_source_record_owner()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    source_work_id uuid;
    source_name text;
    source_measured_at timestamptz;
BEGIN
    IF NEW.source_record_id IS NULL THEN
        RETURN NEW;
    END IF;

    SELECT
        source.work_id,
        source.source,
        source.source_updated_at
    INTO
        source_work_id,
        source_name,
        source_measured_at
    FROM source_record AS source
    WHERE source.id = NEW.source_record_id;

    IF NOT FOUND
       OR NEW.work_id IS DISTINCT FROM source_work_id
       OR NEW.source IS DISTINCT FROM source_name
       OR (
            NEW.metric_name = 'citation_count'
            AND NEW.measured_at IS DISTINCT FROM source_measured_at
       )
    THEN
        RAISE EXCEPTION
            'metric source record provenance is inconsistent'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
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


LOGICAL_SOURCE_LINK_TRIGGER = """
CREATE TRIGGER trg_source_record_link_logical_scope_owner
AFTER INSERT OR UPDATE OF work_id
ON source_record
FOR EACH ROW
EXECUTE FUNCTION link_logical_source_scope_owner()
"""


LATE_SCOPE_OWNER_TRIGGER = """
CREATE TRIGGER trg_scope_assessment_link_logical_owner
BEFORE INSERT
ON scope_assessment
FOR EACH ROW
EXECUTE FUNCTION attach_scope_assessment_logical_owner()
"""


LOGICAL_SOURCE_OWNER_CONSISTENCY_TRIGGER = """
CREATE TRIGGER trg_source_record_logical_owner_consistency
BEFORE INSERT OR UPDATE OF work_id, source, source_record_id
ON source_record
FOR EACH ROW
EXECUTE FUNCTION enforce_logical_source_owner_consistency()
"""


PROJECTION_OWNER_TRIGGER = """
CREATE CONSTRAINT TRIGGER trg_work_projection_source_owner
AFTER INSERT OR UPDATE OF projection_source_record_id
ON work
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_work_projection_source_owner()
"""


METRIC_SOURCE_OWNER_TRIGGER = """
CREATE CONSTRAINT TRIGGER trg_metric_source_record_owner
AFTER INSERT OR UPDATE OF
    source_record_id,
    work_id,
    metric_name,
    measured_at,
    source
ON metric_snapshot
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION enforce_metric_source_record_owner()
"""


MIGRATION_DATA_LOCK = """
LOCK TABLE
    work,
    source_record,
    scope_assessment,
    field_assertion,
    external_identifier,
    metric_snapshot,
    topic,
    method,
    dataset,
    benchmark,
    code_repository,
    work_topic,
    work_method,
    work_dataset,
    work_benchmark,
    work_code_repository
IN SHARE MODE
"""


def upgrade() -> None:
    op.execute(MIGRATION_DATA_LOCK)
    op.execute(
        """
        DO $$
        BEGIN
            IF EXISTS (
                SELECT 1
                FROM (
                    SELECT
                        source.source,
                        source.source_record_id,
                        source.work_id
                    FROM source_record AS source
                    WHERE source.work_id IS NOT NULL

                    UNION ALL

                    SELECT
                        source.source,
                        source.source_record_id,
                        assessment.work_id
                    FROM source_record AS source
                    JOIN scope_assessment AS assessment
                      ON assessment.source_record_id = source.id
                     AND assessment.work_id IS NOT NULL
                ) AS logical_owner
                GROUP BY
                    logical_owner.source,
                    logical_owner.source_record_id
                HAVING count(DISTINCT logical_owner.work_id) > 1
            )
            THEN
                RAISE EXCEPTION
                    'cannot upgrade exact projection scope: '
                    'logical source identity maps to multiple works'
                    USING ERRCODE = '23514';
            END IF;
        END;
        $$;
        """
    )
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_scope_assessment_immutable
        ON scope_assessment
        """
    )
    op.add_column(
        "scope_assessment",
        sa.Column(
            "work_linked_at",
            sa.DateTime(timezone=True),
            nullable=True,
        ),
    )
    op.add_column(
        "scope_assessment",
        sa.Column("work_link_reason", sa.String(length=64), nullable=True),
    )
    op.execute(
        """
        UPDATE scope_assessment
        SET
            work_linked_at = evaluated_at,
            work_link_reason = 'migration_logical_source_owner'
        WHERE NOT included
          AND work_id IS NOT NULL
        """
    )
    op.create_check_constraint(
        "ck_scope_assessment_work_link_metadata",
        "scope_assessment",
        """
        (
            included
            AND work_linked_at IS NULL
            AND work_link_reason IS NULL
        )
        OR
        (
            NOT included
            AND work_id IS NULL
            AND work_linked_at IS NULL
            AND work_link_reason IS NULL
        )
        OR
        (
            NOT included
            AND work_id IS NOT NULL
            AND work_linked_at IS NOT NULL
            AND btrim(work_link_reason) <> ''
        )
        """,
    )
    op.execute(ASSOCIATION_AWARE_IMMUTABILITY_FUNCTION)
    op.execute(IMMUTABLE_TRIGGER)
    op.execute(LOGICAL_SOURCE_LINK_FUNCTION)
    op.execute(LATE_SCOPE_OWNER_FUNCTION)
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_source_record_link_logical_scope_owner
        ON source_record
        """
    )
    op.execute(LOGICAL_SOURCE_LINK_TRIGGER)
    op.execute(LATE_SCOPE_OWNER_TRIGGER)

    op.execute(
        """
        DO $$
        BEGIN
            IF EXISTS (
                SELECT 1
                FROM work
                WHERE (
                    projection_source_record_id IS NULL
                    AND (
                        projection_source IS NOT NULL
                        OR projection_source_updated_at IS NOT NULL
                    )
                )
                OR (
                    projection_source_record_id IS NOT NULL
                    AND projection_source IS NULL
                )
            )
            THEN
                RAISE EXCEPTION
                    'cannot map legacy work projection: '
                    'projection tuple is inconsistent'
                    USING ERRCODE = '55000';
            END IF;
        END;
        $$;
        """
    )
    op.add_column(
        "work",
        sa.Column(
            "projection_source_record_uuid",
            sa.Uuid(),
            nullable=True,
        ),
    )
    op.execute(
        """
        WITH ranked_projection AS (
            SELECT
                work.id AS work_id,
                source.id AS source_record_id,
                source.source,
                source.source_updated_at,
                row_number() OVER (
                    PARTITION BY work.id
                    ORDER BY
                        source.source_updated_at DESC NULLS LAST,
                        source.retrieved_at DESC,
                        source.id DESC
                ) AS position
            FROM work
            JOIN source_record AS source
              ON source.work_id = work.id
             AND source.source = work.projection_source
             AND source.source_record_id =
                 work.projection_source_record_id
             AND source.source_updated_at IS NOT DISTINCT FROM
                 work.projection_source_updated_at
            WHERE work.projection_source_record_id IS NOT NULL
              AND EXISTS (
                  SELECT 1
                  FROM scope_assessment AS assessment
                  WHERE assessment.source_record_id = source.id
                    AND assessment.work_id = work.id
                    AND assessment.included
              )
        )
        UPDATE work
        SET
            projection_source_record_uuid =
                ranked_projection.source_record_id,
            projection_source = ranked_projection.source,
            projection_source_updated_at =
                ranked_projection.source_updated_at
        FROM ranked_projection
        WHERE ranked_projection.work_id = work.id
          AND ranked_projection.position = 1
        """
    )
    op.execute(
        """
        DO $$
        BEGIN
            IF EXISTS (
                SELECT 1
                FROM work
                WHERE projection_source_record_id IS NOT NULL
                  AND projection_source_record_uuid IS NULL
            )
            THEN
                RAISE EXCEPTION
                    'cannot map legacy work projection: '
                    'no included source snapshot matches'
                    USING ERRCODE = '55000';
            END IF;
        END;
        $$;
        """
    )
    op.drop_column("work", "projection_source_record_id")
    op.alter_column(
        "work",
        "projection_source_record_uuid",
        new_column_name="projection_source_record_id",
    )
    op.create_foreign_key(
        "fk_work_projection_source_record_id_source_record",
        "work",
        "source_record",
        ["projection_source_record_id"],
        ["id"],
        ondelete="RESTRICT",
    )
    op.create_index(
        "ix_work_projection_source_record_id",
        "work",
        ["projection_source_record_id"],
        unique=False,
    )
    op.create_index(
        "ix_work_created_at",
        "work",
        ["created_at"],
        unique=False,
    )
    op.create_index(
        "ix_work_canonical_key_trgm",
        "work",
        ["canonical_key"],
        unique=False,
        postgresql_using="gin",
        postgresql_ops={"canonical_key": "gin_trgm_ops"},
    )
    op.create_index(
        "ix_external_identifier_normalized_value_trgm",
        "external_identifier",
        ["normalized_value"],
        unique=False,
        postgresql_using="gin",
        postgresql_ops={"normalized_value": "gin_trgm_ops"},
    )
    op.create_index(
        "ix_external_identifier_raw_value_trgm",
        "external_identifier",
        ["raw_value"],
        unique=False,
        postgresql_using="gin",
        postgresql_ops={"raw_value": "gin_trgm_ops"},
    )
    op.alter_column(
        "work",
        "created_at",
        server_default=sa.text("clock_timestamp()"),
    )
    op.execute(
        """
        CREATE INDEX ix_work_publication_date_desc_canonical_key
        ON work (
            publication_date DESC NULLS LAST,
            canonical_key ASC
        )
        """
    )
    op.create_index(
        "ix_metric_snapshot_repo_metric_time",
        "metric_snapshot",
        ["code_repository_id", "metric_name", "measured_at"],
        unique=False,
    )
    op.add_column(
        "metric_snapshot",
        sa.Column(
            "source_record_id",
            sa.Uuid(),
            nullable=True,
        ),
    )
    op.execute(
        """
        DO $$
        BEGIN
            IF EXISTS (
                SELECT 1
                FROM metric_snapshot
                WHERE metadata ? 'source_record_id'
                  AND (
                      jsonb_typeof(
                          metadata -> 'source_record_id'
                      ) IS DISTINCT FROM 'string'
                      OR NOT pg_input_is_valid(
                          metadata ->> 'source_record_id',
                          'uuid'
                      )
                  )
            )
            THEN
                RAISE EXCEPTION
                    'cannot upgrade metric provenance: '
                    'invalid source record UUID'
                    USING ERRCODE = '23514';
            END IF;
        END;
        $$;
        """
    )
    op.execute(
        """
        UPDATE metric_snapshot
        SET source_record_id =
            (metadata ->> 'source_record_id')::uuid
        WHERE metadata ? 'source_record_id'
        """
    )
    op.execute(
        """
        DO $$
        BEGIN
            IF EXISTS (
                SELECT 1
                FROM metric_snapshot AS metric
                LEFT JOIN source_record AS source
                  ON source.id = metric.source_record_id
                WHERE metric.source_record_id IS NOT NULL
                  AND (
                      source.id IS NULL
                      OR metric.work_id IS DISTINCT FROM source.work_id
                      OR metric.source IS DISTINCT FROM source.source
                      OR (
                          metric.metric_name = 'citation_count'
                          AND metric.measured_at IS DISTINCT FROM
                              source.source_updated_at
                      )
                  )
            )
            THEN
                RAISE EXCEPTION
                    'cannot upgrade metric provenance: '
                    'source record ownership is inconsistent'
                    USING ERRCODE = '23514';
            END IF;
        END;
        $$;
        """
    )
    op.execute(
        """
        WITH ranked_metric_source AS (
            SELECT DISTINCT ON (metric.id)
                metric.id AS metric_id,
                source.id AS source_record_id,
                source.content_hash,
                source.source_url,
                source.retrieved_at,
                source.source_license,
                source.content_license,
                assertion.value AS metric_value
            FROM metric_snapshot AS metric
            JOIN source_record AS original_source
              ON original_source.id = metric.source_record_id
            JOIN source_record AS source
              ON source.work_id = metric.work_id
             AND source.source = metric.source
             AND source.source_updated_at IS NOT DISTINCT FROM
                 metric.measured_at
            JOIN LATERAL (
                SELECT candidate.value
                FROM field_assertion AS candidate
                WHERE candidate.source_record_id = source.id
                  AND candidate.field_name = 'citation_count'
                ORDER BY
                    candidate.created_at DESC,
                    candidate.parser_version DESC,
                    candidate.id DESC
                LIMIT 1
            ) AS assertion ON true
            WHERE metric.metric_name = 'citation_count'
              AND jsonb_typeof(assertion.value) = 'number'
            ORDER BY
                metric.id,
                source.source_updated_at DESC NULLS LAST,
                source.retrieved_at DESC,
                source.id DESC
        )
        UPDATE metric_snapshot AS metric
        SET
            source_record_id =
                ranked_metric_source.source_record_id,
            metric_value =
                (ranked_metric_source.metric_value #>> '{}')::numeric,
            metadata =
                COALESCE(metric.metadata, '{}'::jsonb)
                || jsonb_build_object(
                    'source_record_id',
                    ranked_metric_source.source_record_id::text,
                    'content_hash',
                    ranked_metric_source.content_hash
                ),
            source_url = ranked_metric_source.source_url,
            retrieved_at = ranked_metric_source.retrieved_at,
            source_license =
                ranked_metric_source.source_license,
            content_license =
                ranked_metric_source.content_license,
            updated_at = clock_timestamp()
        FROM ranked_metric_source
        WHERE metric.id = ranked_metric_source.metric_id
        """
    )
    op.create_foreign_key(
        "fk_metric_snapshot_source_record_id_source_record",
        "metric_snapshot",
        "source_record",
        ["source_record_id"],
        ["id"],
        ondelete="RESTRICT",
    )
    op.create_index(
        "ix_metric_snapshot_source_record_id",
        "metric_snapshot",
        ["source_record_id"],
        unique=False,
    )
    op.execute(METRIC_SOURCE_OWNER_FUNCTION)
    op.execute(METRIC_SOURCE_OWNER_TRIGGER)
    op.execute(
        """
        UPDATE work
        SET
            title = COALESCE(
                (
                    SELECT assertion.value #>> '{}'
                    FROM field_assertion AS assertion
                    WHERE assertion.source_record_id =
                        work.projection_source_record_id
                      AND assertion.field_name = 'title'
                    ORDER BY
                        assertion.created_at DESC,
                        assertion.parser_version DESC,
                        assertion.id DESC
                    LIMIT 1
                ),
                work.title
            ),
            abstract = CASE
                WHEN EXISTS (
                    SELECT 1
                    FROM field_assertion AS assertion
                    WHERE assertion.source_record_id =
                        work.projection_source_record_id
                      AND assertion.field_name = 'abstract'
                )
                THEN (
                    SELECT assertion.value #>> '{}'
                    FROM field_assertion AS assertion
                    WHERE assertion.source_record_id =
                        work.projection_source_record_id
                      AND assertion.field_name = 'abstract'
                    ORDER BY
                        assertion.created_at DESC,
                        assertion.parser_version DESC,
                        assertion.id DESC
                    LIMIT 1
                )
                ELSE work.abstract
            END,
            publication_date = CASE
                WHEN EXISTS (
                    SELECT 1
                    FROM field_assertion AS assertion
                    WHERE assertion.source_record_id =
                        work.projection_source_record_id
                      AND assertion.field_name = 'publication_date'
                )
                THEN (
                    SELECT NULLIF(
                        assertion.value #>> '{}',
                        ''
                    )::date
                    FROM field_assertion AS assertion
                    WHERE assertion.source_record_id =
                        work.projection_source_record_id
                      AND assertion.field_name = 'publication_date'
                    ORDER BY
                        assertion.created_at DESC,
                        assertion.parser_version DESC,
                        assertion.id DESC
                    LIMIT 1
                )
                ELSE work.publication_date
            END,
            status = COALESCE(
                (
                    SELECT CASE
                        WHEN assertion.value = 'true'::jsonb
                        THEN 'retracted'::record_status
                        ELSE 'active'::record_status
                    END
                    FROM field_assertion AS assertion
                    WHERE assertion.source_record_id =
                        work.projection_source_record_id
                      AND assertion.field_name = 'is_retracted'
                    ORDER BY
                        assertion.created_at DESC,
                        assertion.parser_version DESC,
                        assertion.id DESC
                    LIMIT 1
                ),
                work.status
            ),
            source = source.source,
            source_url = source.source_url,
            retrieved_at = source.retrieved_at,
            source_license = source.source_license,
            content_license = source.content_license,
            projection_source = source.source,
            projection_source_updated_at = source.source_updated_at
        FROM source_record AS source
        WHERE source.id = work.projection_source_record_id
        """
    )
    op.add_column(
        "work",
        sa.Column(
            "projection_taxonomy_migration_backup",
            postgresql.JSONB(),
            nullable=True,
        ),
    )
    op.execute(
        """
        UPDATE work
        SET projection_taxonomy_migration_backup =
            jsonb_build_object(
                'method_ids',
                COALESCE(
                    (
                        SELECT jsonb_agg(
                            association.method_id
                            ORDER BY association.method_id
                        )
                        FROM work_method AS association
                        WHERE association.work_id = work.id
                    ),
                    '[]'::jsonb
                ),
                'dataset_ids',
                COALESCE(
                    (
                        SELECT jsonb_agg(
                            association.dataset_id
                            ORDER BY association.dataset_id
                        )
                        FROM work_dataset AS association
                        WHERE association.work_id = work.id
                    ),
                    '[]'::jsonb
                ),
                'benchmark_ids',
                COALESCE(
                    (
                        SELECT jsonb_agg(
                            association.benchmark_id
                            ORDER BY association.benchmark_id
                        )
                        FROM work_benchmark AS association
                        WHERE association.work_id = work.id
                    ),
                    '[]'::jsonb
                )
            )
        WHERE projection_source_record_id IS NOT NULL
        """
    )
    op.execute(
        """
        DELETE FROM work_topic AS association
        USING work
        WHERE association.work_id = work.id
          AND work.projection_source_record_id IS NOT NULL
        """
    )
    for association_table in (
        "work_method",
        "work_dataset",
        "work_benchmark",
    ):
        op.execute(
            f"""
            DELETE FROM {association_table} AS association
            USING work
            WHERE association.work_id = work.id
              AND work.projection_source_record_id IS NOT NULL
            """
        )
    op.execute(
        """
        WITH current_topics AS (
            SELECT DISTINCT ON (work.id)
                work.id AS work_id,
                assertion.value
            FROM work
            JOIN field_assertion AS assertion
              ON assertion.source_record_id =
                  work.projection_source_record_id
             AND assertion.field_name = 'topics'
            WHERE work.projection_source_record_id IS NOT NULL
            ORDER BY
                work.id,
                assertion.created_at DESC,
                assertion.parser_version DESC,
                assertion.id DESC
        ),
        projected_topic_names AS (
            SELECT DISTINCT
                current_topics.work_id,
                lower(
                    regexp_replace(
                        btrim(topic_value.item ->> 'display_name'),
                        '[[:space:]]+',
                        ' ',
                        'g'
                    )
                ) AS normalized_name
            FROM current_topics
            CROSS JOIN LATERAL jsonb_array_elements(
                CASE
                    WHEN jsonb_typeof(current_topics.value) = 'array'
                    THEN current_topics.value
                    ELSE '[]'::jsonb
                END
            ) AS topic_value(item)
            WHERE jsonb_typeof(topic_value.item) = 'object'
              AND btrim(
                  COALESCE(
                      topic_value.item ->> 'display_name',
                      ''
                  )
              ) <> ''
        )
        INSERT INTO work_topic (work_id, topic_id)
        SELECT
            projected_topic_names.work_id,
            topic.id
        FROM projected_topic_names
        JOIN topic
          ON topic.normalized_name =
              projected_topic_names.normalized_name
        ON CONFLICT DO NOTHING
        """
    )
    op.execute(LOGICAL_SOURCE_OWNER_CONSISTENCY_FUNCTION)
    op.execute(PROJECTION_OWNER_FUNCTION)
    op.execute(LOGICAL_SOURCE_OWNER_CONSISTENCY_TRIGGER)
    op.execute(PROJECTION_OWNER_TRIGGER)


def downgrade() -> None:
    op.execute(MIGRATION_DATA_LOCK)
    op.execute(
        """
        DO $$
        BEGIN
            IF EXISTS (
                SELECT 1
                FROM scope_assessment
                WHERE work_linked_at IS NOT NULL
                  AND (
                      work_link_reason IS DISTINCT FROM
                          'migration_logical_source_owner'
                      OR work_linked_at IS DISTINCT FROM evaluated_at
                  )
            )
            THEN
                RAISE EXCEPTION
                    'cannot downgrade scope link audit: '
                    '0010-only association history exists'
                    USING ERRCODE = '55000';
            END IF;
        END;
        $$;
        """
    )
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_work_projection_source_owner
        ON work
        """
    )
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_source_record_logical_owner_consistency
        ON source_record
        """
    )
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_metric_source_record_owner
        ON metric_snapshot
        """
    )
    op.execute(
        "DROP FUNCTION IF EXISTS enforce_metric_source_record_owner()"
    )
    op.drop_index(
        "ix_metric_snapshot_source_record_id",
        table_name="metric_snapshot",
    )
    op.drop_constraint(
        "fk_metric_snapshot_source_record_id_source_record",
        "metric_snapshot",
        type_="foreignkey",
    )
    op.execute(
        """
        DO $$
        BEGIN
            IF EXISTS (
                SELECT 1
                FROM metric_snapshot
                WHERE source_record_id IS NOT NULL
                  AND metadata IS NOT NULL
                  AND jsonb_typeof(metadata) IS DISTINCT FROM 'object'
            )
            THEN
                RAISE EXCEPTION
                    'cannot downgrade metric provenance: '
                    'metadata must be an object'
                    USING ERRCODE = '55000';
            END IF;
        END;
        $$;
        """
    )
    op.execute(
        """
        UPDATE metric_snapshot
        SET metadata =
            COALESCE(metadata, '{}'::jsonb)
            || jsonb_build_object(
                'source_record_id',
                source_record_id::text
            )
        WHERE source_record_id IS NOT NULL
        """
    )
    op.execute(
        """
        UPDATE metric_snapshot
        SET metadata = metadata - 'source_record_id'
        WHERE source_record_id IS NULL
          AND jsonb_typeof(metadata) = 'object'
          AND metadata ? 'source_record_id'
        """
    )
    op.drop_column("metric_snapshot", "source_record_id")
    op.execute(
        """
        WITH historical_topic_names AS (
            SELECT DISTINCT
                source.work_id,
                lower(
                    regexp_replace(
                        btrim(topic_value.item ->> 'display_name'),
                        '[[:space:]]+',
                        ' ',
                        'g'
                    )
                ) AS normalized_name
            FROM source_record AS source
            JOIN field_assertion AS assertion
              ON assertion.source_record_id = source.id
             AND assertion.field_name = 'topics'
            CROSS JOIN LATERAL jsonb_array_elements(
                CASE
                    WHEN jsonb_typeof(assertion.value) = 'array'
                    THEN assertion.value
                    ELSE '[]'::jsonb
                END
            ) AS topic_value(item)
            WHERE source.work_id IS NOT NULL
              AND EXISTS (
                  SELECT 1
                  FROM scope_assessment AS assessment
                  WHERE assessment.source_record_id = source.id
                    AND assessment.included
                    AND assessment.work_id = source.work_id
              )
              AND jsonb_typeof(topic_value.item) = 'object'
              AND btrim(
                  COALESCE(
                      topic_value.item ->> 'display_name',
                      ''
                  )
              ) <> ''
        )
        INSERT INTO work_topic (work_id, topic_id)
        SELECT
            historical_topic_names.work_id,
            topic.id
        FROM historical_topic_names
        JOIN topic
          ON topic.normalized_name =
              historical_topic_names.normalized_name
        ON CONFLICT DO NOTHING
        """
    )
    for association_table, backup_key, target_column in (
        ("work_method", "method_ids", "method_id"),
        ("work_dataset", "dataset_ids", "dataset_id"),
        ("work_benchmark", "benchmark_ids", "benchmark_id"),
    ):
        op.execute(
            f"""
            INSERT INTO {association_table} (work_id, {target_column})
            SELECT
                work.id,
                backup.target_id::uuid
            FROM work
            CROSS JOIN LATERAL jsonb_array_elements_text(
                COALESCE(
                    work.projection_taxonomy_migration_backup
                        -> '{backup_key}',
                    '[]'::jsonb
                )
            ) AS backup(target_id)
            ON CONFLICT DO NOTHING
            """
        )
    op.drop_column(
        "work",
        "projection_taxonomy_migration_backup",
    )
    op.execute(
        "DROP FUNCTION IF EXISTS enforce_work_projection_source_owner()"
    )
    op.execute(
        "DROP FUNCTION IF EXISTS enforce_logical_source_owner_consistency()"
    )
    op.drop_index(
        "ix_metric_snapshot_repo_metric_time",
        table_name="metric_snapshot",
    )
    op.drop_index(
        "ix_external_identifier_raw_value_trgm",
        table_name="external_identifier",
    )
    op.drop_index(
        "ix_external_identifier_normalized_value_trgm",
        table_name="external_identifier",
    )
    op.drop_index(
        "ix_work_canonical_key_trgm",
        table_name="work",
    )
    op.alter_column(
        "work",
        "created_at",
        server_default=sa.text("now()"),
    )
    op.drop_index(
        "ix_work_publication_date_desc_canonical_key",
        table_name="work",
    )
    op.drop_index("ix_work_created_at", table_name="work")
    op.add_column(
        "work",
        sa.Column(
            "projection_source_record_logical_id",
            sa.String(length=255),
            nullable=True,
        ),
    )
    op.execute(
        """
        UPDATE work
        SET projection_source_record_logical_id =
            source_record.source_record_id
        FROM source_record
        WHERE source_record.id = work.projection_source_record_id
        """
    )
    op.drop_index(
        "ix_work_projection_source_record_id",
        table_name="work",
    )
    op.drop_constraint(
        "fk_work_projection_source_record_id_source_record",
        "work",
        type_="foreignkey",
    )
    op.drop_column("work", "projection_source_record_id")
    op.alter_column(
        "work",
        "projection_source_record_logical_id",
        new_column_name="projection_source_record_id",
    )

    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_source_record_link_logical_scope_owner
        ON source_record
        """
    )
    op.execute("DROP FUNCTION IF EXISTS link_logical_source_scope_owner()")
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_scope_assessment_link_logical_owner
        ON scope_assessment
        """
    )
    op.execute(
        "DROP FUNCTION IF EXISTS attach_scope_assessment_logical_owner()"
    )
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_scope_assessment_immutable
        ON scope_assessment
        """
    )
    op.drop_constraint(
        "ck_scope_assessment_work_link_metadata",
        "scope_assessment",
        type_="check",
    )
    op.drop_column("scope_assessment", "work_link_reason")
    op.drop_column("scope_assessment", "work_linked_at")
    op.execute(OLD_ASSOCIATION_AWARE_IMMUTABILITY_FUNCTION)
    op.execute(IMMUTABLE_TRIGGER)
