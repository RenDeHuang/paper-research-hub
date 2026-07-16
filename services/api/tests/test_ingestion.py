from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
from datetime import UTC, datetime
import json
from pathlib import Path
from threading import Barrier, Event, Lock
from uuid import UUID

from alembic import command
import pytest
from sqlalchemy import create_engine, event, func, select, text
from sqlalchemy.exc import DBAPIError, SQLAlchemyError
from sqlalchemy.orm import Session, Session as SQLAlchemySession


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


def _with_scope(
    record,
    *,
    rule_version: str,
    included: bool,
):
    from paper_hub.connectors.base import ScopeDecision, ScopeEvidence

    evidence = (
        ScopeEvidence(
            field="title",
            term="scope-upgrade",
            matched_text=record.parsed.title,
        ),
    ) if included else ()
    scope = ScopeDecision(
        included=included,
        rule_version=rule_version,
        evidence=evidence,
        reason=None if included else f"excluded_by_{rule_version}",
    )
    return replace(record, parsed=replace(record.parsed, scope=scope))


def _new_hash_with_scope(
    record,
    *,
    minute: int,
    included: bool,
    rule_version: str = "agent-llm-scope-v1",
):
    raw = json.loads(json.dumps(record.raw_payload))
    raw["cited_by_count"] = int(raw.get("cited_by_count") or 0) + minute + 1
    raw["publication_date"] = datetime.now(UTC).date().isoformat()
    raw["publication_year"] = datetime.now(UTC).year
    raw["title"] = (
        "LLM Agent Scope Reassessment"
        if included
        else "Protein Folding Benchmark"
    )
    raw["display_name"] = raw["title"]
    raw["abstract_inverted_index"] = None
    raw["topics"] = []
    parsed = _record(raw, minute=minute)
    assert parsed.parsed.scope.included is included
    return _with_scope(
        parsed,
        rule_version=rule_version,
        included=included,
    )


def _independent_work(
    *,
    openalex_id: str,
    doi: str,
) -> dict:
    raw = json.loads(json.dumps(_fixture()["results"][0]))
    raw["id"] = f"https://openalex.org/{openalex_id}"
    raw["doi"] = f"https://doi.org/{doi}"
    raw["ids"] = {
        "openalex": raw["id"],
        "doi": raw["doi"],
    }
    raw["primary_location"] = None
    raw["best_oa_location"] = None
    raw["locations"] = []
    return raw


def _concurrent_ingest_at_insert(
    engine,
    *,
    records: tuple,
    table_name: str,
) -> list[str]:
    from paper_hub.ingestion import IngestionService

    start_barrier = Barrier(len(records))
    insert_barrier = Barrier(len(records))
    synchronized_connections: set[int] = set()
    synchronized_connections_lock = Lock()

    def synchronize_insert(
        _connection,
        _cursor,
        statement,
        _parameters,
        _context,
        _executemany,
    ) -> None:
        if not statement.lstrip().startswith(
            f"INSERT INTO {table_name}"
        ):
            return
        connection_id = id(_connection)
        with synchronized_connections_lock:
            if connection_id in synchronized_connections:
                return
            synchronized_connections.add(connection_id)
        insert_barrier.wait(timeout=10)

    def ingest_once(record) -> str:
        with Session(engine) as session:
            start_barrier.wait(timeout=10)
            result = IngestionService(session).ingest(record)
            session.commit()
            return result.status

    event.listen(
        engine,
        "before_cursor_execute",
        synchronize_insert,
    )
    try:
        with ThreadPoolExecutor(max_workers=len(records)) as executor:
            futures = [
                executor.submit(ingest_once, record)
                for record in records
            ]
            return sorted(
                future.result(timeout=30)
                for future in futures
            )
    finally:
        event.remove(
            engine,
            "before_cursor_execute",
            synchronize_insert,
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
            == 8
        )
        assert set(
            session.scalars(select(ExternalIdentifier.scheme)).all()
        ) == {
            "openalex",
            "doi",
            "arxiv",
            "openreview",
            "s2",
            "mag",
            "pmid",
            "pmcid",
        }
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


def test_older_snapshot_does_not_overwrite_newer_work_projection(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import RecordStatus, SourceRecord, Work

    older_raw = json.loads(json.dumps(_fixture()["results"][0]))
    newer_raw = json.loads(json.dumps(older_raw))
    newer_raw["title"] = "Newest Retracted LLM Agent Benchmark"
    newer_raw["display_name"] = newer_raw["title"]
    newer_raw["updated_date"] = "2026-07-16T08:00:00"
    newer_raw["is_retracted"] = True

    with Session(migrated_engine) as session:
        newer = IngestionService(session).ingest(
            _record(newer_raw, minute=5)
        )
        older = IngestionService(session).ingest(_record(older_raw))
        session.commit()

        work = session.get(Work, newer.work_id)
        assert older.status == "updated"
        assert work.title == newer_raw["title"]
        assert work.status == RecordStatus.RETRACTED
        assert work.projection_source == "openalex"
        projection = session.get(
            SourceRecord,
            work.projection_source_record_id,
        )
        assert projection is not None
        assert projection.source_record_id == "W2741809807"
        assert projection.retrieved_at == _record(
            newer_raw,
            minute=5,
        ).retrieved_at
        assert work.projection_source_updated_at == datetime(
            2026,
            7,
            16,
            8,
            0,
            tzinfo=UTC,
        )
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord))
            == 2
        )


def test_replay_with_nullable_projection_timestamp_keeps_newer_projection(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import SourceRecord, Work

    older_raw = json.loads(json.dumps(_fixture()["results"][0]))
    older_raw["title"] = "Older Nullable Timestamp Agent"
    older_raw["display_name"] = older_raw["title"]
    older_raw["updated_date"] = "2026-07-14T08:00:00"
    older_raw["topics"] = [
        {
            "id": "https://openalex.org/T90002",
            "display_name": "Older Nullable Topic",
            "score": 0.8,
        }
    ]
    newer_raw = json.loads(json.dumps(older_raw))
    newer_raw["title"] = "Newer Nullable Timestamp Agent"
    newer_raw["display_name"] = newer_raw["title"]
    newer_raw["updated_date"] = "2026-07-16T08:00:00"
    newer_raw["cited_by_count"] = (
        int(newer_raw.get("cited_by_count") or 0) + 1
    )
    newer_raw["topics"] = [
        {
            "id": "https://openalex.org/T90003",
            "display_name": "Newer Nullable Topic",
            "score": 0.9,
        }
    ]
    older_record = _record(older_raw, minute=1)
    newer_record = _record(newer_raw, minute=2)

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(newer_record)
        session.commit()
        IngestionService(session).ingest(older_record)
        session.commit()

        work = session.get(Work, first.work_id)
        assert work is not None
        newer_projection_id = work.projection_source_record_id
        newer_projection = session.get(SourceRecord, newer_projection_id)
        assert newer_projection is not None
        assert newer_projection.retrieved_at == newer_record.retrieved_at

        session.execute(
            text(
                """
                UPDATE work
                SET projection_source_updated_at = NULL
                WHERE id = :work_id
                """
            ),
            {"work_id": first.work_id},
        )
        session.commit()
        session.expire_all()

        replay = IngestionService(session).ingest(older_record)
        session.commit()
        session.expire_all()

        work = session.get(Work, first.work_id)
        assert work is not None
        assert replay.status == "unchanged"
        assert work.projection_source_record_id == newer_projection_id
        assert work.projection_source_updated_at is None
        assert work.title == newer_raw["title"]
        assert {topic.normalized_name for topic in work.topics} == {
            "newer nullable topic"
        }


def test_new_projection_replaces_work_taxonomy_with_exact_snapshot(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import Benchmark, Dataset, Method, Work

    older_raw = json.loads(json.dumps(_fixture()["results"][0]))
    newer_raw = json.loads(json.dumps(older_raw))
    newer_raw["updated_date"] = "2026-07-16T08:00:00"
    newer_raw["cited_by_count"] = (
        int(newer_raw.get("cited_by_count") or 0) + 1
    )
    newer_raw["topics"] = [
        {
            "id": "https://openalex.org/T90001",
            "display_name": "Exact Projection Topic",
            "score": 0.999,
        }
    ]

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(_record(older_raw))
        session.commit()

        work = session.get(Work, first.work_id)
        assert work is not None
        work.methods.append(
            Method(
                name="Stale Projection Method",
                normalized_name="stale-projection-method",
                description=None,
                source="test",
                retrieved_at=datetime(2026, 7, 15, tzinfo=UTC),
            )
        )
        work.datasets.append(
            Dataset(
                name="Stale Projection Dataset",
                normalized_name="stale-projection-dataset",
                description=None,
                homepage_url=None,
                source="test",
                retrieved_at=datetime(2026, 7, 15, tzinfo=UTC),
            )
        )
        work.benchmarks.append(
            Benchmark(
                name="Stale Projection Benchmark",
                normalized_name="stale-projection-benchmark",
                description=None,
                homepage_url=None,
                source="test",
                retrieved_at=datetime(2026, 7, 15, tzinfo=UTC),
            )
        )
        session.commit()

        second = IngestionService(session).ingest(
            _record(newer_raw, minute=5)
        )
        session.commit()
        session.expire_all()

        work = session.get(Work, first.work_id)
        assert work is not None
        assert second.status == "updated"
        assert work.projection_source_record_id == second.source_record_id
        assert {topic.name for topic in work.topics} == {
            "Exact Projection Topic"
        }
        assert work.methods == []
        assert work.datasets == []
        assert work.benchmarks == []


def test_non_projection_snapshot_does_not_change_work_taxonomy(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import Benchmark, Dataset, Method, Work

    current_raw = json.loads(json.dumps(_fixture()["results"][0]))
    current_raw["updated_date"] = "2026-07-16T08:00:00"
    current_raw["topics"] = [
        {
            "id": "https://openalex.org/T90002",
            "display_name": "Current Projection Topic",
            "score": 0.999,
        }
    ]
    lower_priority_raw = json.loads(json.dumps(current_raw))
    lower_priority_raw["updated_date"] = "2026-07-15T08:00:00"
    lower_priority_raw["cited_by_count"] = (
        int(lower_priority_raw.get("cited_by_count") or 0) + 1
    )
    lower_priority_raw["topics"] = [
        {
            "id": "https://openalex.org/T90003",
            "display_name": "Rejected Lower Priority Topic",
            "score": 0.999,
        }
    ]

    with Session(migrated_engine) as session:
        current = IngestionService(session).ingest(
            _record(current_raw)
        )
        session.commit()

        work = session.get(Work, current.work_id)
        assert work is not None
        work.methods.append(
            Method(
                name="Current Projection Method",
                normalized_name="current-projection-method",
                description=None,
                source="test",
                retrieved_at=datetime(2026, 7, 16, tzinfo=UTC),
            )
        )
        work.datasets.append(
            Dataset(
                name="Current Projection Dataset",
                normalized_name="current-projection-dataset",
                description=None,
                homepage_url=None,
                source="test",
                retrieved_at=datetime(2026, 7, 16, tzinfo=UTC),
            )
        )
        work.benchmarks.append(
            Benchmark(
                name="Current Projection Benchmark",
                normalized_name="current-projection-benchmark",
                description=None,
                homepage_url=None,
                source="test",
                retrieved_at=datetime(2026, 7, 16, tzinfo=UTC),
            )
        )
        session.commit()

        lower_priority = IngestionService(session).ingest(
            _record(lower_priority_raw, minute=5)
        )
        session.commit()
        session.expire_all()

        work = session.get(Work, current.work_id)
        assert work is not None
        assert lower_priority.status == "updated"
        assert (
            work.projection_source_record_id
            == current.source_record_id
        )
        assert {topic.name for topic in work.topics} == {
            "Current Projection Topic"
        }
        assert {method.name for method in work.methods} == {
            "Current Projection Method"
        }
        assert {dataset.name for dataset in work.datasets} == {
            "Current Projection Dataset"
        }
        assert {benchmark.name for benchmark in work.benchmarks} == {
            "Current Projection Benchmark"
        }


@pytest.mark.parametrize(
    ("replay_order", "expected_statuses"),
    [
        (("older", "newer"), ["updated", "updated"]),
        (("newer", "older"), ["updated", "unchanged"]),
    ],
)
def test_same_rule_replay_after_projection_reset_converges_to_latest_snapshot(
    migrated_engine,
    replay_order: tuple[str, str],
    expected_statuses: list[str],
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import RecordStatus, SourceRecord, Work

    older_raw = json.loads(json.dumps(_fixture()["results"][0]))
    older_raw["title"] = "Older LLM Agent Projection"
    older_raw["display_name"] = older_raw["title"]
    older_raw["abstract_inverted_index"] = {
        "Older": [0],
        "LLM": [1],
        "agent": [2],
        "abstract": [3],
    }
    older_raw["updated_date"] = "2026-07-14T08:00:00"
    older_raw["is_retracted"] = False

    newer_raw = json.loads(json.dumps(older_raw))
    newer_raw["title"] = "Newest Retracted LLM Agent Projection"
    newer_raw["display_name"] = newer_raw["title"]
    newer_raw["abstract_inverted_index"] = {
        "Newest": [0],
        "retracted": [1],
        "LLM": [2],
        "agent": [3],
        "abstract": [4],
    }
    newer_raw["updated_date"] = "2026-07-16T08:00:00"
    newer_raw["is_retracted"] = True

    records = {
        "older": _record(older_raw),
        "newer": _record(newer_raw, minute=1),
    }
    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(records["older"])
        IngestionService(session).ingest(records["newer"])
        session.commit()

        session.execute(
            text(
                """
                UPDATE work
                SET
                    title = 'Unverified pre-0006 projection',
                    abstract = 'Unverified abstract',
                    status = 'active',
                    projection_source = NULL,
                    projection_source_record_id = NULL,
                    projection_source_updated_at = NULL
                WHERE id = :work_id
                """
            ),
            {"work_id": first.work_id},
        )
        session.commit()

        statuses = []
        for key in replay_order:
            statuses.append(
                IngestionService(session).ingest(records[key]).status
            )
            session.commit()

        session.expire_all()
        work = session.get(Work, first.work_id)
        assert statuses == expected_statuses
        assert work.title == newer_raw["title"]
        assert work.abstract == "Newest retracted LLM agent abstract"
        assert work.status == RecordStatus.RETRACTED
        assert work.projection_source == "openalex"
        projection = session.get(
            SourceRecord,
            work.projection_source_record_id,
        )
        assert projection is not None
        assert projection.source_record_id == "W2741809807"
        assert projection.retrieved_at == records["newer"].retrieved_at
        assert (
            work.projection_source_updated_at
            == records["newer"].source_updated_at
        )
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord))
            == 2
        )


def test_concurrent_same_snapshot_is_idempotent_in_real_postgresql(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import ScopeAssessment, SourceRecord, Work

    record = _record(_fixture()["results"][0])
    barrier = Barrier(2)

    def ingest_once() -> str:
        with Session(migrated_engine) as session:
            barrier.wait(timeout=10)
            result = IngestionService(session).ingest(record)
            session.commit()
            return result.status

    with ThreadPoolExecutor(max_workers=2) as executor:
        statuses = sorted(
            future.result(timeout=30)
            for future in (
                executor.submit(ingest_once),
                executor.submit(ingest_once),
            )
        )

    assert statuses == ["inserted", "unchanged"]
    with Session(migrated_engine) as session:
        assert session.scalar(select(func.count()).select_from(Work)) == 1
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord))
            == 1
        )
        assert (
            session.scalar(
                select(func.count()).select_from(ScopeAssessment)
            )
            == 1
        )


def test_concurrent_shared_canonical_identity_creates_one_work(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import SourceRecord, Work

    first_raw = json.loads(json.dumps(_fixture()["results"][0]))
    first_raw["ids"] = {
        "openalex": first_raw["id"],
        "doi": first_raw["doi"],
    }
    first_raw["primary_location"] = None
    first_raw["best_oa_location"] = None
    first_raw["locations"] = []

    second_raw = json.loads(json.dumps(first_raw))
    second_raw["id"] = "https://openalex.org/W2222222222"
    second_raw["ids"]["openalex"] = second_raw["id"]
    second_raw["updated_date"] = "2026-07-15T08:00:00"
    barrier = Barrier(2)

    def ingest_once(raw: dict, minute: int) -> str:
        with Session(migrated_engine) as session:
            barrier.wait(timeout=10)
            result = IngestionService(session).ingest(
                _record(raw, minute=minute)
            )
            session.commit()
            return result.status

    with ThreadPoolExecutor(max_workers=2) as executor:
        statuses = sorted(
            future.result(timeout=30)
            for future in (
                executor.submit(ingest_once, first_raw, 0),
                executor.submit(ingest_once, second_raw, 1),
            )
        )

    assert statuses == ["inserted", "updated"]
    with Session(migrated_engine) as session:
        assert session.scalar(select(func.count()).select_from(Work)) == 1
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord))
            == 2
        )


def test_concurrent_disjoint_identifiers_cannot_regress_work_projection(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import SourceRecord, Work

    seed_raw = json.loads(json.dumps(_fixture()["results"][0]))
    seed_raw["updated_date"] = "2026-07-13T08:00:00"
    seed_record = _record(seed_raw)
    identifiers = {
        identifier.scheme: identifier
        for identifier in seed_record.parsed.external_identifiers
    }
    doi_identifier = identifiers["doi"]
    openalex_identifier = identifiers["openalex"]

    older_raw = json.loads(json.dumps(_fixture()["results"][0]))
    older_raw["id"] = "https://openalex.org/W9000000001"
    older_raw["ids"]["openalex"] = older_raw["id"]
    older_raw["title"] = "Stale Older Disjoint Identity Projection"
    older_raw["display_name"] = older_raw["title"]
    older_raw["updated_date"] = "2026-07-14T08:00:00"
    older = _record(older_raw, minute=1)
    older = replace(
        older,
        parsed=replace(
            older.parsed,
            canonical_key=(
                f"{doi_identifier.scheme}:"
                f"{doi_identifier.normalized_value}"
            ),
            external_identifiers=(doi_identifier,),
        ),
    )

    newer_raw = json.loads(json.dumps(_fixture()["results"][0]))
    newer_raw["id"] = "https://openalex.org/W9000000002"
    newer_raw["ids"]["openalex"] = newer_raw["id"]
    newer_raw["title"] = "Fresh Newer Disjoint Identity Projection"
    newer_raw["display_name"] = newer_raw["title"]
    newer_raw["updated_date"] = "2026-07-16T08:00:00"
    newer = _record(newer_raw, minute=2)
    newer = replace(
        newer,
        parsed=replace(
            newer.parsed,
            canonical_key=(
                f"{openalex_identifier.scheme}:"
                f"{openalex_identifier.normalized_value}"
            ),
            external_identifiers=(openalex_identifier,),
        ),
    )
    assert {
        identifier.scheme
        for identifier in older.parsed.external_identifiers
    } == {"doi"}
    assert {
        identifier.scheme
        for identifier in newer.parsed.external_identifiers
    } == {"openalex"}
    assert older.source_record_id != newer.source_record_id
    assert older.content_hash != newer.content_hash
    assert older.source_updated_at < newer.source_updated_at

    with Session(migrated_engine) as session:
        seed = IngestionService(session).ingest(seed_record)
        session.commit()

    resolution_barrier = Barrier(2)
    newer_committed = Event()

    class CoordinatedIngestionService(IngestionService):
        def __init__(self, session, *, label: str) -> None:
            super().__init__(session)
            self.label = label

        def _resolve_work(self, parsed):
            work = super()._resolve_work(parsed)
            resolution_barrier.wait(timeout=10)
            if self.label == "older":
                assert newer_committed.wait(timeout=10)
            return work

    def ingest_once(label: str, record) -> tuple[str, UUID, UUID, str]:
        with Session(migrated_engine) as session:
            result = CoordinatedIngestionService(
                session,
                label=label,
            ).ingest(record)
            observed = session.get(Work, seed.work_id)
            assert observed is not None
            observed_projection_id = observed.projection_source_record_id
            observed_title = observed.title
            session.commit()
            if label == "newer":
                newer_committed.set()
            assert result.work_id is not None
            assert observed_projection_id is not None
            return (
                result.status,
                result.work_id,
                observed_projection_id,
                observed_title,
            )

    with ThreadPoolExecutor(max_workers=2) as executor:
        results = tuple(
            future.result(timeout=30)
            for future in (
                executor.submit(ingest_once, "older", older),
                executor.submit(ingest_once, "newer", newer),
            )
        )

    assert sorted(result[0] for result in results) == [
        "updated",
        "updated",
    ]
    assert {result[1] for result in results} == {seed.work_id}
    with Session(migrated_engine) as session:
        work = session.get(Work, seed.work_id)
        assert work is not None
        projection = session.get(
            SourceRecord,
            work.projection_source_record_id,
        )
        assert projection is not None
        assert work.title == newer_raw["title"]
        assert work.projection_source_updated_at == newer.source_updated_at
        assert projection.source_record_id == newer.source_record_id
        assert {
            result[2] for result in results
        } == {work.projection_source_record_id}
        assert {result[3] for result in results} == {newer_raw["title"]}
        assert session.scalar(select(func.count()).select_from(Work)) == 1
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord))
            == 3
        )


def test_concurrent_distinct_works_share_topics_without_unique_failure(
    migrated_engine,
) -> None:
    from paper_hub.models import SourceRecord, Topic, Work, work_topic

    first_raw = _independent_work(
        openalex_id="W5000000001",
        doi="10.1000/concurrent-topic-one",
    )
    second_raw = _independent_work(
        openalex_id="W5000000002",
        doi="10.1000/concurrent-topic-two",
    )
    for raw in (first_raw, second_raw):
        raw["abstract_inverted_index"] = {
            "LLM": [0],
            "agents": [1],
            "share": [2],
            "topics": [3],
        }
    second_raw["topics"] = list(reversed(second_raw["topics"]))

    statuses = _concurrent_ingest_at_insert(
        migrated_engine,
        records=(
            _record(first_raw),
            _record(second_raw, minute=1),
        ),
        table_name="topic",
    )

    assert statuses == ["inserted", "inserted"]
    with Session(migrated_engine) as session:
        works = session.scalars(
            select(Work).order_by(Work.canonical_key)
        ).all()
        assert len(works) == 2
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord))
            == 2
        )
        assert session.scalar(select(func.count()).select_from(Topic)) == 2
        assert (
            session.scalar(select(func.count()).select_from(work_topic))
            == 4
        )


def test_concurrent_distinct_works_share_repository_without_unique_failure(
    migrated_engine,
) -> None:
    from paper_hub.models import CodeRepository, SourceRecord, Work

    first_raw = _independent_work(
        openalex_id="W5000000003",
        doi="10.1000/concurrent-repository-one",
    )
    second_raw = _independent_work(
        openalex_id="W5000000004",
        doi="10.1000/concurrent-repository-two",
    )
    for raw in (first_raw, second_raw):
        raw["topics"] = []
    first_raw["abstract_inverted_index"] = {
        "LLM": [0],
        "agents": [1],
        "publish": [2],
        "https://github.com/example/shared-one": [3],
        "and": [4],
        "https://github.com/example/shared-two": [5],
    }
    second_raw["abstract_inverted_index"] = {
        "LLM": [0],
        "agents": [1],
        "publish": [2],
        "https://github.com/example/shared-two": [3],
        "and": [4],
        "https://github.com/example/shared-one": [5],
    }

    statuses = _concurrent_ingest_at_insert(
        migrated_engine,
        records=(
            _record(first_raw),
            _record(second_raw, minute=1),
        ),
        table_name="code_repository",
    )

    assert statuses == ["inserted", "inserted"]
    with Session(migrated_engine) as session:
        works = session.scalars(
            select(Work).order_by(Work.canonical_key)
        ).all()
        assert len(works) == 2
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord))
            == 2
        )
        assert (
            session.scalar(
                select(func.count()).select_from(CodeRepository)
            )
            == 2
        )
        expected_urls = {
            "https://github.com/example/shared-one",
            "https://github.com/example/shared-two",
        }
        assert all(
            {
                repository.normalized_url
                for repository in work.code_repositories
            }
            == expected_urls
            for work in works
        )


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


def test_stronger_canonical_identifier_upgrades_existing_work(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import Work

    openalex_only = json.loads(json.dumps(_fixture()["results"][0]))
    openalex_only["doi"] = None
    openalex_only["ids"] = {"openalex": openalex_only["id"]}
    openalex_only["primary_location"] = None
    openalex_only["best_oa_location"] = None
    openalex_only["locations"] = []

    with_doi = json.loads(json.dumps(openalex_only))
    with_doi["doi"] = "https://doi.org/10.1000/stronger-id"
    with_doi["ids"]["doi"] = with_doi["doi"]
    with_doi["updated_date"] = "2026-07-15T08:00:00"

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(_record(openalex_only))
        session.commit()
        second = IngestionService(session).ingest(
            _record(with_doi, minute=1)
        )
        session.commit()

        work = session.get(Work, first.work_id)
        assert second.work_id == first.work_id
        assert work.canonical_key == "doi:10.1000/stronger-id"


def test_canonical_upgrade_conflict_is_explicit(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IdentityConflictError, IngestionService
    from paper_hub.models import Work

    first_raw = json.loads(json.dumps(_fixture()["results"][0]))
    first_raw["doi"] = None
    first_raw["ids"] = {"openalex": first_raw["id"]}
    first_raw["primary_location"] = None
    first_raw["best_oa_location"] = None
    first_raw["locations"] = []

    target_raw = json.loads(json.dumps(first_raw))
    target_raw["id"] = "https://openalex.org/W3333333333"
    target_raw["doi"] = "https://doi.org/10.1000/conflicting-upgrade"
    target_raw["ids"] = {
        "openalex": target_raw["id"],
        "doi": target_raw["doi"],
    }

    upgrade_raw = json.loads(json.dumps(first_raw))
    upgrade_raw["doi"] = target_raw["doi"]
    upgrade_raw["ids"]["doi"] = target_raw["doi"]
    upgrade_raw["updated_date"] = "2026-07-15T09:00:00"

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(_record(first_raw))
        target = IngestionService(session).ingest(
            _record(target_raw, minute=1)
        )
        session.commit()

        with pytest.raises(
            IdentityConflictError,
            match="multiple works",
        ):
            IngestionService(session).ingest(
                _record(upgrade_raw, minute=2)
            )
        session.rollback()

        assert first.work_id != target.work_id
        assert session.scalar(select(func.count()).select_from(Work)) == 2
        assert (
            session.get(Work, first.work_id).canonical_key
            == "openalex:W2741809807"
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


def test_noncanonical_external_identifier_does_not_merge_works(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IdentityConflictError, IngestionService
    from paper_hub.models import Work

    first_raw = json.loads(json.dumps(_fixture()["results"][0]))
    first_raw["doi"] = None
    first_raw["ids"] = {
        "openalex": first_raw["id"],
        "mag": "2741809807",
    }
    first_raw["primary_location"] = None
    first_raw["best_oa_location"] = None
    first_raw["locations"] = []

    second_raw = json.loads(json.dumps(first_raw))
    second_raw["id"] = "https://openalex.org/W2222222222"
    second_raw["ids"]["openalex"] = second_raw["id"]
    second_raw["title"] = "Another LLM Agent Evaluation"
    second_raw["display_name"] = second_raw["title"]

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(_record(first_raw))
        session.commit()

        with pytest.raises(
            IdentityConflictError,
            match="external identifier is already owned",
        ):
            IngestionService(session).ingest(
                _record(second_raw, minute=10)
            )
        session.rollback()

        work = session.get(Work, first.work_id)
        assert work.canonical_key == "openalex:W2741809807"
        assert work.title == first_raw["title"]
        assert session.scalar(select(func.count()).select_from(Work)) == 1


def test_out_of_scope_record_is_auditable_and_idempotent(
    migrated_engine,
) -> None:
    from paper_hub.connectors.openalex import OPENALEX_SCOPE_RULE_VERSION
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import (
        FieldAssertion,
        ScopeAssessment,
        SourceRecord,
        Work,
    )

    with Session(migrated_engine) as session:
        record = _record(_fixture()["results"][1])
        first = IngestionService(session).ingest(record)
        second = IngestionService(session).ingest(record)
        session.commit()

        source_records = session.scalars(select(SourceRecord)).all()
        assertions = session.scalars(select(FieldAssertion)).all()
        assessments = session.scalars(select(ScopeAssessment)).all()

        assert first.status == "excluded"
        assert second.status == "excluded"
        assert first.source_record_id == second.source_record_id
        assert first.reason == "no_agent_llm_scope_term_match"
        assert first.scope_rule_version == OPENALEX_SCOPE_RULE_VERSION
        assert first.scope_evidence == ()
        assert session.scalar(select(func.count()).select_from(Work)) == 0
        assert len(source_records) == 1
        assert source_records[0].source == record.source
        assert source_records[0].source_record_id == record.source_record_id
        assert source_records[0].retrieved_at == record.retrieved_at
        assert source_records[0].content_hash == record.content_hash
        assert source_records[0].source_updated_at == (
            record.source_updated_at
        )
        assert source_records[0].work_id is None
        assert source_records[0].paper_version_id is None
        assert source_records[0].raw_payload == record.raw_payload
        assert assertions == []
        assert len(assessments) == 1
        assert assessments[0].source_record_id == source_records[0].id
        assert assessments[0].rule_version == OPENALEX_SCOPE_RULE_VERSION
        assert assessments[0].included is False
        assert assessments[0].reason == "no_agent_llm_scope_term_match"
        assert assessments[0].evidence == []
        assert assessments[0].evaluated_at is not None
        assert assessments[0].work_id is None

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
                    "source_record_id": first.source_record_id,
                },
            )


def test_scope_rule_upgrade_from_excluded_to_included_reuses_snapshot(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import SourceRecord, Work

    record = _record(_fixture()["results"][1])
    v1 = _with_scope(
        record,
        rule_version="agent-llm-scope-v1",
        included=False,
    )
    v2 = _with_scope(
        record,
        rule_version="agent-llm-scope-v2",
        included=True,
    )

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(v1)
        session.commit()
        second = IngestionService(session).ingest(v2)
        session.commit()
        replay = IngestionService(session).ingest(v2)
        session.commit()

        assert first.status == "excluded"
        assert second.status == "inserted"
        assert replay.status == "unchanged"
        assert replay.work_id == second.work_id
        assert session.scalar(select(func.count()).select_from(Work)) == 1
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord)) == 1
        )
        source_record = session.scalar(select(SourceRecord))
        assert source_record.work_id == second.work_id

        assessments = session.execute(
            text(
                """
                SELECT
                    rule_version,
                    included,
                    reason,
                    evidence,
                    evaluated_at,
                    work_id,
                    work_linked_at,
                    work_link_reason
                FROM scope_assessment
                WHERE source_record_id = :source_record_id
                ORDER BY rule_version
                """
            ),
            {"source_record_id": source_record.id},
        ).mappings().all()
        assert len(assessments) == 2
        assert assessments[0]["rule_version"] == "agent-llm-scope-v1"
        assert assessments[0]["included"] is False
        assert assessments[0]["reason"] == (
            "excluded_by_agent-llm-scope-v1"
        )
        assert assessments[0]["evidence"] == []
        assert assessments[0]["evaluated_at"] is not None
        assert assessments[0]["work_id"] == second.work_id
        assert assessments[0]["work_linked_at"] is not None
        assert assessments[0]["work_link_reason"] == (
            "logical_source_owner_trigger"
        )
        assert assessments[1]["rule_version"] == "agent-llm-scope-v2"
        assert assessments[1]["included"] is True
        assert assessments[1]["reason"] is None
        assert assessments[1]["evidence"] == [
            {
                "field": "title",
                "term": "scope-upgrade",
                "matched_text": record.parsed.title,
            }
        ]
        assert assessments[1]["evaluated_at"] is not None
        assert assessments[1]["work_id"] == second.work_id


def test_scope_rule_upgrade_from_included_to_excluded_is_current(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import SourceRecord, Work

    record = _record(_fixture()["results"][0])
    v1 = _with_scope(
        record,
        rule_version="agent-llm-scope-v1",
        included=True,
    )
    v2 = _with_scope(
        record,
        rule_version="agent-llm-scope-v2",
        included=False,
    )

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(v1)
        session.commit()
        second = IngestionService(session).ingest(v2)
        session.commit()
        replay = IngestionService(session).ingest(v2)
        session.commit()

        assert first.status == "inserted"
        assert second.status == "excluded"
        assert second.scope_rule_version == "agent-llm-scope-v2"
        assert replay.status == "excluded"
        assert replay.source_record_id == first.source_record_id
        assert session.scalar(select(func.count()).select_from(Work)) == 1
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord)) == 1
        )
        source_record = session.scalar(select(SourceRecord))
        assert source_record.work_id == first.work_id

        current = session.execute(
            text(
                """
                SELECT included, reason, work_id
                FROM scope_assessment
                WHERE source_record_id = :source_record_id
                  AND rule_version = :rule_version
                """
            ),
            {
                "source_record_id": source_record.id,
                "rule_version": "agent-llm-scope-v2",
            },
        ).mappings().one()
        assert current["included"] is False
        assert current["reason"] == "excluded_by_agent-llm-scope-v2"
        assert current["work_id"] == first.work_id
        assert session.scalar(
            text(
                """
                SELECT count(*)
                FROM scope_assessment
                WHERE source_record_id = :source_record_id
                """
            ),
            {"source_record_id": source_record.id},
        ) == 2


def test_scope_rule_upgrade_reuses_existing_work_and_returns_updated(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import ScopeAssessment, SourceRecord, Work

    target_raw = json.loads(json.dumps(_fixture()["results"][1]))
    existing_raw = json.loads(json.dumps(target_raw))
    existing_raw["id"] = "https://openalex.org/W1111111111"
    existing_raw["ids"]["openalex"] = existing_raw["id"]

    existing = _with_scope(
        _record(existing_raw),
        rule_version="agent-llm-scope-v1",
        included=True,
    )
    target = _record(target_raw, minute=1)
    v1 = _with_scope(
        target,
        rule_version="agent-llm-scope-v1",
        included=False,
    )
    v2 = _with_scope(
        target,
        rule_version="agent-llm-scope-v2",
        included=True,
    )

    with Session(migrated_engine) as session:
        existing_result = IngestionService(session).ingest(existing)
        session.commit()
        excluded_result = IngestionService(session).ingest(v1)
        session.commit()
        included_result = IngestionService(session).ingest(v2)
        session.commit()

        assert existing_result.status == "inserted"
        assert excluded_result.status == "excluded"
        assert included_result.status == "updated"
        assert included_result.work_id == existing_result.work_id
        assert session.scalar(select(func.count()).select_from(Work)) == 1
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord)) == 2
        )
        target_source = session.scalar(
            select(SourceRecord).where(
                SourceRecord.source_record_id == target.source_record_id
            )
        )
        assert target_source.work_id == existing_result.work_id
        assert (
            session.scalar(
                select(func.count())
                .select_from(ScopeAssessment)
                .where(
                    ScopeAssessment.source_record_id == target_source.id
                )
            )
            == 2
        )


def test_new_excluded_hash_hides_prior_included_work_across_public_apis(
    migrated_engine,
) -> None:
    from fastapi.testclient import TestClient

    from paper_hub.db import get_session
    from paper_hub.ingestion import IngestionService
    from paper_hub.main import app
    from paper_hub.models import Method, ScopeAssessment, SourceRecord, Work
    from paper_hub.repositories import slug_for_canonical_key

    first_record = _with_scope(
        _record(_fixture()["results"][0]),
        rule_version="agent-llm-scope-v1",
        included=True,
    )
    excluded_record = _new_hash_with_scope(
        first_record,
        minute=1,
        included=False,
    )

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(first_record)
        session.commit()
        excluded = IngestionService(session).ingest(excluded_record)
        session.commit()

        assert excluded.work_id == first.work_id
        excluded_source = session.get(SourceRecord, excluded.source_record_id)
        assert excluded_source is not None
        assert excluded_source.work_id is None
        excluded_assessment = session.scalar(
            select(ScopeAssessment).where(
                ScopeAssessment.source_record_id == excluded_source.id
            )
        )
        assert excluded_assessment is not None
        assert excluded_assessment.included is False
        assert excluded_assessment.work_id == first.work_id

        work = session.get(Work, first.work_id)
        assert work is not None
        work.methods.append(
            Method(
                name="Logical Source Method",
                normalized_name="logical-source-method",
                description=None,
                source="test",
                retrieved_at=datetime.now(UTC),
            )
        )
        session.commit()
        canonical_key = work.canonical_key

    def override_session():
        with Session(migrated_engine) as session:
            yield session

    app.dependency_overrides[get_session] = override_session
    try:
        with TestClient(app) as client:
            listing = client.get("/api/v1/papers")
            detail = client.get(
                f"/api/v1/papers/{slug_for_canonical_key(canonical_key)}"
            )
            stats = client.get("/api/v1/stats")
            topics = client.get("/api/v1/topics")
            methods = client.get("/api/v1/methods")

            assert listing.status_code == 200
            assert listing.json()["total"] == 0
            assert detail.status_code == 404
            assert stats.json()["papers"] == 0
            assert topics.json()["items"] == []
            assert methods.json()["items"] == []

            for ranking in (
                "latest",
                "citation_velocity",
                "code_growth",
            ):
                payload = client.get(
                    "/api/v1/trends/papers",
                    params={"ranking": ranking, "window_days": 90},
                ).json()
                exposed_keys = {
                    item["canonical_key"] for item in payload["items"]
                } | {
                    item["canonical_key"]
                    for item in payload["missing_signals"]
                    if item["canonical_key"] is not None
                }
                assert canonical_key not in exposed_keys

            assert client.get(
                "/api/v1/trends/topics",
                params={"window_days": 90},
            ).json()["items"] == []
            assert client.get(
                "/api/v1/trends/methods",
                params={"window_days": 90},
            ).json()["items"] == []
    finally:
        app.dependency_overrides.clear()


def test_new_included_hash_overrides_prior_unassociated_exclusion(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import ScopeAssessment, Work
    from paper_hub.repositories import public_work_predicate

    excluded_record = _new_hash_with_scope(
        _record(_fixture()["results"][0]),
        minute=0,
        included=False,
    )
    included_record = _new_hash_with_scope(
        excluded_record,
        minute=1,
        included=True,
    )

    with Session(migrated_engine) as session:
        excluded = IngestionService(session).ingest(excluded_record)
        session.commit()
        included = IngestionService(session).ingest(included_record)
        session.commit()

        old_assessment = session.scalar(
            select(ScopeAssessment).where(
                ScopeAssessment.source_record_id == excluded.source_record_id
            )
        )
        assert old_assessment is not None
        assert old_assessment.work_id == included.work_id
        assert included.work_id is not None
        assert session.scalar(
            select(func.count())
            .select_from(Work)
            .where(public_work_predicate())
        ) == 1


def test_newer_exclusion_ingested_first_is_linked_when_older_include_creates_work(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import ScopeAssessment, Work
    from paper_hub.repositories import public_work_predicate

    older_included = _with_scope(
        _record(_fixture()["results"][0], minute=0),
        rule_version="agent-llm-scope-v1",
        included=True,
    )
    newer_excluded = _new_hash_with_scope(
        older_included,
        minute=1,
        included=False,
        rule_version="agent-llm-scope-v1",
    )

    with Session(migrated_engine) as session:
        excluded = IngestionService(session).ingest(newer_excluded)
        session.commit()
        included = IngestionService(session).ingest(older_included)
        session.commit()
        session.expire_all()

        assessment = session.scalar(
            select(ScopeAssessment).where(
                ScopeAssessment.source_record_id
                == excluded.source_record_id
            )
        )
        assert assessment is not None
        assert included.work_id is not None
        assert assessment.work_id == included.work_id
        assert assessment.work_linked_at is not None
        assert assessment.work_link_reason == (
            "logical_source_owner_trigger"
        )
        assert session.scalar(
            select(func.count())
            .select_from(Work)
            .where(public_work_predicate())
        ) == 0


def test_projection_uses_exact_latest_snapshot_when_source_times_match(
    migrated_engine,
) -> None:
    from fastapi.testclient import TestClient

    from paper_hub.db import get_session
    from paper_hub.ingestion import IngestionService
    from paper_hub.main import app
    from paper_hub.models import MetricSnapshot, SourceRecord, Work

    older_raw = json.loads(json.dumps(_fixture()["results"][0]))
    older_raw["title"] = "Older LLM Agent Projection"
    older_raw["display_name"] = older_raw["title"]
    older_raw["type"] = "preprint"
    older_raw["authorships"][0]["author"]["display_name"] = (
        "Old Projection Author"
    )
    newer_raw = json.loads(json.dumps(older_raw))
    newer_raw["title"] = "Newer LLM Agent Projection"
    newer_raw["display_name"] = newer_raw["title"]
    newer_raw["type"] = "article"
    newer_raw["authorships"][0]["author"]["display_name"] = (
        "New Projection Author"
    )
    newer_raw["cited_by_count"] = (
        int(newer_raw.get("cited_by_count") or 0) + 1
    )
    older_record = _record(older_raw, minute=0)
    newer_record = _record(newer_raw, minute=5)
    assert older_record.source_updated_at == newer_record.source_updated_at

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(older_record)
        session.commit()
        second = IngestionService(session).ingest(newer_record)
        session.commit()
        session.expire_all()

        work = session.get(Work, first.work_id)
        assert work is not None
        newer_source = session.get(SourceRecord, second.source_record_id)
        assert newer_source is not None
        assert work.projection_source_record_id == newer_source.id
        assert work.title == newer_raw["title"]
        metric = session.scalar(
            select(MetricSnapshot).where(
                MetricSnapshot.work_id == first.work_id,
                MetricSnapshot.metric_name == "citation_count",
                MetricSnapshot.measured_at
                == newer_record.source_updated_at,
                MetricSnapshot.source == newer_record.source,
            )
        )
        assert metric is not None
        assert int(metric.metric_value) == (
            newer_record.parsed.citation_count
        )
        assert metric.details["source_record_id"] == str(
            second.source_record_id
        )

    def override_session():
        with Session(migrated_engine) as session:
            yield session

    app.dependency_overrides[get_session] = override_session
    try:
        with TestClient(app) as client:
            old_author = client.get(
                "/api/v1/papers",
                params={"q": "Old Projection Author"},
            )
            new_author = client.get(
                "/api/v1/papers",
                params={"q": "New Projection Author"},
            )
            old_type = client.get(
                "/api/v1/papers",
                params={"type": "preprint"},
            )
            new_type = client.get(
                "/api/v1/papers",
                params={"type": "article"},
            )

        assert old_author.json()["total"] == 0
        assert new_author.json()["total"] == 1
        assert new_author.json()["items"][0]["authors"][0][
            "display_name"
        ] == "New Projection Author"
        assert old_type.json()["total"] == 0
        assert new_type.json()["total"] == 1
    finally:
        app.dependency_overrides.clear()


def test_replayed_exclusion_links_to_work_created_by_independent_source(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import ScopeAssessment

    excluded_record = _new_hash_with_scope(
        _record(_fixture()["results"][0]),
        minute=0,
        included=False,
    )
    included_record = replace(
        _new_hash_with_scope(
            excluded_record,
            minute=1,
            included=True,
        ),
        source="crossref",
    )

    with Session(migrated_engine) as session:
        excluded = IngestionService(session).ingest(excluded_record)
        session.commit()
        included = IngestionService(session).ingest(included_record)
        session.commit()
        replay = IngestionService(session).ingest(excluded_record)
        session.commit()

        assert included.work_id is not None
        assert replay.status == "excluded"
        assert replay.work_id == included.work_id
        assessment = session.scalar(
            select(ScopeAssessment).where(
                ScopeAssessment.source_record_id == excluded.source_record_id
            )
        )
        assert assessment is not None
        assert assessment.work_id == included.work_id


def test_other_included_logical_source_keeps_work_public(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import Work
    from paper_hub.repositories import public_work_predicate

    openalex_record = _with_scope(
        _record(_fixture()["results"][0]),
        rule_version="agent-llm-scope-v1",
        included=True,
    )
    independent_source = replace(
        openalex_record,
        source="crossref",
    )
    excluded_openalex = _new_hash_with_scope(
        openalex_record,
        minute=1,
        included=False,
    )

    with Session(migrated_engine) as session:
        first = IngestionService(session).ingest(openalex_record)
        session.commit()
        second = IngestionService(session).ingest(independent_source)
        session.commit()
        excluded = IngestionService(session).ingest(excluded_openalex)
        session.commit()

        assert second.work_id == first.work_id
        assert excluded.work_id == first.work_id
        assert session.scalar(
            select(func.count())
            .select_from(Work)
            .where(public_work_predicate())
        ) == 1


def test_logical_source_scope_order_is_stable_when_times_match(
    migrated_engine,
    monkeypatch,
) -> None:
    import paper_hub.ingestion as ingestion_module
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import SourceRecord, Work
    from paper_hub.repositories import public_work_predicate

    fixed_time = datetime(2026, 7, 15, 9, tzinfo=UTC)

    class FrozenDateTime(datetime):
        @classmethod
        def now(cls, tz=None):
            if tz is None:
                return fixed_time.replace(tzinfo=None)
            return fixed_time

    monkeypatch.setattr(ingestion_module, "datetime", FrozenDateTime)
    included_record = _with_scope(
        _record(_fixture()["results"][0]),
        rule_version="agent-llm-scope-v1",
        included=True,
    )
    excluded_record = _new_hash_with_scope(
        included_record,
        minute=0,
        included=False,
    )
    source_ids = iter((UUID(int=1), UUID(int=2)))

    with Session(migrated_engine) as session:
        def assign_source_ids(
            target_session,
            _flush_context,
            _instances,
        ) -> None:
            for item in target_session.new:
                if isinstance(item, SourceRecord) and item.id is None:
                    item.id = next(source_ids)

        event.listen(session, "before_flush", assign_source_ids)
        try:
            first = IngestionService(session).ingest(included_record)
            session.commit()
            second = IngestionService(session).ingest(excluded_record)
            session.commit()
        finally:
            event.remove(session, "before_flush", assign_source_ids)

        assert first.work_id == second.work_id
        assert session.scalar(
            select(func.count())
            .select_from(Work)
            .where(public_work_predicate())
        ) == 0


def test_cli_rolls_back_and_zeros_counts_when_later_page_fails(
    migrated_engine,
    monkeypatch,
    capsys,
) -> None:
    from paper_hub import cli
    from paper_hub.config import get_settings
    from paper_hub.connectors.openalex import OpenAlexResponseError
    from paper_hub.models import SourceRecord, Work

    class FailingPaginationConnector:
        def __init__(self, **_: object) -> None:
            pass

        def __enter__(self):
            return self

        def __exit__(self, *_: object) -> None:
            pass

        def fetch_records(self, **_: object):
            yield _record(_fixture()["results"][0])
            raise OpenAlexResponseError("OpenAlex page 2 failed")

    database_url = migrated_engine.url.render_as_string(
        hide_password=False
    )
    monkeypatch.setenv("DATABASE_URL", database_url)
    monkeypatch.setenv("OPENALEX_CONTACT_EMAIL", "research@example.com")
    monkeypatch.setattr(cli, "OpenAlexConnector", FailingPaginationConnector)
    get_settings.cache_clear()

    exit_code = cli.main(
        ["sync-openalex", "--query", "agents", "--max-results", "2"]
    )
    captured = capsys.readouterr()
    output = json.loads(captured.out)

    assert exit_code == 1
    assert output == {
        "excluded": 0,
        "failed": 1,
        "inserted": 0,
        "unchanged": 0,
        "updated": 0,
    }
    assert "OpenAlex page 2 failed" in captured.err
    with Session(migrated_engine) as session:
        assert session.scalar(select(func.count()).select_from(Work)) == 0
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord)) == 0
        )
    get_settings.cache_clear()


def test_cli_rolls_back_and_zeros_counts_when_commit_fails(
    migrated_engine,
    monkeypatch,
    capsys,
) -> None:
    from paper_hub import cli
    from paper_hub.config import get_settings
    from paper_hub.models import SourceRecord, Work

    class OneRecordConnector:
        def __init__(self, **_: object) -> None:
            pass

        def __enter__(self):
            return self

        def __exit__(self, *_: object) -> None:
            pass

        def fetch_records(self, **_: object):
            yield _record(_fixture()["results"][0])
            yield _record(_fixture()["results"][1], minute=1)

    class CommitFailingSession(SQLAlchemySession):
        def commit(self) -> None:
            raise SQLAlchemyError("forced commit failure")

    database_url = migrated_engine.url.render_as_string(
        hide_password=False
    )
    monkeypatch.setenv("DATABASE_URL", database_url)
    monkeypatch.setenv("OPENALEX_CONTACT_EMAIL", "research@example.com")
    monkeypatch.setattr(cli, "OpenAlexConnector", OneRecordConnector)
    monkeypatch.setattr(cli, "Session", CommitFailingSession)
    get_settings.cache_clear()

    exit_code = cli.main(
        ["sync-openalex", "--query", "agents", "--max-results", "2"]
    )
    captured = capsys.readouterr()
    output = json.loads(captured.out)

    assert exit_code == 1
    assert output == {
        "excluded": 0,
        "failed": 1,
        "inserted": 0,
        "unchanged": 0,
        "updated": 0,
    }
    assert "database commit failed: SQLAlchemyError" in captured.err
    with Session(migrated_engine) as session:
        assert session.scalar(select(func.count()).select_from(Work)) == 0
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord)) == 0
        )
    get_settings.cache_clear()


def test_cli_reports_committed_inserted_and_excluded_counts(
    migrated_engine,
    monkeypatch,
    capsys,
) -> None:
    from paper_hub import cli
    from paper_hub.config import get_settings
    from paper_hub.models import ScopeAssessment, SourceRecord, Work

    class IncludedAndExcludedConnector:
        def __init__(self, **_: object) -> None:
            pass

        def __enter__(self):
            return self

        def __exit__(self, *_: object) -> None:
            pass

        def fetch_records(self, **_: object):
            yield _record(_fixture()["results"][0])
            yield _record(_fixture()["results"][1], minute=1)

    database_url = migrated_engine.url.render_as_string(
        hide_password=False
    )
    monkeypatch.setenv("DATABASE_URL", database_url)
    monkeypatch.setenv("OPENALEX_CONTACT_EMAIL", "research@example.com")
    monkeypatch.setattr(
        cli,
        "OpenAlexConnector",
        IncludedAndExcludedConnector,
    )
    get_settings.cache_clear()

    exit_code = cli.main(
        ["sync-openalex", "--query", "agents", "--max-results", "2"]
    )
    captured = capsys.readouterr()
    output = json.loads(captured.out)

    assert exit_code == 0
    assert captured.err == ""
    assert output == {
        "excluded": 1,
        "failed": 0,
        "inserted": 1,
        "unchanged": 0,
        "updated": 0,
    }
    with Session(migrated_engine) as session:
        assert session.scalar(select(func.count()).select_from(Work)) == 1
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord)) == 2
        )
        excluded_record = session.scalar(
            select(SourceRecord).where(SourceRecord.work_id.is_(None))
        )
        scope_assessment = session.scalar(
            select(ScopeAssessment).where(
                ScopeAssessment.source_record_id == excluded_record.id,
            )
        )
        assert scope_assessment.included is False
    get_settings.cache_clear()


def test_cli_holds_global_batch_lock_before_first_ingest(
    migrated_engine,
    monkeypatch,
    capsys,
) -> None:
    from paper_hub import cli
    from paper_hub.config import get_settings

    class OneRecordConnector:
        def __init__(self, **_: object) -> None:
            pass

        def __enter__(self):
            return self

        def __exit__(self, *_: object) -> None:
            pass

        def fetch_records(self, **_: object):
            yield _record(_fixture()["results"][0])

    real_ingestion_service = cli.IngestionService
    competing_lock_results: list[bool] = []

    class LockObservingIngestionService:
        def __init__(self, session) -> None:
            self.delegate = real_ingestion_service(session)

        def ingest(self, record):
            with migrated_engine.connect() as connection:
                competing_lock_results.append(
                    bool(
                        connection.scalar(
                            text(
                                """
                                SELECT pg_try_advisory_xact_lock(
                                    hashtextextended(
                                        'paper-hub:sync-openalex:batch:v1',
                                        0
                                    )
                                )
                                """
                            )
                        )
                    )
                )
            return self.delegate.ingest(record)

    database_url = migrated_engine.url.render_as_string(
        hide_password=False
    )
    monkeypatch.setenv("DATABASE_URL", database_url)
    monkeypatch.setenv("OPENALEX_CONTACT_EMAIL", "research@example.com")
    monkeypatch.setattr(cli, "OpenAlexConnector", OneRecordConnector)
    monkeypatch.setattr(
        cli,
        "IngestionService",
        LockObservingIngestionService,
    )
    get_settings.cache_clear()

    exit_code = cli.main(
        ["sync-openalex", "--query", "agents", "--max-results", "1"]
    )
    captured = capsys.readouterr()

    assert exit_code == 0, captured.err
    assert competing_lock_results == [False]
    get_settings.cache_clear()


def test_cli_reports_excluded_for_current_rule_after_prior_inclusion(
    migrated_engine,
    monkeypatch,
    capsys,
) -> None:
    from paper_hub import cli
    from paper_hub.config import get_settings
    from paper_hub.models import ScopeAssessment, SourceRecord, Work

    record = _record(_fixture()["results"][0])
    v1 = _with_scope(
        record,
        rule_version="agent-llm-scope-v1",
        included=True,
    )
    v2 = _with_scope(
        record,
        rule_version="agent-llm-scope-v2",
        included=False,
    )

    class V1Connector:
        def __init__(self, **_: object) -> None:
            pass

        def __enter__(self):
            return self

        def __exit__(self, *_: object) -> None:
            pass

        def fetch_records(self, **_: object):
            yield v1

    class V2Connector(V1Connector):
        def fetch_records(self, **_: object):
            yield v2

    database_url = migrated_engine.url.render_as_string(
        hide_password=False
    )
    monkeypatch.setenv("DATABASE_URL", database_url)
    monkeypatch.setenv("OPENALEX_CONTACT_EMAIL", "research@example.com")
    get_settings.cache_clear()

    monkeypatch.setattr(cli, "OpenAlexConnector", V1Connector)
    first_exit = cli.main(
        ["sync-openalex", "--query", "agents", "--max-results", "1"]
    )
    first_output = json.loads(capsys.readouterr().out)

    monkeypatch.setattr(cli, "OpenAlexConnector", V2Connector)
    second_exit = cli.main(
        ["sync-openalex", "--query", "agents", "--max-results", "1"]
    )
    second_capture = capsys.readouterr()
    second_output = json.loads(second_capture.out)

    assert first_exit == 0
    assert first_output == {
        "excluded": 0,
        "failed": 0,
        "inserted": 1,
        "unchanged": 0,
        "updated": 0,
    }
    assert second_exit == 0
    assert second_capture.err == ""
    assert second_output == {
        "excluded": 1,
        "failed": 0,
        "inserted": 0,
        "unchanged": 0,
        "updated": 0,
    }
    with Session(migrated_engine) as session:
        assert session.scalar(select(func.count()).select_from(Work)) == 1
        assert (
            session.scalar(select(func.count()).select_from(SourceRecord)) == 1
        )
        assert (
            session.scalar(
                select(func.count()).select_from(ScopeAssessment)
            )
            == 2
        )
        current = session.scalar(
            select(ScopeAssessment).where(
                ScopeAssessment.rule_version == "agent-llm-scope-v2"
            )
        )
        assert current.included is False
        assert current.work_id == session.scalar(select(Work.id))
    get_settings.cache_clear()


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


def test_session_and_sql_work_delete_preserve_raw_source_records(
    migrated_engine,
) -> None:
    from paper_hub.ingestion import IngestionService
    from paper_hub.models import ScopeAssessment, SourceRecord, Work

    first_raw = json.loads(json.dumps(_fixture()["results"][0]))
    second_raw = json.loads(json.dumps(first_raw))
    second_raw["id"] = "https://openalex.org/W4444444444"
    second_raw["doi"] = "https://doi.org/10.1000/sql-delete"
    second_raw["ids"] = {
        "openalex": second_raw["id"],
        "doi": second_raw["doi"],
    }
    second_raw["primary_location"] = None
    second_raw["best_oa_location"] = None
    second_raw["locations"] = []

    with Session(migrated_engine) as session:
        orm_result = IngestionService(session).ingest(_record(first_raw))
        sql_result = IngestionService(session).ingest(
            _record(second_raw, minute=1)
        )
        session.commit()
        expected_payloads = {
            orm_result.source_record_id: first_raw,
            sql_result.source_record_id: second_raw,
        }

        work = session.get(Work, orm_result.work_id)
        session.delete(work)
        session.commit()

    with migrated_engine.begin() as connection:
        connection.execute(
            text("DELETE FROM work WHERE id = :work_id"),
            {"work_id": sql_result.work_id},
        )

    with Session(migrated_engine) as session:
        for source_record_id, raw_payload in expected_payloads.items():
            source_record = session.get(SourceRecord, source_record_id)
            assert source_record is not None
            assert source_record.work_id is None
            assert source_record.raw_payload == raw_payload
            assert (
                session.scalar(
                    select(func.count())
                    .select_from(ScopeAssessment)
                    .where(
                        ScopeAssessment.source_record_id
                        == source_record_id
                    )
                )
                == 0
            )


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


def test_cli_rejects_blank_query_before_loading_database_config(
    monkeypatch,
    capsys,
) -> None:
    from paper_hub import cli

    def unexpected_settings_load():
        pytest.fail("blank query must fail before loading database config")

    monkeypatch.setattr(cli, "get_settings", unexpected_settings_load)

    with pytest.raises(SystemExit) as exc_info:
        cli.main(["sync-openalex", "--query", "   "])

    assert exc_info.value.code == 2
    assert "query" in capsys.readouterr().err
