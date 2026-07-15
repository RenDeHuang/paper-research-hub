from datetime import UTC, datetime
import hashlib
from io import StringIO
import json
from pathlib import Path
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
                "id": ids["repository"],
                "work_id": ids["work"],
                "repository_name": suffix,
                "repository_url": f"https://github.test/paper/{suffix}",
                "normalized_url": f"https://github.test/paper/{suffix}",
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
            assert (
                work.projection_source_record_id
                == latest_record.source_record_id
            )
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
            ) == "0006_reset_work_projections"
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
        _assert_integrity_error(
            engine,
            """
            INSERT INTO scope_assessment (
                id, source_record_id, rule_version, included,
                reason, evidence, evaluated_at, work_id
            )
            VALUES (
                :id, :source_record_id, 'scope-v2', false,
                'excluded', '[]'::jsonb, :evaluated_at, :work_id
            )
            """,
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
            "repository": 0,
            "metric": 0,
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
