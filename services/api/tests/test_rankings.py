from datetime import UTC, datetime, timedelta
from decimal import Decimal
from types import SimpleNamespace
from uuid import UUID


def test_metric_velocity_requires_two_distinct_snapshots() -> None:
    from paper_hub.rankings import MetricObservation, metric_velocity

    now = datetime(2026, 7, 16, 12, tzinfo=UTC)

    assert (
        metric_velocity(
            (
                MetricObservation(measured_at=now, value=Decimal("12")),
            )
        )
        is None
    )
    assert metric_velocity(
        (
            MetricObservation(measured_at=now, value=Decimal("12")),
            MetricObservation(measured_at=now, value=Decimal("15")),
        )
    ) is None


def test_metric_velocity_uses_boundary_snapshots_not_in_window_neighbors() -> None:
    from paper_hub.rankings import MetricObservation, metric_velocity

    now = datetime(2026, 7, 16, 12, tzinfo=UTC)
    window_start = now - timedelta(days=30)

    assert metric_velocity(
        (
            MetricObservation(
                measured_at=now - timedelta(days=40),
                value=Decimal("100"),
            ),
            MetricObservation(
                measured_at=now - timedelta(days=20),
                value=Decimal("500"),
            ),
            MetricObservation(
                measured_at=now,
                value=Decimal("140"),
            ),
            MetricObservation(
                measured_at=now + timedelta(minutes=1),
                value=Decimal("999"),
            ),
        ),
        window_start=window_start,
        as_of=now,
    ) == Decimal("1")


def test_citation_velocity_uses_topic_month_type_cohorts() -> None:
    from paper_hub.rankings import (
        CitationSeries,
        MetricObservation,
        calculate_citation_velocity,
    )

    now = datetime(2026, 7, 16, 12, tzinfo=UTC)
    fast_id = UUID("00000000-0000-0000-0000-000000000001")
    slow_id = UUID("00000000-0000-0000-0000-000000000002")
    other_cohort_id = UUID("00000000-0000-0000-0000-000000000003")
    missing_id = UUID("00000000-0000-0000-0000-000000000004")
    reasoning_peer_id = UUID(
        "00000000-0000-0000-0000-000000000005"
    )
    reasoning_fast_peer_id = UUID(
        "00000000-0000-0000-0000-000000000006"
    )

    result = calculate_citation_velocity(
        (
            CitationSeries(
                subject_id=fast_id,
                publication_month="2026-07",
                work_type="preprint",
                topics=(
                    "agent-planning",
                    "reasoning-agents",
                    "tool-agents",
                ),
                observations=(
                    MetricObservation(
                        measured_at=now - timedelta(days=10),
                        value=Decimal("10"),
                    ),
                    MetricObservation(
                        measured_at=now,
                        value=Decimal("30"),
                    ),
                ),
            ),
            CitationSeries(
                subject_id=slow_id,
                publication_month="2026-07",
                work_type="preprint",
                topics=("agent-planning",),
                observations=(
                    MetricObservation(
                        measured_at=now - timedelta(days=10),
                        value=Decimal("10"),
                    ),
                    MetricObservation(
                        measured_at=now,
                        value=Decimal("15"),
                    ),
                ),
            ),
            CitationSeries(
                subject_id=other_cohort_id,
                publication_month="2026-07",
                work_type="preprint",
                topics=("tool-agents",),
                observations=(
                    MetricObservation(
                        measured_at=now - timedelta(days=10),
                        value=Decimal("0"),
                    ),
                    MetricObservation(
                        measured_at=now,
                        value=Decimal("100"),
                    ),
                ),
            ),
            CitationSeries(
                subject_id=missing_id,
                publication_month="2026-07",
                work_type="preprint",
                topics=("agent-planning",),
                observations=(
                    MetricObservation(
                        measured_at=now,
                        value=Decimal("99"),
                    ),
                ),
            ),
            CitationSeries(
                subject_id=reasoning_peer_id,
                publication_month="2026-07",
                work_type="preprint",
                topics=("reasoning-agents",),
                observations=(
                    MetricObservation(
                        measured_at=now - timedelta(days=10),
                        value=Decimal("0"),
                    ),
                    MetricObservation(
                        measured_at=now,
                        value=Decimal("30"),
                    ),
                ),
            ),
            CitationSeries(
                subject_id=reasoning_fast_peer_id,
                publication_month="2026-07",
                work_type="preprint",
                topics=("reasoning-agents",),
                observations=(
                    MetricObservation(
                        measured_at=now - timedelta(days=10),
                        value=Decimal("0"),
                    ),
                    MetricObservation(
                        measured_at=now,
                        value=Decimal("40"),
                    ),
                ),
            ),
        )
    )

    by_id = {item.subject_id: item for item in result.items}
    assert by_id[fast_id].velocity_per_day == Decimal("2")
    assert by_id[slow_id].velocity_per_day == Decimal("0.5")
    assert by_id[fast_id].percentile == Decimal("0.5")
    assert by_id[slow_id].percentile == Decimal("0.5")
    assert by_id[other_cohort_id].percentile == Decimal("1")
    assert [
        (
            cohort.topic,
            cohort.cohort_size,
            cohort.percentile,
        )
        for cohort in by_id[fast_id].topic_cohorts
    ] == [
        ("agent-planning", 2, Decimal("1")),
        ("reasoning-agents", 3, Decimal("0.3333333333333333333333333333")),
        ("tool-agents", 2, Decimal("0.5")),
    ]
    assert result.missing_signals == (
        {
            "subject_id": str(missing_id),
            "signal": "citation_count",
            "reason": "requires_at_least_two_distinct_snapshots",
        },
    )


def test_window_growth_and_method_adoption_keep_formulas_separate() -> None:
    from paper_hub.rankings import (
        adoption_share_delta,
        growth_rate_delta,
        sample_confidence,
    )

    assert growth_rate_delta(
        current_count=9,
        baseline_count=2,
        window_days=7,
    ) == Decimal("1")
    assert adoption_share_delta(
        current_count=9,
        current_total=30,
        baseline_count=2,
        baseline_total=20,
    ) == Decimal("0.2")
    assert (
        adoption_share_delta(
            current_count=0,
            current_total=0,
            baseline_count=0,
            baseline_total=0,
        )
        is None
    )

    confidence = sample_confidence(sample_size=6)
    assert confidence.level == "low"
    assert confidence.sample_size == 6
    assert "small sample" in confidence.explanation


def test_ranking_contract_rejects_invalid_coverage_and_percentiles() -> None:
    import pytest
    from pydantic import ValidationError

    from paper_hub.schemas import (
        PaperRankingCoverageResponse,
        PaperTrendItemResponse,
        TaxonomyRankingCoverageResponse,
    )
    from paper_hub.models import RecordStatus

    with pytest.raises(ValidationError):
        PaperRankingCoverageResponse(
            total_eligible=1,
            subjects_with_signal=1,
            ranked_subjects=1,
            returned_subjects=1,
            signal_coverage=1.01,
            confidence={
                "level": "low",
                "sample_size": 1,
                "explanation": "Small sample",
            },
        )

    with pytest.raises(ValidationError):
        PaperTrendItemResponse(
            slug="paper-example",
            canonical_key="doi:10.1000/example",
            title="Example",
            status=RecordStatus.ACTIVE,
            score=1,
            percentile=-0.01,
        )

    required_coverage_fields = {
        "total_eligible",
        "subjects_with_signal",
        "ranked_subjects",
        "returned_subjects",
        "signal_coverage",
        "confidence",
    }
    for model in (
        PaperRankingCoverageResponse,
        TaxonomyRankingCoverageResponse,
    ):
        assert required_coverage_fields <= set(
            model.model_json_schema()["properties"]
        )


def test_citation_ranking_service_uses_bounded_repository_query() -> None:
    import pytest

    from paper_hub.models import RecordStatus
    from paper_hub.rankings import RankingService
    from paper_hub.repositories import BoundedPaperRanking

    now = datetime(2026, 7, 16, 12, tzinfo=UTC)

    class BoundedRepository:
        def __init__(self) -> None:
            self.calls: list[dict[str, object]] = []

        def citation_velocity_ranking(
            self,
            **kwargs: object,
        ) -> BoundedPaperRanking:
            self.calls.append(kwargs)
            return BoundedPaperRanking(
                items=(
                    {
                        "slug": "paper-example",
                        "canonical_key": "doi:10.1000/example",
                        "title": "Example",
                        "publication_date": None,
                        "status": "active",
                        "score": 1.5,
                        "percentile": 0.75,
                        "details": {},
                    },
                ),
                missing_signals=(),
                total_in_scope=3,
                ranked_subjects=2,
            )

    repository = BoundedRepository()
    service = object.__new__(RankingService)
    service.repository = repository

    payload = service._citation_velocity(
        now=now,
        window_days=30,
        limit=1,
    )

    assert repository.calls == [
        {
            "generated_at": now,
            "window_days": 30,
            "limit": 1,
            "excluded_statuses": {
                RecordStatus.RETRACTED,
                RecordStatus.WITHDRAWN,
            },
        }
    ]
    assert payload["coverage"] == {
        "total_eligible": 3,
        "subjects_with_signal": 2,
        "ranked_subjects": 2,
        "returned_subjects": 1,
        "signal_coverage": pytest.approx(2 / 3),
        "confidence": {
            "level": "low",
            "sample_size": 2,
            "explanation": (
                "Low confidence: small sample; the score is descriptive "
                "and is not a significance claim."
            ),
        },
    }
    assert payload["items"][0]["canonical_key"] == "doi:10.1000/example"


def test_taxonomy_ranking_service_uses_bounded_repository_query() -> None:
    import pytest

    from paper_hub.models import RecordStatus
    from paper_hub.rankings import RankingService

    now = datetime(2026, 7, 16, 12, tzinfo=UTC)

    class BoundedRepository:
        def __init__(self) -> None:
            self.calls: list[dict[str, object]] = []

        def taxonomy_window_ranking(
            self,
            **kwargs: object,
        ) -> SimpleNamespace:
            self.calls.append(kwargs)
            return SimpleNamespace(
                items=(
                    SimpleNamespace(
                        id=UUID(
                            "00000000-0000-0000-0000-000000000001"
                        ),
                        name="Agent Planning",
                        normalized_name="agent-planning",
                        score=Decimal("0.5"),
                        current_count=5,
                        baseline_count=2,
                        current_total=5,
                        baseline_total=2,
                    ),
                ),
                missing_signals=(),
                current_total=8,
                baseline_total=4,
                eligible_subjects=3,
                ranked_subjects=3,
            )

    repository = BoundedRepository()
    service = object.__new__(RankingService)
    service.repository = repository

    payload = service.taxonomy_ranking(
        subject="topic",
        window_days=7,
        limit=1,
        generated_at=now,
    )

    assert repository.calls == [
        {
            "subject": "topic",
            "generated_at": now,
            "window_days": 7,
            "limit": 1,
            "excluded_statuses": {
                RecordStatus.RETRACTED,
                RecordStatus.WITHDRAWN,
            },
        }
    ]
    assert payload["coverage"]["total_eligible"] == 3
    assert payload["coverage"]["subjects_with_signal"] == 3
    assert payload["coverage"]["ranked_subjects"] == 3
    assert payload["coverage"]["returned_subjects"] == 1
    assert payload["items"][0]["score"] == pytest.approx(0.5)
