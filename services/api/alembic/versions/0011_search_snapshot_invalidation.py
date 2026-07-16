"""Invalidate paper search snapshots after observable work changes.

Revision ID: 0011_search_snapshot_revision
Revises: 0010_exact_projection_scope
Create Date: 2026-07-16
"""

from collections.abc import Sequence

from alembic import op
import sqlalchemy as sa


revision: str = "0011_search_snapshot_revision"
down_revision: str | None = "0010_exact_projection_scope"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


RECORD_CHANGE_FUNCTION = """
CREATE FUNCTION record_paper_search_change(
    target_work_id uuid,
    target_work_created_at timestamptz,
    target_source_table text
)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF target_work_id IS NULL
       OR target_work_created_at IS NULL
    THEN
        RETURN;
    END IF;

    INSERT INTO paper_search_change (
        transaction_id,
        work_id,
        work_created_at,
        source_table
    )
    VALUES (
        txid_current(),
        target_work_id,
        target_work_created_at,
        target_source_table
    )
    ON CONFLICT (transaction_id, work_id) DO UPDATE
    SET work_created_at = LEAST(
        paper_search_change.work_created_at,
        EXCLUDED.work_created_at
    );
END;
$$;
"""


RECORD_CHANGE_FOR_WORK_FUNCTION = """
CREATE FUNCTION record_paper_search_change_for_work(
    target_work_id uuid,
    target_source_table text
)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM record_paper_search_change(
        target.id,
        target.created_at,
        target_source_table
    )
    FROM work AS target
    WHERE target.id = target_work_id;
END;
$$;
"""


WORK_CHANGE_TRIGGER_FUNCTION = """
CREATE FUNCTION capture_work_search_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM record_paper_search_change(
            OLD.id,
            OLD.created_at,
            TG_TABLE_NAME
        );
        RETURN OLD;
    END IF;

    IF TG_OP = 'UPDATE'
       AND OLD IS NOT DISTINCT FROM NEW
    THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE'
       AND OLD.id IS DISTINCT FROM NEW.id
    THEN
        PERFORM record_paper_search_change(
            OLD.id,
            OLD.created_at,
            TG_TABLE_NAME
        );
    END IF;

    PERFORM record_paper_search_change(
        NEW.id,
        CASE
            WHEN TG_OP = 'UPDATE'
                 AND OLD.id = NEW.id
            THEN LEAST(OLD.created_at, NEW.created_at)
            ELSE NEW.created_at
        END,
        TG_TABLE_NAME
    );
    RETURN NEW;
END;
$$;
"""


DIRECT_WORK_CHANGE_TRIGGER_FUNCTION = """
CREATE FUNCTION capture_direct_work_search_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM record_paper_search_change_for_work(
            OLD.work_id,
            TG_TABLE_NAME
        );
        RETURN OLD;
    END IF;

    IF TG_OP = 'UPDATE'
       AND OLD IS NOT DISTINCT FROM NEW
    THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE'
       AND OLD.work_id IS DISTINCT FROM NEW.work_id
    THEN
        PERFORM record_paper_search_change_for_work(
            OLD.work_id,
            TG_TABLE_NAME
        );
    END IF;

    PERFORM record_paper_search_change_for_work(
        NEW.work_id,
        TG_TABLE_NAME
    );
    RETURN NEW;
END;
$$;
"""


ASSERTION_CHANGE_TRIGGER_FUNCTION = """
CREATE FUNCTION capture_assertion_search_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    target_source_record_id uuid;
    target_work_id uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN
        target_source_record_id = OLD.source_record_id;
        SELECT source.work_id
        INTO target_work_id
        FROM source_record AS source
        WHERE source.id = target_source_record_id;
        PERFORM record_paper_search_change_for_work(
            target_work_id,
            TG_TABLE_NAME
        );
        RETURN OLD;
    END IF;

    IF TG_OP = 'UPDATE'
       AND OLD IS NOT DISTINCT FROM NEW
    THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE'
       AND OLD.source_record_id IS DISTINCT FROM NEW.source_record_id
    THEN
        SELECT source.work_id
        INTO target_work_id
        FROM source_record AS source
        WHERE source.id = OLD.source_record_id;
        PERFORM record_paper_search_change_for_work(
            target_work_id,
            TG_TABLE_NAME
        );
    END IF;

    SELECT source.work_id
    INTO target_work_id
    FROM source_record AS source
    WHERE source.id = NEW.source_record_id;
    PERFORM record_paper_search_change_for_work(
        target_work_id,
        TG_TABLE_NAME
    );

    RETURN NEW;
END;
$$;
"""


TAXONOMY_CHANGE_TRIGGER_FUNCTION = """
CREATE FUNCTION capture_taxonomy_search_change()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_TABLE_NAME = 'topic' THEN
        INSERT INTO paper_search_change (
            transaction_id,
            work_id,
            work_created_at,
            source_table
        )
        SELECT
            txid_current(),
            target.id,
            target.created_at,
            TG_TABLE_NAME
        FROM work_topic AS association
        JOIN work AS target
          ON target.id = association.work_id
        WHERE association.topic_id = NEW.id
        ON CONFLICT (transaction_id, work_id) DO NOTHING;
    ELSIF TG_TABLE_NAME = 'method' THEN
        INSERT INTO paper_search_change (
            transaction_id,
            work_id,
            work_created_at,
            source_table
        )
        SELECT
            txid_current(),
            target.id,
            target.created_at,
            TG_TABLE_NAME
        FROM work_method AS association
        JOIN work AS target
          ON target.id = association.work_id
        WHERE association.method_id = NEW.id
        ON CONFLICT (transaction_id, work_id) DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;
"""


FINALIZE_CHANGE_REVISIONS_FUNCTION = """
CREATE FUNCTION finalize_paper_search_change_revisions()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    pending_revision bigint;
    revision_sequence regclass;
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM paper_search_change
        WHERE transaction_id = txid_current()
          AND NOT commit_ordered
    )
    THEN
        RETURN NULL;
    END IF;

    PERFORM pg_advisory_xact_lock(1346456912, 20260716);
    SELECT pg_get_serial_sequence(
        'paper_search_change',
        'revision'
    )::regclass
    INTO revision_sequence;

    FOR pending_revision IN
        SELECT revision
        FROM paper_search_change
        WHERE transaction_id = txid_current()
          AND NOT commit_ordered
        ORDER BY revision
    LOOP
        UPDATE paper_search_change
        SET
            revision = nextval(revision_sequence),
            changed_at = clock_timestamp(),
            commit_ordered = true
        WHERE revision = pending_revision;
    END LOOP;
    RETURN NULL;
END;
$$;
"""


DIRECT_WORK_TABLES = (
    "source_record",
    "scope_assessment",
    "external_identifier",
    "metric_snapshot",
    "work_topic",
    "work_method",
    "work_code_repository",
)


def upgrade() -> None:
    op.create_table(
        "paper_search_snapshot_state",
        sa.Column(
            "id",
            sa.Boolean(),
            server_default=sa.text("true"),
            nullable=False,
        ),
        sa.Column(
            "valid_from",
            sa.DateTime(timezone=True),
            server_default=sa.text("clock_timestamp()"),
            nullable=False,
        ),
        sa.CheckConstraint(
            "id",
            name="ck_paper_search_snapshot_state_singleton",
        ),
        sa.PrimaryKeyConstraint(
            "id",
            name="pk_paper_search_snapshot_state",
        ),
    )
    op.execute(
        """
        INSERT INTO paper_search_snapshot_state (id)
        VALUES (true)
        """
    )
    op.create_table(
        "paper_search_change",
        sa.Column(
            "revision",
            sa.BigInteger(),
            sa.Identity(),
            nullable=False,
        ),
        sa.Column(
            "transaction_id",
            sa.BigInteger(),
            nullable=False,
        ),
        sa.Column("work_id", sa.Uuid(), nullable=False),
        sa.Column(
            "work_created_at",
            sa.DateTime(timezone=True),
            nullable=False,
        ),
        sa.Column(
            "changed_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("clock_timestamp()"),
            nullable=False,
        ),
        sa.Column(
            "source_table",
            sa.String(length=64),
            nullable=False,
        ),
        sa.Column(
            "commit_ordered",
            sa.Boolean(),
            server_default=sa.text("false"),
            nullable=False,
        ),
        sa.PrimaryKeyConstraint(
            "revision",
            name="pk_paper_search_change",
        ),
        sa.UniqueConstraint(
            "transaction_id",
            "work_id",
            name="uq_paper_search_change_transaction_work",
        ),
    )
    op.create_index(
        "ix_paper_search_change_revision_work_created",
        "paper_search_change",
        ["revision", "work_created_at"],
        unique=False,
    )
    op.create_index(
        "ix_paper_search_change_changed_revision",
        "paper_search_change",
        ["changed_at", "revision"],
        unique=False,
    )
    op.execute(RECORD_CHANGE_FUNCTION)
    op.execute(RECORD_CHANGE_FOR_WORK_FUNCTION)
    op.execute(WORK_CHANGE_TRIGGER_FUNCTION)
    op.execute(DIRECT_WORK_CHANGE_TRIGGER_FUNCTION)
    op.execute(ASSERTION_CHANGE_TRIGGER_FUNCTION)
    op.execute(TAXONOMY_CHANGE_TRIGGER_FUNCTION)
    op.execute(FINALIZE_CHANGE_REVISIONS_FUNCTION)
    op.execute(
        """
        CREATE CONSTRAINT TRIGGER
            trg_paper_search_change_finalize_revision
        AFTER INSERT
        ON paper_search_change
        DEFERRABLE INITIALLY DEFERRED
        FOR EACH ROW
        EXECUTE FUNCTION finalize_paper_search_change_revisions()
        """
    )

    op.execute(
        """
        CREATE TRIGGER trg_work_search_change_write
        AFTER INSERT OR UPDATE
        ON work
        FOR EACH ROW
        EXECUTE FUNCTION capture_work_search_change()
        """
    )
    op.execute(
        """
        CREATE TRIGGER trg_work_search_change_delete
        BEFORE DELETE
        ON work
        FOR EACH ROW
        EXECUTE FUNCTION capture_work_search_change()
        """
    )
    for table_name in DIRECT_WORK_TABLES:
        op.execute(
            f"""
            CREATE TRIGGER trg_{table_name}_search_change
            AFTER INSERT OR UPDATE OR DELETE
            ON {table_name}
            FOR EACH ROW
            EXECUTE FUNCTION capture_direct_work_search_change()
            """
        )
    op.execute(
        """
        CREATE TRIGGER trg_field_assertion_search_change
        AFTER INSERT OR UPDATE OR DELETE
        ON field_assertion
        FOR EACH ROW
        EXECUTE FUNCTION capture_assertion_search_change()
        """
    )
    for table_name in ("topic", "method"):
        op.execute(
            f"""
            CREATE TRIGGER trg_{table_name}_search_change
            AFTER UPDATE OF name, normalized_name
            ON {table_name}
            FOR EACH ROW
            WHEN (
                OLD.name IS DISTINCT FROM NEW.name
                OR OLD.normalized_name IS DISTINCT FROM
                    NEW.normalized_name
            )
            EXECUTE FUNCTION capture_taxonomy_search_change()
            """
        )


def downgrade() -> None:
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_paper_search_change_finalize_revision
        ON paper_search_change
        """
    )
    op.execute(
        "DROP FUNCTION IF EXISTS finalize_paper_search_change_revisions()"
    )
    for table_name in ("topic", "method"):
        op.execute(
            f"""
            DROP TRIGGER IF EXISTS
                trg_{table_name}_search_change
            ON {table_name}
            """
        )
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_field_assertion_search_change
        ON field_assertion
        """
    )
    for table_name in reversed(DIRECT_WORK_TABLES):
        op.execute(
            f"""
            DROP TRIGGER IF EXISTS
                trg_{table_name}_search_change
            ON {table_name}
            """
        )
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_work_search_change_delete
        ON work
        """
    )
    op.execute(
        """
        DROP TRIGGER IF EXISTS
            trg_work_search_change_write
        ON work
        """
    )
    op.execute("DROP FUNCTION IF EXISTS capture_taxonomy_search_change()")
    op.execute("DROP FUNCTION IF EXISTS capture_assertion_search_change()")
    op.execute("DROP FUNCTION IF EXISTS capture_direct_work_search_change()")
    op.execute("DROP FUNCTION IF EXISTS capture_work_search_change()")
    op.execute(
        """
        DROP FUNCTION IF EXISTS
            record_paper_search_change_for_work(uuid, text)
        """
    )
    op.execute(
        """
        DROP FUNCTION IF EXISTS
            record_paper_search_change(uuid, timestamptz, text)
        """
    )
    op.drop_index(
        "ix_paper_search_change_changed_revision",
        table_name="paper_search_change",
    )
    op.drop_index(
        "ix_paper_search_change_revision_work_created",
        table_name="paper_search_change",
    )
    op.drop_table("paper_search_change")
    op.drop_table("paper_search_snapshot_state")
