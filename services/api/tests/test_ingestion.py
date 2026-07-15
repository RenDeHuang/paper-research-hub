from datetime import UTC, datetime
import json
from pathlib import Path

from alembic import command
import pytest
from sqlalchemy import create_engine, func, select, text
from sqlalchemy.exc import DBAPIError
from sqlalchemy.orm import Session


FIXTURE_PATH = Path(__file__).parent / "fixtures" / "openalex_works.json"


def _fixture() -> dict:
    return json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))


def _record(raw: dict, *, minute: int = 0):
    from paper_hub.connectors.openalex import OpenAlexConnector

    return OpenAlexConnector.parse_record(
        raw,
        retrieved_at=datetime(2026, 7, 15, 9, minute, tzinfo=UTC),
        http_status=200,
    )


@pytest.fixture
def migrated_engine(alembic_config, clean_postgres_url):
    command.upgrade(alembic_config, "head")
    engine = create_engine(clean_postgres_url)
    try:
        yield engine
    finally:
        engine.dispose()


def test_same_snapshot_is_idempotent_in_real_postgresql(migrated_engine) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import (
        ExternalIdentifier,
        FieldAssertion,
        SourceRecord,
        Work,
    )

    record = _record(_fixture()["results"][0])
    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(record)
        second = IngestionService(session).ingest(record)
        session.commit()

        assert first.status == "inserted"
        assert second.status == "unchanged"
        assert second.work_id == first.work_id
        assert second.source_record_id == first.source_record_id
        assert session.scalar(select(func.count()).select_from(Work)) == 1
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord)) == 1
        )
        assert (
            session.scalar(
                select(func.count()).select_from(ExternalIdentifier)
            )
            == 5
        )
        assert (
            session.scalar(select(func.count()).select_from(FieldAssertion))
            >= 12
        )


def test_new_hash_adds_snapshot_and_assertions_without_duplicate_work(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import FieldAssertion, SourceRecord, Work

    raw = _fixture()["results"][0]
    updated = json.loads(json.dumps(raw))
    updated["title"] = "AgentBench Updated: Evaluating LLM Agents"
    updated["display_name"] = updated["title"]
    updated["cited_by_count"] = 45
    updated["updated_date"] = "2026-07-15T08:00:00"

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(_record(raw))
        second = IngestionService(session).ingest(
            _record(updated, minute=5)
        )
        session.commit()

        work = session.get(Work, first.work_id)
        source_records = session.scalars(
            select(SourceRecord).order_by(SourceRecord.retrieved_at)
        ).all()
        assertions = session.scalars(
            select(FieldAssertion).where(
                FieldAssertion.field_name == "citation_count"
            )
        ).all()

        assert second.status == "updated"
        assert second.work_id == first.work_id
        assert session.scalar(select(func.count()).select_from(Work)) == 1
        assert len(source_records) == 2
        assert source_records[0].raw_payload["cited_by_count"] == 42
        assert source_records[1].raw_payload["cited_by_count"] == 45
        assert source_records[1].source_updated_at == datetime(
            2026,
            7,
            15,
            8,
            0,
            tzinfo=UTC,
        )
        assert {assertion.value for assertion in assertions} == {42, 45}
        assert work.title == updated["title"]


def test_raw_snapshot_columns_are_database_immutable(migrated_engine) -> None:
    from paper_hub.ingestion import IngestionService

    with Session(migrated_engine) as session:
        result = IngestionService(session).ingest(
            _record(_fixture()["results"][0])
        )
        session.commit()

    with pytest.raises(DBAPIError, match="immutable"):
        with migrated_engine.begin() as connection:
            connection.execute(
                text(
                    """
                    UPDATE source_record
                    SET raw_payload = CAST(:payload AS jsonb)
                    WHERE id = :source_record_id
                    """
                ),
                {
                    "payload": json.dumps({"mutated": True}),
                    "source_record_id": result.source_record_id,
                },
            )


def test_mutated_raw_payload_is_rejected_before_insert(migrated_engine) -> None:
    from paper_hub.ingestion import IngestionError, IngestionService
    from paper_hub.models import SourceRecord, Work

    record = _record(_fixture()["results"][0])
    record.raw_payload["cited_by_count"] = 999

    with Session(migrated_engine) as session:
        with pytest.raises(IngestionError, match="content hash"):
            IngestionService(session).ingest(record)
        session.rollback()

        assert session.scalar(select(func.count()).select_from(Work)) == 0
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord)) == 0
        )


def test_controlled_external_identifier_deduplicates_without_title_matching(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import SourceRecord, Work

    first_raw = _fixture()["results"][0]
    second_raw = json.loads(json.dumps(first_raw))
    second_raw["id"] = "https://openalex.org/W1111111111"
    second_raw["ids"]["openalex"] = second_raw["id"]
    second_raw["title"] = "A Completely Different Display Title"
    second_raw["display_name"] = second_raw["title"]
    second_raw["updated_date"] = "2026-07-15T09:00:00"

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(_record(first_raw))
        second = IngestionService(session).ingest(
            _record(second_raw, minute=10)
        )
        session.commit()

        assert first.work_id == second.work_id
        assert session.scalar(select(func.count()).select_from(Work)) == 1
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord)) == 2
        )


def test_similar_titles_without_shared_controlled_id_do_not_merge(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import Work

    first_raw = json.loads(json.dumps(_fixture()["results"][0]))
    first_raw["doi"] = None
    first_raw["ids"] = {"openalex": first_raw["id"]}
    first_raw["primary_location"] = None
    first_raw["best_oa_location"] = None
    first_raw["locations"] = []

    second_raw = json.loads(json.dumps(first_raw))
    second_raw["id"] = "https://openalex.org/W2222222222"
    second_raw["ids"] = {"openalex": second_raw["id"]}
    second_raw["title"] = "AgentBench Evaluating LLM Agents with Tool-Use"
    second_raw["display_name"] = second_raw["title"]

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(_record(first_raw))
        second = IngestionService(session).ingest(
            _record(second_raw, minute=10)
        )
        session.commit()

        assert first.work_id != second.work_id
        assert session.scalar(select(func.count()).select_from(Work)) == 2


def test_out_of_scope_record_is_reported_and_not_persisted(
    migrated_engine,
) -> None:
    from paper_hub.connectors.openalex import OPENALEX_SCOPE_RULE_VERSION
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import SourceRecord, Work

    with Session(migrated_engine) as session:
        result = IngestionService(session).ingest(
            _record(_fixture()["results"][1])
        )
        session.commit()

        assert result.status == "excluded"
        assert result.reason == "no_agent_llm_scope_term_match"
        assert result.scope_rule_version == OPENALEX_SCOPE_RULE_VERSION
        assert result.scope_evidence == ()
        assert session.scalar(select(func.count()).select_from(Work)) == 0
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord)) == 0
        )


def test_ingestion_persists_topics_code_and_citation_snapshot(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import CodeRepository, MetricSnapshot, Topic, Work

    with Session(migrated_engine) as session:
        result = IngestionService(session).ingest(
            _record(_fixture()["results"][0])
        )
        session.commit()
        work = session.get(Work, result.work_id)

        assert {topic.name for topic in work.topics} == {
            "Large Language Models",
            "Autonomous Agents",
        }
        repository = session.scalar(select(CodeRepository))
        assert repository.normalized_url == (
            "https://github.com/example/agentbench"
        )
        metric = session.scalar(select(MetricSnapshot))
        assert metric.metric_name == "citation_count"
        assert int(metric.metric_value) == 42
        assert session.scalar(select(func.count()).select_from(Topic)) == 2


def test_cli_requires_database_url_and_contact_email(
    monkeypatch,
    capsys,
) -> None:
    from paper_hub.cli import main
    from paper_hub.config import get_settings

    monkeypatch.delenv("DATABASE_URL", raising=False)
    monkeypatch.setenv("OPENALEX_CONTACT_EMAIL", "research@example.com")
    get_settings.cache_clear()
    assert main(["sync-openalex", "--query", "agents"]) == 2
    assert "DATABASE_URL" in capsys.readouterr().err

    monkeypatch.setenv(
        "DATABASE_URL",
        "postgresql+psycopg://paper_hub:paper_hub@localhost/paper_hub",
    )
    monkeypatch.delenv("OPENALEX_CONTACT_EMAIL", raising=False)
    get_settings.cache_clear()
    assert main(["sync-openalex", "--query", "agents"]) == 2
    assert "OPENALEX_CONTACT_EMAIL" in capsys.readouterr().err
    get_settings.cache_clear()


def test_cli_reports_invalid_contact_email_as_email_error(
    monkeypatch,
    capsys,
) -> None:
    from paper_hub.cli import main
    from paper_hub.config import get_settings

    monkeypatch.setenv(
        "DATABASE_URL",
        "postgresql+psycopg://paper_hub:paper_hub@localhost/paper_hub",
    )
    monkeypatch.setenv("OPENALEX_CONTACT_EMAIL", "not-an-email")
    get_settings.cache_clear()

    assert main(["sync-openalex", "--query", "agents"]) == 2
    error = capsys.readouterr().err
    assert "OPENALEX_CONTACT_EMAIL" in error
    assert "DATABASE_URL" not in error
    get_settings.cache_clear()


def test_cli_rejects_max_results_above_hard_limit(capsys) -> None:
    from paper_hub.cli import main
    from paper_hub.connectors.openalex import OPENALEX_MAX_RESULTS

    with pytest.raises(SystemExit) as exc_info:
        main(
            [
                "sync-openalex",
                "--query",
                "agents",
                "--max-results",
                str(OPENALEX_MAX_RESULTS + 1),
            ]
        )

    assert exc_info.value.code == 2
    assert "max-results" in capsys.readouterr().err
