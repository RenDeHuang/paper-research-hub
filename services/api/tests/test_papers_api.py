from __future__ import annotations

from collections.abc import Generator
from datetime import UTC, date, datetime, timedelta
import hashlib
import os
from pathlib import Path
from uuid import UUID

from alembic import command
from fastapi.testclient import TestClient
import psycopg
import pytest
from sqlalchemy import create_engine, event, inspect, text
from sqlalchemy.orm import Session


NOW = datetime(2026, 7, 16, 12, tzinfo=UTC)


def _provenance(
    *,
    source: str = "openalex",
    status=None,
) -> dict[str, object]:
    from paper_hub.models import RecordStatus

    return {
        "source": source,
        "source_url": f"https://{source}.test/record",
        "retrieved_at": NOW,
        "source_license": "CC0",
        "content_license": "cc-by",
        "status": status or RecordStatus.ACTIVE,
    }


def _assertion_value(
    author: str,
    institution: str,
) -> tuple[list[dict[str, object]], list[dict[str, object]]]:
    institution_data = {
        "openalex_id": "I123",
        "display_name": institution,
        "ror": "012345678",
        "country_code": "US",
        "institution_type": "education",
    }
    authors = [
        {
            "openalex_id": "A123",
            "display_name": author,
            "orcid": "0000-0001-2345-6789",
            "position": "first",
            "is_corresponding": True,
            "institutions": [institution_data],
        }
    ]
    return authors, [institution_data]


def _entity(session: Session, model, cache: dict, name: str):
    normalized = "-".join(name.casefold().split())
    key = (model.__name__, normalized)
    if key not in cache:
        cache[key] = model(
            name=name,
            normalized_name=normalized,
            description=f"{name} description",
            **_provenance(),
        )
        session.add(cache[key])
        session.flush()
    return cache[key]


def _add_work(
    session: Session,
    cache: dict,
    *,
    suffix: str,
    title: str,
    publication_date: date,
    work_type: str,
    author: str,
    institution: str,
    identifiers: dict[str, str],
    topics: tuple[str, ...] = (),
    methods: tuple[str, ...] = (),
    has_code: bool = False,
    status=None,
    source: str = "openalex",
    current_scope_included: bool = True,
    source_updated_at: datetime | None = NOW,
) -> UUID:
    from paper_hub.models import (
        Benchmark,
        CodeRepository,
        Dataset,
        ExternalIdentifier,
        FieldAssertion,
        Method,
        PaperVersion,
        ScopeAssessment,
        SourceRecord,
        Topic,
        Work,
    )

    provenance = _provenance(source=source, status=status)
    source_record_id = f"SRC-{suffix}"
    work = Work(
        canonical_key=f"doi:10.1000/{suffix}",
        title=title,
        abstract=f"Abstract for {title}",
        publication_date=publication_date,
        projection_source=source,
        projection_source_record_id=source_record_id,
        projection_source_updated_at=source_updated_at,
        **provenance,
    )
    session.add(work)
    session.flush()

    source_record = SourceRecord(
        work_id=work.id,
        source_record_id=source_record_id,
        content_hash=hashlib.sha256(suffix.encode()).hexdigest(),
        raw_payload={"id": source_record_id},
        source_updated_at=source_updated_at,
        http_status=200,
        **provenance,
    )
    session.add(source_record)
    session.flush()

    session.add(
        ScopeAssessment(
            source_record_id=source_record.id,
            rule_version="scope-v1",
            included=True,
            reason=None,
            evidence=[{"field": "title", "term": "agent", "matched_text": title}],
            evaluated_at=NOW - timedelta(minutes=1),
            work_id=work.id,
        )
    )
    if not current_scope_included:
        session.add(
            ScopeAssessment(
                source_record_id=source_record.id,
                rule_version="scope-v2",
                included=False,
                reason="excluded_by_scope_v2",
                evidence=[],
                evaluated_at=NOW,
                work_id=work.id,
            )
        )

    authors, institutions = _assertion_value(author, institution)
    for field_name, value in (
        ("authors", authors),
        ("institutions", institutions),
        ("type", work_type),
    ):
        session.add(
            FieldAssertion(
                source_record_id=source_record.id,
                field_name=field_name,
                value=value,
                parser_version="test-v1",
                **provenance,
            )
        )

    for scheme, value in identifiers.items():
        session.add(
            ExternalIdentifier(
                work_id=work.id,
                scheme=scheme,
                normalized_value=value,
                raw_value=value,
                **provenance,
            )
        )

    work.topics.extend(
        _entity(session, Topic, cache, topic) for topic in topics
    )
    work.methods.extend(
        _entity(session, Method, cache, method) for method in methods
    )
    if suffix == "alpha":
        work.datasets.append(
            _entity(session, Dataset, cache, "Agent Dataset")
        )
        work.benchmarks.append(
            _entity(session, Benchmark, cache, "Agent Benchmark")
        )
        work.versions.extend(
            [
                PaperVersion(
                    version_label="v1",
                    version_number=1,
                    version_type="preprint",
                    title=title,
                    submitted_at=NOW - timedelta(days=10),
                    version_url="https://arxiv.test/abs/alpha-v1",
                    **provenance,
                ),
                PaperVersion(
                    version_label="published",
                    version_number=2,
                    version_type="published",
                    title=title,
                    published_at=NOW - timedelta(days=2),
                    version_url="https://publisher.test/alpha",
                    **provenance,
                ),
            ]
        )
    if has_code:
        repository = CodeRepository(
            provider="github",
            repository_name=f"example/{suffix}",
            repository_url=f"https://github.test/example/{suffix}",
            normalized_url=f"https://github.test/example/{suffix}",
            is_official=True,
            **provenance,
        )
        work.code_repositories.append(repository)

    session.flush()
    return work.id


@pytest.fixture(scope="module")
def papers_api(
    postgres_container_url: str,
) -> Generator[tuple[TestClient, object], None, None]:
    from alembic.config import Config
    from paper_hub.config import get_settings
    from paper_hub.db import get_session
    from paper_hub.main import app

    service_root = Path(__file__).resolve().parents[1]
    psycopg_url = postgres_container_url.replace(
        "postgresql+psycopg://",
        "postgresql://",
    )
    with psycopg.connect(psycopg_url, autocommit=True) as connection:
        connection.execute("DROP SCHEMA public CASCADE")
        connection.execute("CREATE SCHEMA public")
    previous_database_url = os.environ.get("DATABASE_URL")
    os.environ["DATABASE_URL"] = postgres_container_url
    get_settings.cache_clear()
    config = Config(service_root / "alembic.ini")
    command.upgrade(config, "head")
    engine = create_engine(postgres_container_url)

    from paper_hub.models import RecordStatus

    with Session(engine) as session:
        cache: dict = {}
        _add_work(
            session,
            cache,
            suffix="alpha",
            title="Agent Planning with Tool Use",
            publication_date=date(2026, 7, 15),
            work_type="preprint",
            author="Ada Lovelace",
            institution="Analytical Engine Institute",
            identifiers={
                "doi": "10.1000/alpha",
                "arxiv": "2607.00001",
                "openalex": "W1001",
                "s2": "a" * 40,
            },
            topics=("Autonomous Agents",),
            methods=("Tool Use",),
            has_code=True,
        )
        _add_work(
            session,
            cache,
            suffix="beta",
            title="Retracted Agent Evaluation",
            publication_date=date(2026, 7, 15),
            work_type="article",
            author="Grace Hopper",
            institution="Compiler Lab",
            identifiers={
                "doi": "10.1000/beta",
                "openalex": "W1002",
                "s2": "b" * 40,
            },
            topics=("Autonomous Agents",),
            status=RecordStatus.RETRACTED,
        )
        _add_work(
            session,
            cache,
            suffix="gamma",
            title="Agent Search Systems",
            publication_date=date(2026, 7, 14),
            work_type="preprint",
            author="Edsger Dijkstra",
            institution="Structured Systems Lab",
            identifiers={"doi": "10.1000/gamma", "openalex": "W1003"},
            topics=("Autonomous Agents",),
            methods=("Tree Search",),
            status=RecordStatus.WITHDRAWN,
        )
        _add_work(
            session,
            cache,
            suffix="delta",
            title="Language Model Agents",
            publication_date=date(2026, 7, 13),
            work_type="preprint",
            author="Barbara Liskov",
            institution="Distributed Systems Lab",
            identifiers={"doi": "10.1000/delta", "openalex": "W1004"},
            topics=("Language Models",),
            methods=("Tool Use",),
            source="crossref",
        )
        _add_work(
            session,
            cache,
            suffix="epsilon",
            title="Excluded Agent Paper",
            publication_date=date(2026, 7, 16),
            work_type="preprint",
            author="Hidden Author",
            institution="Hidden Lab",
            identifiers={"doi": "10.1000/epsilon", "openalex": "W1005"},
            topics=("Autonomous Agents",),
            current_scope_included=False,
        )
        session.commit()

    def override_session():
        with Session(engine) as session:
            yield session

    app.dependency_overrides[get_session] = override_session
    with TestClient(app) as client:
        yield client, engine
    app.dependency_overrides.clear()
    engine.dispose()
    if previous_database_url is None:
        os.environ.pop("DATABASE_URL", None)
    else:
        os.environ["DATABASE_URL"] = previous_database_url
    get_settings.cache_clear()


@pytest.mark.parametrize(
    ("query", "expected_key"),
    [
        ("Planning with Tool", "doi:10.1000/alpha"),
        ("Ada Lovelace", "doi:10.1000/alpha"),
        ("10.1000/alpha", "doi:10.1000/alpha"),
        ("2607.00001", "doi:10.1000/alpha"),
        ("W1001", "doi:10.1000/alpha"),
        ("a" * 40, "doi:10.1000/alpha"),
    ],
)
def test_searches_title_author_and_controlled_identifiers(
    papers_api,
    query: str,
    expected_key: str,
) -> None:
    client, _ = papers_api

    response = client.get("/api/v1/papers", params={"q": query})

    assert response.status_code == 200
    payload = response.json()
    assert payload["total"] == 1
    assert payload["items"][0]["canonical_key"] == expected_key


def test_combined_filters_return_total_facets_and_code_state(papers_api) -> None:
    client, _ = papers_api

    response = client.get(
        "/api/v1/papers",
        params={
            "type": "preprint",
            "topic": "autonomous-agents",
            "method": "tool-use",
            "has_code": "true",
            "date_from": "2026-07-01",
            "date_to": "2026-07-31",
            "status": "active",
            "source": "openalex",
        },
    )

    assert response.status_code == 200
    payload = response.json()
    assert payload["total"] == 1
    assert payload["items"][0]["canonical_key"] == "doi:10.1000/alpha"
    assert payload["items"][0]["has_code"] is True
    assert payload["facets"]["types"] == [
        {"value": "preprint", "count": 1}
    ]
    assert payload["facets"]["topics"] == [
        {"value": "autonomous-agents", "label": "Autonomous Agents", "count": 1}
    ]


def test_default_search_keeps_retracted_visible_but_hides_new_scope_exclusion(
    papers_api,
) -> None:
    client, _ = papers_api

    response = client.get("/api/v1/papers", params={"q": "Agent"})

    assert response.status_code == 200
    by_key = {
        item["canonical_key"]: item for item in response.json()["items"]
    }
    assert "doi:10.1000/epsilon" not in by_key
    assert by_key["doi:10.1000/beta"]["status"] == "retracted"
    assert by_key["doi:10.1000/gamma"]["status"] == "withdrawn"


def test_pagination_is_stable_with_canonical_key_secondary_sort(papers_api) -> None:
    client, _ = papers_api

    first = client.get(
        "/api/v1/papers",
        params={"page": 1, "page_size": 2},
    ).json()
    second = client.get(
        "/api/v1/papers",
        params={"page": 2, "page_size": 2},
    ).json()
    repeated = client.get(
        "/api/v1/papers",
        params={"page": 1, "page_size": 2},
    ).json()

    first_keys = [item["canonical_key"] for item in first["items"]]
    second_keys = [item["canonical_key"] for item in second["items"]]
    repeated_keys = [
        item["canonical_key"] for item in repeated["items"]
    ]
    assert first_keys == repeated_keys
    assert first_keys == [
        "doi:10.1000/alpha",
        "doi:10.1000/beta",
    ]
    assert set(first_keys).isdisjoint(second_keys)
    assert first["total"] == 4


def test_paper_detail_returns_relations_provenance_metrics_and_scope(
    papers_api,
) -> None:
    client, _ = papers_api
    listing = client.get(
        "/api/v1/papers",
        params={"q": "10.1000/alpha"},
    ).json()
    slug = listing["items"][0]["slug"]

    response = client.get(f"/api/v1/papers/{slug}")

    assert response.status_code == 200
    payload = response.json()
    assert payload["canonical_key"] == "doi:10.1000/alpha"
    assert [version["version_label"] for version in payload["versions"]] == [
        "v1",
        "published",
    ]
    assert {item["scheme"] for item in payload["identifiers"]} >= {
        "doi",
        "arxiv",
        "openalex",
        "s2",
    }
    assert payload["authors"][0]["display_name"] == "Ada Lovelace"
    assert (
        payload["institutions"][0]["display_name"]
        == "Analytical Engine Institute"
    )
    assert payload["datasets"][0]["name"] == "Agent Dataset"
    assert payload["benchmarks"][0]["name"] == "Agent Benchmark"
    assert payload["code_repositories"][0]["is_official"] is True
    assert payload["provenance"]["source"] == "openalex"
    assert payload["provenance"]["source_license"] == "CC0"
    assert payload["license"]["content"] == "cc-by"
    assert payload["source_records"][0]["content_hash"]
    assert payload["scope_state"]["included"] is True
    assert payload["scope_state"]["rule_version"] == "scope-v1"


def test_topics_methods_stats_and_not_found_share_public_scope(papers_api) -> None:
    client, _ = papers_api

    topics = client.get("/api/v1/topics").json()
    methods = client.get("/api/v1/methods").json()
    stats = client.get("/api/v1/stats").json()
    missing = client.get("/api/v1/papers/not-a-valid-slug")

    assert topics["items"][0]["paper_count"] >= 1
    assert {item["normalized_name"] for item in methods["items"]} == {
        "tool-use",
        "tree-search",
    }
    assert stats["papers"] == 4
    assert stats["retracted"] == 1
    assert stats["withdrawn"] == 1
    assert missing.status_code == 404
    assert missing.json()["error"]["code"] == "paper_not_found"


@pytest.mark.parametrize(
    "params",
    [
        {"page": 0},
        {"page_size": 101},
        {"sort": "quality"},
        {"date_from": "2026-08-01", "date_to": "2026-07-01"},
        {"source": "s" * 65},
    ],
)
def test_invalid_paper_parameters_return_consistent_422(
    papers_api,
    params: dict[str, object],
) -> None:
    client, _ = papers_api

    response = client.get("/api/v1/papers", params=params)

    assert response.status_code == 422
    assert response.json()["error"]["code"] == "validation_error"


def test_search_is_parameterized_and_query_count_is_not_per_item(
    papers_api,
) -> None:
    client, engine = papers_api
    statements: list[str] = []

    def record_statement(
        _connection,
        _cursor,
        statement,
        _parameters,
        _context,
        _executemany,
    ) -> None:
        statements.append(statement)

    event.listen(engine, "before_cursor_execute", record_statement)
    try:
        injection = client.get(
            "/api/v1/papers",
            params={"q": "%' OR true --", "page_size": 1},
        )
        statements.clear()
        one = client.get(
            "/api/v1/papers",
            params={"page_size": 1},
        )
        one_count = len(statements)
        statements.clear()
        four = client.get(
            "/api/v1/papers",
            params={"page_size": 4},
        )
        four_count = len(statements)
    finally:
        event.remove(engine, "before_cursor_execute", record_statement)

    assert injection.status_code == 200
    assert injection.json()["total"] == 0
    assert one.status_code == four.status_code == 200
    assert four_count <= one_count + 2


def test_search_indexes_are_installed(papers_api) -> None:
    _, engine = papers_api

    indexes = {
        index["name"]
        for table in ("work", "source_record", "field_assertion")
        for index in inspect(engine).get_indexes(table)
    }

    assert {
        "ix_work_title_trgm",
        "ix_work_publication_date_canonical_key",
        "ix_field_assertion_authors_trgm",
    } <= indexes


def test_postgresql_query_plans_use_search_and_projection_indexes(
    papers_api,
) -> None:
    _, engine = papers_api

    with engine.begin() as connection:
        projection = connection.execute(
            text(
                """
                SELECT
                    id,
                    projection_source,
                    projection_source_record_id,
                    projection_source_updated_at
                FROM work
                WHERE canonical_key = 'doi:10.1000/alpha'
                """
            )
        ).mappings().one()
        connection.execute(text("SET LOCAL enable_seqscan = off"))
        connection.execute(text("SET LOCAL enable_indexscan = off"))
        title_plan = connection.scalar(
            text(
                """
                EXPLAIN (FORMAT JSON)
                SELECT id
                FROM work
                WHERE title ILIKE :pattern
                """
            ),
            {"pattern": "%Agent%"},
        )
        author_plan = connection.scalar(
            text(
                """
                EXPLAIN (FORMAT JSON)
                SELECT id
                FROM field_assertion
                WHERE field_name = 'authors'
                  AND CAST(value AS text) ILIKE :pattern
                """
            ),
            {"pattern": "%Ada Lovelace%"},
        )
        publication_plan = connection.scalar(
            text(
                """
                EXPLAIN (FORMAT JSON)
                SELECT id
                FROM work
                WHERE publication_date >= :date_from
                  AND publication_date <= :date_to
                ORDER BY publication_date ASC NULLS LAST, canonical_key ASC
                """
            ),
            {
                "date_from": date(2026, 7, 1),
                "date_to": date(2026, 7, 31),
            },
        )
        projection_plan = connection.scalar(
            text(
                """
                EXPLAIN (FORMAT JSON)
                SELECT id
                FROM source_record
                WHERE work_id = :work_id
                  AND source = :source
                  AND source_record_id = :source_record_id
                  AND source_updated_at
                      IS NOT DISTINCT FROM :source_updated_at
                """
            ),
            {
                "work_id": projection["id"],
                "source": projection["projection_source"],
                "source_record_id": (
                    projection["projection_source_record_id"]
                ),
                "source_updated_at": (
                    projection["projection_source_updated_at"]
                ),
            },
        )

    assert "ix_work_title_trgm" in str(title_plan)
    assert "Bitmap Index Scan" in str(author_plan)
    assert "Seq Scan" not in str(author_plan)
    assert "ix_work_publication_date_canonical_key" in str(publication_plan)
    assert "uq_source_record_source_record_hash" in str(projection_plan)


def test_nullable_projection_timestamp_keeps_author_searchable(
    papers_api,
) -> None:
    client, engine = papers_api
    with Session(engine) as session:
        work_id = _add_work(
            session,
            {},
            suffix="nullable-projection-time",
            title="Projection Timestamp Semantics",
            publication_date=date(2026, 7, 12),
            work_type="article",
            author="Null Timestamp Author",
            institution="Projection Lab",
            identifiers={"doi": "10.1000/nullable-projection-time"},
            source_updated_at=None,
        )
        session.commit()

    try:
        response = client.get(
            "/api/v1/papers",
            params={"q": "Null Timestamp Author"},
        )

        assert response.status_code == 200
        assert response.json()["total"] == 1
        assert (
            response.json()["items"][0]["canonical_key"]
            == "doi:10.1000/nullable-projection-time"
        )
        assert response.json()["items"][0]["type"] == "article"
        assert response.json()["facets"]["types"] == [
            {"value": "article", "count": 1}
        ]
    finally:
        from paper_hub.models import Work

        with Session(engine) as session:
            work = session.get(Work, work_id)
            assert work is not None
            session.delete(work)
            session.commit()


def test_detail_scope_state_uses_visibility_source_tiebreak(
    papers_api,
) -> None:
    from paper_hub.models import ScopeAssessment, SourceRecord
    from paper_hub.repositories import slug_for_canonical_key

    client, engine = papers_api
    with Session(engine) as session:
        work = session.scalar(
            text(
                """
                SELECT id
                FROM work
                WHERE canonical_key = 'doi:10.1000/alpha'
                """
            )
        )
        assert work is not None
        source = SourceRecord(
            work_id=work,
            source_record_id="SRC-alpha-newer-source",
            content_hash=hashlib.sha256(b"alpha-newer-source").hexdigest(),
            raw_payload={"id": "SRC-alpha-newer-source"},
            source_updated_at=NOW + timedelta(minutes=1),
            http_status=200,
            **_provenance(),
        )
        session.add(source)
        session.flush()
        session.add(
            ScopeAssessment(
                source_record_id=source.id,
                rule_version="scope-a",
                included=True,
                reason=None,
                evidence=[{"field": "source", "term": "newer"}],
                evaluated_at=NOW - timedelta(minutes=1),
                work_id=work,
            )
        )
        session.commit()
        source_id = source.id

    try:
        slug = slug_for_canonical_key("doi:10.1000/alpha")
        response = client.get(f"/api/v1/papers/{slug}")

        assert response.status_code == 200
        assert response.json()["scope_state"]["rule_version"] == "scope-a"
    finally:
        with Session(engine) as session:
            source = session.get(SourceRecord, source_id)
            assert source is not None
            session.delete(source)
            session.commit()


def test_detail_scope_state_aggregates_current_logical_source_decisions(
    papers_api,
) -> None:
    from paper_hub.models import ScopeAssessment, SourceRecord
    from paper_hub.repositories import slug_for_canonical_key

    client, engine = papers_api
    with Session(engine) as session:
        work_id = session.scalar(
            text(
                """
                SELECT id
                FROM work
                WHERE canonical_key = 'doi:10.1000/alpha'
                """
            )
        )
        assert work_id is not None
        excluded_source = SourceRecord(
            work_id=work_id,
            source_record_id="CR-alpha",
            content_hash=hashlib.sha256(
                b"crossref-alpha-excluded"
            ).hexdigest(),
            raw_payload={"id": "CR-alpha"},
            source_updated_at=NOW + timedelta(minutes=2),
            http_status=200,
            **_provenance(source="crossref"),
        )
        session.add(excluded_source)
        session.flush()
        session.add_all(
            [
                ScopeAssessment(
                    source_record_id=excluded_source.id,
                    rule_version="scope-crossref-v1",
                    included=True,
                    reason=None,
                    evidence=[],
                    evaluated_at=NOW + timedelta(minutes=1),
                    work_id=work_id,
                ),
                ScopeAssessment(
                    source_record_id=excluded_source.id,
                    rule_version="scope-crossref-v2",
                    included=False,
                    reason="excluded_by_crossref_scope",
                    evidence=[],
                    evaluated_at=NOW + timedelta(minutes=2),
                    work_id=work_id,
                ),
            ]
        )
        session.commit()
        excluded_source_id = excluded_source.id

    try:
        slug = slug_for_canonical_key("doi:10.1000/alpha")
        response = client.get(f"/api/v1/papers/{slug}")

        assert response.status_code == 200
        scope_state = response.json()["scope_state"]
        assert scope_state["included"] is True
        assert scope_state["reason"] is None
        decisions = {
            (item["source"], item["source_record_id"]): item
            for item in scope_state["evidence"]
        }
        assert set(decisions) == {
            ("openalex", "SRC-alpha"),
            ("crossref", "CR-alpha"),
        }
        assert decisions[("openalex", "SRC-alpha")]["included"] is True
        assert decisions[("crossref", "CR-alpha")] == {
            "source": "crossref",
            "source_record_id": "CR-alpha",
            "rule_version": "scope-crossref-v2",
            "included": False,
            "reason": "excluded_by_crossref_scope",
            "evaluated_at": "2026-07-16T12:02:00Z",
        }
    finally:
        with Session(engine) as session:
            source = session.get(SourceRecord, excluded_source_id)
            assert source is not None
            session.delete(source)
            session.commit()
