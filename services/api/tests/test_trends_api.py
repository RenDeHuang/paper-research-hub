from __future__ import annotations

from collections.abc import Generator
from datetime import UTC, datetime, timedelta
from decimal import Decimal
import hashlib
import os
from pathlib import Path
from uuid import UUID

from alembic import command
from fastapi.testclient import TestClient
import psycopg
import pytest
from sqlalchemy import create_engine
from sqlalchemy.orm import Session


def _provenance(
    status=None,
    *,
    retrieved_at: datetime | None = None,
) -> dict[str, object]:
    from paper_hub.models import RecordStatus

    return {
        "source": "test",
        "source_url": "https://source.test",
        "retrieved_at": retrieved_at or datetime.now(UTC),
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
    work_type: object = "preprint",
    topics: tuple[str, ...] = ("Agent Planning",),
    method: str = "Tool Use",
    citation_values: tuple[int, ...] = (),
    star_values: tuple[int, ...] = (),
    status=None,
    included: bool = True,
    repositories_without_signal: int = 0,
    source_updated_days_ago: int | None = None,
) -> UUID:
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
    projection_days_ago = (
        published_days_ago
        if source_updated_days_ago is None
        else source_updated_days_ago
    )
    projection_time = now - timedelta(days=projection_days_ago)
    provenance = _provenance(status, retrieved_at=projection_time)
    source = SourceRecord(
        work_id=None,
        source_record_id=source_record_id,
        content_hash=hashlib.sha256(suffix.encode()).hexdigest(),
        raw_payload={"id": source_record_id},
        source_updated_at=projection_time,
        **provenance,
    )
    session.add(source)
    session.flush()
    work = Work(
        canonical_key=f"doi:10.2000/{suffix}",
        title=f"Agent Trend {suffix}",
        publication_date=(now - timedelta(days=published_days_ago)).date(),
        projection_source="test",
        projection_source_record_id=source.id,
        projection_source_updated_at=projection_time,
        **provenance,
    )
    session.add(work)
    session.flush()
    source.work_id = work.id
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
    work.topics.extend(
        _get_entity(session, Topic, cache, topic) for topic in topics
    )
    work.methods.append(_get_entity(session, Method, cache, method))

    for index, value in enumerate(citation_values):
        measured_at = now - timedelta(
            days=30 * (len(citation_values) - 1 - index)
        )
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
                days=30 * (len(star_values) - 1 - index)
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
        for index in range(repositories_without_signal):
            work.code_repositories.append(
                CodeRepository(
                    provider="github",
                    repository_name=f"example/{suffix}-missing-{index}",
                    repository_url=(
                        "https://github.test/example/"
                        f"{suffix}-missing-{index}"
                    ),
                    normalized_url=(
                        "https://github.test/example/"
                        f"{suffix}-missing-{index}"
                    ),
                    **provenance,
                )
            )

    session.flush()
    return work.id


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
            topics=(
                "Agent Planning",
                "Reasoning Agents",
                "Tool Agents",
            ),
            citation_values=(10, 30),
            star_values=(100, 130),
            repositories_without_signal=2,
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
            suffix="tool-peer",
            published_days_ago=2,
            topics=("Tool Agents",),
            citation_values=(0, 30),
        )
        _add_rankable_work(
            session,
            cache,
            suffix="reasoning-peer",
            published_days_ago=2,
            topics=("Reasoning Agents",),
            method="Citation Cohort",
            citation_values=(0, 30),
        )
        _add_rankable_work(
            session,
            cache,
            suffix="reasoning-fast-peer",
            published_days_ago=2,
            topics=("Reasoning Agents",),
            method="Citation Cohort",
            citation_values=(0, 60),
        )
        _add_rankable_work(
            session,
            cache,
            suffix="invalid-work-type",
            published_days_ago=2,
            work_type=123,
            method="Citation Cohort",
            citation_values=(0, 300),
        )
        parser_upgrade_id = _add_rankable_work(
            session,
            cache,
            suffix="parser-upgrade-type",
            published_days_ago=2,
            work_type={"legacy": "invalid"},
            topics=("Parser Cohort",),
            method="Parser Method",
            citation_values=(0, 90),
        )
        from paper_hub.models import FieldAssertion, Work

        parser_upgrade = session.get(Work, parser_upgrade_id)
        assert parser_upgrade is not None
        session.add(
            FieldAssertion(
                id=UUID(int=0),
                source_record_id=(
                    parser_upgrade.projection_source_record_id
                ),
                field_name="type",
                value="preprint",
                parser_version="test-v2",
                created_at=datetime.now(UTC) + timedelta(seconds=1),
                **_provenance(),
            )
        )
        for index in range(5):
            _add_rankable_work(
                session,
                cache,
                suffix=f"method-no-topic-{index}",
                published_days_ago=2,
                topics=(),
            )
        _add_rankable_work(
            session,
            cache,
            suffix="unrelated",
            published_days_ago=2,
            topics=("Computer Vision",),
            method="Vision Method",
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
            suffix="dormant-method-history",
            published_days_ago=365,
            topics=("Agent Planning",),
            method="Dormant Method",
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
        version_fresh_id = _add_rankable_work(
            session,
            cache,
            suffix="version-fresh",
            published_days_ago=100,
        )
        _add_rankable_work(
            session,
            cache,
            suffix="projection-fresh",
            published_days_ago=100,
            source_updated_days_ago=1,
        )
        from paper_hub.models import PaperVersion, Work

        version_fresh = session.get(Work, version_fresh_id)
        assert version_fresh is not None
        version_fresh.versions.append(
            PaperVersion(
                version_label="v2",
                version_number=2,
                version_type="revised",
                published_at=datetime.now(UTC) - timedelta(hours=1),
                **_provenance(
                    retrieved_at=datetime.now(UTC) - timedelta(hours=1)
                ),
            )
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
    assert payload["coverage"]["total_eligible"] >= len(payload["items"])
    assert (
        payload["coverage"]["subjects_with_signal"]
        >= len(payload["items"])
    )
    assert payload["coverage"]["ranked_subjects"] >= len(payload["items"])
    assert payload["coverage"]["returned_subjects"] == len(payload["items"])
    assert payload["coverage"]["confidence"]["sample_size"] >= 0
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
    assert by_key["doi:10.2000/fast"]["score"] == pytest.approx(2 / 3)
    assert by_key["doi:10.2000/fast"]["percentile"] == 0.5
    assert by_key["doi:10.2000/slow"]["percentile"] == 0.5
    assert by_key["doi:10.2000/fast"]["details"][
        "percentile_aggregation"
    ] == "median_across_topics"
    assert {
        item["topic"]: (item["cohort_size"], item["percentile"])
        for item in by_key["doi:10.2000/fast"]["details"][
            "topic_cohorts"
        ]
    } == {
        "agent-planning": (2, 1.0),
        "reasoning-agents": (3, pytest.approx(1 / 3)),
        "tool-agents": (2, 0.5),
    }
    assert any(
        item["canonical_key"] == "doi:10.2000/missing"
        and item["reason"] == "requires_at_least_two_distinct_snapshots"
        for item in payload["missing_signals"]
    )
    assert "doi:10.2000/invalid-work-type" not in by_key
    assert by_key["doi:10.2000/parser-upgrade-type"]["details"][
        "work_type"
    ] == "preprint"
    assert any(
        item["canonical_key"] == "doi:10.2000/invalid-work-type"
        and item["signal"] == "work_type"
        and item["reason"] == "work_type_is_required_for_cohort"
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
    assert payload["items"][0]["score"] == 1.0
    assert payload["items"][0]["details"]["signal_status"] == "partial"
    assert payload["items"][0]["details"]["repositories_missing"] == 2
    fast_missing = [
        item
        for item in payload["missing_signals"]
        if item["canonical_key"] == "doi:10.2000/fast"
        and item["reason"] == "partial_repository_metric_coverage"
    ]
    assert len(fast_missing) == 2
    assert all(item["repository_id"] for item in fast_missing)
    assert all(item["repository_url"] for item in fast_missing)
    assert any(
        item["canonical_key"] == "doi:10.2000/slow"
        and item["reason"] == "requires_at_least_two_distinct_snapshots"
        for item in payload["missing_signals"]
    )

    limited = trends_api.get(
        "/api/v1/trends/papers",
        params={
            "ranking": "code_growth",
            "window_days": 30,
            "limit": 1,
        },
    ).json()
    limited_fast_missing = [
        item
        for item in limited["missing_signals"]
        if item["canonical_key"] == "doi:10.2000/fast"
        and item["reason"] == "partial_repository_metric_coverage"
    ]
    assert len(limited_fast_missing) == 2
    assert {
        item["canonical_key"]
        for item in limited["missing_signals"]
    } == {"doi:10.2000/fast"}


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
    assert payload["coverage"]["confidence"]["level"] in {
        "low",
        "medium",
        "high",
    }
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
    if window_days == 7:
        by_name = {
            item["normalized_name"]: item for item in payload["items"]
        }
        tool_use = by_name["tool-use"]
        assert tool_use["current_count"] <= tool_use["current_total"]
        assert tool_use["baseline_count"] <= tool_use["baseline_total"]
        assert tool_use["current_count"] == 3
        assert tool_use["current_total"] == 7
        assert tool_use["baseline_total"] == 1
        assert tool_use["score"] == pytest.approx(-4 / 7)
        assert tool_use["confidence"]["sample_size"] == 8
        dormant = by_name["dormant-method"]
        assert dormant["current_count"] == 0
        assert dormant["baseline_count"] == 0
        assert dormant["current_total"] == 4
        assert dormant["baseline_total"] == 1
        assert dormant["score"] == 0


@pytest.mark.parametrize(
    ("path", "ranking_name"),
    [
        ("/api/v1/trends/topics", "topic_growth"),
        ("/api/v1/trends/methods", "method_adoption"),
    ],
)
def test_taxonomy_rankings_separate_database_limit_from_coverage(
    trends_api: TestClient,
    path: str,
    ranking_name: str,
) -> None:
    response = trends_api.get(
        path,
        params={"window_days": 7, "limit": 1},
    )

    assert response.status_code == 200, response.text
    payload = response.json()
    assert payload["ranking_name"] == ranking_name
    assert payload["coverage"]["ranked_subjects"] > 1
    assert payload["coverage"]["returned_subjects"] == 1
    assert len(payload["items"]) == 1


def test_latest_uses_effective_time_and_separates_limit_from_coverage(
    trends_api: TestClient,
) -> None:
    response = trends_api.get(
        "/api/v1/trends/papers",
        params={"ranking": "latest", "window_days": 7, "limit": 1},
    )

    assert response.status_code == 200, response.text
    payload = response.json()
    assert payload["coverage"]["ranked_subjects"] > 1
    assert payload["coverage"]["returned_subjects"] == 1

    all_latest = trends_api.get(
        "/api/v1/trends/papers",
        params={"ranking": "latest", "window_days": 7, "limit": 100},
    ).json()
    by_key = {
        item["canonical_key"]: item for item in all_latest["items"]
    }
    assert by_key["doi:10.2000/version-fresh"]["details"][
        "effective_source"
    ] == "version"
    assert by_key["doi:10.2000/projection-fresh"]["details"][
        "effective_source"
    ] == "projection"
    assert by_key["doi:10.2000/version-fresh"]["details"]["effective_at"]


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
    percentile = schema["components"]["schemas"][
        "PaperTrendItemResponse"
    ]["properties"]["percentile"]["anyOf"][0]
    assert percentile["minimum"] == 0
    assert percentile["maximum"] == 1
    coverage = schema["components"]["schemas"][
        "PaperRankingCoverageResponse"
    ]["properties"]["signal_coverage"]
    assert coverage["minimum"] == 0
    assert coverage["maximum"] == 1
