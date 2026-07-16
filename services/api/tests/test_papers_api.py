from __future__ import annotations

from collections.abc import Generator
from concurrent.futures import ThreadPoolExecutor, TimeoutError
from datetime import UTC, date, datetime, timedelta
import hashlib
import os
from pathlib import Path
from threading import Event
from uuid import UUID

from alembic import command
from fastapi.testclient import TestClient
import psycopg
import pytest
from sqlalchemy import create_engine, event, func, inspect, select, text
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
    work_type: object,
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
    created_at: datetime | None = None,
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
    source_record = SourceRecord(
        work_id=None,
        source_record_id=source_record_id,
        content_hash=hashlib.sha256(suffix.encode()).hexdigest(),
        raw_payload={"id": source_record_id},
        source_updated_at=source_updated_at,
        http_status=200,
        **provenance,
    )
    session.add(source_record)
    session.flush()
    work_values = {
        "canonical_key": f"doi:10.1000/{suffix}",
        "title": title,
        "abstract": f"Abstract for {title}",
        "publication_date": publication_date,
        "projection_source": source,
        "projection_source_record_id": source_record.id,
        "projection_source_updated_at": source_updated_at,
        **provenance,
    }
    if created_at is not None:
        work_values["created_at"] = created_at
    work = Work(
        **work_values,
    )
    session.add(work)
    session.flush()
    source_record.work_id = work.id
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
    previous_scope_rule = os.environ.get("CURRENT_SCOPE_RULE_VERSION")
    os.environ["DATABASE_URL"] = postgres_container_url
    os.environ["CURRENT_SCOPE_RULE_VERSION"] = "scope-current"
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
    if previous_scope_rule is None:
        os.environ.pop("CURRENT_SCOPE_RULE_VERSION", None)
    else:
        os.environ["CURRENT_SCOPE_RULE_VERSION"] = previous_scope_rule
    get_settings.cache_clear()


@pytest.mark.parametrize(
    ("query", "expected_key"),
    [
        ("Planning with Tool", "doi:10.1000/alpha"),
        ("Ada Lovelace", "doi:10.1000/alpha"),
        ("10.1000/alpha", "doi:10.1000/alpha"),
        ("1000/alpha", "doi:10.1000/alpha"),
        ("2607.00001", "doi:10.1000/alpha"),
        ("W1001", "doi:10.1000/alpha"),
        ("1001", "doi:10.1000/alpha"),
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
        params={
            "page": 2,
            "page_size": 2,
            "snapshot_at": first["snapshot_at"],
            "snapshot_revision": first["snapshot_revision"],
        },
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
    assert first["snapshot_at"].endswith(("Z", "+00:00"))


def test_supported_sort_orders_use_normalized_titles_and_stable_ties(
    papers_api,
) -> None:
    from paper_hub.models import SourceRecord, Work

    client, engine = papers_api
    inserted_ids: list[UUID] = []
    source_ids: list[UUID] = []
    rows = (
        (
            "sort-alpha",
            "alpha Reviewer Sort Contract",
            date(2026, 7, 11),
        ),
        (
            "sort-beta-a",
            "beta Reviewer Sort Contract",
            date(2026, 7, 12),
        ),
        (
            "sort-beta-z",
            "Beta Reviewer Sort Contract",
            date(2026, 7, 12),
        ),
        (
            "sort-zulu",
            "zulu Reviewer Sort Contract",
            date(2026, 7, 13),
        ),
    )
    with Session(engine) as session:
        for suffix, title, publication_date in rows:
            inserted_ids.append(
                _add_work(
                    session,
                    {},
                    suffix=suffix,
                    title=title,
                    publication_date=publication_date,
                    work_type="preprint",
                    author="Sort Contract Author",
                    institution="Sort Contract Lab",
                    identifiers={"doi": f"10.1000/{suffix}"},
                )
            )
        session.commit()
        source_ids = list(
            session.scalars(
                select(Work.projection_source_record_id).where(
                    Work.id.in_(inserted_ids)
                )
            )
        )

    expected = {
        "published_asc": [
            "doi:10.1000/sort-alpha",
            "doi:10.1000/sort-beta-a",
            "doi:10.1000/sort-beta-z",
            "doi:10.1000/sort-zulu",
        ],
        "title_asc": [
            "doi:10.1000/sort-alpha",
            "doi:10.1000/sort-beta-a",
            "doi:10.1000/sort-beta-z",
            "doi:10.1000/sort-zulu",
        ],
        "title_desc": [
            "doi:10.1000/sort-zulu",
            "doi:10.1000/sort-beta-a",
            "doi:10.1000/sort-beta-z",
            "doi:10.1000/sort-alpha",
        ],
    }
    expected_titles = {
        "title_asc": [
            "alpha Reviewer Sort Contract",
            "beta Reviewer Sort Contract",
            "Beta Reviewer Sort Contract",
            "zulu Reviewer Sort Contract",
        ],
        "title_desc": [
            "zulu Reviewer Sort Contract",
            "beta Reviewer Sort Contract",
            "Beta Reviewer Sort Contract",
            "alpha Reviewer Sort Contract",
        ],
    }
    try:
        for sort, expected_keys in expected.items():
            response = client.get(
                "/api/v1/papers",
                params={
                    "q": "Reviewer Sort Contract",
                    "sort": sort,
                    "page_size": 10,
                },
            )

            assert response.status_code == 200, response.text
            payload = response.json()
            assert payload["total"] == 4
            assert [
                item["canonical_key"] for item in payload["items"]
            ] == expected_keys
            if sort in expected_titles:
                assert [
                    item["title"] for item in payload["items"]
                ] == expected_titles[sort]
    finally:
        with Session(engine) as session:
            for work in session.scalars(
                select(Work).where(Work.id.in_(inserted_ids))
            ):
                session.delete(work)
            session.flush()
            for source in session.scalars(
                select(SourceRecord).where(
                    SourceRecord.id.in_(source_ids)
                )
            ):
                session.delete(source)
            session.commit()


def test_pagination_snapshot_ignores_noop_work_and_source_updates(
    papers_api,
) -> None:
    client, engine = papers_api
    first_response = client.get(
        "/api/v1/papers",
        params={"page": 1, "page_size": 2},
    )
    assert first_response.status_code == 200, first_response.text
    first = first_response.json()

    with engine.begin() as connection:
        connection.execute(text("UPDATE work SET title = title"))
        connection.execute(
            text("UPDATE source_record SET work_id = work_id")
        )

    second = client.get(
        "/api/v1/papers",
        params={
            "page": 2,
            "page_size": 2,
            "snapshot_at": first["snapshot_at"],
            "snapshot_revision": first["snapshot_revision"],
        },
    )

    assert second.status_code == 200, second.text
    assert [
        item["canonical_key"] for item in second.json()["items"]
    ] == [
        "doi:10.1000/gamma",
        "doi:10.1000/delta",
    ]


def test_pagination_snapshot_excludes_concurrent_inserts(papers_api) -> None:
    client, engine = papers_api
    first = client.get(
        "/api/v1/papers",
        params={"page": 1, "page_size": 2},
    ).json()
    snapshot_at = first["snapshot_at"]
    snapshot_revision = first["snapshot_revision"]

    with Session(engine) as session:
        inserted_id = _add_work(
            session,
            {},
            suffix="concurrent-newest",
            title="Newest Concurrent Agent Paper",
            publication_date=date(2026, 7, 16),
            work_type="preprint",
            author="Concurrent Author",
            institution="Snapshot Lab",
            identifiers={"doi": "10.1000/concurrent-newest"},
        )
        session.commit()

    try:
        second = client.get(
            "/api/v1/papers",
            params={
                "page": 2,
                "page_size": 2,
                "snapshot_at": snapshot_at,
                "snapshot_revision": snapshot_revision,
            },
        )

        assert second.status_code == 200, second.text
        payload = second.json()
        assert payload["snapshot_at"] == snapshot_at
        assert payload["total"] == 4
        assert [
            item["canonical_key"] for item in payload["items"]
        ] == [
            "doi:10.1000/gamma",
            "doi:10.1000/delta",
        ]
    finally:
        from paper_hub.models import Work

        with Session(engine) as session:
            work = session.get(Work, inserted_id)
            assert work is not None
            session.delete(work)
            session.commit()


def test_pagination_snapshot_rejects_late_commit_with_lower_revision(
    papers_api,
) -> None:
    from paper_hub.models import ExternalIdentifier, FieldAssertion, Work

    client, engine = papers_api
    writer = Session(engine)
    try:
        work = writer.scalar(
            select(Work).where(
                Work.canonical_key == "doi:10.1000/gamma"
            )
        )
        assert work is not None
        assertion = writer.scalar(
            select(FieldAssertion).where(
                FieldAssertion.source_record_id
                == work.projection_source_record_id,
                FieldAssertion.field_name == "authors",
            )
        )
        assert assertion is not None
        assertion_id = assertion.id
        original_assertion_value = assertion.value
        assertion.value = []
        writer.flush()

        with Session(engine) as committed_writer:
            identifier = committed_writer.scalar(
                select(ExternalIdentifier).where(
                    ExternalIdentifier.scheme == "doi",
                    ExternalIdentifier.normalized_value
                    == "10.1000/delta",
                )
            )
            assert identifier is not None
            identifier_id = identifier.id
            original_raw_value = identifier.raw_value
            identifier.raw_value = "https://doi.org/10.1000/delta"
            committed_writer.commit()

        first = client.get(
            "/api/v1/papers",
            params={"page": 1, "page_size": 2},
        ).json()
        writer.commit()

        second = client.get(
            "/api/v1/papers",
            params={
                "page": 2,
                "page_size": 2,
                "snapshot_at": first["snapshot_at"],
                "snapshot_revision": first["snapshot_revision"],
            },
        )

        assert second.status_code == 409, second.text
        assert second.json()["error"]["code"] == (
            "search_snapshot_invalidated"
        )
    finally:
        if writer.in_transaction():
            writer.rollback()
        writer.close()
        with Session(engine) as session:
            assertion = session.get(FieldAssertion, assertion_id)
            identifier = session.get(ExternalIdentifier, identifier_id)
            assert assertion is not None
            assert identifier is not None
            assertion.value = original_assertion_value
            identifier.raw_value = original_raw_value
            session.commit()


def test_pagination_snapshot_rejects_mismatched_time_and_revision(
    papers_api,
) -> None:
    from paper_hub.models import Work

    client, engine = papers_api
    first = client.get(
        "/api/v1/papers",
        params={"page": 1, "page_size": 2},
    ).json()

    with Session(engine) as session:
        work = session.scalar(
            select(Work).where(
                Work.canonical_key == "doi:10.1000/gamma"
            )
        )
        assert work is not None
        original_title = work.title
        work.title = "Retitled Agent Search Systems"
        session.commit()

    try:
        newer = client.get(
            "/api/v1/papers",
            params={"page": 1, "page_size": 2},
        ).json()
        mixed = client.get(
            "/api/v1/papers",
            params={
                "page": 2,
                "page_size": 2,
                "snapshot_at": first["snapshot_at"],
                "snapshot_revision": newer["snapshot_revision"],
            },
        )

        assert mixed.status_code == 409, mixed.text
        assert mixed.json()["error"]["code"] == (
            "search_snapshot_invalidated"
        )
    finally:
        with Session(engine) as session:
            work = session.scalar(
                select(Work).where(
                    Work.canonical_key == "doi:10.1000/gamma"
                )
            )
            assert work is not None
            work.title = original_title
            session.commit()


def test_pagination_snapshot_rejects_existing_sort_key_changes(
    papers_api,
) -> None:
    from paper_hub.models import Work

    client, engine = papers_api
    first = client.get(
        "/api/v1/papers",
        params={"page": 1, "page_size": 2},
    ).json()
    assert "snapshot_revision" in first

    with Session(engine) as session:
        work = session.scalar(
            select(Work).where(
                Work.canonical_key == "doi:10.1000/gamma"
            )
        )
        assert work is not None
        original_publication_date = work.publication_date
        work.publication_date = date(2026, 7, 16)
        session.commit()

    try:
        second = client.get(
            "/api/v1/papers",
            params={
                "page": 2,
                "page_size": 2,
                "snapshot_at": first["snapshot_at"],
                "snapshot_revision": first["snapshot_revision"],
            },
        )

        assert second.status_code == 409, second.text
        assert second.json()["error"]["code"] == (
            "search_snapshot_invalidated"
        )
    finally:
        with Session(engine) as session:
            work = session.scalar(
                select(Work).where(
                    Work.canonical_key == "doi:10.1000/gamma"
                )
            )
            assert work is not None
            work.publication_date = original_publication_date
            session.commit()


def test_pagination_snapshot_rejects_existing_work_creation_time_changes(
    papers_api,
) -> None:
    from paper_hub.models import Work

    client, engine = papers_api
    first = client.get(
        "/api/v1/papers",
        params={"page": 1, "page_size": 2},
    ).json()
    snapshot_at = datetime.fromisoformat(first["snapshot_at"])

    with Session(engine) as session:
        work = session.scalar(
            select(Work).where(
                Work.canonical_key == "doi:10.1000/gamma"
            )
        )
        assert work is not None
        original_created_at = work.created_at
        work.created_at = snapshot_at + timedelta(days=1)
        session.commit()

    try:
        second = client.get(
            "/api/v1/papers",
            params={
                "page": 2,
                "page_size": 2,
                "snapshot_at": first["snapshot_at"],
                "snapshot_revision": first["snapshot_revision"],
            },
        )

        assert second.status_code == 409, second.text
        assert second.json()["error"]["code"] == (
            "search_snapshot_invalidated"
        )
    finally:
        with Session(engine) as session:
            work = session.scalar(
                select(Work).where(
                    Work.canonical_key == "doi:10.1000/gamma"
                )
            )
            assert work is not None
            work.created_at = original_created_at
            session.commit()


def test_pagination_snapshot_rejects_same_transaction_work_backdating(
    papers_api,
) -> None:
    from paper_hub.models import SourceRecord, Work

    client, engine = papers_api
    first = client.get(
        "/api/v1/papers",
        params={"page": 1, "page_size": 2},
    ).json()
    snapshot_at = datetime.fromisoformat(first["snapshot_at"])

    with Session(engine) as session:
        inserted_id = _add_work(
            session,
            {},
            suffix="same-transaction-backdate",
            title="Same Transaction Backdated Work",
            publication_date=date(2026, 7, 16),
            work_type="preprint",
            author="Backdated Author",
            institution="Temporal Lab",
            identifiers={
                "doi": "10.1000/same-transaction-backdate"
            },
            created_at=snapshot_at + timedelta(days=1),
        )
        work = session.get(Work, inserted_id)
        assert work is not None
        source_id = work.projection_source_record_id
        work.created_at = snapshot_at - timedelta(days=1)
        session.commit()

    try:
        second = client.get(
            "/api/v1/papers",
            params={
                "page": 2,
                "page_size": 2,
                "snapshot_at": first["snapshot_at"],
                "snapshot_revision": first["snapshot_revision"],
            },
        )

        assert second.status_code == 409, second.text
        assert second.json()["error"]["code"] == (
            "search_snapshot_invalidated"
        )
    finally:
        with Session(engine) as session:
            work = session.get(Work, inserted_id)
            source = session.get(SourceRecord, source_id)
            assert work is not None
            assert source is not None
            session.delete(work)
            session.flush()
            session.delete(source)
            session.commit()


def test_pagination_snapshot_rejects_assertion_owner_changes(
    papers_api,
) -> None:
    from paper_hub.models import FieldAssertion, SourceRecord, Work

    client, engine = papers_api
    first = client.get(
        "/api/v1/papers",
        params={"page": 1, "page_size": 2},
    ).json()

    with Session(engine) as session:
        existing_work = session.scalar(
            select(Work).where(
                Work.canonical_key == "doi:10.1000/gamma"
            )
        )
        assert existing_work is not None
        existing_source_id = existing_work.projection_source_record_id
        moved_assertion = session.scalar(
            select(FieldAssertion).where(
                FieldAssertion.source_record_id == existing_source_id,
                FieldAssertion.field_name == "authors",
            )
        )
        assert moved_assertion is not None
        moved_assertion_id = moved_assertion.id

        inserted_id = _add_work(
            session,
            {},
            suffix="assertion-owner-change",
            title="New Assertion Owner",
            publication_date=date(2026, 7, 16),
            work_type="preprint",
            author="New Owner Author",
            institution="Ownership Lab",
            identifiers={"doi": "10.1000/assertion-owner-change"},
        )
        inserted_work = session.get(Work, inserted_id)
        assert inserted_work is not None
        inserted_source_id = inserted_work.projection_source_record_id
        inserted_assertion = session.scalar(
            select(FieldAssertion).where(
                FieldAssertion.source_record_id == inserted_source_id,
                FieldAssertion.field_name == "authors",
            )
        )
        assert inserted_assertion is not None
        session.delete(inserted_assertion)
        session.flush()
        moved_assertion.source_record_id = inserted_source_id
        session.commit()

    try:
        second = client.get(
            "/api/v1/papers",
            params={
                "page": 2,
                "page_size": 2,
                "snapshot_at": first["snapshot_at"],
                "snapshot_revision": first["snapshot_revision"],
            },
        )

        assert second.status_code == 409, second.text
        assert second.json()["error"]["code"] == (
            "search_snapshot_invalidated"
        )
    finally:
        with Session(engine) as session:
            moved_assertion = session.get(
                FieldAssertion,
                moved_assertion_id,
            )
            inserted_work = session.get(Work, inserted_id)
            inserted_source = session.get(
                SourceRecord,
                inserted_source_id,
            )
            assert moved_assertion is not None
            assert inserted_work is not None
            assert inserted_source is not None
            moved_assertion.source_record_id = existing_source_id
            session.flush()
            session.delete(inserted_work)
            session.flush()
            session.delete(inserted_source)
            session.commit()


def test_pagination_snapshot_does_not_block_on_uncommitted_insert(
    papers_api,
) -> None:
    client, engine = papers_api
    writer = Session(engine)
    inserted_id = _add_work(
        writer,
        {},
        suffix="late-commit",
        title="Late Commit Agent Paper",
        publication_date=date(2026, 7, 16),
        work_type="preprint",
        author="Late Commit Author",
        institution="Transaction Lab",
        identifiers={"doi": "10.1000/late-commit"},
    )
    writer.flush()

    try:
        with ThreadPoolExecutor(max_workers=1) as executor:
            future = executor.submit(
                client.get,
                "/api/v1/papers",
                params={"page": 1, "page_size": 1},
            )
            try:
                first_response = future.result(timeout=2)
            except TimeoutError:
                writer.rollback()
                future.result(timeout=5)
                pytest.fail(
                    "first-page snapshot blocked on an uncommitted Work insert"
                )
        writer.commit()

        assert first_response.status_code == 200, first_response.text
        first = first_response.json()
        second_response = client.get(
            "/api/v1/papers",
            params={
                "page": 2,
                "page_size": 1,
                "snapshot_at": first["snapshot_at"],
                "snapshot_revision": first["snapshot_revision"],
            },
        )
        assert second_response.status_code == 409, second_response.text
        assert second_response.json()["error"]["code"] == (
            "search_snapshot_invalidated"
        )
    finally:
        writer.close()
        from paper_hub.models import Work

        with Session(engine) as session:
            work = session.get(Work, inserted_id)
            if work is not None:
                session.delete(work)
                session.commit()


def test_first_page_snapshot_does_not_lock_related_tables(
    papers_api,
) -> None:
    client, engine = papers_api
    writer_connection = engine.connect()
    writer_transaction = writer_connection.begin()
    executor = ThreadPoolExecutor(max_workers=1)
    try:
        writer_connection.execute(
            text("LOCK TABLE work IN ROW SHARE MODE")
        )
        writer_connection.execute(
            text("LOCK TABLE source_record IN ROW EXCLUSIVE MODE")
        )

        future = executor.submit(
            client.get,
            "/api/v1/papers",
            params={"page": 1, "page_size": 1},
        )
        try:
            response = future.result(timeout=2)
        except TimeoutError:
            writer_transaction.rollback()
            future.result(timeout=5)
            raise

        assert response.status_code == 200, response.text
    finally:
        if writer_transaction.is_active:
            writer_transaction.rollback()
        writer_connection.close()
        executor.shutdown(wait=True)


@pytest.mark.parametrize(
    "change_source",
    [
        "source_record",
        "scope_assessment",
        "external_identifier",
        "topic_association",
        "method_association",
        "code_association",
        "work_metric",
        "topic_name",
        "method_name",
    ],
)
def test_pagination_snapshot_rejects_each_observable_related_change(
    papers_api,
    change_source: str,
) -> None:
    from paper_hub.models import (
        ExternalIdentifier,
        MetricSnapshot,
        ScopeAssessment,
        SourceRecord,
        Work,
    )

    client, engine = papers_api
    first = client.get(
        "/api/v1/papers",
        params={"page": 1, "page_size": 2},
    ).json()
    cleanup: dict[str, object] = {}

    with Session(engine) as session:
        work = session.scalar(
            select(Work).where(
                Work.canonical_key == "doi:10.1000/alpha"
            )
        )
        assert work is not None
        cleanup["work_id"] = work.id
        source = session.get(
            SourceRecord,
            work.projection_source_record_id,
        )
        assert source is not None

        if change_source == "source_record":
            inserted_source = SourceRecord(
                work_id=work.id,
                source_record_id="SRC-search-change-matrix",
                content_hash="f" * 64,
                raw_payload={"matrix_change": True},
                source_updated_at=NOW + timedelta(minutes=1),
                http_status=200,
                **_provenance(),
            )
            session.add(inserted_source)
            session.flush()
            cleanup["source_id"] = inserted_source.id
        elif change_source == "scope_assessment":
            assessment = ScopeAssessment(
                source_record_id=source.id,
                rule_version="scope-matrix-change",
                included=False,
                reason="matrix_change",
                evidence=[],
                evaluated_at=NOW + timedelta(days=1),
                work_id=work.id,
                work_linked_at=NOW,
                work_link_reason="matrix_test",
            )
            session.add(assessment)
            session.flush()
            cleanup["assessment_id"] = assessment.id
        elif change_source == "external_identifier":
            identifier = session.scalar(
                select(ExternalIdentifier).where(
                    ExternalIdentifier.work_id == work.id,
                    ExternalIdentifier.scheme == "doi",
                )
            )
            assert identifier is not None
            cleanup["identifier_id"] = identifier.id
            cleanup["raw_value"] = identifier.raw_value
            identifier.raw_value = "https://doi.org/10.1000/alpha"
        elif change_source == "topic_association":
            topic = work.topics[0]
            cleanup["topic_id"] = topic.id
            work.topics.remove(topic)
        elif change_source == "method_association":
            method = work.methods[0]
            cleanup["method_id"] = method.id
            work.methods.remove(method)
        elif change_source == "code_association":
            repository = work.code_repositories[0]
            cleanup["repository_id"] = repository.id
            work.code_repositories.remove(repository)
        elif change_source == "work_metric":
            metric = MetricSnapshot(
                work_id=work.id,
                code_repository_id=None,
                source_record_id=source.id,
                metric_name="citation_count",
                metric_value=42,
                measured_at=source.source_updated_at,
                details={
                    "source_record_id": str(source.id),
                    "content_hash": source.content_hash,
                },
                **_provenance(),
            )
            session.add(metric)
            session.flush()
            cleanup["metric_id"] = metric.id
        elif change_source == "topic_name":
            topic = work.topics[0]
            cleanup["topic_id"] = topic.id
            cleanup["name"] = topic.name
            cleanup["normalized_name"] = topic.normalized_name
            topic.name = "Autonomous Agents Revised"
            topic.normalized_name = "autonomous-agents-revised"
        elif change_source == "method_name":
            method = work.methods[0]
            cleanup["method_id"] = method.id
            cleanup["name"] = method.name
            cleanup["normalized_name"] = method.normalized_name
            method.name = "Tool Use Revised"
            method.normalized_name = "tool-use-revised"
        else:
            raise AssertionError(f"unsupported change source: {change_source}")
        session.commit()

    try:
        second = client.get(
            "/api/v1/papers",
            params={
                "page": 2,
                "page_size": 2,
                "snapshot_at": first["snapshot_at"],
                "snapshot_revision": first["snapshot_revision"],
            },
        )

        assert second.status_code == 409, second.text
        assert second.json()["error"]["code"] == (
            "search_snapshot_invalidated"
        )
    finally:
        from paper_hub.models import (
            CodeRepository,
            Method,
            Topic,
        )

        with Session(engine) as session:
            work = session.get(Work, cleanup["work_id"])
            assert work is not None
            if change_source == "source_record":
                source = session.get(
                    SourceRecord,
                    cleanup["source_id"],
                )
                assert source is not None
                session.delete(source)
            elif change_source == "scope_assessment":
                assessment = session.get(
                    ScopeAssessment,
                    cleanup["assessment_id"],
                )
                assert assessment is not None
                session.delete(assessment)
            elif change_source == "external_identifier":
                identifier = session.get(
                    ExternalIdentifier,
                    cleanup["identifier_id"],
                )
                assert identifier is not None
                identifier.raw_value = cleanup["raw_value"]
            elif change_source == "topic_association":
                topic = session.get(Topic, cleanup["topic_id"])
                assert topic is not None
                work.topics.append(topic)
            elif change_source == "method_association":
                method = session.get(Method, cleanup["method_id"])
                assert method is not None
                work.methods.append(method)
            elif change_source == "code_association":
                repository = session.get(
                    CodeRepository,
                    cleanup["repository_id"],
                )
                assert repository is not None
                work.code_repositories.append(repository)
            elif change_source == "work_metric":
                metric = session.get(
                    MetricSnapshot,
                    cleanup["metric_id"],
                )
                assert metric is not None
                session.delete(metric)
            elif change_source == "topic_name":
                topic = session.get(Topic, cleanup["topic_id"])
                assert topic is not None
                topic.name = cleanup["name"]
                topic.normalized_name = cleanup["normalized_name"]
            elif change_source == "method_name":
                method = session.get(Method, cleanup["method_id"])
                assert method is not None
                method.name = cleanup["name"]
                method.normalized_name = cleanup["normalized_name"]
            session.commit()


def test_non_string_projection_type_is_omitted_from_items_and_facets(
    papers_api,
) -> None:
    client, engine = papers_api
    with Session(engine) as session:
        work_id = _add_work(
            session,
            {},
            suffix="invalid-type",
            title="Malformed Type Agent Paper",
            publication_date=date(2026, 7, 13),
            work_type={"unexpected": "object"},
            author="Malformed Type Author",
            institution="Schema Lab",
            identifiers={"doi": "10.1000/invalid-type"},
        )
        session.commit()

    try:
        response = client.get(
            "/api/v1/papers",
            params={"q": "Malformed Type Agent Paper"},
        )

        assert response.status_code == 200, response.text
        payload = response.json()
        assert payload["items"][0]["type"] is None
        assert payload["facets"]["types"] == []
    finally:
        from paper_hub.models import Work

        with Session(engine) as session:
            work = session.get(Work, work_id)
            assert work is not None
            session.delete(work)
            session.commit()


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
    assert payload["scope_state"]["rule_version"] == "scope-current"


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
        {"q": "   "},
        {"page": 2},
        {"snapshot_at": NOW.isoformat()},
        {"snapshot_revision": 0},
        {"page": 2, "snapshot_at": NOW.isoformat()},
        {"page": 2, "snapshot_revision": 0},
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


@pytest.mark.parametrize(
    ("path", "query"),
    [
        ("/api/v1/papers", " "),
        ("/api/v1/papers", "\t"),
        ("/api/v1/topics", "\r\n"),
        ("/api/v1/methods", " \t "),
    ],
)
def test_blank_search_queries_are_rejected(
    papers_api,
    path: str,
    query: str,
) -> None:
    client, _ = papers_api

    response = client.get(path, params={"q": query})

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
        facet_aggregation_count = sum(
            "filtered_work_ids" in statement
            for statement in statements
        )
    finally:
        event.remove(engine, "before_cursor_execute", record_statement)

    assert injection.status_code == 200
    assert injection.json()["total"] == 0
    assert one.status_code == four.status_code == 200
    assert four_count <= one_count + 2
    assert facet_aggregation_count == 1
    assert any(
        "FILTERED_WORK_IDS AS MATERIALIZED" in statement.upper()
        for statement in statements
    )


def test_public_reads_use_repeatable_read_only_transactions(
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
        response = client.get("/api/v1/papers", params={"page_size": 1})
    finally:
        event.remove(engine, "before_cursor_execute", record_statement)

    assert response.status_code == 200
    assert any(
        "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY"
        in statement.upper()
        for statement in statements
    )


def test_paper_list_total_items_and_facets_share_repeatable_read_snapshot(
    papers_api,
) -> None:
    from paper_hub.models import SourceRecord, Work

    client, engine = papers_api
    total_query_finished = Event()
    concurrent_write_finished = Event()
    total_query_intercepted = Event()
    inserted_id: UUID | None = None
    inserted_source_id: UUID | None = None

    def pause_after_total_query(
        _connection,
        _cursor,
        statement,
        _parameters,
        _context,
        _executemany,
    ) -> None:
        normalized = " ".join(statement.upper().split())
        if (
            not total_query_intercepted.is_set()
            and normalized.startswith(
                "SELECT COUNT(*) AS COUNT_1 FROM WORK"
            )
        ):
            total_query_intercepted.set()
            total_query_finished.set()
            if not concurrent_write_finished.wait(timeout=10):
                raise AssertionError(
                    "concurrent write did not commit after total query"
                )

    def insert_matching_work() -> tuple[UUID, UUID]:
        if not total_query_finished.wait(timeout=10):
            raise AssertionError("paper-list total query was not observed")
        try:
            with Session(engine) as session:
                work_id = _add_work(
                    session,
                    {},
                    suffix="repeatable-read-visible",
                    title="Repeatable Read Concurrent Visibility",
                    publication_date=date(2026, 7, 16),
                    work_type="preprint",
                    author="Concurrent Snapshot Author",
                    institution="Repeatable Read Lab",
                    identifiers={
                        "doi": "10.1000/repeatable-read-visible"
                    },
                )
                work = session.get(Work, work_id)
                assert work is not None
                source_id = work.projection_source_record_id
                assert source_id is not None
                session.commit()
                return work_id, source_id
        finally:
            concurrent_write_finished.set()

    event.listen(engine, "after_cursor_execute", pause_after_total_query)
    try:
        with ThreadPoolExecutor(max_workers=1) as executor:
            writer = executor.submit(insert_matching_work)
            response = client.get(
                "/api/v1/papers",
                params={"q": "Repeatable Read Concurrent Visibility"},
            )
            inserted_id, inserted_source_id = writer.result(timeout=10)
    finally:
        event.remove(engine, "after_cursor_execute", pause_after_total_query)

    try:
        assert total_query_intercepted.is_set()
        assert response.status_code == 200, response.text
        payload = response.json()
        assert payload["total"] == 0
        assert payload["items"] == []
        assert payload["facets"] == {
            "types": [],
            "topics": [],
            "methods": [],
            "statuses": [],
            "sources": [],
            "has_code": [
                {"value": "true", "count": 0},
                {"value": "false", "count": 0},
            ],
        }

        current = client.get(
            "/api/v1/papers",
            params={"q": "Repeatable Read Concurrent Visibility"},
        )
        assert current.status_code == 200, current.text
        assert current.json()["total"] == 1
    finally:
        if inserted_id is not None and inserted_source_id is not None:
            with Session(engine) as session:
                work = session.get(Work, inserted_id)
                source = session.get(SourceRecord, inserted_source_id)
                assert work is not None
                assert source is not None
                session.delete(work)
                session.flush()
                session.delete(source)
                session.commit()


def test_search_indexes_are_installed(papers_api) -> None:
    _, engine = papers_api

    indexes = {
        index["name"]
        for table in (
            "work",
            "source_record",
            "external_identifier",
            "field_assertion",
        )
        for index in inspect(engine).get_indexes(table)
    }

    assert {
        "ix_work_title_trgm",
        "ix_work_canonical_key_trgm",
        "ix_work_publication_date_canonical_key",
        "ix_work_publication_date_desc_canonical_key",
        "ix_work_created_at",
        "ix_external_identifier_normalized_value_trgm",
        "ix_external_identifier_raw_value_trgm",
        "ix_field_assertion_authors_trgm",
    } <= indexes


def test_postgresql_query_plans_use_search_and_projection_indexes(
    papers_api,
) -> None:
    from paper_hub.models import Work
    from paper_hub.repositories import PaperRepository, PaperSearchFilters

    client, engine = papers_api
    public_before_response = client.get(
        "/api/v1/papers",
        params={"page_size": 100},
    )
    assert public_before_response.status_code == 200
    public_before = public_before_response.json()

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
        publication_plan = connection.scalar(
            text(
                """
                EXPLAIN (FORMAT JSON)
                SELECT id
                FROM work
                WHERE publication_date >= :date_from
                  AND publication_date <= :date_to
                ORDER BY publication_date DESC NULLS LAST, canonical_key ASC
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
                WHERE id = :source_record_id
                """
            ),
            {
                "source_record_id": (
                    projection["projection_source_record_id"]
                ),
            },
        )
        connection.execute(
            text(
                """
                WITH generated AS (
                    SELECT
                        sequence_number,
                        md5(
                            'reviewer-a-query-plan-work-'
                            || sequence_number::text
                        )::uuid AS work_id
                    FROM generate_series(1, 4096)
                        AS generated(sequence_number)
                )
                INSERT INTO work (
                    id,
                    canonical_key,
                    title,
                    source,
                    status
                )
                SELECT
                    work_id,
                    'doi:10.9900/reviewer-a-plan-'
                        || sequence_number::text,
                    'Reviewer A hidden query-plan work '
                        || sequence_number::text,
                    'reviewer-a-query-plan',
                    'active'
                FROM generated
                """
            )
        )
        connection.execute(
            text(
                """
                WITH generated AS (
                    SELECT
                        sequence_number,
                        md5(
                            'reviewer-a-query-plan-work-'
                            || sequence_number::text
                        )::uuid AS work_id,
                        md5(
                            'reviewer-a-query-plan-identifier-'
                            || sequence_number::text
                        )::uuid AS identifier_id
                    FROM generate_series(1, 4096)
                        AS generated(sequence_number)
                )
                INSERT INTO external_identifier (
                    id,
                    work_id,
                    scheme,
                    normalized_value,
                    raw_value,
                    source,
                    status
                )
                SELECT
                    identifier_id,
                    work_id,
                    'openalex',
                    'W9' || lpad(sequence_number::text, 10, '0'),
                    'W9' || lpad(sequence_number::text, 10, '0'),
                    'reviewer-a-query-plan',
                    'active'
                FROM generated
                """
            )
        )
        connection.execute(text("ANALYZE work, external_identifier"))
        snapshot_at = connection.scalar(select(func.clock_timestamp()))
        repository_exact_plans: dict[str, object] = {}
        with Session(bind=connection) as repository_session:
            repository = PaperRepository(repository_session)
            for query_kind, query in (
                ("doi", "10.1000/alpha"),
                ("openalex", "W1001"),
            ):
                statement = select(Work.id).where(
                    *repository._search_predicates(
                        PaperSearchFilters(query=query),
                        snapshot_at=snapshot_at,
                    )
                )
                compiled = statement.compile(
                    dialect=connection.dialect,
                    compile_kwargs={"literal_binds": True},
                )
                repository_exact_plans[query_kind] = connection.scalar(
                    text(f"EXPLAIN (FORMAT JSON) {compiled}")
                )
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
        canonical_plan = connection.scalar(
            text(
                """
                EXPLAIN (FORMAT JSON)
                SELECT id
                FROM work
                WHERE canonical_key ILIKE :pattern
                """
            ),
            {"pattern": "%10.1000/alpha%"},
        )
        external_plan = connection.scalar(
            text(
                """
                EXPLAIN (FORMAT JSON)
                SELECT id
                FROM external_identifier
                WHERE raw_value ILIKE :pattern
                """
            ),
            {"pattern": "%W1001%"},
        )
        connection.execute(text("SET LOCAL enable_indexscan = on"))
        connection.execute(text("SET LOCAL enable_seqscan = on"))

    public_after_response = client.get(
        "/api/v1/papers",
        params={"page_size": 100},
    )
    assert public_after_response.status_code == 200
    public_after = public_after_response.json()
    assert public_after["total"] == public_before["total"]
    assert [
        item["canonical_key"] for item in public_after["items"]
    ] == [
        item["canonical_key"] for item in public_before["items"]
    ]
    assert "ix_work_title_trgm" in str(title_plan)
    assert "Bitmap Index Scan" in str(author_plan)
    assert "Seq Scan" not in str(author_plan)
    assert "ix_work_canonical_key_trgm" in str(canonical_plan)
    assert "ix_external_identifier_raw_value_trgm" in str(
        external_plan
    )
    for query_kind, plan in repository_exact_plans.items():
        plan_text = str(plan)
        assert "uq_work_canonical_key" in plan_text, query_kind
        assert (
            "uq_external_identifier_scheme_value" in plan_text
        ), query_kind
    assert "ix_work_publication_date_desc_canonical_key" in str(
        publication_plan
    )
    assert "Sort" not in str(publication_plan)
    assert "pk_source_record" in str(projection_plan)


def test_projection_type_uses_one_current_parser_assertion_everywhere(
    papers_api,
) -> None:
    from paper_hub.models import FieldAssertion, SourceRecord, Work

    client, engine = papers_api
    with Session(engine) as session:
        work = session.scalar(
            select(Work).where(
                Work.canonical_key == "doi:10.1000/alpha"
            )
        )
        assert work is not None
        source = session.get(
            SourceRecord,
            work.projection_source_record_id,
        )
        assert source is not None
        current = session.scalar(
            select(FieldAssertion)
            .where(
                FieldAssertion.source_record_id == source.id,
                FieldAssertion.field_name == "type",
            )
            .order_by(FieldAssertion.created_at.desc())
        )
        assert current is not None
        replacement = FieldAssertion(
            source_record_id=source.id,
            field_name="type",
            value="article",
            parser_version="test-v2",
            created_at=current.created_at + timedelta(seconds=1),
            **_provenance(),
        )
        session.add(replacement)
        session.commit()
        replacement_id = replacement.id

    try:
        listing = client.get(
            "/api/v1/papers",
            params={"q": "10.1000/alpha"},
        )
        old_filter = client.get(
            "/api/v1/papers",
            params={"type": "preprint", "q": "10.1000/alpha"},
        )
        new_filter = client.get(
            "/api/v1/papers",
            params={"type": "article", "q": "10.1000/alpha"},
        )

        assert listing.status_code == 200, listing.text
        assert listing.json()["items"][0]["type"] == "article"
        assert listing.json()["facets"]["types"] == [
            {"value": "article", "count": 1}
        ]
        assert old_filter.json()["total"] == 0
        assert new_filter.json()["total"] == 1
    finally:
        with Session(engine) as session:
            replacement = session.get(FieldAssertion, replacement_id)
            assert replacement is not None
            session.delete(replacement)
            session.commit()


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
        scope_state = response.json()["scope_state"]
        assert scope_state["rule_version"] == "scope-current"
        assert any(
            item["rule_version"] == "scope-a"
            for item in scope_state["evidence"]
        )
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
        assert scope_state["rule_version"] == "scope-current"
        assert scope_state["evaluated_at"] == "2026-07-16T12:02:00Z"
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
