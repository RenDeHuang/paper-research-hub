from __future__ import annotations

from collections.abc import Generator
from datetime import UTC, datetime, timedelta
from decimal import Decimal
import hashlib
import os
from pathlib import Path

from alembic import command
from fastapi.testclient import TestClient
import psycopg
import pytest
from sqlalchemy import create_engine
from sqlalchemy.orm import Session


def _provenance(status=None) -> dict[str, object]:
    from paper_hub.models import RecordStatus

    return {
        "source": "test",
        "source_url": "https://source.test",
        "retrieved_at": datetime.now(UTC),
        "source_license": "CC0",
        "content_license": "cc-by",
        "status": status or RecordStatus.ACTIVE,
    }


def _get_entity(session: Session, model, cache: dict, name: str):
    normalized = "-".join(name.casefold().split())
    key = (model.__name__, normalized)
    if key not in cache:
        cache[key] = model(
            name=name,
            normalized_name=normalized,
            description=None,
            **_provenance(),
        )
        session.add(cache[key])
        session.flush()
    return cache[key]


def _add_rankable_work(
    session: Session,
    cache: dict,
    *,
    suffix: str,
    published_days_ago: int,
    work_type: str = "preprint",
    topic: str = "Agent Planning",
    method: str = "Tool Use",
    citation_values: tuple[int, ...] = (),
    star_values: tuple[int, ...] = (),
    status=None,
    included: bool = True,
) -> None:
    from paper_hub.models import (
        CodeRepository,
        FieldAssertion,
        Method,
        MetricSnapshot,
        ScopeAssessment,
        SourceRecord,
        Topic,
        Work,
    )

    now = datetime.now(UTC).replace(microsecond=0)
    source_record_id = f"SRC-{suffix}"
    provenance = _provenance(status)
    work = Work(
        canonical_key=f"doi:10.2000/{suffix}",
        title=f"Agent Trend {suffix}",
        publication_date=(now - timedelta(days=published_days_ago)).date(),
        projection_source="test",
        projection_source_record_id=source_record_id,
        projection_source_updated_at=now,
        **provenance,
    )
    session.add(work)
    session.flush()
    source = SourceRecord(
        work_id=work.id,
        source_record_id=source_record_id,
        content_hash=hashlib.sha256(suffix.encode()).hexdigest(),
        raw_payload={"id": source_record_id},
        source_updated_at=now,
        **provenance,
    )
    session.add(source)
    session.flush()
    session.add(
        ScopeAssessment(
            source_record_id=source.id,
            rule_version="scope-v1",
            included=included,
            reason=None if included else "excluded",
            evidence=[],
            evaluated_at=now,
            work_id=work.id if included else None,
        )
    )
    session.add(
        FieldAssertion(
            source_record_id=source.id,
            field_name="type",
            value=work_type,
            parser_version="test-v1",
            **provenance,
        )
    )
    work.topics.append(_get_entity(session, Topic, cache, topic))
    work.methods.append(_get_entity(session, Method, cache, method))

    for index, value in enumerate(citation_values):
        measured_at = now - timedelta(days=10 * (len(citation_values) - 1 - index))
        session.add(
            MetricSnapshot(
                work_id=work.id,
                metric_name="citation_count",
                metric_value=Decimal(value),
                measured_at=measured_at,
                details=None,
                **provenance,
            )
        )

    if star_values:
        repository = CodeRepository(
            provider="github",
            repository_name=f"example/{suffix}",
            repository_url=f"https://github.test/example/{suffix}",
            normalized_url=f"https://github.test/example/{suffix}",
            **provenance,
        )
        work.code_repositories.append(repository)
        session.flush()
        for index, value in enumerate(star_values):
            measured_at = now - timedelta(
                days=10 * (len(star_values) - 1 - index)
            )
            session.add(
                MetricSnapshot(
                    code_repository_id=repository.id,
                    metric_name="stars",
                    metric_value=Decimal(value),
                    measured_at=measured_at,
                    details=None,
                    **provenance,
                )
            )


@pytest.fixture(scope="module")
def trends_api(
    postgres_container_url: str,
) -> Generator[TestClient, None, None]:
    from alembic.config import Config
    from paper_hub.config import get_settings
    from paper_hub.db import get_session
    from paper_hub.main import app
    from paper_hub.models import RecordStatus

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

    with Session(engine) as session:
        cache: dict = {}
        _add_rankable_work(
            session,
            cache,
            suffix="fast",
            published_days_ago=2,
            citation_values=(10, 30),
            star_values=(100, 130),
        )
        _add_rankable_work(
            session,
            cache,
            suffix="slow",
            published_days_ago=3,
            citation_values=(10, 15),
            star_values=(20,),
        )
        _add_rankable_work(
            session,
            cache,
            suffix="missing",
            published_days_ago=4,
            citation_values=(7,),
            method="Tree Search",
        )
        _add_rankable_work(
            session,
            cache,
            suffix="baseline",
            published_days_ago=10,
            citation_values=(),
        )
        _add_rankable_work(
            session,
            cache,
            suffix="retracted",
            published_days_ago=1,
            citation_values=(0, 1000),
            star_values=(0, 1000),
            status=RecordStatus.RETRACTED,
        )
        _add_rankable_work(
            session,
            cache,
            suffix="withdrawn",
            published_days_ago=1,
            citation_values=(0, 900),
            status=RecordStatus.WITHDRAWN,
        )
        _add_rankable_work(
            session,
            cache,
            suffix="excluded",
            published_days_ago=1,
            citation_values=(0, 800),
            included=False,
        )
        session.commit()

    def override_session():
        with Session(engine) as session:
            yield session

    app.dependency_overrides[get_session] = override_session
    with TestClient(app) as client:
        yield client
    app.dependency_overrides.clear()
    engine.dispose()
    if previous_database_url is None:
        os.environ.pop("DATABASE_URL", None)
    else:
        os.environ["DATABASE_URL"] = previous_database_url
    get_settings.cache_clear()


@pytest.mark.parametrize(
    ("ranking", "formula_version"),
    [
        ("latest", "latest-v1"),
        ("citation_velocity", "citation-velocity-v1"),
        ("code_growth", "code-growth-v1"),
    ],
)
def test_paper_rankings_are_separate_and_transparent(
    trends_api: TestClient,
    ranking: str,
    formula_version: str,
) -> None:
    response = trends_api.get(
        "/api/v1/trends/papers",
        params={"ranking": ranking, "window_days": 30},
    )

    assert response.status_code == 200, response.text
    payload = response.json()
    assert payload["ranking_name"] == ranking
    assert payload["formula_version"] == formula_version
    assert payload["window_days"] == 30
    assert payload["generated_at"].endswith(("Z", "+00:00"))
    assert payload["coverage"]["eligible_subjects"] >= len(payload["items"])
    assert isinstance(payload["missing_signals"], list)
    assert payload["explanation"]
    keys = {item["canonical_key"] for item in payload["items"]}
    assert "doi:10.2000/retracted" not in keys
    assert "doi:10.2000/withdrawn" not in keys
    assert "doi:10.2000/excluded" not in keys


def test_citation_velocity_reports_percentiles_and_missing_signals(
    trends_api: TestClient,
) -> None:
    payload = trends_api.get(
        "/api/v1/trends/papers",
        params={"ranking": "citation_velocity", "window_days": 30},
    ).json()

    by_key = {item["canonical_key"]: item for item in payload["items"]}
    assert by_key["doi:10.2000/fast"]["score"] == 2.0
    assert by_key["doi:10.2000/fast"]["percentile"] == 1.0
    assert by_key["doi:10.2000/slow"]["percentile"] == 0.5
    assert any(
        item["canonical_key"] == "doi:10.2000/missing"
        and item["reason"] == "requires_at_least_two_distinct_snapshots"
        for item in payload["missing_signals"]
    )


def test_code_growth_does_not_turn_one_snapshot_into_zero(
    trends_api: TestClient,
) -> None:
    payload = trends_api.get(
        "/api/v1/trends/papers",
        params={"ranking": "code_growth", "window_days": 30},
    ).json()

    assert payload["items"][0]["canonical_key"] == "doi:10.2000/fast"
    assert payload["items"][0]["score"] == 3.0
    assert any(
        item["canonical_key"] == "doi:10.2000/slow"
        and item["reason"] == "requires_at_least_two_distinct_snapshots"
        for item in payload["missing_signals"]
    )


@pytest.mark.parametrize("window_days", [7, 30, 90])
def test_topic_growth_uses_current_and_baseline_windows_with_confidence(
    trends_api: TestClient,
    window_days: int,
) -> None:
    response = trends_api.get(
        "/api/v1/trends/topics",
        params={"window_days": window_days},
    )

    assert response.status_code == 200
    payload = response.json()
    assert payload["ranking_name"] == "topic_growth"
    assert payload["formula_version"] == "topic-growth-v1"
    assert payload["window_days"] == window_days
    assert payload["coverage"]["confidence"] in {"low", "medium", "high"}
    assert "baseline" in payload["explanation"].casefold()
    if payload["items"]:
        assert payload["items"][0]["confidence"]["level"] == "low"


@pytest.mark.parametrize("window_days", [7, 30, 90])
def test_method_adoption_is_share_delta_not_topic_growth(
    trends_api: TestClient,
    window_days: int,
) -> None:
    response = trends_api.get(
        "/api/v1/trends/methods",
        params={"window_days": window_days},
    )

    assert response.status_code == 200
    payload = response.json()
    assert payload["ranking_name"] == "method_adoption"
    assert payload["formula_version"] == "method-adoption-v1"
    assert "share" in payload["explanation"].casefold()
    assert payload["coverage"]["current_total"] >= 0
    assert payload["coverage"]["baseline_total"] >= 0


@pytest.mark.parametrize(
    ("path", "params"),
    [
        ("/api/v1/trends/papers", {"ranking": "quality"}),
        ("/api/v1/trends/papers", {"window_days": 14}),
        ("/api/v1/trends/topics", {"window_days": 1}),
        ("/api/v1/trends/methods", {"limit": 101}),
    ],
)
def test_invalid_trend_parameters_return_consistent_422(
    trends_api: TestClient,
    path: str,
    params: dict[str, object],
) -> None:
    response = trends_api.get(path, params=params)

    assert response.status_code == 422
    assert response.json()["error"]["code"] == "validation_error"


def test_openapi_has_resolved_response_schemas(trends_api: TestClient) -> None:
    response = trends_api.get("/openapi.json")

    assert response.status_code == 200
    schema = response.json()
    for path in (
        "/api/v1/papers",
        "/api/v1/papers/{slug}",
        "/api/v1/topics",
        "/api/v1/methods",
        "/api/v1/trends/papers",
        "/api/v1/trends/topics",
        "/api/v1/trends/methods",
        "/api/v1/stats",
    ):
        assert path in schema["paths"]
        assert (
            schema["paths"][path]["get"]["responses"]["200"]["content"][
                "application/json"
            ]["schema"]
        )
