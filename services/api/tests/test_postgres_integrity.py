from datetime import UTC, datetime, timedelta
import hashlib
from io import StringIO
import json
from pathlib import Path
import time
from uuid import UUID, uuid4

from alembic import command
from alembic.autogenerate import compare_metadata
from alembic.migration import MigrationContext
import psycopg
import pytest
from sqlalchemy import create_engine, func, inspect, select, text
from sqlalchemy.exc import DBAPIError, IntegrityError
from sqlalchemy.orm import Session


SERVICE_ROOT = Path(__file__).resolve().parents[1]
INITIAL_MIGRATION = (
    SERVICE_ROOT / "alembic" / "versions" / "0001_initial_schema.py"
)
INITIAL_MIGRATION_SHA256 = (
    "7771e5358c3679aed87a1299e3aff52ad07bb0e945a2754ccc7c973bccd7c931"
)
FIXTURES_ROOT = Path(__file__).resolve().parent / "fixtures"
C74476A_SCHEMA_VARIANT = (
    FIXTURES_ROOT / "c74476a_schema_variant.sql"
)
HISTORICAL_ENV_SOURCE = """
from alembic import context
from sqlalchemy import MetaData, create_engine, pool

config = context.config
target_metadata = MetaData(
    naming_convention={
        "ix": "ix_%(column_0_label)s",
        "uq": "uq_%(table_name)s_%(column_0_name)s",
        "ck": "ck_%(table_name)s_%(constraint_name)s",
        "fk": "fk_%(table_name)s_%(column_0_name)s_%(referred_table_name)s",
        "pk": "pk_%(table_name)s",
    }
)


def run_migrations_online() -> None:
    connectable = create_engine(
        config.get_main_option("sqlalchemy.url"),
        poolclass=pool.NullPool,
    )
    with connectable.connect() as connection:
        context.configure(connection=connection, target_metadata=target_metadata)
        with context.begin_transaction():
            context.run_migrations()


run_migrations_online()
"""


def _insert_old_schema_fixture(connection) -> dict[str, UUID]:
    ids = {
        "work": uuid4(),
        "version": uuid4(),
        "source": uuid4(),
        "assertion": uuid4(),
        "ranking": uuid4(),
    }
    now = datetime(2026, 7, 15, 8, 30, tzinfo=UTC)
    connection.execute(
        text(
            """
            INSERT INTO work (id, canonical_key, title, source)
            VALUES (:id, :canonical_key, :title, :source)
            """
        ),
        {
            "id": ids["work"],
            "canonical_key": "doi:10.1000/old-schema",
            "title": "Old schema fixture",
            "source": "test",
        },
    )
    connection.execute(
        text(
            """
            INSERT INTO paper_version (
                id, work_id, version_label, version_number, version_type, source
            )
            VALUES (
                :id, :work_id, 'v1', 1, 'preprint', 'test'
            )
            """
        ),
        {"id": ids["version"], "work_id": ids["work"]},
    )
    connection.execute(
        text(
            """
            INSERT INTO source_record (
                id, work_id, paper_version_id, source_record_id,
                content_hash, raw_payload, source
            )
            VALUES (
                :id, :work_id, :paper_version_id, 'old-record',
                :content_hash, CAST(:raw_payload AS jsonb), 'test'
            )
            """
        ),
        {
            "id": ids["source"],
            "work_id": ids["work"],
            "paper_version_id": ids["version"],
            "content_hash": "a" * 64,
            "raw_payload": json.dumps({"old": True}),
        },
    )
    connection.execute(
        text(
            """
            INSERT INTO field_assertion (
                id, work_id, paper_version_id, source_record_id,
                field_name, value, parser_version, source
            )
            VALUES (
                :id, :work_id, :paper_version_id, :source_record_id,
                'title', CAST(:value AS jsonb), 'v1', 'test'
            )
            """
        ),
        {
            "id": ids["assertion"],
            "work_id": ids["work"],
            "paper_version_id": ids["version"],
            "source_record_id": ids["source"],
            "value": json.dumps("Old schema fixture"),
        },
    )
    connection.execute(
        text(
            """
            INSERT INTO ranking_snapshot (
                id, ranking_name, subject_type, subject_id, rank_position,
                score, window_days, computed_at, formula_version, coverage, source
            )
            VALUES (
                :id, 'weekly', 'work', :subject_id, 1,
                1.0, 7, :computed_at, 'v1', CAST(:coverage AS jsonb), 'test'
            )
            """
        ),
        {
            "id": ids["ranking"],
            "subject_id": ids["work"],
            "computed_at": now,
            "coverage": json.dumps({"records": 1}),
        },
    )
    return ids


def _assert_integrity_error(engine, statement: str, parameters: dict) -> None:
    with pytest.raises(IntegrityError):
        with engine.begin() as connection:
            connection.execute(text(statement), parameters)


def _insert_search_trigger_works(engine) -> tuple[UUID, UUID]:
    first_work_id = uuid4()
    second_work_id = uuid4()
    revision = _paper_search_change_revision(engine)
    with engine.begin() as connection:
        connection.execute(
            text(
                """
                INSERT INTO work (id, canonical_key, title, source)
                VALUES
                    (
                        :first_work_id,
                        'doi:10.1000/search-trigger-first',
                        'Search trigger first work',
                        'test'
                    ),
                    (
                        :second_work_id,
                        'doi:10.1000/search-trigger-second',
                        'Search trigger second work',
                        'test'
                    )
                """
            ),
            {
                "first_work_id": first_work_id,
                "second_work_id": second_work_id,
            },
        )
    assert _paper_search_changes_after(engine, revision) == {
        (first_work_id, "work"),
        (second_work_id, "work"),
    }
    return first_work_id, second_work_id


def _paper_search_change_revision(engine) -> int:
    with engine.connect() as connection:
        return int(
            connection.scalar(
                text(
                    """
                    SELECT COALESCE(max(revision), 0)
                    FROM paper_search_change
                    """
                )
            )
            or 0
        )


def _paper_search_changes_after(
    engine,
    revision: int,
) -> set[tuple[UUID, str]]:
    with engine.connect() as connection:
        rows = connection.execute(
            text(
                """
                SELECT work_id, source_table, commit_ordered
                FROM paper_search_change
                WHERE revision > :revision
                ORDER BY revision
                """
            ),
            {"revision": revision},
        ).all()
    assert rows
    assert all(row.commit_ordered for row in rows)
    return {
        (row.work_id, row.source_table)
        for row in rows
    }


def _wait_for_relation_lock(
    engine,
    *,
    relation_name: str,
    mode: str,
    granted: bool,
    timeout: float = 5,
) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        with engine.connect() as connection:
            found = connection.scalar(
                text(
                    """
                    SELECT EXISTS (
                        SELECT 1
                        FROM pg_locks AS relation_lock
                        JOIN pg_class AS relation
                          ON relation.oid = relation_lock.relation
                        JOIN pg_namespace AS namespace
                          ON namespace.oid = relation.relnamespace
                        WHERE namespace.nspname = 'public'
                          AND relation.relname = :relation_name
                          AND relation_lock.mode = :mode
                          AND relation_lock.granted = :granted
                    )
                    """
                ),
                {
                    "relation_name": relation_name,
                    "mode": mode,
                    "granted": granted,
                },
            )
        if bool(found):
            return
        time.sleep(0.01)
    raise AssertionError(
        f"timed out waiting for {mode} on {relation_name}"
    )


def _wait_for_waiting_relation_lock(
    engine,
    *,
    relation_names: tuple[str, ...],
    timeout: float = 5,
) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        with engine.connect() as connection:
            found = connection.scalar(
                text(
                    """
                    SELECT EXISTS (
                        SELECT 1
                        FROM pg_locks AS relation_lock
                        JOIN pg_class AS relation
                          ON relation.oid = relation_lock.relation
                        JOIN pg_namespace AS namespace
                          ON namespace.oid = relation.relnamespace
                        WHERE namespace.nspname = 'public'
                          AND relation.relname = ANY(:relation_names)
                          AND NOT relation_lock.granted
                    )
                    """
                ),
                {"relation_names": list(relation_names)},
            )
        if bool(found):
            return
        time.sleep(0.01)
    raise AssertionError("timed out waiting for relation lock")


def _historical_alembic_config(
    tmp_path: Path,
    database_url: str,
):
    from alembic.config import Config

    script_root = tmp_path / "historical_alembic"
    versions_root = script_root / "versions"
    versions_root.mkdir(parents=True)
    (script_root / "env.py").write_text(
        HISTORICAL_ENV_SOURCE,
        encoding="utf-8",
    )
    # The checked-in 0001 is byte-for-byte d1aacd8, enforced by its SHA test.
    (versions_root / "0001_initial_schema.py").write_text(
        INITIAL_MIGRATION.read_text(encoding="utf-8"),
        encoding="utf-8",
    )

    config = Config()
    config.set_main_option("script_location", str(script_root))
    config.set_main_option("sqlalchemy.url", database_url)
    return config


def _install_historical_schema(
    tmp_path: Path,
    database_url: str,
    variant: str,
) -> None:
    historical_config = _historical_alembic_config(tmp_path, database_url)
    command.upgrade(historical_config, "0001_initial_schema")
    if variant == "c74476a":
        engine = create_engine(database_url)
        try:
            with engine.begin() as connection:
                connection.exec_driver_sql(
                    C74476A_SCHEMA_VARIANT.read_text(encoding="utf-8")
                )
        finally:
            engine.dispose()


def _offline_head_upgrade_sql() -> str:
    from alembic.config import Config

    output = StringIO()
    config = Config(SERVICE_ROOT / "alembic.ini", output_buffer=output)
    command.upgrade(
        config,
        "0001_initial_schema:head",
        sql=True,
    )
    return output.getvalue()


def _insert_delete_graph(engine, suffix: str) -> dict[str, UUID]:
    ids = {
        "work": uuid4(),
        "version": uuid4(),
        "source": uuid4(),
        "assertion": uuid4(),
        "external": uuid4(),
        "repository": uuid4(),
        "metric": uuid4(),
        "ranking": uuid4(),
    }
    now = datetime(2026, 7, 15, 8, 30, tzinfo=UTC)
    with engine.begin() as connection:
        connection.execute(
            text(
                """
                INSERT INTO work (id, canonical_key, title, source)
                VALUES (:id, :canonical_key, :title, 'test')
                """
            ),
            {
                "id": ids["work"],
                "canonical_key": f"doi:10.1000/{suffix}",
                "title": f"Delete graph {suffix}",
            },
        )
        connection.execute(
            text(
                """
                INSERT INTO paper_version (
                    id, work_id, version_label, version_number, version_type, source
                )
                VALUES (:id, :work_id, 'v1', 1, 'preprint', 'test')
                """
            ),
            {"id": ids["version"], "work_id": ids["work"]},
        )
        connection.execute(
            text(
                """
                INSERT INTO source_record (
                    id, paper_version_id, source_record_id,
                    content_hash, raw_payload, source
                )
                VALUES (
                    :id, :paper_version_id, :source_record_id,
                    :content_hash, '{}'::jsonb, 'test'
                )
                """
            ),
            {
                "id": ids["source"],
                "paper_version_id": ids["version"],
                "source_record_id": f"source-{suffix}",
                "content_hash": suffix.ljust(64, "0"),
            },
        )
        connection.execute(
            text(
                """
                INSERT INTO field_assertion (
                    id, source_record_id, field_name, value,
                    parser_version, confidence, source
                )
                VALUES (
                    :id, :source_record_id, 'title',
                    CAST(:value AS jsonb), 'v1', 1.0, 'test'
                )
                """
            ),
            {
                "id": ids["assertion"],
                "source_record_id": ids["source"],
                "value": json.dumps(f"Delete graph {suffix}"),
            },
        )
        connection.execute(
            text(
                """
                INSERT INTO external_identifier (
                    id, work_id, scheme, normalized_value, raw_value, source
                )
                VALUES (
                    :id, :work_id, 'doi',
                    :normalized_value, :raw_value, 'test'
                )
                """
            ),
            {
                "id": ids["external"],
                "work_id": ids["work"],
                "normalized_value": f"10.1000/{suffix}",
                "raw_value": f"10.1000/{suffix}",
            },
        )
        connection.execute(
            text(
                """
                INSERT INTO code_repository (
                    id, provider, repository_name,
                    repository_url, normalized_url, source
                )
                VALUES (
                    :id, 'github', :repository_name,
                    :repository_url, :normalized_url, 'test'
                )
                """
            ),
            {
                "id": ids["repository"],
                "repository_name": suffix,
                "repository_url": f"https://github.test/paper/{suffix}",
                "normalized_url": f"https://github.test/paper/{suffix}",
            },
        )
        connection.execute(
            text(
                """
                INSERT INTO work_code_repository (
                    work_id, code_repository_id
                )
                VALUES (:work_id, :repository_id)
                """
            ),
            {
                "work_id": ids["work"],
                "repository_id": ids["repository"],
            },
        )
        connection.execute(
            text(
                """
                INSERT INTO metric_snapshot (
                    id, code_repository_id, metric_name, metric_value,
                    measured_at, window_days, source
                )
                VALUES (
                    :id, :repository_id, 'stars', 1,
                    :measured_at, 7, 'test'
                )
                """
            ),
            {
                "id": ids["metric"],
                "repository_id": ids["repository"],
                "measured_at": now,
            },
        )
        connection.execute(
            text(
                """
                INSERT INTO ranking_snapshot (
                    id, ranking_name, work_id, rank_position, score,
                    window_days, computed_at, formula_version, coverage, source
                )
                VALUES (
                    :id, :ranking_name, :work_id, 1, 1,
                    7, :computed_at, 'v1', '{}'::jsonb, 'test'
                )
                """
            ),
            {
                "id": ids["ranking"],
                "ranking_name": f"weekly-{suffix}",
                "work_id": ids["work"],
                "computed_at": now,
            },
        )
    return ids


def _delete_result(engine, ids: dict[str, UUID]) -> dict[str, object]:
    with engine.connect() as connection:
        return {
            "work": connection.scalar(
                text("SELECT count(*) FROM work WHERE id = :id"),
                {"id": ids["work"]},
            ),
            "version": connection.scalar(
                text("SELECT count(*) FROM paper_version WHERE id = :id"),
                {"id": ids["version"]},
            ),
            "source": connection.execute(
                text(
                    """
                    SELECT work_id, paper_version_id
                    FROM source_record WHERE id = :id
                    """
                ),
                {"id": ids["source"]},
            ).one(),
            "assertion": connection.scalar(
                text("SELECT count(*) FROM field_assertion WHERE id = :id"),
                {"id": ids["assertion"]},
            ),
            "external": connection.scalar(
                text("SELECT count(*) FROM external_identifier WHERE id = :id"),
                {"id": ids["external"]},
            ),
            "repository": connection.scalar(
                text("SELECT count(*) FROM code_repository WHERE id = :id"),
                {"id": ids["repository"]},
            ),
            "repository_link": connection.scalar(
                text(
                    """
                    SELECT count(*)
                    FROM work_code_repository
                    WHERE work_id = :work_id
                      AND code_repository_id = :repository_id
                    """
                ),
                {
                    "work_id": ids["work"],
                    "repository_id": ids["repository"],
                },
            ),
            "metric": connection.scalar(
                text("SELECT count(*) FROM metric_snapshot WHERE id = :id"),
                {"id": ids["metric"]},
            ),
            "ranking": connection.scalar(
                text("SELECT count(*) FROM ranking_snapshot WHERE id = :id"),
                {"id": ids["ranking"]},
            ),
        }


def test_0001_migration_matches_committed_baseline() -> None:
    digest = hashlib.sha256(INITIAL_MIGRATION.read_bytes()).hexdigest()

    assert digest == INITIAL_MIGRATION_SHA256


def test_0001_to_0002_upgrade_and_downgrade_are_executable(
    alembic_config,
    clean_postgres_url: str,
    tmp_path: Path,
) -> None:
    _install_historical_schema(
        tmp_path,
        clean_postgres_url,
        "d1aacd8",
    )
    engine = create_engine(clean_postgres_url)
    try:
        old_columns = {
            column["name"]
            for column in inspect(engine).get_columns("ranking_snapshot")
        }
        assert {"subject_type", "subject_id"} <= old_columns
        assert "work_id" not in old_columns
        assert (
            "ck_metric_snapshot_ck_metric_snapshot_single_target"
            in {
                constraint["name"]
                for constraint in inspect(engine).get_check_constraints(
                    "metric_snapshot"
                )
            }
        )

        with engine.begin() as connection:
            ids = _insert_old_schema_fixture(connection)

        command.upgrade(alembic_config, "head")

        new_columns = {
            column["name"]
            for column in inspect(engine).get_columns("ranking_snapshot")
        }
        assert {"work_id", "topic_id", "method_id"} <= new_columns
        assert "subject_type" not in new_columns
        assert engine.connect().scalar(
            text("SELECT count(*) FROM ranking_snapshot")
        ) == 0
        with engine.connect() as connection:
            assert connection.scalar(
                text("SELECT count(*) FROM field_assertion WHERE id = :id"),
                {"id": ids["assertion"]},
            ) == 1
            assert connection.execute(
                text(
                    """
                    SELECT work_id, paper_version_id
                    FROM source_record WHERE id = :id
                    """
                ),
                {"id": ids["source"]},
            ).one() == (None, ids["version"])
            assertion_columns = {
                column["name"]
                for column in inspect(connection).get_columns("field_assertion")
            }
            assert "work_id" not in assertion_columns
            assert "paper_version_id" not in assertion_columns
            external_identifier_columns = {
                column["name"]
                for column in inspect(connection).get_columns(
                    "external_identifier"
                )
            }
            assert "paper_version_id" not in external_identifier_columns
            assert "source_record_id" not in external_identifier_columns
            metric_check_names = {
                constraint["name"]
                for constraint in inspect(connection).get_check_constraints(
                    "metric_snapshot"
                )
            }
            assert "ck_metric_snapshot_single_target" in metric_check_names
            assert (
                "ck_metric_snapshot_ck_metric_snapshot_single_target"
                not in metric_check_names
            )

        command.downgrade(alembic_config, "0001_initial_schema")
        downgraded_columns = {
            column["name"]
            for column in inspect(engine).get_columns("ranking_snapshot")
        }
        assert {"subject_type", "subject_id"} <= downgraded_columns
        assert "work_id" not in downgraded_columns
        with engine.connect() as connection:
            external_identifier_columns = {
                column["name"]
                for column in inspect(connection).get_columns(
                    "external_identifier"
                )
            }
            assert {
                "paper_version_id",
                "source_record_id",
            } <= external_identifier_columns
            assert connection.execute(
                text(
                    """
                    SELECT work_id, paper_version_id
                    FROM source_record WHERE id = :id
                    """
                ),
                {"id": ids["source"]},
            ).one() == (ids["work"], ids["version"])
            assert connection.execute(
                text(
                    """
                    SELECT work_id, paper_version_id
                    FROM field_assertion WHERE id = :id
                    """
                ),
                {"id": ids["assertion"]},
            ).one() == (ids["work"], ids["version"])

        command.upgrade(alembic_config, "head")
    finally:
        engine.dispose()


def test_0003_to_0004_scope_assessment_upgrade_and_downgrade_are_executable(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "0003_openalex_source_provenance")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    included_source_id = uuid4()
    excluded_source_id = uuid4()
    post_upgrade_source_id = uuid4()
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (
                        id, canonical_key, title, source
                    )
                    VALUES (
                        :id, 'doi:10.1000/scope-migration',
                        'Scope migration fixture', 'openalex'
                    )
                    """
                ),
                {"id": work_id},
            )
            for source_id, source_record_id, content_hash, owner_id in (
                (
                    included_source_id,
                    "W1000000001",
                    "a" * 64,
                    work_id,
                ),
                (
                    excluded_source_id,
                    "W1000000002",
                    "b" * 64,
                    None,
                ),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source
                        )
                        VALUES (
                            :id,
                            :work_id,
                            :source_record_id,
                            :content_hash,
                            CAST(:raw_payload AS jsonb),
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": source_id,
                        "work_id": owner_id,
                        "source_record_id": source_record_id,
                        "content_hash": content_hash,
                        "raw_payload": json.dumps({"id": source_record_id}),
                    },
                )
            for source_id, value in (
                (
                    included_source_id,
                    {
                        "included": True,
                        "rule_version": "agent-llm-scope-v1",
                        "reason": None,
                        "evidence": [
                            {
                                "field": "title",
                                "term": "agent",
                                "matched_text": "Agent benchmark",
                            }
                        ],
                    },
                ),
                (
                    excluded_source_id,
                    {
                        "included": False,
                        "rule_version": "agent-llm-scope-v1",
                        "reason": "no_agent_llm_scope_term_match",
                        "evidence": [],
                    },
                ),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO field_assertion (
                            id,
                            source_record_id,
                            field_name,
                            value,
                            parser_version,
                            source
                        )
                        VALUES (
                            :id,
                            :source_record_id,
                            'scope',
                            CAST(:value AS jsonb),
                            'openalex-work-parser-v1',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "source_record_id": source_id,
                        "value": json.dumps(value),
                    },
                )

        command.upgrade(alembic_config, "head")

        inspector = inspect(engine)
        assert "scope_assessment" in inspector.get_table_names()
        assert {
            "id",
            "source_record_id",
            "rule_version",
            "included",
            "reason",
                "evidence",
                "evaluated_at",
                "work_id",
                "work_linked_at",
                "work_link_reason",
            } == {
            column["name"]
            for column in inspector.get_columns("scope_assessment")
        }
        assert {
            constraint["name"]
            for constraint in inspector.get_unique_constraints(
                "scope_assessment"
            )
        } == {"uq_scope_assessment_record_rule"}
        assert "ck_scope_assessment_inclusion_consistency" in {
            constraint["name"]
            for constraint in inspector.get_check_constraints(
                "scope_assessment"
            )
        }

        with engine.connect() as connection:
            assessments = connection.execute(
                text(
                    """
                    SELECT
                        source_record_id,
                        rule_version,
                        included,
                        reason,
                        evidence,
                        evaluated_at,
                        work_id
                    FROM scope_assessment
                    ORDER BY source_record_id
                    """
                )
            ).mappings().all()
            assert len(assessments) == 2
            by_source = {
                assessment["source_record_id"]: assessment
                for assessment in assessments
            }
            included = by_source[included_source_id]
            assert included["rule_version"] == "agent-llm-scope-v1"
            assert included["included"] is True
            assert included["reason"] is None
            assert included["evidence"][0]["term"] == "agent"
            assert included["evaluated_at"] is not None
            assert included["work_id"] == work_id
            excluded = by_source[excluded_source_id]
            assert excluded["included"] is False
            assert excluded["reason"] == (
                "no_agent_llm_scope_term_match"
            )
            assert excluded["evidence"] == []
            assert excluded["evaluated_at"] is not None
            assert excluded["work_id"] is None
            assert connection.scalar(
                text(
                    """
                    SELECT count(*)
                    FROM field_assertion
                    WHERE field_name = 'scope'
                    """
                )
            ) == 2

        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W1000000003',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": post_upgrade_source_id,
                    "work_id": work_id,
                    "content_hash": "c" * 64,
                },
            )
            for rule_version in (
                "agent-llm-scope-v2",
                "agent-llm-scope-v3",
            ):
                connection.execute(
                    text(
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
                        VALUES (
                            :id,
                            :source_record_id,
                            :rule_version,
                            true,
                            NULL,
                            '[]'::jsonb,
                            :evaluated_at,
                            :work_id
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "source_record_id": post_upgrade_source_id,
                        "rule_version": rule_version,
                        "evaluated_at": datetime.now(UTC),
                        "work_id": work_id,
                    },
                )

        command.downgrade(
            alembic_config,
            "0003_openalex_source_provenance",
        )
        assert "scope_assessment" not in inspect(engine).get_table_names()
        with engine.connect() as connection:
            scope_assertions = connection.execute(
                text(
                    """
                    SELECT value, parser_version
                    FROM field_assertion
                    WHERE source_record_id = :source_record_id
                      AND field_name = 'scope'
                    ORDER BY value ->> 'rule_version'
                    """
                ),
                {"source_record_id": post_upgrade_source_id},
            ).mappings().all()
            assert [
                assertion["value"]["rule_version"]
                for assertion in scope_assertions
            ] == [
                "agent-llm-scope-v2",
                "agent-llm-scope-v3",
            ]
            assert len(
                {
                    assertion["parser_version"]
                    for assertion in scope_assertions
                }
            ) == 2
    finally:
        engine.dispose()


def test_0005_adds_projection_provenance_and_cascading_scope_work_fk(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "0004_versioned_scope_assessment")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    source_id = uuid4()
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (
                        id, canonical_key, title, source
                    )
                    VALUES (
                        :id, 'doi:10.1000/projection-migration',
                        'Projection migration fixture', 'openalex'
                    )
                    """
                ),
                {"id": work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source_updated_at,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W1000000004',
                        :content_hash,
                        '{}'::jsonb,
                        :source_updated_at,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": source_id,
                    "work_id": work_id,
                    "content_hash": "d" * 64,
                    "source_updated_at": datetime(
                        2026,
                        7,
                        15,
                        8,
                        0,
                        tzinfo=UTC,
                    ),
                },
            )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'agent-llm-v1',
                        true,
                        NULL,
                        '[]'::jsonb,
                        :evaluated_at,
                        :work_id
                    )
                    """
                ),
                {
                    "id": uuid4(),
                    "source_record_id": source_id,
                    "evaluated_at": datetime.now(UTC),
                    "work_id": work_id,
                },
            )

        command.upgrade(
            alembic_config,
            "0005_work_projection_integrity",
        )

        inspector = inspect(engine)
        work_columns = {
            column["name"] for column in inspector.get_columns("work")
        }
        assert {
            "projection_source",
            "projection_source_record_id",
            "projection_source_updated_at",
        } <= work_columns
        scope_work_fk = next(
            foreign_key
            for foreign_key in inspector.get_foreign_keys(
                "scope_assessment"
            )
            if foreign_key["constrained_columns"] == ["work_id"]
        )
        assert scope_work_fk["options"].get("ondelete") == "CASCADE"
        with engine.connect() as connection:
            projection = connection.execute(
                text(
                    """
                    SELECT
                        projection_source,
                        projection_source_record_id,
                        projection_source_updated_at
                    FROM work
                    WHERE id = :work_id
                    """
                ),
                {"work_id": work_id},
            ).one()
            assert projection == (
                "openalex",
                "W1000000004",
                datetime(2026, 7, 15, 8, 0, tzinfo=UTC),
            )

        with engine.begin() as connection:
            connection.execute(
                text("DELETE FROM work WHERE id = :work_id"),
                {"work_id": work_id},
            )
        with engine.connect() as connection:
            assert connection.scalar(
                text(
                    "SELECT count(*) FROM source_record WHERE id = :id"
                ),
                {"id": source_id},
            ) == 1
            assert connection.scalar(
                text(
                    """
                    SELECT count(*)
                    FROM scope_assessment
                    WHERE source_record_id = :source_record_id
                    """
                ),
                {"source_record_id": source_id},
            ) == 0

        command.downgrade(
            alembic_config,
            "0004_versioned_scope_assessment",
        )
        downgraded_columns = {
            column["name"] for column in inspect(engine).get_columns("work")
        }
        assert "projection_source_updated_at" not in downgraded_columns
    finally:
        engine.dispose()


def test_0006_resets_unverified_projection_and_replay_repairs_latest_snapshot(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    from paper_hub.connectors.openalex import OpenAlexConnector
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import (
        FieldAssertion,
        RecordStatus,
        SourceRecord,
        Work,
    )

    fixture = json.loads(
        (FIXTURES_ROOT / "openalex_works.json").read_text(
            encoding="utf-8"
        )
    )["results"][0]
    older_raw = json.loads(json.dumps(fixture))
    older_raw["title"] = "Stale LLM Agent Projection"
    older_raw["display_name"] = older_raw["title"]
    older_raw["abstract_inverted_index"] = {
        "Stale": [0],
        "LLM": [1],
        "agent": [2],
        "abstract": [3],
    }
    older_raw["updated_date"] = "2026-07-14T08:00:00"
    older_raw["is_retracted"] = False

    latest_raw = json.loads(json.dumps(fixture))
    latest_raw["title"] = "Latest Retracted LLM Agent Projection"
    latest_raw["display_name"] = latest_raw["title"]
    latest_raw["abstract_inverted_index"] = {
        "Latest": [0],
        "LLM": [1],
        "agent": [2],
        "abstract": [3],
    }
    latest_raw["updated_date"] = "2026-07-16T08:00:00"
    latest_raw["is_retracted"] = True

    older_record = OpenAlexConnector.parse_record(
        older_raw,
        retrieved_at=datetime(2026, 7, 16, 9, 5, tzinfo=UTC),
        http_status=200,
    )
    latest_record = OpenAlexConnector.parse_record(
        latest_raw,
        retrieved_at=datetime(2026, 7, 16, 9, 0, tzinfo=UTC),
        http_status=200,
    )

    command.upgrade(alembic_config, "0004_versioned_scope_assessment")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (
                        id,
                        canonical_key,
                        title,
                        abstract,
                        status,
                        source
                    )
                    VALUES (
                        :id,
                        :canonical_key,
                        :title,
                        :abstract,
                        'active',
                        'openalex'
                    )
                    """
                ),
                {
                    "id": work_id,
                    "canonical_key": latest_record.parsed.canonical_key,
                    "title": older_record.parsed.title,
                    "abstract": older_record.parsed.abstract,
                },
            )
            for source_id, record in (
                (uuid4(), latest_record),
                (uuid4(), older_record),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source_updated_at,
                            http_status,
                            source,
                            retrieved_at
                        )
                        VALUES (
                            :id,
                            :work_id,
                            :source_record_id,
                            :content_hash,
                            CAST(:raw_payload AS jsonb),
                            :source_updated_at,
                            :http_status,
                            :source,
                            :retrieved_at
                        )
                        """
                    ),
                    {
                        "id": source_id,
                        "work_id": work_id,
                        "source_record_id": record.source_record_id,
                        "content_hash": record.content_hash,
                        "raw_payload": json.dumps(record.raw_payload),
                        "source_updated_at": record.source_updated_at,
                        "http_status": record.http_status,
                        "source": record.source,
                        "retrieved_at": record.retrieved_at,
                    },
                )
                connection.execute(
                    text(
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
                        VALUES (
                            :id,
                            :source_record_id,
                            :rule_version,
                            true,
                            NULL,
                            '[]'::jsonb,
                            :evaluated_at,
                            :work_id
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "source_record_id": source_id,
                        "rule_version": (
                            record.parsed.scope.rule_version
                        ),
                        "evaluated_at": record.retrieved_at,
                        "work_id": work_id,
                    },
                )

        command.upgrade(
            alembic_config,
            "0005_work_projection_integrity",
        )
        with engine.connect() as connection:
            assert connection.execute(
                text(
                    """
                    SELECT
                        title,
                        abstract,
                        status,
                        projection_source,
                        projection_source_record_id,
                        projection_source_updated_at
                    FROM work
                    WHERE id = :work_id
                    """
                ),
                {"work_id": work_id},
            ).one() == (
                older_record.parsed.title,
                older_record.parsed.abstract,
                "active",
                latest_record.source,
                latest_record.source_record_id,
                latest_record.source_updated_at,
            )

        command.upgrade(alembic_config, "head")
        with engine.connect() as connection:
            assert connection.execute(
                text(
                    """
                    SELECT
                        projection_source,
                        projection_source_record_id,
                        projection_source_updated_at
                    FROM work
                    WHERE id = :work_id
                    """
                ),
                {"work_id": work_id},
            ).one() == (None, None, None)

        with Session(engine) as session:
            repaired = IngestionService(session).ingest(latest_record)
            session.commit()
            work = session.get(Work, work_id)

            assert repaired.status == "updated"
            assert repaired.work_id == work_id
            assert work.title == latest_record.parsed.title
            assert work.abstract == latest_record.parsed.abstract
            assert work.status == RecordStatus.RETRACTED
            assert work.projection_source == latest_record.source
            projection = session.get(
                SourceRecord,
                work.projection_source_record_id,
            )
            assert projection is not None
            assert projection.source_record_id == (
                latest_record.source_record_id
            )
            assert projection.content_hash == latest_record.content_hash
            assert (
                work.projection_source_updated_at
                == latest_record.source_updated_at
            )
            assert work.source == latest_record.source
            assert work.retrieved_at == latest_record.retrieved_at
            assert (
                session.scalar(
                    select(func.count()).select_from(SourceRecord)
                )
                == 2
            )
            assert (
                session.scalar(
                    select(func.count()).select_from(FieldAssertion)
                )
                > 0
            )

            replay = IngestionService(session).ingest(latest_record)
            session.commit()
            assert replay.status == "unchanged"
    finally:
        engine.dispose()


def test_0007_migrates_repository_ownership_to_many_to_many_and_downgrades_safely(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "0006_reset_work_projections")
    engine = create_engine(clean_postgres_url)
    first_work_id = uuid4()
    second_work_id = uuid4()
    first_repository_id = uuid4()
    second_repository_id = uuid4()
    try:
        with engine.begin() as connection:
            for work_id, canonical_key in (
                (first_work_id, "doi:10.1000/repo-migration-one"),
                (second_work_id, "doi:10.1000/repo-migration-two"),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (
                            id, canonical_key, title, source
                        )
                        VALUES (
                            :id, :canonical_key,
                            'Repository migration fixture', 'test'
                        )
                        """
                    ),
                    {"id": work_id, "canonical_key": canonical_key},
                )
            for repository_id, work_id, suffix in (
                (first_repository_id, first_work_id, "one"),
                (second_repository_id, second_work_id, "two"),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO code_repository (
                            id, work_id, provider, repository_name,
                            repository_url, normalized_url, source
                        )
                        VALUES (
                            :id, :work_id, 'github', :repository_name,
                            :repository_url, :normalized_url, 'test'
                        )
                        """
                    ),
                    {
                        "id": repository_id,
                        "work_id": work_id,
                        "repository_name": f"repo-{suffix}",
                        "repository_url": (
                            f"https://github.com/example/repo-{suffix}"
                        ),
                        "normalized_url": (
                            f"https://github.com/example/repo-{suffix}"
                        ),
                    },
                )

        command.upgrade(
            alembic_config,
            "0007_repo_work_many_to_many",
        )

        inspector = inspect(engine)
        assert "work_id" not in {
            column["name"]
            for column in inspector.get_columns("code_repository")
        }
        assert set(
            inspector.get_pk_constraint(
                "work_code_repository"
            )["constrained_columns"]
        ) == {"work_id", "code_repository_id"}
        foreign_keys = {
            tuple(foreign_key["constrained_columns"]): (
                foreign_key["referred_table"],
                foreign_key["options"].get("ondelete"),
            )
            for foreign_key in inspector.get_foreign_keys(
                "work_code_repository"
            )
        }
        assert foreign_keys == {
            ("work_id",): ("work", "CASCADE"),
            ("code_repository_id",): (
                "code_repository",
                "CASCADE",
            ),
        }
        with engine.connect() as connection:
            assert set(
                connection.execute(
                    text(
                        """
                        SELECT work_id, code_repository_id
                        FROM work_code_repository
                        """
                    )
                ).all()
            ) == {
                (first_work_id, first_repository_id),
                (second_work_id, second_repository_id),
            }

        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work_code_repository (
                        work_id, code_repository_id
                    )
                    VALUES (:work_id, :repository_id)
                    """
                ),
                {
                    "work_id": second_work_id,
                    "repository_id": first_repository_id,
                },
            )

        with pytest.raises(
            DBAPIError,
            match=(
                "cannot downgrade code repositories with "
                "zero or multiple work associations"
            ),
        ):
            command.downgrade(
                alembic_config,
                "0006_reset_work_projections",
            )

        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    DELETE FROM work_code_repository
                    WHERE work_id = :work_id
                      AND code_repository_id = :repository_id
                    """
                ),
                {
                    "work_id": second_work_id,
                    "repository_id": first_repository_id,
                },
            )
        command.downgrade(
            alembic_config,
            "0006_reset_work_projections",
        )

        downgraded_columns = {
            column["name"]
            for column in inspect(engine).get_columns("code_repository")
        }
        assert "work_id" in downgraded_columns
        assert "work_code_repository" not in inspect(engine).get_table_names()
        with engine.connect() as connection:
            assert connection.execute(
                text(
                    """
                    SELECT id, work_id
                    FROM code_repository
                    ORDER BY normalized_url
                    """
                )
            ).all() == [
                (first_repository_id, first_work_id),
                (second_repository_id, second_work_id),
            ]
    finally:
        engine.dispose()


def test_c744_physical_0001_upgrades_to_current_head(
    alembic_config,
    clean_postgres_url: str,
    tmp_path: Path,
) -> None:
    _install_historical_schema(
        tmp_path,
        clean_postgres_url,
        "c74476a",
    )
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    ranking_id = uuid4()
    now = datetime(2026, 7, 15, 8, 30, tzinfo=UTC)
    try:
        before = inspect(engine)
        assert {
            "work_id",
            "topic_id",
            "method_id",
        } <= {
            column["name"]
            for column in before.get_columns("ranking_snapshot")
        }
        assert "subject_id" not in {
            column["name"]
            for column in before.get_columns("ranking_snapshot")
        }
        assert (
            "ck_metric_snapshot_ck_metric_snapshot_single_target"
            in {
                constraint["name"]
                for constraint in before.get_check_constraints(
                    "metric_snapshot"
                )
            }
        )

        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (id, canonical_key, title, source)
                    VALUES (
                        :id, 'doi:10.1000/c744-variant',
                        'C744 variant', 'test'
                    )
                    """
                ),
                {"id": work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO ranking_snapshot (
                        id, ranking_name, work_id, rank_position, score,
                        window_days, computed_at, formula_version,
                        coverage, source
                    )
                    VALUES (
                        :id, 'weekly-c744', :work_id, 1, 1,
                        7, :computed_at, 'v1', '{}'::jsonb, 'test'
                    )
                    """
                ),
                {
                    "id": ranking_id,
                    "work_id": work_id,
                    "computed_at": now,
                },
            )

        command.upgrade(alembic_config, "head")

        with engine.connect() as connection:
            final_inspector = inspect(connection)
            assert connection.scalar(
                text("SELECT count(*) FROM ranking_snapshot WHERE id = :id"),
                {"id": ranking_id},
            ) == 1
            assert {
                column["name"]
                for column in final_inspector.get_columns(
                    "external_identifier"
                )
            }.isdisjoint({"paper_version_id", "source_record_id"})
            metric_check_names = {
                constraint["name"]
                for constraint in final_inspector.get_check_constraints(
                    "metric_snapshot"
                )
            }
            assert "ck_metric_snapshot_single_target" in metric_check_names
            assert (
                "ck_metric_snapshot_ck_metric_snapshot_single_target"
                not in metric_check_names
            )

            from paper_hub.models import Base

            context = MigrationContext.configure(
                connection,
                opts={
                    "compare_type": True,
                    "compare_server_default": True,
                },
            )
            assert compare_metadata(context, Base.metadata) == []
    finally:
        engine.dispose()


@pytest.mark.parametrize("variant", ["d1aacd8", "c74476a"])
def test_offline_head_sql_applies_to_historical_schema_variants(
    clean_postgres_url: str,
    tmp_path: Path,
    variant: str,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    from paper_hub.config import get_settings

    monkeypatch.setenv("DATABASE_URL", clean_postgres_url)
    get_settings.cache_clear()
    _install_historical_schema(
        tmp_path,
        clean_postgres_url,
        variant,
    )
    offline_sql = _offline_head_upgrade_sql()
    psycopg_url = clean_postgres_url.replace(
        "postgresql+psycopg://",
        "postgresql://",
    )

    with psycopg.connect(psycopg_url, autocommit=True) as connection:
        connection.execute(offline_sql)

    engine = create_engine(clean_postgres_url)
    try:
        with engine.connect() as connection:
            from paper_hub.models import Base

            assert connection.scalar(
                text("SELECT version_num FROM alembic_version")
            ) == "0011_search_snapshot_revision"
            context = MigrationContext.configure(
                connection,
                opts={
                    "compare_type": True,
                    "compare_server_default": True,
                },
            )
            assert compare_metadata(context, Base.metadata) == []
    finally:
        engine.dispose()
        get_settings.cache_clear()


def test_0002_rejects_inconsistent_legacy_source_ownership(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "0001_initial_schema")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    other_work_id = uuid4()
    version_id = uuid4()
    try:
        with engine.begin() as connection:
            for item_id, canonical_key in (
                (work_id, "doi:10.1000/legacy-owner"),
                (other_work_id, "doi:10.1000/legacy-other"),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (id, canonical_key, title, source)
                        VALUES (:id, :canonical_key, 'Legacy work', 'test')
                        """
                    ),
                    {"id": item_id, "canonical_key": canonical_key},
                )
            connection.execute(
                text(
                    """
                    INSERT INTO paper_version (
                        id, work_id, version_label, version_type, source
                    )
                    VALUES (:id, :work_id, 'v1', 'preprint', 'test')
                    """
                ),
                {"id": version_id, "work_id": work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id, work_id, paper_version_id, source_record_id,
                        content_hash, raw_payload, source
                    )
                    VALUES (
                        :id, :work_id, :paper_version_id, 'legacy-conflict',
                        :content_hash, '{}'::jsonb, 'test'
                    )
                    """
                ),
                {
                    "id": uuid4(),
                    "work_id": other_work_id,
                    "paper_version_id": version_id,
                    "content_hash": "f" * 64,
                },
            )

        with pytest.raises(
            DBAPIError,
            match="inconsistent source record ownership",
        ):
            command.upgrade(alembic_config, "head")
    finally:
        engine.dispose()


def test_database_constraints_reject_invalid_writes(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    other_work_id = uuid4()
    version_id = uuid4()
    source_id = uuid4()
    topic_id = uuid4()
    now = datetime(2026, 7, 15, 8, 30, tzinfo=UTC)
    try:
        with engine.begin() as connection:
            for item_id, canonical_key in (
                (work_id, "doi:10.1000/valid"),
                (other_work_id, "doi:10.1000/other"),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (id, canonical_key, title, source)
                        VALUES (:id, :canonical_key, 'Valid work', 'test')
                        """
                    ),
                    {"id": item_id, "canonical_key": canonical_key},
                )
            connection.execute(
                text(
                    """
                    INSERT INTO paper_version (
                        id, work_id, version_label, version_number,
                        version_type, source
                    )
                    VALUES (:id, :work_id, 'v1', 1, 'preprint', 'test')
                    """
                ),
                {"id": version_id, "work_id": work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO topic (
                        id, name, normalized_name, source
                    )
                    VALUES (:id, 'Agents', 'agents', 'test')
                    """
                ),
                {"id": topic_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id, paper_version_id, source_record_id,
                        content_hash, raw_payload, source
                    )
                    VALUES (
                        :id, :paper_version_id, 'valid-source',
                        :content_hash, '{}'::jsonb, 'test'
                    )
                    """
                ),
                {
                    "id": source_id,
                    "paper_version_id": version_id,
                    "content_hash": "b" * 64,
                },
            )

        _assert_integrity_error(
            engine,
            """
            INSERT INTO work (id, canonical_key, title, source)
            VALUES (:id, 'doi:not valid', 'Invalid', 'test')
            """,
            {"id": uuid4()},
        )
        _assert_integrity_error(
            engine,
            """
            INSERT INTO paper_version (
                id, work_id, version_label, version_number, version_type, source
            )
            VALUES (:id, :work_id, 'bad', 0, 'preprint', 'test')
            """,
            {"id": uuid4(), "work_id": work_id},
        )
        _assert_integrity_error(
            engine,
            """
            INSERT INTO source_record (
                id, work_id, paper_version_id, source_record_id,
                content_hash, raw_payload, source
            )
            VALUES (
                :id, :work_id, :paper_version_id, 'dual-owner',
                :content_hash, '{}'::jsonb, 'test'
            )
            """,
            {
                "id": uuid4(),
                "work_id": work_id,
                "paper_version_id": version_id,
                "content_hash": "c" * 64,
            },
        )
        _assert_integrity_error(
            engine,
            """
            INSERT INTO field_assertion (
                id, source_record_id, field_name, value,
                parser_version, confidence, source
            )
            VALUES (
                :id, :source_record_id, 'title',
                '\"bad confidence\"'::jsonb, 'v1', 1.1, 'test'
            )
            """,
            {"id": uuid4(), "source_record_id": source_id},
        )
        _assert_integrity_error(
            engine,
            """
            INSERT INTO metric_snapshot (
                id, work_id, metric_name, metric_value,
                measured_at, window_days, source
            )
            VALUES (
                :id, :work_id, 'citations', 1,
                :measured_at, 0, 'test'
            )
            """,
            {"id": uuid4(), "work_id": work_id, "measured_at": now},
        )
        _assert_integrity_error(
            engine,
            """
            INSERT INTO ranking_snapshot (
                id, ranking_name, rank_position, score, window_days,
                computed_at, formula_version, coverage, source
            )
            VALUES (
                :id, 'weekly', 1, 1, 7,
                :computed_at, 'v1', '{}'::jsonb, 'test'
            )
            """,
            {"id": uuid4(), "computed_at": now},
        )
        _assert_integrity_error(
            engine,
            """
            INSERT INTO ranking_snapshot (
                id, ranking_name, work_id, topic_id, rank_position,
                score, window_days, computed_at,
                formula_version, coverage, source
            )
            VALUES (
                :id, 'weekly', :work_id, :topic_id, 1,
                1, 7, :computed_at,
                'v1', '{}'::jsonb, 'test'
            )
            """,
            {
                "id": uuid4(),
                "work_id": work_id,
                "topic_id": topic_id,
                "computed_at": now,
            },
        )
        _assert_integrity_error(
            engine,
            """
            INSERT INTO ranking_snapshot (
                id, ranking_name, work_id, rank_position, score, window_days,
                computed_at, formula_version, coverage, source
            )
            VALUES (
                :id, 'weekly', :work_id, 0, 1, 7,
                :computed_at, 'v1', '{}'::jsonb, 'test'
            )
            """,
            {"id": uuid4(), "work_id": work_id, "computed_at": now},
        )
        _assert_integrity_error(
            engine,
            """
            INSERT INTO ranking_snapshot (
                id, ranking_name, work_id, rank_position, score, window_days,
                computed_at, formula_version, coverage, source
            )
            VALUES (
                :id, 'weekly', :work_id, 1, 1, 0,
                :computed_at, 'v1', '{}'::jsonb, 'test'
            )
            """,
            {"id": uuid4(), "work_id": work_id, "computed_at": now},
        )
    finally:
        engine.dispose()


def test_0008_to_0009_links_legacy_exclusions_and_downgrades_safely(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "0008_search_api_indexes")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    other_work_id = uuid4()
    source_id = uuid4()
    assessment_id = uuid4()
    legacy_unbound_source_id = uuid4()
    legacy_unbound_assessment_id = uuid4()
    unbound_source_id = uuid4()
    unbound_assessment_id = uuid4()
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (id, canonical_key, title, source)
                    VALUES (
                        :id,
                        'doi:10.1000/logical-source-scope',
                        'Logical source scope',
                        'openalex'
                    )
                    """
                ),
                {"id": work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W9000000001',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": source_id,
                    "work_id": work_id,
                    "content_hash": "9" * 64,
                },
            )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v2',
                        false,
                        'excluded',
                        '[]'::jsonb,
                        :evaluated_at,
                        NULL
                    )
                    """
                ),
                {
                    "id": assessment_id,
                    "source_record_id": source_id,
                    "evaluated_at": datetime.now(UTC),
                },
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        NULL,
                        'W9000000001',
                        :content_hash,
                        '{"legacy_new_hash": true}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": legacy_unbound_source_id,
                    "content_hash": "7" * 64,
                },
            )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v2',
                        false,
                        'excluded',
                        '[]'::jsonb,
                        :evaluated_at,
                        NULL
                    )
                    """
                ),
                {
                    "id": legacy_unbound_assessment_id,
                    "source_record_id": legacy_unbound_source_id,
                    "evaluated_at": datetime.now(UTC),
                },
            )

        command.upgrade(alembic_config, "0009_logical_source_scope")

        with engine.connect() as connection:
            for linked_assessment_id in (
                assessment_id,
                legacy_unbound_assessment_id,
            ):
                assert connection.scalar(
                    text(
                        """
                        SELECT work_id
                        FROM scope_assessment
                        WHERE id = :id
                        """
                    ),
                    {"id": linked_assessment_id},
                ) == work_id
        assert "ix_scope_assessment_work_evaluated_at" in {
            index["name"]
            for index in inspect(engine).get_indexes("scope_assessment")
        }
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (id, canonical_key, title, source)
                    VALUES (
                        :id,
                        'doi:10.1000/logical-source-other',
                        'Logical source other work',
                        'openalex'
                    )
                    """
                ),
                {"id": other_work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        NULL,
                        'W9000000001',
                        :content_hash,
                        '{"new": true}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": unbound_source_id,
                    "content_hash": "8" * 64,
                },
            )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v2',
                        false,
                        'excluded',
                        '[]'::jsonb,
                        :evaluated_at,
                        :work_id
                    )
                    """
                ),
                {
                    "id": unbound_assessment_id,
                    "source_record_id": unbound_source_id,
                    "evaluated_at": datetime.now(UTC),
                    "work_id": work_id,
                },
            )
        with pytest.raises(
            DBAPIError,
            match="scope assessment work must match source record work",
        ):
            with engine.begin() as connection:
                connection.execute(
                    text(
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
                        VALUES (
                            :id,
                            :source_record_id,
                            'scope-v3',
                            true,
                            NULL,
                            '[]'::jsonb,
                            :evaluated_at,
                            :work_id
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "source_record_id": unbound_source_id,
                        "evaluated_at": datetime.now(UTC),
                        "work_id": work_id,
                    },
                )
        with pytest.raises(
            DBAPIError,
            match="scope assessment work must match logical source work",
        ):
            with engine.begin() as connection:
                connection.execute(
                    text(
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
                        VALUES (
                            :id,
                            :source_record_id,
                            'scope-v4',
                            false,
                            'excluded',
                            '[]'::jsonb,
                            :evaluated_at,
                            :work_id
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "source_record_id": unbound_source_id,
                        "evaluated_at": datetime.now(UTC),
                        "work_id": other_work_id,
                    },
                )

        command.downgrade(alembic_config, "0008_search_api_indexes")

        with engine.connect() as connection:
            for linked_assessment_id in (
                assessment_id,
                legacy_unbound_assessment_id,
            ):
                assert connection.scalar(
                    text(
                        """
                        SELECT work_id
                        FROM scope_assessment
                        WHERE id = :id
                        """
                    ),
                    {"id": linked_assessment_id},
                ) is None
            assert connection.scalar(
                text(
                    """
                    SELECT work_id
                    FROM scope_assessment
                    WHERE id = :id
                    """
                ),
                {"id": unbound_assessment_id},
            ) is None

        command.upgrade(alembic_config, "0009_logical_source_scope")
    finally:
        engine.dispose()


def test_0009_rejects_ambiguous_logical_source_owners(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "0008_search_api_indexes")
    engine = create_engine(clean_postgres_url)
    try:
        with engine.begin() as connection:
            for index, work_id in enumerate((uuid4(), uuid4()), start=1):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (
                            id,
                            canonical_key,
                            title,
                            source
                        )
                        VALUES (
                            :id,
                            :canonical_key,
                            'Ambiguous logical source',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": work_id,
                        "canonical_key": (
                            f"doi:10.1000/ambiguous-logical-source-{index}"
                        ),
                    },
                )
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source
                        )
                        VALUES (
                            :id,
                            :work_id,
                            'W9000000002',
                            :content_hash,
                            '{}'::jsonb,
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "work_id": work_id,
                        "content_hash": str(index) * 64,
                    },
                )

        with pytest.raises(
            DBAPIError,
            match="logical source identity maps to multiple works",
        ):
            command.upgrade(
                alembic_config,
                "0009_logical_source_scope",
            )
    finally:
        engine.dispose()


def test_scope_trigger_rejects_conflicting_assessment_only_logical_owner(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    first_work_id = uuid4()
    second_work_id = uuid4()
    first_source_id = uuid4()
    second_source_id = uuid4()
    try:
        with engine.begin() as connection:
            for index, work_id in enumerate(
                (first_work_id, second_work_id),
                start=1,
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (
                            id,
                            canonical_key,
                            title,
                            source
                        )
                        VALUES (
                            :id,
                            :canonical_key,
                            'Assessment-only logical source owner',
                            'crossref'
                        )
                        """
                    ),
                    {
                        "id": work_id,
                        "canonical_key": (
                            f"doi:10.1000/assessment-owner-{index}"
                        ),
                    },
                )
            for source_id, content_hash in (
                (first_source_id, "a" * 64),
                (second_source_id, "b" * 64),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source
                        )
                        VALUES (
                            :id,
                            NULL,
                            'W9000000003',
                            :content_hash,
                            '{}'::jsonb,
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": source_id,
                        "content_hash": content_hash,
                    },
                )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v1',
                        false,
                        'excluded',
                        '[]'::jsonb,
                        :evaluated_at,
                        :work_id
                    )
                    """
                ),
                {
                    "id": uuid4(),
                    "source_record_id": first_source_id,
                    "evaluated_at": datetime.now(UTC),
                    "work_id": first_work_id,
                },
            )

        with pytest.raises(
            DBAPIError,
            match="source record work must match logical source assessments",
        ):
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        UPDATE source_record
                        SET work_id = :work_id
                        WHERE id = :source_record_id
                        """
                    ),
                    {
                        "source_record_id": second_source_id,
                        "work_id": second_work_id,
                    },
                )

        with pytest.raises(
            DBAPIError,
            match="scope assessment work must match logical source work",
        ):
            with engine.begin() as connection:
                connection.execute(
                    text(
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
                        VALUES (
                            :id,
                            :source_record_id,
                            'scope-v2',
                            false,
                            'excluded',
                            '[]'::jsonb,
                            :evaluated_at,
                            :work_id
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "source_record_id": second_source_id,
                        "evaluated_at": datetime.now(UTC),
                        "work_id": second_work_id,
                    },
                )
    finally:
        engine.dispose()


def test_scope_assessment_constraints_reject_inconsistent_rows(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    other_work_id = uuid4()
    source_id = uuid4()
    assessment_id = uuid4()
    try:
        assert "scope_assessment" in inspect(engine).get_table_names()
        with engine.begin() as connection:
            for item_id, canonical_key in (
                (work_id, "doi:10.1000/scope-valid"),
                (other_work_id, "doi:10.1000/scope-other"),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (id, canonical_key, title, source)
                        VALUES (:id, :canonical_key, 'Scope work', 'test')
                        """
                    ),
                    {"id": item_id, "canonical_key": canonical_key},
                )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'scope-source',
                        :content_hash,
                        '{}'::jsonb,
                        'test'
                    )
                    """
                ),
                {
                    "id": source_id,
                    "work_id": work_id,
                    "content_hash": "d" * 64,
                },
            )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v1',
                        true,
                        NULL,
                        '[]'::jsonb,
                        :evaluated_at,
                        :work_id
                    )
                    """
                ),
                {
                    "id": assessment_id,
                    "source_record_id": source_id,
                    "evaluated_at": datetime.now(UTC),
                    "work_id": work_id,
                },
            )

        _assert_integrity_error(
            engine,
            """
            INSERT INTO scope_assessment (
                id, source_record_id, rule_version, included,
                reason, evidence, evaluated_at, work_id
            )
            VALUES (
                :id, :source_record_id, 'scope-v1', true,
                NULL, '[]'::jsonb, :evaluated_at, :work_id
            )
            """,
            {
                "id": uuid4(),
                "source_record_id": source_id,
                "evaluated_at": datetime.now(UTC),
                "work_id": work_id,
            },
        )
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO scope_assessment (
                        id, source_record_id, rule_version, included,
                        reason, evidence, evaluated_at, work_id
                    )
                    VALUES (
                        :id, :source_record_id, 'scope-v2', false,
                        'excluded', '[]'::jsonb, :evaluated_at, :work_id
                    )
                    """
                ),
                {
                    "id": uuid4(),
                    "source_record_id": source_id,
                    "evaluated_at": datetime.now(UTC),
                    "work_id": work_id,
                },
            )
        with pytest.raises(
            DBAPIError,
            match="scope assessment work must match source record work",
        ):
            with engine.begin() as connection:
                connection.execute(
                    text(
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
                        VALUES (
                            :id,
                            :source_record_id,
                            'scope-v3',
                            false,
                            'excluded',
                            '[]'::jsonb,
                            :evaluated_at,
                            :work_id
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "source_record_id": source_id,
                        "evaluated_at": datetime.now(UTC),
                        "work_id": other_work_id,
                    },
                )
        with pytest.raises(
            DBAPIError,
            match="scope assessment work must match source record work",
        ):
            with engine.begin() as connection:
                connection.execute(
                    text(
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
                        VALUES (
                            :id,
                            :source_record_id,
                            'scope-v4',
                            true,
                            NULL,
                            '[]'::jsonb,
                            :evaluated_at,
                            :work_id
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "source_record_id": source_id,
                        "evaluated_at": datetime.now(UTC),
                        "work_id": other_work_id,
                    },
                )
        with pytest.raises(
            DBAPIError,
            match="scope assessments are immutable",
        ):
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        UPDATE scope_assessment
                        SET evidence = '[{"term": "changed"}]'::jsonb
                        WHERE id = :id
                        """
                    ),
                    {"id": assessment_id},
                )
        with pytest.raises(
            DBAPIError,
            match="source record work must match included scope assessments",
        ):
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        UPDATE source_record
                        SET work_id = :work_id
                        WHERE id = :source_record_id
                        """
                    ),
                    {
                        "work_id": other_work_id,
                        "source_record_id": source_id,
                    },
                )
    finally:
        engine.dispose()


def test_0010_projection_tiebreak_and_scope_trigger_survive_roundtrip(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    from paper_hub.repositories import (
        PaperRepository,
        PaperSearchFilters,
        slug_for_canonical_key,
    )

    command.upgrade(alembic_config, "0009_logical_source_scope")
    engine = create_engine(clean_postgres_url)
    work_id = UUID("00000000-0000-0000-0000-000000000100")
    older_source_id = UUID("00000000-0000-0000-0000-000000000101")
    newer_source_id = UUID("00000000-0000-0000-0000-000000000102")
    excluded_source_id = UUID("00000000-0000-0000-0000-000000000103")
    excluded_assessment_id = UUID(
        "00000000-0000-0000-0000-000000000104"
    )
    older_topic_id = UUID("00000000-0000-0000-0000-000000000105")
    newer_topic_id = UUID("00000000-0000-0000-0000-000000000106")
    metric_id = UUID("00000000-0000-0000-0000-000000000107")
    method_id = UUID("00000000-0000-0000-0000-000000000108")
    dataset_id = UUID("00000000-0000-0000-0000-000000000109")
    benchmark_id = UUID("00000000-0000-0000-0000-000000000110")
    repository_id = UUID("00000000-0000-0000-0000-000000000111")
    source_updated_at = datetime(2026, 7, 15, 8, tzinfo=UTC)
    older_retrieved_at = datetime(2026, 7, 15, 9, tzinfo=UTC)
    newer_retrieved_at = datetime(2026, 7, 15, 10, tzinfo=UTC)
    parser_upgrade_created_at = datetime.now(UTC) + timedelta(hours=1)
    canonical_key = "doi:10.1000/projection-roundtrip"

    def assert_exact_projection() -> None:
        with engine.connect() as connection:
            projection = connection.execute(
                text(
                    """
                    SELECT
                        projection_source_record_id,
                        title,
                        abstract,
                        publication_date,
                        retrieved_at
                    FROM work
                    WHERE id = :work_id
                    """
                ),
                {"work_id": work_id},
            ).one()
        assert projection == (
            newer_source_id,
            "Newest parser title",
            "Newest parser abstract",
            datetime(2026, 7, 3).date(),
            newer_retrieved_at,
        )
        with engine.connect() as connection:
            metric = connection.execute(
                text(
                    """
                    SELECT
                        source_record_id,
                        metric_value,
                        metadata ->> 'source_record_id',
                        retrieved_at
                    FROM metric_snapshot
                    WHERE id = :metric_id
                    """
                ),
                {"metric_id": metric_id},
            ).one()
        assert metric.source_record_id == newer_source_id
        assert int(metric.metric_value) == 20
        assert metric[2] == str(newer_source_id)
        assert metric.retrieved_at == newer_retrieved_at
        with Session(engine) as session:
            detail = PaperRepository(session).detail(
                slug_for_canonical_key(canonical_key)
            )
        assert detail is not None
        assert detail["title"] == "Newest parser title"
        assert detail["type"] == "article"
        assert detail["authors"] == ["New Author"]
        assert {
            topic["normalized_name"] for topic in detail["topics"]
        } == {"new migration topic"}
        with Session(engine) as session:
            repository = PaperRepository(session)
            old_topic = repository.search(
                PaperSearchFilters(
                    topics=("old migration topic",),
                )
            )
            new_topic = repository.search(
                PaperSearchFilters(
                    topics=("new migration topic",),
                )
            )
        assert old_topic["total"] == 0
        assert new_topic["total"] == 1
        assert {
            item["value"]
            for item in new_topic["facets"]["topics"]
        } == {"new migration topic"}
        with engine.connect() as connection:
            association_counts = connection.execute(
                text(
                    """
                    SELECT
                        (
                            SELECT count(*)
                            FROM work_method
                            WHERE work_id = :work_id
                        ) AS methods,
                        (
                            SELECT count(*)
                            FROM work_dataset
                            WHERE work_id = :work_id
                        ) AS datasets,
                        (
                            SELECT count(*)
                            FROM work_benchmark
                            WHERE work_id = :work_id
                        ) AS benchmarks,
                        (
                            SELECT count(*)
                            FROM work_code_repository
                            WHERE work_id = :work_id
                        ) AS repositories
                    """
                ),
                {"work_id": work_id},
            ).one()
        assert association_counts == (0, 0, 0, 1)

    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (
                        id,
                        canonical_key,
                        title,
                        abstract,
                        publication_date,
                        projection_source,
                        projection_source_record_id,
                        projection_source_updated_at,
                        source,
                        retrieved_at
                    )
                    VALUES (
                        :id,
                        :canonical_key,
                        'Legacy projection title',
                        'Legacy projection abstract',
                        DATE '2026-07-01',
                        'openalex',
                        'W-PROJECTION-ROUNDTRIP',
                        :source_updated_at,
                        'openalex',
                        :older_retrieved_at
                    )
                    """
                ),
                {
                    "id": work_id,
                    "canonical_key": canonical_key,
                    "source_updated_at": source_updated_at,
                    "older_retrieved_at": older_retrieved_at,
                },
            )
            for source_id, retrieved_at, content_hash in (
                (older_source_id, older_retrieved_at, "a" * 64),
                (newer_source_id, newer_retrieved_at, "b" * 64),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source_updated_at,
                            source,
                            retrieved_at
                        )
                        VALUES (
                            :id,
                            :work_id,
                            'W-PROJECTION-ROUNDTRIP',
                            :content_hash,
                            '{}'::jsonb,
                            :source_updated_at,
                            'openalex',
                            :retrieved_at
                        )
                        """
                    ),
                    {
                        "id": source_id,
                        "work_id": work_id,
                        "content_hash": content_hash,
                        "source_updated_at": source_updated_at,
                        "retrieved_at": retrieved_at,
                    },
                )
                connection.execute(
                    text(
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
                        VALUES (
                            :id,
                            :source_record_id,
                            'scope-v1',
                            true,
                            NULL,
                            '[]'::jsonb,
                            :evaluated_at,
                            :work_id
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "source_record_id": source_id,
                        "evaluated_at": retrieved_at,
                        "work_id": work_id,
                    },
                )
            for topic_id, name, source_url in (
                (
                    older_topic_id,
                    "Old Migration Topic",
                    "https://openalex.org/T-MIGRATION-OLD",
                ),
                (
                    newer_topic_id,
                    "New Migration Topic",
                    "https://openalex.org/T-MIGRATION-NEW",
                ),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO topic (
                            id,
                            name,
                            normalized_name,
                            description,
                            source,
                            source_url
                        )
                        VALUES (
                            :id,
                            :name,
                            :normalized_name,
                            NULL,
                            'openalex',
                            :source_url
                        )
                        """
                    ),
                    {
                        "id": topic_id,
                        "name": name,
                        "normalized_name": name.casefold(),
                        "source_url": source_url,
                    },
                )
                connection.execute(
                    text(
                        """
                        INSERT INTO work_topic (work_id, topic_id)
                        VALUES (:work_id, :topic_id)
                        """
                    ),
                    {"work_id": work_id, "topic_id": topic_id},
                )
            for table_name, association_table, entity_id, name in (
                (
                    "method",
                    "work_method",
                    method_id,
                    "Legacy Projection Method",
                ),
                (
                    "dataset",
                    "work_dataset",
                    dataset_id,
                    "Legacy Projection Dataset",
                ),
                (
                    "benchmark",
                    "work_benchmark",
                    benchmark_id,
                    "Legacy Projection Benchmark",
                ),
            ):
                connection.execute(
                    text(
                        f"""
                        INSERT INTO {table_name} (
                            id,
                            name,
                            normalized_name,
                            description,
                            source
                        )
                        VALUES (
                            :id,
                            :name,
                            :normalized_name,
                            NULL,
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": entity_id,
                        "name": name,
                        "normalized_name": name.casefold(),
                    },
                )
                connection.execute(
                    text(
                        f"""
                        INSERT INTO {association_table} (
                            work_id,
                            {table_name}_id
                        )
                        VALUES (:work_id, :entity_id)
                        """
                    ),
                    {
                        "work_id": work_id,
                        "entity_id": entity_id,
                    },
                )
            connection.execute(
                text(
                    """
                    INSERT INTO code_repository (
                        id,
                        provider,
                        repository_name,
                        repository_url,
                        normalized_url,
                        is_official,
                        source
                    )
                    VALUES (
                        :id,
                        'github',
                        'example/projection-history',
                        'https://github.test/example/projection-history',
                        'https://github.test/example/projection-history',
                        true,
                        'openalex'
                    )
                    """
                ),
                {"id": repository_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO work_code_repository (
                        work_id,
                        code_repository_id
                    )
                    VALUES (:work_id, :repository_id)
                    """
                ),
                {
                    "work_id": work_id,
                    "repository_id": repository_id,
                },
            )
            for source_id, values in (
                (
                    older_source_id,
                    {
                        "title": "Old projection title",
                        "abstract": "Old projection abstract",
                        "publication_date": "2026-07-01",
                        "type": "preprint",
                        "authors": ["Old Author"],
                        "citation_count": 10,
                        "topics": [
                            {
                                "openalex_id": "T-MIGRATION-OLD",
                                "display_name": "Old Migration Topic",
                                "score": 1.0,
                            }
                        ],
                    },
                ),
                (
                    newer_source_id,
                    {
                        "title": "New projection title",
                        "abstract": "New projection abstract",
                        "publication_date": "2026-07-02",
                        "type": "article",
                        "authors": ["New Author"],
                        "citation_count": 20,
                        "topics": [
                            {
                                "openalex_id": "T-MIGRATION-NEW",
                                "display_name": "New Migration Topic",
                                "score": 1.0,
                            }
                        ],
                    },
                ),
            ):
                for field_name, value in values.items():
                    connection.execute(
                        text(
                            """
                            INSERT INTO field_assertion (
                                id,
                                source_record_id,
                                field_name,
                                value,
                                parser_version,
                                confidence,
                                source,
                                retrieved_at
                            )
                            VALUES (
                                :id,
                                :source_record_id,
                                :field_name,
                                CAST(:value AS jsonb),
                                'parser-v1',
                                1.0,
                                'openalex',
                                :retrieved_at
                            )
                            """
                        ),
                        {
                            "id": uuid4(),
                            "source_record_id": source_id,
                            "field_name": field_name,
                            "value": json.dumps(value),
                            "retrieved_at": (
                                older_retrieved_at
                                if source_id == older_source_id
                                else newer_retrieved_at
                            ),
                        },
                    )
            for assertion_id, field_name, value in (
                (
                    UUID("00000000-0000-0000-0000-000000000001"),
                    "title",
                    "Newest parser title",
                ),
                (
                    UUID("00000000-0000-0000-0000-000000000002"),
                    "abstract",
                    "Newest parser abstract",
                ),
                (
                    UUID("00000000-0000-0000-0000-000000000003"),
                    "publication_date",
                    "2026-07-03",
                ),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO field_assertion (
                            id,
                            source_record_id,
                            field_name,
                            value,
                            parser_version,
                            confidence,
                            source,
                            retrieved_at,
                            created_at
                        )
                        VALUES (
                            :id,
                            :source_record_id,
                            :field_name,
                            CAST(:value AS jsonb),
                            'parser-v2',
                            1.0,
                            'openalex',
                            :retrieved_at,
                            :created_at
                        )
                        """
                    ),
                    {
                        "id": assertion_id,
                        "source_record_id": newer_source_id,
                        "field_name": field_name,
                        "value": json.dumps(value),
                        "retrieved_at": newer_retrieved_at,
                        "created_at": parser_upgrade_created_at,
                        },
                    )

            connection.execute(
                text(
                    """
                    INSERT INTO metric_snapshot (
                        id,
                        work_id,
                        metric_name,
                        metric_value,
                        measured_at,
                        metadata,
                        source,
                        retrieved_at
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'citation_count',
                        10,
                        :measured_at,
                        CAST(:details AS jsonb),
                        'openalex',
                        :retrieved_at
                    )
                    """
                ),
                {
                    "id": metric_id,
                    "work_id": work_id,
                    "measured_at": source_updated_at,
                    "details": json.dumps(
                        {
                            "source_record_id": str(older_source_id),
                            "content_hash": "a" * 64,
                        }
                    ),
                    "retrieved_at": older_retrieved_at,
                },
            )

        command.upgrade(alembic_config, "head")
        assert_exact_projection()

        command.downgrade(alembic_config, "0009_logical_source_scope")
        with engine.connect() as connection:
            assert connection.scalar(
                text(
                    """
                    SELECT projection_source_record_id
                    FROM work
                    WHERE id = :work_id
                    """
                ),
                {"work_id": work_id},
            ) == "W-PROJECTION-ROUNDTRIP"
            assert set(
                connection.scalars(
                    text(
                        """
                        SELECT topic.normalized_name
                        FROM work_topic
                        JOIN topic ON topic.id = work_topic.topic_id
                        WHERE work_topic.work_id = :work_id
                        """
                    ),
                    {"work_id": work_id},
                )
            ) == {
                "old migration topic",
                "new migration topic",
            }
            restored_association_counts = connection.execute(
                text(
                    """
                    SELECT
                        (
                            SELECT count(*)
                            FROM work_method
                            WHERE work_id = :work_id
                        ) AS methods,
                        (
                            SELECT count(*)
                            FROM work_dataset
                            WHERE work_id = :work_id
                        ) AS datasets,
                        (
                            SELECT count(*)
                            FROM work_benchmark
                            WHERE work_id = :work_id
                        ) AS benchmarks,
                        (
                            SELECT count(*)
                            FROM work_code_repository
                            WHERE work_id = :work_id
                        ) AS repositories
                    """
                ),
                {"work_id": work_id},
            ).one()
        assert restored_association_counts == (1, 1, 1, 1)

        command.upgrade(alembic_config, "head")
        assert_exact_projection()

        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source_updated_at,
                        source,
                        retrieved_at
                    )
                    VALUES (
                        :id,
                        NULL,
                        'W-PROJECTION-ROUNDTRIP',
                        :content_hash,
                        '{}'::jsonb,
                        :source_updated_at,
                        'openalex',
                        :retrieved_at
                    )
                    """
                ),
                {
                    "id": excluded_source_id,
                    "content_hash": "c" * 64,
                    "source_updated_at": source_updated_at,
                    "retrieved_at": datetime(2026, 7, 15, 11, tzinfo=UTC),
                },
            )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v2',
                        false,
                        'excluded',
                        '[]'::jsonb,
                        :evaluated_at,
                        NULL
                    )
                    """
                ),
                {
                    "id": excluded_assessment_id,
                    "source_record_id": excluded_source_id,
                    "evaluated_at": datetime(
                        2026, 7, 15, 11, tzinfo=UTC
                    ),
                },
            )
            connection.execute(
                text(
                    """
                    UPDATE source_record
                    SET work_id = :work_id
                    WHERE id = :source_record_id
                    """
                ),
                {
                    "work_id": work_id,
                    "source_record_id": excluded_source_id,
                },
            )
            linked = connection.execute(
                text(
                    """
                    SELECT work_id, work_linked_at, work_link_reason
                    FROM scope_assessment
                    WHERE id = :assessment_id
                    """
                ),
                {"assessment_id": excluded_assessment_id},
            ).one()
        assert linked.work_id == work_id
        assert linked.work_linked_at is not None
        assert linked.work_link_reason == "logical_source_owner_trigger"
    finally:
        engine.dispose()


def test_0010_rejects_unmappable_legacy_projection_without_data_loss(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "0009_logical_source_scope")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    projection_updated_at = datetime(2026, 7, 16, 1, tzinfo=UTC)
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (
                        id,
                        canonical_key,
                        title,
                        projection_source,
                        projection_source_record_id,
                        projection_source_updated_at,
                        source
                    )
                    VALUES (
                        :id,
                        'doi:10.1000/unmappable-legacy-projection',
                        'Unmappable legacy projection',
                        'openalex',
                        'W-UNMAPPABLE-LEGACY-PROJECTION',
                        :projection_updated_at,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": work_id,
                    "projection_updated_at": projection_updated_at,
                },
            )

        with pytest.raises(
            DBAPIError,
            match="cannot map legacy work projection",
        ):
            command.upgrade(
                alembic_config,
                "0010_exact_projection_scope",
            )

        with engine.connect() as connection:
            assert connection.scalar(
                text("SELECT version_num FROM alembic_version")
            ) == "0009_logical_source_scope"
            projection = connection.execute(
                text(
                    """
                    SELECT
                        projection_source,
                        projection_source_record_id,
                        projection_source_updated_at
                    FROM work
                    WHERE id = :work_id
                    """
                ),
                {"work_id": work_id},
            ).one()
        assert projection == (
            "openalex",
            "W-UNMAPPABLE-LEGACY-PROJECTION",
            projection_updated_at,
        )
    finally:
        engine.dispose()


def test_0010_rejects_preexisting_cross_table_logical_owner_conflict(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "0009_logical_source_scope")
    engine = create_engine(clean_postgres_url)
    assessment_work_id = uuid4()
    source_work_id = uuid4()
    assessment_source_id = uuid4()
    try:
        with engine.begin() as connection:
            for index, work_id in enumerate(
                (assessment_work_id, source_work_id),
                start=1,
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (id, canonical_key, title, source)
                        VALUES (
                            :id,
                            :canonical_key,
                            'Preexisting cross-table owner',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": work_id,
                        "canonical_key": (
                            "doi:10.1000/preexisting-cross-owner-"
                            f"{index}"
                        ),
                    },
                )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        NULL,
                        'W-PREEXISTING-CROSS-OWNER',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": assessment_source_id,
                    "content_hash": "a" * 64,
                },
            )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v1',
                        false,
                        'excluded',
                        '[]'::jsonb,
                        :evaluated_at,
                        :work_id
                    )
                    """
                ),
                {
                    "id": uuid4(),
                    "source_record_id": assessment_source_id,
                    "evaluated_at": datetime.now(UTC),
                    "work_id": assessment_work_id,
                },
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W-PREEXISTING-CROSS-OWNER',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": uuid4(),
                    "work_id": source_work_id,
                    "content_hash": "b" * 64,
                },
            )

        with pytest.raises(
            DBAPIError,
            match=(
                "cannot upgrade exact projection scope: "
                "logical source identity maps to multiple works"
            ),
        ):
            command.upgrade(alembic_config, "head")
    finally:
        engine.dispose()


def test_0010_links_late_exclusion_to_existing_logical_owner_with_audit(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    owner_source_id = uuid4()
    excluded_source_id = uuid4()
    assessment_id = uuid4()
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (id, canonical_key, title, source)
                    VALUES (
                        :id,
                        'doi:10.1000/late-exclusion-owner',
                        'Late exclusion owner',
                        'openalex'
                    )
                    """
                ),
                {"id": work_id},
            )
            for source_id, owner_id, content_hash in (
                (owner_source_id, work_id, "d" * 64),
                (excluded_source_id, None, "e" * 64),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source
                        )
                        VALUES (
                            :id,
                            :work_id,
                            'W-LATE-EXCLUSION',
                            :content_hash,
                            '{}'::jsonb,
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": source_id,
                        "work_id": owner_id,
                        "content_hash": content_hash,
                    },
                )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v2',
                        false,
                        'excluded',
                        '[]'::jsonb,
                        :evaluated_at,
                        NULL
                    )
                    """
                ),
                {
                    "id": assessment_id,
                    "source_record_id": excluded_source_id,
                    "evaluated_at": datetime.now(UTC),
                },
            )
            linked = connection.execute(
                text(
                    """
                    SELECT work_id, work_linked_at, work_link_reason
                    FROM scope_assessment
                    WHERE id = :assessment_id
                    """
                ),
                {"assessment_id": assessment_id},
            ).one()

        assert linked.work_id == work_id
        assert linked.work_linked_at is not None
        assert linked.work_link_reason == "logical_source_owner_trigger"
    finally:
        engine.dispose()


def test_0010_audits_explicit_excluded_owner_insert(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    source_id = uuid4()
    assessment_id = uuid4()
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (id, canonical_key, title, source)
                    VALUES (
                        :id,
                        'doi:10.1000/explicit-exclusion-owner',
                        'Explicit exclusion owner',
                        'openalex'
                    )
                    """
                ),
                {"id": work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        NULL,
                        'W-EXPLICIT-EXCLUSION-OWNER',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": source_id,
                    "content_hash": "e" * 64,
                },
            )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v2',
                        false,
                        'excluded',
                        '[]'::jsonb,
                        :evaluated_at,
                        :work_id
                    )
                    """
                ),
                {
                    "id": assessment_id,
                    "source_record_id": source_id,
                    "evaluated_at": datetime.now(UTC),
                    "work_id": work_id,
                },
            )
            linked = connection.execute(
                text(
                    """
                    SELECT work_id, work_linked_at, work_link_reason
                    FROM scope_assessment
                    WHERE id = :assessment_id
                    """
                ),
                {"assessment_id": assessment_id},
            ).one()

        assert linked.work_id == work_id
        assert linked.work_linked_at is not None
        assert linked.work_link_reason == "explicit_scope_owner_insert"
    finally:
        engine.dispose()


def test_0010_rejects_second_work_on_logical_source_insert(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    first_work_id = uuid4()
    second_work_id = uuid4()
    try:
        with engine.begin() as connection:
            for index, work_id in enumerate(
                (first_work_id, second_work_id),
                start=1,
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (id, canonical_key, title, source)
                        VALUES (
                            :id,
                            :canonical_key,
                            'Logical source insert owner',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": work_id,
                        "canonical_key": (
                            f"doi:10.1000/logical-insert-owner-{index}"
                        ),
                    },
                )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W-LOGICAL-INSERT',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": uuid4(),
                    "work_id": first_work_id,
                    "content_hash": "1" * 64,
                },
            )

        with pytest.raises(IntegrityError):
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source
                        )
                        VALUES (
                            :id,
                            :work_id,
                            'W-LOGICAL-INSERT',
                            :content_hash,
                            '{}'::jsonb,
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "work_id": second_work_id,
                        "content_hash": "2" * 64,
                    },
                )
    finally:
        engine.dispose()


def test_0010_rejects_second_work_on_logical_source_update(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    first_work_id = uuid4()
    second_work_id = uuid4()
    first_source_id = uuid4()
    second_source_id = uuid4()
    try:
        with engine.begin() as connection:
            for index, work_id in enumerate(
                (first_work_id, second_work_id),
                start=1,
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (id, canonical_key, title, source)
                        VALUES (
                            :id,
                            :canonical_key,
                            'Logical source update owner',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": work_id,
                        "canonical_key": (
                            f"doi:10.1000/logical-update-owner-{index}"
                        ),
                    },
                )
            for source_id, work_id, logical_id, content_hash in (
                (
                    first_source_id,
                    first_work_id,
                    "W-LOGICAL-UPDATE",
                    "3" * 64,
                ),
                (
                    second_source_id,
                    None,
                    "W-LOGICAL-UPDATE",
                    "4" * 64,
                ),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source
                        )
                        VALUES (
                            :id,
                            :work_id,
                            :source_record_id,
                            :content_hash,
                            '{}'::jsonb,
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": source_id,
                        "work_id": work_id,
                        "source_record_id": logical_id,
                        "content_hash": content_hash,
                    },
                )

        with pytest.raises(IntegrityError):
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        UPDATE source_record
                        SET work_id = :work_id
                        WHERE id = :source_record_id
                        """
                    ),
                    {
                        "work_id": second_work_id,
                        "source_record_id": second_source_id,
                    },
                )
    finally:
        engine.dispose()


def test_0010_serializes_concurrent_logical_source_owners(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    from concurrent.futures import ThreadPoolExecutor, TimeoutError

    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    first_work_id = uuid4()
    second_work_id = uuid4()
    first_source_id = uuid4()
    second_source_id = uuid4()
    first_connection = engine.connect()
    first_transaction = first_connection.begin()
    try:
        with engine.begin() as connection:
            for index, work_id in enumerate(
                (first_work_id, second_work_id),
                start=1,
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (id, canonical_key, title, source)
                        VALUES (
                            :id,
                            :canonical_key,
                            'Concurrent logical owner',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": work_id,
                        "canonical_key": (
                            f"doi:10.1000/concurrent-owner-{index}"
                        ),
                    },
                )
        first_connection.execute(
            text(
                """
                INSERT INTO source_record (
                    id,
                    work_id,
                    source_record_id,
                    content_hash,
                    raw_payload,
                    source
                )
                VALUES (
                    :id,
                    :work_id,
                    'W-CONCURRENT-OWNER',
                    :content_hash,
                    '{}'::jsonb,
                    'openalex'
                )
                """
            ),
            {
                "id": first_source_id,
                "work_id": first_work_id,
                "content_hash": "9" * 64,
            },
        )

        def insert_second_owner() -> None:
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source
                        )
                        VALUES (
                            :id,
                            :work_id,
                            'W-CONCURRENT-OWNER',
                            :content_hash,
                            '{}'::jsonb,
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": second_source_id,
                        "work_id": second_work_id,
                        "content_hash": "0" * 64,
                    },
                )

        with ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(insert_second_owner)
            try:
                future.result(timeout=0.5)
            except TimeoutError:
                first_transaction.commit()
                with pytest.raises(IntegrityError):
                    future.result(timeout=5)
            else:
                first_transaction.commit()

        with engine.connect() as connection:
            assert connection.scalar(
                text(
                    """
                    SELECT count(DISTINCT work_id)
                    FROM source_record
                    WHERE source = 'openalex'
                      AND source_record_id = 'W-CONCURRENT-OWNER'
                      AND work_id IS NOT NULL
                    """
                )
            ) == 1
    finally:
        if first_transaction.is_active:
            first_transaction.rollback()
        first_connection.close()
        engine.dispose()


def test_0010_upgrade_locks_owner_tables_before_validating(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    from concurrent.futures import ThreadPoolExecutor, TimeoutError

    command.upgrade(alembic_config, "0009_logical_source_scope")
    engine = create_engine(clean_postgres_url)
    first_work_id = uuid4()
    second_work_id = uuid4()
    blocker_connection = engine.connect()
    blocker_transaction = blocker_connection.begin()
    executor = ThreadPoolExecutor(max_workers=2)
    migration_future = None
    writer_future = None
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (id, canonical_key, title, source)
                    VALUES
                        (
                            :first_work_id,
                            'doi:10.1000/migration-race-first',
                            'Migration race first',
                            'test'
                        ),
                        (
                            :second_work_id,
                            'doi:10.1000/migration-race-second',
                            'Migration race second',
                            'test'
                        )
                    """
                ),
                {
                    "first_work_id": first_work_id,
                    "second_work_id": second_work_id,
                },
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W-MIGRATION-UPGRADE-RACE',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": uuid4(),
                    "work_id": first_work_id,
                    "content_hash": "a" * 64,
                },
            )

        blocker_connection.execute(
            text("LOCK TABLE scope_assessment IN ACCESS SHARE MODE")
        )
        migration_future = executor.submit(
            command.upgrade,
            alembic_config,
            "head",
        )
        _wait_for_relation_lock(
            engine,
            relation_name="scope_assessment",
            mode="AccessExclusiveLock",
            granted=False,
        )

        def insert_conflicting_owner() -> None:
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source
                        )
                        VALUES (
                            :id,
                            :work_id,
                            'W-MIGRATION-UPGRADE-RACE',
                            :content_hash,
                            '{}'::jsonb,
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "work_id": second_work_id,
                        "content_hash": "b" * 64,
                    },
                )

        writer_future = executor.submit(insert_conflicting_owner)
        with pytest.raises(TimeoutError):
            writer_future.result(timeout=0.5)

        blocker_transaction.commit()
        migration_future.result(timeout=10)
        with pytest.raises(IntegrityError):
            writer_future.result(timeout=10)

        with engine.connect() as connection:
            assert connection.scalar(
                text(
                    """
                    SELECT count(DISTINCT work_id)
                    FROM source_record
                    WHERE source = 'openalex'
                      AND source_record_id =
                          'W-MIGRATION-UPGRADE-RACE'
                      AND work_id IS NOT NULL
                    """
                )
            ) == 1
    finally:
        if blocker_transaction.is_active:
            blocker_transaction.rollback()
        blocker_connection.close()
        if migration_future is not None:
            try:
                migration_future.result(timeout=10)
            except Exception:
                pass
        if writer_future is not None:
            try:
                writer_future.result(timeout=10)
            except Exception:
                pass
        executor.shutdown(wait=True)
        engine.dispose()


def test_0010_downgrade_locks_audit_tables_before_validating(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    from concurrent.futures import ThreadPoolExecutor

    command.upgrade(
        alembic_config,
        "0010_exact_projection_scope",
    )
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    source_id = uuid4()
    assessment_id = uuid4()
    writer_connection = engine.connect()
    writer_transaction = writer_connection.begin()
    executor = ThreadPoolExecutor(max_workers=1)
    migration_future = None
    linked_at = datetime(2026, 7, 16, 12, tzinfo=UTC)
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (id, canonical_key, title, source)
                    VALUES (
                        :id,
                        'doi:10.1000/migration-downgrade-race',
                        'Migration downgrade race',
                        'test'
                    )
                    """
                ),
                {"id": work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W-MIGRATION-DOWNGRADE-RACE',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": source_id,
                    "work_id": work_id,
                    "content_hash": "c" * 64,
                },
            )

        writer_connection.execute(
            text(
                """
                INSERT INTO scope_assessment (
                    id,
                    source_record_id,
                    rule_version,
                    included,
                    reason,
                    evidence,
                    evaluated_at,
                    work_id,
                    work_linked_at,
                    work_link_reason
                )
                VALUES (
                    :id,
                    :source_record_id,
                    'migration-downgrade-race',
                    false,
                    'excluded',
                    '[]'::jsonb,
                    :evaluated_at,
                    :work_id,
                    :work_linked_at,
                    'identity_replay'
                )
                """
            ),
            {
                "id": assessment_id,
                "source_record_id": source_id,
                "evaluated_at": linked_at,
                "work_id": work_id,
                "work_linked_at": linked_at,
            },
        )
        migration_future = executor.submit(
            command.downgrade,
            alembic_config,
            "0009_logical_source_scope",
        )
        _wait_for_waiting_relation_lock(
            engine,
            relation_names=(
                "work",
                "source_record",
                "scope_assessment",
            ),
        )

        with pytest.raises(DBAPIError):
            writer_transaction.commit()
            migration_future.result(timeout=10)

        with engine.connect() as connection:
            assert connection.scalar(
                text("SELECT version_num FROM alembic_version")
            ) == "0010_exact_projection_scope"
            assert connection.execute(
                text(
                    """
                    SELECT work_linked_at, work_link_reason
                    FROM scope_assessment
                    WHERE id = :assessment_id
                    """
                ),
                {"assessment_id": assessment_id},
            ).one() == (linked_at, "identity_replay")
    finally:
        if writer_transaction.is_active:
            writer_transaction.rollback()
        writer_connection.close()
        if migration_future is not None:
            try:
                migration_future.result(timeout=10)
            except Exception:
                pass
        executor.shutdown(wait=True)
        engine.dispose()


def test_0010_rejects_source_owner_conflicting_with_assessment_owner(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    assessment_work_id = uuid4()
    source_work_id = uuid4()
    unowned_source_id = uuid4()
    try:
        with engine.begin() as connection:
            for index, work_id in enumerate(
                (assessment_work_id, source_work_id),
                start=1,
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (id, canonical_key, title, source)
                        VALUES (
                            :id,
                            :canonical_key,
                            'Assessment logical owner',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": work_id,
                        "canonical_key": (
                            "doi:10.1000/assessment-logical-owner-"
                            f"{index}"
                        ),
                    },
                )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        NULL,
                        'W-ASSESSMENT-OWNER',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": unowned_source_id,
                    "content_hash": "5" * 64,
                },
            )
            connection.execute(
                text(
                    """
                    INSERT INTO scope_assessment (
                        id,
                        source_record_id,
                        rule_version,
                        included,
                        reason,
                        evidence,
                        evaluated_at,
                        work_id,
                        work_linked_at,
                        work_link_reason
                    )
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v2',
                        false,
                        'excluded',
                        '[]'::jsonb,
                        :evaluated_at,
                        :work_id,
                        :work_linked_at,
                        'explicit_logical_owner'
                    )
                    """
                ),
                {
                    "id": uuid4(),
                    "source_record_id": unowned_source_id,
                    "evaluated_at": datetime.now(UTC),
                    "work_id": assessment_work_id,
                    "work_linked_at": datetime.now(UTC),
                },
            )

        with pytest.raises(IntegrityError):
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source
                        )
                        VALUES (
                            :id,
                            :work_id,
                            'W-ASSESSMENT-OWNER',
                            :content_hash,
                            '{}'::jsonb,
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "work_id": source_work_id,
                        "content_hash": "6" * 64,
                    },
                )
    finally:
        engine.dispose()


def test_0010_serializes_assessment_and_source_logical_owners(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    from concurrent.futures import ThreadPoolExecutor, TimeoutError

    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    assessment_work_id = uuid4()
    source_work_id = uuid4()
    unowned_source_id = uuid4()
    first_connection = engine.connect()
    first_transaction = first_connection.begin()
    try:
        with engine.begin() as connection:
            for index, work_id in enumerate(
                (assessment_work_id, source_work_id),
                start=1,
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (id, canonical_key, title, source)
                        VALUES (
                            :id,
                            :canonical_key,
                            'Concurrent assessment logical owner',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": work_id,
                        "canonical_key": (
                            "doi:10.1000/concurrent-assessment-owner-"
                            f"{index}"
                        ),
                    },
                )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        NULL,
                        'W-CONCURRENT-ASSESSMENT-OWNER',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": unowned_source_id,
                    "content_hash": "7" * 64,
                },
            )

        first_connection.execute(
            text(
                """
                INSERT INTO scope_assessment (
                    id,
                    source_record_id,
                    rule_version,
                    included,
                    reason,
                    evidence,
                    evaluated_at,
                    work_id,
                    work_linked_at,
                    work_link_reason
                )
                VALUES (
                    :id,
                    :source_record_id,
                    'scope-v2',
                    false,
                    'excluded',
                    '[]'::jsonb,
                    :evaluated_at,
                    :work_id,
                    :work_linked_at,
                    'explicit_logical_owner'
                )
                """
            ),
            {
                "id": uuid4(),
                "source_record_id": unowned_source_id,
                "evaluated_at": datetime.now(UTC),
                "work_id": assessment_work_id,
                "work_linked_at": datetime.now(UTC),
            },
        )

        def insert_source_owner() -> None:
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source
                        )
                        VALUES (
                            :id,
                            :work_id,
                            'W-CONCURRENT-ASSESSMENT-OWNER',
                            :content_hash,
                            '{}'::jsonb,
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "work_id": source_work_id,
                        "content_hash": "8" * 64,
                    },
                )

        with ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(insert_source_owner)
            try:
                future.result(timeout=0.5)
            except TimeoutError:
                first_transaction.commit()
                with pytest.raises(IntegrityError):
                    future.result(timeout=5)
            else:
                first_transaction.commit()

        with engine.connect() as connection:
            owner_count = connection.scalar(
                text(
                    """
                    SELECT count(DISTINCT owner.work_id)
                    FROM (
                        SELECT source.work_id
                        FROM source_record AS source
                        WHERE source.source = 'openalex'
                          AND source.source_record_id =
                              'W-CONCURRENT-ASSESSMENT-OWNER'
                          AND source.work_id IS NOT NULL

                        UNION ALL

                        SELECT assessment.work_id
                        FROM scope_assessment AS assessment
                        JOIN source_record AS source
                          ON source.id = assessment.source_record_id
                        WHERE source.source = 'openalex'
                          AND source.source_record_id =
                              'W-CONCURRENT-ASSESSMENT-OWNER'
                          AND assessment.work_id IS NOT NULL
                    ) AS owner
                    """
                )
            )
        assert owner_count == 1
    finally:
        if first_transaction.is_active:
            first_transaction.rollback()
        first_connection.close()
        engine.dispose()


def test_0010_serializes_assessment_update_before_source_insert(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    from concurrent.futures import ThreadPoolExecutor, TimeoutError

    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    assessment_work_id = uuid4()
    source_work_id = uuid4()
    assessment_source_id = uuid4()
    assessment_id = uuid4()
    first_connection = engine.connect()
    first_transaction = first_connection.begin()
    try:
        with engine.begin() as connection:
            for index, work_id in enumerate(
                (assessment_work_id, source_work_id),
                start=1,
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (id, canonical_key, title, source)
                        VALUES (
                            :id,
                            :canonical_key,
                            'Assessment update logical owner',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": work_id,
                        "canonical_key": (
                            "doi:10.1000/assessment-update-owner-"
                            f"{index}"
                        ),
                    },
                )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        NULL,
                        'W-ASSESSMENT-UPDATE-OWNER',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": assessment_source_id,
                    "content_hash": "c" * 64,
                },
            )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v1',
                        false,
                        'excluded',
                        '[]'::jsonb,
                        :evaluated_at,
                        NULL
                    )
                    """
                ),
                {
                    "id": assessment_id,
                    "source_record_id": assessment_source_id,
                    "evaluated_at": datetime.now(UTC),
                },
            )

        first_connection.execute(
            text(
                """
                UPDATE scope_assessment
                SET
                    work_id = :work_id,
                    work_linked_at = :work_linked_at,
                    work_link_reason = 'explicit_logical_owner'
                WHERE id = :assessment_id
                """
            ),
            {
                "work_id": assessment_work_id,
                "work_linked_at": datetime.now(UTC),
                "assessment_id": assessment_id,
            },
        )

        def insert_source_owner() -> None:
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        INSERT INTO source_record (
                            id,
                            work_id,
                            source_record_id,
                            content_hash,
                            raw_payload,
                            source
                        )
                        VALUES (
                            :id,
                            :work_id,
                            'W-ASSESSMENT-UPDATE-OWNER',
                            :content_hash,
                            '{}'::jsonb,
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "work_id": source_work_id,
                        "content_hash": "d" * 64,
                    },
                )

        with ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(insert_source_owner)
            try:
                future.result(timeout=0.5)
            except TimeoutError:
                first_transaction.commit()
                with pytest.raises(IntegrityError):
                    future.result(timeout=5)
            else:
                first_transaction.commit()

        with engine.connect() as connection:
            owner_count = connection.scalar(
                text(
                    """
                    SELECT count(DISTINCT owner.work_id)
                    FROM (
                        SELECT source.work_id
                        FROM source_record AS source
                        WHERE source.source = 'openalex'
                          AND source.source_record_id =
                              'W-ASSESSMENT-UPDATE-OWNER'
                          AND source.work_id IS NOT NULL

                        UNION ALL

                        SELECT assessment.work_id
                        FROM scope_assessment AS assessment
                        JOIN source_record AS source
                          ON source.id = assessment.source_record_id
                        WHERE source.source = 'openalex'
                          AND source.source_record_id =
                              'W-ASSESSMENT-UPDATE-OWNER'
                          AND assessment.work_id IS NOT NULL
                    ) AS owner
                    """
                )
            )
        assert owner_count == 1
    finally:
        if first_transaction.is_active:
            first_transaction.rollback()
        first_connection.close()
        engine.dispose()


def test_0010_serializes_source_insert_before_assessment_update(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    from concurrent.futures import ThreadPoolExecutor, TimeoutError

    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    assessment_work_id = uuid4()
    source_work_id = uuid4()
    assessment_source_id = uuid4()
    assessment_id = uuid4()
    first_connection = engine.connect()
    first_transaction = first_connection.begin()
    try:
        with engine.begin() as connection:
            for index, work_id in enumerate(
                (assessment_work_id, source_work_id),
                start=1,
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (id, canonical_key, title, source)
                        VALUES (
                            :id,
                            :canonical_key,
                            'Source insert logical owner',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": work_id,
                        "canonical_key": (
                            "doi:10.1000/source-insert-owner-"
                            f"{index}"
                        ),
                    },
                )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        NULL,
                        'W-SOURCE-INSERT-OWNER',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": assessment_source_id,
                    "content_hash": "e" * 64,
                },
            )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v1',
                        false,
                        'excluded',
                        '[]'::jsonb,
                        :evaluated_at,
                        NULL
                    )
                    """
                ),
                {
                    "id": assessment_id,
                    "source_record_id": assessment_source_id,
                    "evaluated_at": datetime.now(UTC),
                },
            )

        first_connection.execute(
            text(
                """
                INSERT INTO source_record (
                    id,
                    work_id,
                    source_record_id,
                    content_hash,
                    raw_payload,
                    source
                )
                VALUES (
                    :id,
                    :work_id,
                    'W-SOURCE-INSERT-OWNER',
                    :content_hash,
                    '{}'::jsonb,
                    'openalex'
                )
                """
            ),
            {
                "id": uuid4(),
                "work_id": source_work_id,
                "content_hash": "f" * 64,
            },
        )

        def update_assessment_owner() -> None:
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        UPDATE scope_assessment
                        SET
                            work_id = :work_id,
                            work_linked_at = :work_linked_at,
                            work_link_reason = 'explicit_logical_owner'
                        WHERE id = :assessment_id
                        """
                    ),
                    {
                        "work_id": assessment_work_id,
                        "work_linked_at": datetime.now(UTC),
                        "assessment_id": assessment_id,
                    },
                )

        with ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(update_assessment_owner)
            try:
                future.result(timeout=0.5)
            except TimeoutError:
                first_transaction.commit()
                with pytest.raises(DBAPIError):
                    future.result(timeout=5)
            else:
                first_transaction.commit()

        with engine.connect() as connection:
            owner_count = connection.scalar(
                text(
                    """
                    SELECT count(DISTINCT owner.work_id)
                    FROM (
                        SELECT source.work_id
                        FROM source_record AS source
                        WHERE source.source = 'openalex'
                          AND source.source_record_id =
                              'W-SOURCE-INSERT-OWNER'
                          AND source.work_id IS NOT NULL

                        UNION ALL

                        SELECT assessment.work_id
                        FROM scope_assessment AS assessment
                        JOIN source_record AS source
                          ON source.id = assessment.source_record_id
                        WHERE source.source = 'openalex'
                          AND source.source_record_id =
                              'W-SOURCE-INSERT-OWNER'
                          AND assessment.work_id IS NOT NULL
                    ) AS owner
                    """
                )
            )
        assert owner_count == 1
    finally:
        if first_transaction.is_active:
            first_transaction.rollback()
        first_connection.close()
        engine.dispose()


def test_0010_rejects_projection_snapshot_from_another_work(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    first_work_id = uuid4()
    second_work_id = uuid4()
    second_source_id = uuid4()
    try:
        with engine.begin() as connection:
            for index, work_id in enumerate(
                (first_work_id, second_work_id),
                start=1,
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (id, canonical_key, title, source)
                        VALUES (
                            :id,
                            :canonical_key,
                            'Projection owner',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": work_id,
                        "canonical_key": (
                            f"doi:10.1000/projection-owner-{index}"
                        ),
                    },
                )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W-PROJECTION-OTHER',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": second_source_id,
                    "work_id": second_work_id,
                    "content_hash": "5" * 64,
                },
            )

        with pytest.raises(IntegrityError):
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        UPDATE work
                        SET projection_source_record_id = :source_record_id
                        WHERE id = :work_id
                        """
                    ),
                    {
                        "source_record_id": second_source_id,
                        "work_id": first_work_id,
                    },
                )
    finally:
        engine.dispose()


def test_0010_rejects_moving_metric_source_to_another_work(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    first_work_id = uuid4()
    second_work_id = uuid4()
    source_id = uuid4()
    measured_at = datetime(2026, 7, 16, 8, tzinfo=UTC)
    try:
        with engine.begin() as connection:
            for index, work_id in enumerate(
                (first_work_id, second_work_id),
                start=1,
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO work (
                            id,
                            canonical_key,
                            title,
                            source
                        )
                        VALUES (
                            :id,
                            :canonical_key,
                            'Metric source owner',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": work_id,
                        "canonical_key": (
                            f"doi:10.1000/metric-source-owner-{index}"
                        ),
                    },
                )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source_updated_at,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W-METRIC-SOURCE-OWNER',
                        :content_hash,
                        '{}'::jsonb,
                        :measured_at,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": source_id,
                    "work_id": first_work_id,
                    "content_hash": "6" * 64,
                    "measured_at": measured_at,
                },
            )
            connection.execute(
                text(
                    """
                    INSERT INTO metric_snapshot (
                        id,
                        work_id,
                        source_record_id,
                        metric_name,
                        metric_value,
                        measured_at,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        :source_record_id,
                        'citation_count',
                        10,
                        :measured_at,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": uuid4(),
                    "work_id": first_work_id,
                    "source_record_id": source_id,
                    "measured_at": measured_at,
                },
            )

        with pytest.raises(
            DBAPIError,
            match="metric source record provenance is inconsistent",
        ):
            with engine.begin() as connection:
                connection.execute(
                    text(
                        """
                        UPDATE source_record
                        SET work_id = :work_id
                        WHERE id = :source_record_id
                        """
                    ),
                    {
                        "work_id": second_work_id,
                        "source_record_id": source_id,
                    },
                )
    finally:
        engine.dispose()


def test_0010_restricts_deleting_projection_source_snapshot(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    source_id = uuid4()
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (id, canonical_key, title, source)
                    VALUES (
                        :id,
                        'doi:10.1000/projection-delete-restrict',
                        'Projection delete restrict',
                        'openalex'
                    )
                    """
                ),
                {"id": work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W-PROJECTION-DELETE',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": source_id,
                    "work_id": work_id,
                    "content_hash": "6" * 64,
                },
            )
            connection.execute(
                text(
                    """
                    UPDATE work
                    SET projection_source_record_id = :source_record_id
                    WHERE id = :work_id
                    """
                ),
                {
                    "source_record_id": source_id,
                    "work_id": work_id,
                },
            )

        with pytest.raises(IntegrityError):
            with engine.begin() as connection:
                connection.execute(
                    text("DELETE FROM source_record WHERE id = :id"),
                    {"id": source_id},
                )
    finally:
        engine.dispose()


def test_0010_json_null_exact_assertions_clear_nullable_projection_fields(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "0009_logical_source_scope")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    source_id = uuid4()
    source_updated_at = datetime(2026, 7, 16, 8, tzinfo=UTC)
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (
                        id,
                        canonical_key,
                        title,
                        abstract,
                        publication_date,
                        projection_source,
                        projection_source_record_id,
                        projection_source_updated_at,
                        source
                    )
                    VALUES (
                        :id,
                        'doi:10.1000/json-null-projection',
                        'JSON null projection',
                        'Stale abstract',
                        DATE '2026-07-01',
                        'openalex',
                        'W-JSON-NULL',
                        :source_updated_at,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": work_id,
                    "source_updated_at": source_updated_at,
                },
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source_updated_at,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W-JSON-NULL',
                        :content_hash,
                        '{}'::jsonb,
                        :source_updated_at,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": source_id,
                    "work_id": work_id,
                    "content_hash": "7" * 64,
                    "source_updated_at": source_updated_at,
                },
            )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v1',
                        true,
                        NULL,
                        '[]'::jsonb,
                        :evaluated_at,
                        :work_id
                    )
                    """
                ),
                {
                    "id": uuid4(),
                    "source_record_id": source_id,
                    "evaluated_at": source_updated_at,
                    "work_id": work_id,
                },
            )
            for field_name in ("abstract", "publication_date"):
                connection.execute(
                    text(
                        """
                        INSERT INTO field_assertion (
                            id,
                            source_record_id,
                            field_name,
                            value,
                            parser_version,
                            source
                        )
                        VALUES (
                            :id,
                            :source_record_id,
                            :field_name,
                            'null'::jsonb,
                            'parser-v1',
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": uuid4(),
                        "source_record_id": source_id,
                        "field_name": field_name,
                    },
                )

        command.upgrade(alembic_config, "head")

        with engine.connect() as connection:
            projection = connection.execute(
                text(
                    """
                    SELECT
                        projection_source_record_id,
                        abstract,
                        publication_date
                    FROM work
                    WHERE id = :work_id
                    """
                ),
                {"work_id": work_id},
            ).one()
        assert projection == (source_id, None, None)
    finally:
        engine.dispose()


def test_0010_downgrade_preserves_authoritative_metric_provenance(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "0010_exact_projection_scope")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    source_id = uuid4()
    missing_metadata_metric_id = uuid4()
    conflicting_metadata_metric_id = uuid4()
    measured_at = datetime(2026, 7, 16, 8, tzinfo=UTC)
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (id, canonical_key, title, source)
                    VALUES (
                        :id,
                        'doi:10.1000/metric-downgrade-provenance',
                        'Metric downgrade provenance',
                        'openalex'
                    )
                    """
                ),
                {"id": work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source_updated_at,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W-METRIC-DOWNGRADE',
                        :content_hash,
                        '{}'::jsonb,
                        :measured_at,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": source_id,
                    "work_id": work_id,
                    "content_hash": "9" * 64,
                    "measured_at": measured_at,
                },
            )
            for metric_id, metric_name, metadata in (
                (
                    missing_metadata_metric_id,
                    "citation_count",
                    {"custom": "missing"},
                ),
                (
                    conflicting_metadata_metric_id,
                    "reference_count",
                    {
                        "custom": "conflicting",
                        "source_record_id": str(uuid4()),
                    },
                ),
            ):
                connection.execute(
                    text(
                        """
                        INSERT INTO metric_snapshot (
                            id,
                            work_id,
                            source_record_id,
                            metric_name,
                            metric_value,
                            measured_at,
                            metadata,
                            source
                        )
                        VALUES (
                            :id,
                            :work_id,
                            :source_record_id,
                            :metric_name,
                            10,
                            :measured_at,
                            CAST(:metadata AS jsonb),
                            'openalex'
                        )
                        """
                    ),
                    {
                        "id": metric_id,
                        "work_id": work_id,
                        "source_record_id": source_id,
                        "metric_name": metric_name,
                        "measured_at": measured_at,
                        "metadata": json.dumps(metadata),
                    },
                )

        command.downgrade(
            alembic_config,
            "0009_logical_source_scope",
        )
        with engine.connect() as connection:
            metadata_rows = connection.execute(
                text(
                    """
                    SELECT id, metadata
                    FROM metric_snapshot
                    WHERE id IN (
                        :missing_metadata_metric_id,
                        :conflicting_metadata_metric_id
                    )
                    ORDER BY id
                    """
                ),
                {
                    "missing_metadata_metric_id": (
                        missing_metadata_metric_id
                    ),
                    "conflicting_metadata_metric_id": (
                        conflicting_metadata_metric_id
                    ),
                },
            ).all()
        metadata_by_id = {
            row.id: row.metadata for row in metadata_rows
        }
        assert metadata_by_id[missing_metadata_metric_id] == {
            "custom": "missing",
            "source_record_id": str(source_id),
        }
        assert metadata_by_id[conflicting_metadata_metric_id] == {
            "custom": "conflicting",
            "source_record_id": str(source_id),
        }

        command.upgrade(
            alembic_config,
            "0010_exact_projection_scope",
        )
        with engine.connect() as connection:
            restored_source_ids = set(
                connection.scalars(
                    text(
                        """
                        SELECT source_record_id
                        FROM metric_snapshot
                        WHERE id IN (
                            :missing_metadata_metric_id,
                            :conflicting_metadata_metric_id
                        )
                        """
                    ),
                    {
                        "missing_metadata_metric_id": (
                            missing_metadata_metric_id
                        ),
                        "conflicting_metadata_metric_id": (
                            conflicting_metadata_metric_id
                        ),
                    },
                )
            )
        assert restored_source_ids == {source_id}
    finally:
        engine.dispose()


def test_0010_downgrade_rejects_non_object_metric_metadata(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "0010_exact_projection_scope")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    source_id = uuid4()
    metric_id = uuid4()
    measured_at = datetime(2026, 7, 16, 8, tzinfo=UTC)
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (id, canonical_key, title, source)
                    VALUES (
                        :id,
                        'doi:10.1000/non-object-metric-metadata',
                        'Non-object metric metadata',
                        'openalex'
                    )
                    """
                ),
                {"id": work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source_updated_at,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W-NON-OBJECT-METRIC-METADATA',
                        :content_hash,
                        '{}'::jsonb,
                        :measured_at,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": source_id,
                    "work_id": work_id,
                    "content_hash": "a" * 64,
                    "measured_at": measured_at,
                },
            )
            connection.execute(
                text(
                    """
                    INSERT INTO metric_snapshot (
                        id,
                        work_id,
                        source_record_id,
                        metric_name,
                        metric_value,
                        measured_at,
                        metadata,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        :source_record_id,
                        'citation_count',
                        10,
                        :measured_at,
                        '[]'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": metric_id,
                    "work_id": work_id,
                    "source_record_id": source_id,
                    "measured_at": measured_at,
                },
            )

        with pytest.raises(
            DBAPIError,
            match=(
                "cannot downgrade metric provenance: "
                "metadata must be an object"
            ),
        ):
            command.downgrade(
                alembic_config,
                "0009_logical_source_scope",
            )

        with engine.connect() as connection:
            assert connection.scalar(
                text("SELECT version_num FROM alembic_version")
            ) == "0010_exact_projection_scope"
            assert connection.scalar(
                text(
                    """
                    SELECT metadata
                    FROM metric_snapshot
                    WHERE id = :metric_id
                    """
                ),
                {"metric_id": metric_id},
            ) == []
    finally:
        engine.dispose()


def test_0010_downgrade_removes_stale_null_metric_provenance(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "0010_exact_projection_scope")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    metric_id = uuid4()
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (id, canonical_key, title, source)
                    VALUES (
                        :id,
                        'doi:10.1000/stale-null-metric-provenance',
                        'Stale null metric provenance',
                        'openalex'
                    )
                    """
                ),
                {"id": work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO metric_snapshot (
                        id,
                        work_id,
                        source_record_id,
                        metric_name,
                        metric_value,
                        measured_at,
                        metadata,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        NULL,
                        'reference_count',
                        10,
                        :measured_at,
                        CAST(:metadata AS jsonb),
                        'openalex'
                    )
                    """
                ),
                {
                    "id": metric_id,
                    "work_id": work_id,
                    "measured_at": datetime(
                        2026,
                        7,
                        16,
                        8,
                        tzinfo=UTC,
                    ),
                    "metadata": json.dumps(
                        {
                            "custom": "preserve",
                            "source_record_id": str(uuid4()),
                        }
                    ),
                },
            )

        command.downgrade(
            alembic_config,
            "0009_logical_source_scope",
        )
        with engine.connect() as connection:
            metadata = connection.scalar(
                text(
                    """
                    SELECT metadata
                    FROM metric_snapshot
                    WHERE id = :metric_id
                    """
                ),
                {"metric_id": metric_id},
            )
        assert metadata == {"custom": "preserve"}
    finally:
        engine.dispose()


def test_0010_downgrade_rejects_non_migration_scope_link_audit(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    work_id = uuid4()
    source_id = uuid4()
    assessment_id = uuid4()
    linked_at = datetime(2026, 7, 16, 9, tzinfo=UTC)
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work (id, canonical_key, title, source)
                    VALUES (
                        :id,
                        'doi:10.1000/non-migration-audit',
                        'Non-migration audit',
                        'openalex'
                    )
                    """
                ),
                {"id": work_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'W-NON-MIGRATION-AUDIT',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "id": source_id,
                    "work_id": work_id,
                    "content_hash": "8" * 64,
                },
            )
            connection.execute(
                text(
                    """
                    INSERT INTO scope_assessment (
                        id,
                        source_record_id,
                        rule_version,
                        included,
                        reason,
                        evidence,
                        evaluated_at,
                        work_id,
                        work_linked_at,
                        work_link_reason
                    )
                    VALUES (
                        :id,
                        :source_record_id,
                        'scope-v2',
                        false,
                        'excluded',
                        '[]'::jsonb,
                        :evaluated_at,
                        :work_id,
                        :work_linked_at,
                        'identity_replay'
                    )
                    """
                ),
                {
                    "id": assessment_id,
                    "source_record_id": source_id,
                    "evaluated_at": linked_at,
                    "work_id": work_id,
                    "work_linked_at": linked_at,
                },
            )

        with pytest.raises(
            DBAPIError,
            match="cannot downgrade scope link audit",
        ):
            command.downgrade(
                alembic_config,
                "0009_logical_source_scope",
            )

        with engine.connect() as connection:
            assert connection.scalar(
                text("SELECT version_num FROM alembic_version")
            ) == "0011_search_snapshot_revision"
            assert connection.execute(
                text(
                    """
                    SELECT work_linked_at, work_link_reason
                    FROM scope_assessment
                    WHERE id = :assessment_id
                    """
                ),
                {"assessment_id": assessment_id},
            ).one() == (linked_at, "identity_replay")
    finally:
        engine.dispose()


def test_search_change_triggers_record_work_delete_and_identifier_lifecycle(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    first_work_id, second_work_id = _insert_search_trigger_works(engine)
    identifier_id = uuid4()
    moved_work_id = uuid4()
    earlier_created_at = datetime(2026, 7, 14, 8, tzinfo=UTC)
    later_created_at = datetime(2026, 7, 18, 8, tzinfo=UTC)
    try:
        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE work
                    SET created_at = :later_created_at
                    WHERE id = :work_id
                    """
                ),
                {
                    "work_id": first_work_id,
                    "later_created_at": later_created_at,
                },
            )
            connection.execute(
                text(
                    """
                    UPDATE work
                    SET created_at = :earlier_created_at
                    WHERE id = :work_id
                    """
                ),
                {
                    "work_id": first_work_id,
                    "earlier_created_at": earlier_created_at,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "work")
        }
        with engine.connect() as connection:
            assert connection.scalar(
                text(
                    """
                    SELECT work_created_at
                    FROM paper_search_change
                    WHERE revision > :revision
                      AND work_id = :work_id
                    """
                ),
                {
                    "revision": revision,
                    "work_id": first_work_id,
                },
            ) == earlier_created_at

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE work
                    SET title = title
                    WHERE id = :work_id
                    """
                ),
                {"work_id": first_work_id},
            )
        assert _paper_search_change_revision(engine) == revision

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO external_identifier (
                        id,
                        work_id,
                        scheme,
                        normalized_value,
                        raw_value,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'doi',
                        '10.1000/search-trigger-identifier',
                        '10.1000/search-trigger-identifier',
                        'test'
                    )
                    """
                ),
                {
                    "id": identifier_id,
                    "work_id": first_work_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "external_identifier")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE external_identifier
                    SET raw_value =
                        'https://doi.org/10.1000/search-trigger-identifier'
                    WHERE id = :id
                    """
                ),
                {"id": identifier_id},
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "external_identifier")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE external_identifier
                    SET work_id = :second_work_id
                    WHERE id = :id
                    """
                ),
                {
                    "id": identifier_id,
                    "second_work_id": second_work_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "external_identifier"),
            (second_work_id, "external_identifier"),
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    DELETE FROM external_identifier
                    WHERE id = :id
                    """
                ),
                {"id": identifier_id},
            )
        assert _paper_search_changes_after(engine, revision) == {
            (second_work_id, "external_identifier")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE work
                    SET id = :moved_work_id
                    WHERE id = :work_id
                    """
                ),
                {
                    "work_id": first_work_id,
                    "moved_work_id": moved_work_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "work"),
            (moved_work_id, "work"),
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text("DELETE FROM work WHERE id = :work_id"),
                {"work_id": moved_work_id},
            )
        assert _paper_search_changes_after(engine, revision) == {
            (moved_work_id, "work")
        }
    finally:
        engine.dispose()


def test_search_change_triggers_record_source_and_assertion_lifecycles(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    first_work_id, second_work_id = _insert_search_trigger_works(engine)
    first_source_id = uuid4()
    second_source_id = uuid4()
    assertion_id = uuid4()
    try:
        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES
                        (
                            :first_source_id,
                            :first_work_id,
                            'search-trigger-source-first',
                            :first_content_hash,
                            '{}'::jsonb,
                            'test'
                        ),
                        (
                            :second_source_id,
                            :first_work_id,
                            'search-trigger-source-second',
                            :second_content_hash,
                            '{}'::jsonb,
                            'test'
                        )
                    """
                ),
                {
                    "first_source_id": first_source_id,
                    "first_work_id": first_work_id,
                    "first_content_hash": "a" * 64,
                    "second_source_id": second_source_id,
                    "second_content_hash": "b" * 64,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "source_record")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE source_record
                    SET work_id = :second_work_id
                    WHERE id = :first_source_id
                    """
                ),
                {
                    "first_source_id": first_source_id,
                    "second_work_id": second_work_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "source_record"),
            (second_work_id, "source_record"),
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE source_record
                    SET work_id = work_id
                    WHERE id = :first_source_id
                    """
                ),
                {"first_source_id": first_source_id},
            )
        assert _paper_search_change_revision(engine) == revision

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE source_record
                    SET work_id = NULL
                    WHERE id = :first_source_id
                    """
                ),
                {"first_source_id": first_source_id},
            )
        assert _paper_search_changes_after(engine, revision) == {
            (second_work_id, "source_record")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE source_record
                    SET work_id = :second_work_id
                    WHERE id = :first_source_id
                    """
                ),
                {
                    "first_source_id": first_source_id,
                    "second_work_id": second_work_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (second_work_id, "source_record")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO field_assertion (
                        id,
                        source_record_id,
                        field_name,
                        value,
                        parser_version,
                        source
                    )
                    VALUES (
                        :id,
                        :source_record_id,
                        'authors',
                        '["Search Trigger Author"]'::jsonb,
                        'search-trigger-v1',
                        'test'
                    )
                    """
                ),
                {
                    "id": assertion_id,
                    "source_record_id": first_source_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (second_work_id, "field_assertion")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE field_assertion
                    SET value = value
                    WHERE id = :id
                    """
                ),
                {"id": assertion_id},
            )
        assert _paper_search_change_revision(engine) == revision

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE field_assertion
                    SET value = '["Updated Search Trigger Author"]'::jsonb
                    WHERE id = :id
                    """
                ),
                {"id": assertion_id},
            )
        assert _paper_search_changes_after(engine, revision) == {
            (second_work_id, "field_assertion")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE field_assertion
                    SET source_record_id = :second_source_id
                    WHERE id = :id
                    """
                ),
                {
                    "id": assertion_id,
                    "second_source_id": second_source_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "field_assertion"),
            (second_work_id, "field_assertion"),
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    DELETE FROM field_assertion
                    WHERE id = :id
                    """
                ),
                {"id": assertion_id},
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "field_assertion")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    DELETE FROM source_record
                    WHERE id = :first_source_id
                    """
                ),
                {"first_source_id": first_source_id},
            )
        assert _paper_search_changes_after(engine, revision) == {
            (second_work_id, "source_record")
        }
    finally:
        engine.dispose()


def test_search_change_trigger_records_scope_insert_delete_and_late_owner(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    first_work_id, _ = _insert_search_trigger_works(engine)
    unowned_source_id = uuid4()
    owned_source_id = uuid4()
    unowned_assertion_id = uuid4()
    delayed_assessment_id = uuid4()
    inserted_assessment_id = uuid4()
    evaluated_at = datetime(2026, 7, 16, 10, tzinfo=UTC)
    try:
        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :source_id,
                        NULL,
                        'search-trigger-logical-source',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "source_id": unowned_source_id,
                    "content_hash": "c" * 64,
                },
            )
            connection.execute(
                text(
                    """
                    INSERT INTO field_assertion (
                        id,
                        source_record_id,
                        field_name,
                        value,
                        parser_version,
                        source
                    )
                    VALUES (
                        :id,
                        :source_record_id,
                        'title',
                        '"Unowned Search Trigger Assertion"'::jsonb,
                        'search-trigger-unowned',
                        'test'
                    )
                    """
                ),
                {
                    "id": unowned_assertion_id,
                    "source_record_id": unowned_source_id,
                },
            )
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'search-trigger-delayed',
                        false,
                        'not-yet-linked',
                        '[]'::jsonb,
                        :evaluated_at,
                        NULL
                    )
                    """
                ),
                {
                    "id": delayed_assessment_id,
                    "source_record_id": unowned_source_id,
                    "evaluated_at": evaluated_at,
                },
            )
        assert _paper_search_change_revision(engine) == revision

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO source_record (
                        id,
                        work_id,
                        source_record_id,
                        content_hash,
                        raw_payload,
                        source
                    )
                    VALUES (
                        :source_id,
                        :work_id,
                        'search-trigger-logical-source',
                        :content_hash,
                        '{}'::jsonb,
                        'openalex'
                    )
                    """
                ),
                {
                    "source_id": owned_source_id,
                    "work_id": first_work_id,
                    "content_hash": "d" * 64,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "scope_assessment")
        }

        with engine.connect() as connection:
            assert connection.execute(
                text(
                    """
                    SELECT work_id, work_link_reason
                    FROM scope_assessment
                    WHERE id = :id
                    """
                ),
                {"id": delayed_assessment_id},
            ).one() == (
                first_work_id,
                "logical_source_owner_trigger",
            )

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
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
                    VALUES (
                        :id,
                        :source_record_id,
                        'search-trigger-insert',
                        true,
                        NULL,
                        '[]'::jsonb,
                        :evaluated_at,
                        :work_id
                    )
                    """
                ),
                {
                    "id": inserted_assessment_id,
                    "source_record_id": owned_source_id,
                    "evaluated_at": evaluated_at + timedelta(minutes=1),
                    "work_id": first_work_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "scope_assessment")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    DELETE FROM scope_assessment
                    WHERE id = :id
                    """
                ),
                {"id": inserted_assessment_id},
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "scope_assessment")
        }
    finally:
        engine.dispose()


def test_search_change_triggers_record_association_and_metric_lifecycles(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    first_work_id, second_work_id = _insert_search_trigger_works(engine)
    topic_id = uuid4()
    method_id = uuid4()
    metric_id = uuid4()
    measured_at = datetime(2026, 7, 16, 11, tzinfo=UTC)
    try:
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO topic (
                        id,
                        name,
                        normalized_name,
                        source
                    )
                    VALUES (
                        :id,
                        'Search Trigger Topic',
                        'search-trigger-topic',
                        'test'
                    )
                    """
                ),
                {"id": topic_id},
            )
            connection.execute(
                text(
                    """
                    INSERT INTO method (
                        id,
                        name,
                        normalized_name,
                        source
                    )
                    VALUES (
                        :id,
                        'Search Trigger Method',
                        'search-trigger-method',
                        'test'
                    )
                    """
                ),
                {"id": method_id},
            )

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work_topic (work_id, topic_id)
                    VALUES (:work_id, :topic_id)
                    """
                ),
                {
                    "work_id": first_work_id,
                    "topic_id": topic_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "work_topic")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE topic
                    SET name = 'Search Trigger Topic Revised'
                    WHERE id = :topic_id
                    """
                ),
                {"topic_id": topic_id},
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "topic")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE topic
                    SET name = name
                    WHERE id = :topic_id
                    """
                ),
                {"topic_id": topic_id},
            )
        assert _paper_search_change_revision(engine) == revision

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE work_topic
                    SET work_id = :second_work_id
                    WHERE work_id = :first_work_id
                      AND topic_id = :topic_id
                    """
                ),
                {
                    "first_work_id": first_work_id,
                    "second_work_id": second_work_id,
                    "topic_id": topic_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "work_topic"),
            (second_work_id, "work_topic"),
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    DELETE FROM work_topic
                    WHERE work_id = :work_id
                      AND topic_id = :topic_id
                    """
                ),
                {
                    "work_id": second_work_id,
                    "topic_id": topic_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (second_work_id, "work_topic")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO work_method (work_id, method_id)
                    VALUES (:work_id, :method_id)
                    """
                ),
                {
                    "work_id": first_work_id,
                    "method_id": method_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "work_method")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE method
                    SET normalized_name = 'search-trigger-method-revised'
                    WHERE id = :method_id
                    """
                ),
                {"method_id": method_id},
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "method")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE work_method
                    SET work_id = :second_work_id
                    WHERE work_id = :first_work_id
                      AND method_id = :method_id
                    """
                ),
                {
                    "first_work_id": first_work_id,
                    "second_work_id": second_work_id,
                    "method_id": method_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "work_method"),
            (second_work_id, "work_method"),
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    DELETE FROM work_method
                    WHERE work_id = :work_id
                      AND method_id = :method_id
                    """
                ),
                {
                    "work_id": second_work_id,
                    "method_id": method_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (second_work_id, "work_method")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    INSERT INTO metric_snapshot (
                        id,
                        work_id,
                        metric_name,
                        metric_value,
                        measured_at,
                        source
                    )
                    VALUES (
                        :id,
                        :work_id,
                        'search_trigger_metric',
                        1,
                        :measured_at,
                        'test'
                    )
                    """
                ),
                {
                    "id": metric_id,
                    "work_id": first_work_id,
                    "measured_at": measured_at,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "metric_snapshot")
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE metric_snapshot
                    SET work_id = :second_work_id
                    WHERE id = :id
                    """
                ),
                {
                    "id": metric_id,
                    "second_work_id": second_work_id,
                },
            )
        assert _paper_search_changes_after(engine, revision) == {
            (first_work_id, "metric_snapshot"),
            (second_work_id, "metric_snapshot"),
        }

        revision = _paper_search_change_revision(engine)
        with engine.begin() as connection:
            connection.execute(
                text(
                    """
                    DELETE FROM metric_snapshot
                    WHERE id = :id
                    """
                ),
                {"id": metric_id},
            )
        assert _paper_search_changes_after(engine, revision) == {
            (second_work_id, "metric_snapshot")
        }
    finally:
        engine.dispose()


def test_session_delete_and_sql_delete_have_identical_results(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    from paper_hub.models import Work

    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    try:
        orm_ids = _insert_delete_graph(engine, "orm-delete")
        sql_ids = _insert_delete_graph(engine, "sql-delete")

        with Session(engine) as session:
            work = session.get(Work, orm_ids["work"])
            assert work is not None
            session.delete(work)
            session.commit()

        with engine.begin() as connection:
            connection.execute(
                text("DELETE FROM work WHERE id = :id"),
                {"id": sql_ids["work"]},
            )

        expected = {
            "work": 0,
            "version": 0,
            "source": (None, None),
            "assertion": 1,
            "external": 0,
            "repository": 1,
            "repository_link": 0,
            "metric": 1,
            "ranking": 0,
        }
        assert _delete_result(engine, orm_ids) == expected
        assert _delete_result(engine, sql_ids) == expected
    finally:
        engine.dispose()


def test_loaded_version_delete_matches_sql_cascades(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    from paper_hub.models import PaperVersion

    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    try:
        orm_ids = _insert_delete_graph(engine, "orm-version-delete")
        sql_ids = _insert_delete_graph(engine, "sql-version-delete")

        with Session(engine) as session:
            version = session.get(PaperVersion, orm_ids["version"])
            assert version is not None
            assert len(version.source_records) == 1
            assert len(version.source_records[0].field_assertions) == 1
            session.delete(version)
            session.commit()

        with engine.begin() as connection:
            connection.execute(
                text("DELETE FROM paper_version WHERE id = :id"),
                {"id": sql_ids["version"]},
            )

        expected = {
            "work": 1,
            "version": 0,
            "source": (None, None),
            "assertion": 1,
            "external": 1,
            "repository": 1,
            "repository_link": 1,
            "metric": 1,
            "ranking": 1,
        }
        assert _delete_result(engine, orm_ids) == expected
        assert _delete_result(engine, sql_ids) == expected
    finally:
        engine.dispose()


def test_database_schema_matches_orm_metadata(
    alembic_config,
    clean_postgres_url: str,
) -> None:
    from paper_hub.models import Base

    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    try:
        with engine.connect() as connection:
            context = MigrationContext.configure(
                connection,
                opts={
                    "compare_type": True,
                    "compare_server_default": True,
                },
            )
            assert compare_metadata(context, Base.metadata) == []
    finally:
        engine.dispose()
