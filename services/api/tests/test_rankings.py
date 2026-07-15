from datetime import UTC, datetime, timedelta
from decimal import Decimal
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


def test_citation_velocity_uses_delta_per_day_and_month_type_percentile() -> None:
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

    result = calculate_citation_velocity(
        (
            CitationSeries(
                subject_id=fast_id,
                publication_month="2026-07",
                work_type="preprint",
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
                work_type="article",
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
                observations=(
                    MetricObservation(
                        measured_at=now,
                        value=Decimal("99"),
                    ),
                ),
            ),
        )
    )

    by_id = {item.subject_id: item for item in result.items}
    assert by_id[fast_id].velocity_per_day == Decimal("2")
    assert by_id[slow_id].velocity_per_day == Decimal("0.5")
    assert by_id[fast_id].percentile == Decimal("1")
    assert by_id[slow_id].percentile == Decimal("0.5")
    assert by_id[other_cohort_id].percentile == Decimal("1")
    assert by_id[fast_id].cohort_size == 2
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
    assert "small sample" in confidence.explanation
