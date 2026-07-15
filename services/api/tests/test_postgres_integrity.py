from datetime import UTC, datetime
import hashlib
import json
from pathlib import Path
from uuid import UUID, uuid4

from alembic import command
from alembic.autogenerate import compare_metadata
from alembic.migration import MigrationContext
import pytest
from sqlalchemy import create_engine, inspect, text
from sqlalchemy.exc import DBAPIError, IntegrityError
from sqlalchemy.orm import Session


SERVICE_ROOT = Path(__file__).resolve().parents[1]
INITIAL_MIGRATION = (
    SERVICE_ROOT / "alembic" / "versions" / "0001_initial_schema.py"
)
INITIAL_MIGRATION_SHA256 = (
    "7771e5358c3679aed87a1299e3aff52ad07bb0e945a2754ccc7c973bccd7c931"
)


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
                    id, work_id, paper_version_id, source_record_id,
                    scheme, normalized_value, raw_value, source
                )
                VALUES (
                    :id, :work_id, :paper_version_id, :source_record_id,
                    'doi', :normalized_value, :raw_value, 'test'
                )
                """
            ),
            {
                "id": ids["external"],
                "work_id": ids["work"],
                "paper_version_id": ids["version"],
                "source_record_id": ids["source"],
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
) -> None:
    command.upgrade(alembic_config, "0001_initial_schema")
    engine = create_engine(clean_postgres_url)
    try:
        old_columns = {
            column["name"]
            for column in inspect(engine).get_columns("ranking_snapshot")
        }
        assert {"subject_type", "subject_id"} <= old_columns
        assert "work_id" not in old_columns

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

        command.downgrade(alembic_config, "0001_initial_schema")
        downgraded_columns = {
            column["name"]
            for column in inspect(engine).get_columns("ranking_snapshot")
        }
        assert {"subject_type", "subject_id"} <= downgraded_columns
        assert "work_id" not in downgraded_columns
        with engine.connect() as connection:
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
            assert len(version.external_identifiers) == 1
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
            "external": 0,
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
