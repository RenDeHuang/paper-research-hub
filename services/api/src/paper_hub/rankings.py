from __future__ import annotations

from collections import defaultdict
from collections.abc import Sequence
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from decimal import Decimal
from uuid import UUID

from sqlalchemy.orm import Session

from paper_hub.models import RecordStatus
from paper_hub.repositories import PaperRepository


LATEST_FORMULA_VERSION = "latest-v1"
CITATION_VELOCITY_FORMULA_VERSION = "citation-velocity-v1"
CODE_GROWTH_FORMULA_VERSION = "code-growth-v1"
TOPIC_GROWTH_FORMULA_VERSION = "topic-growth-v1"
METHOD_ADOPTION_FORMULA_VERSION = "method-adoption-v1"

RANKING_EXCLUDED_STATUSES = {
    RecordStatus.RETRACTED,
    RecordStatus.WITHDRAWN,
}


@dataclass(frozen=True)
class MetricObservation:
    measured_at: datetime
    value: Decimal


@dataclass(frozen=True)
class CitationSeries:
    subject_id: UUID
    publication_month: str
    work_type: str
    observations: tuple[MetricObservation, ...]


@dataclass(frozen=True)
class CitationVelocity:
    subject_id: UUID
    velocity_per_day: Decimal
    percentile: Decimal
    cohort_size: int
    publication_month: str
    work_type: str


@dataclass(frozen=True)
class CitationVelocityResult:
    items: tuple[CitationVelocity, ...]
    missing_signals: tuple[dict[str, str], ...]


@dataclass(frozen=True)
class Confidence:
    level: str
    explanation: str


def metric_velocity(
    observations: Sequence[MetricObservation],
) -> Decimal | None:
    ordered = sorted(
        observations,
        key=lambda observation: observation.measured_at,
    )
    distinct_times = {observation.measured_at for observation in ordered}
    if len(ordered) < 2 or len(distinct_times) < 2:
        return None
    first = ordered[0]
    last = ordered[-1]
    elapsed_days = Decimal(
        str((last.measured_at - first.measured_at).total_seconds())
    ) / Decimal("86400")
    if elapsed_days <= 0:
        return None
    return (last.value - first.value) / elapsed_days


def calculate_citation_velocity(
    series: Sequence[CitationSeries],
) -> CitationVelocityResult:
    measured: list[tuple[CitationSeries, Decimal]] = []
    missing: list[dict[str, str]] = []
    for candidate in series:
        velocity = metric_velocity(candidate.observations)
        if velocity is None:
            missing.append(
                {
                    "subject_id": str(candidate.subject_id),
                    "signal": "citation_count",
                    "reason": (
                        "requires_at_least_two_distinct_snapshots"
                    ),
                }
            )
            continue
        measured.append((candidate, velocity))

    cohorts: dict[tuple[str, str], list[Decimal]] = defaultdict(list)
    for candidate, velocity in measured:
        cohorts[
            (candidate.publication_month, candidate.work_type)
        ].append(velocity)

    items = []
    for candidate, velocity in measured:
        cohort = cohorts[
            (candidate.publication_month, candidate.work_type)
        ]
        percentile = Decimal(
            sum(value <= velocity for value in cohort)
        ) / Decimal(len(cohort))
        items.append(
            CitationVelocity(
                subject_id=candidate.subject_id,
                velocity_per_day=velocity,
                percentile=percentile,
                cohort_size=len(cohort),
                publication_month=candidate.publication_month,
                work_type=candidate.work_type,
            )
        )

    items.sort(
        key=lambda item: (
            -item.velocity_per_day,
            str(item.subject_id),
        )
    )
    missing.sort(key=lambda item: item["subject_id"])
    return CitationVelocityResult(
        items=tuple(items),
        missing_signals=tuple(missing),
    )


def growth_rate_delta(
    *,
    current_count: int,
    baseline_count: int,
    window_days: int,
) -> Decimal:
    return Decimal(current_count - baseline_count) / Decimal(window_days)


def adoption_share_delta(
    *,
    current_count: int,
    current_total: int,
    baseline_count: int,
    baseline_total: int,
) -> Decimal | None:
    if current_total <= 0 or baseline_total <= 0:
        return None
    current_share = Decimal(current_count) / Decimal(current_total)
    baseline_share = Decimal(baseline_count) / Decimal(baseline_total)
    return current_share - baseline_share


def sample_confidence(*, sample_size: int) -> Confidence:
    if sample_size < 20:
        return Confidence(
            level="low",
            explanation=(
                "Low confidence: small sample; the score is descriptive "
                "and is not a significance claim."
            ),
        )
    if sample_size < 100:
        return Confidence(
            level="medium",
            explanation=(
                "Medium confidence: moderate sample; interpret rank "
                "differences cautiously."
            ),
        )
    return Confidence(
        level="high",
        explanation=(
            "High descriptive coverage; this still does not imply "
            "causal or statistical significance."
        ),
    )


class RankingService:
    def __init__(self, session: Session) -> None:
        self.repository = PaperRepository(session)

    def paper_ranking(
        self,
        *,
        ranking_name: str,
        window_days: int,
        limit: int,
        generated_at: datetime | None = None,
    ) -> dict[str, object]:
        now = generated_at or datetime.now(UTC)
        if ranking_name == "latest":
            return self._latest(
                now=now,
                window_days=window_days,
                limit=limit,
            )
        if ranking_name == "citation_velocity":
            return self._citation_velocity(
                now=now,
                window_days=window_days,
                limit=limit,
            )
        if ranking_name == "code_growth":
            return self._code_growth(
                now=now,
                window_days=window_days,
                limit=limit,
            )
        raise ValueError(f"unsupported paper ranking: {ranking_name}")

    def taxonomy_ranking(
        self,
        *,
        subject: str,
        window_days: int,
        limit: int,
        generated_at: datetime | None = None,
    ) -> dict[str, object]:
        now = generated_at or datetime.now(UTC)
        counts = self.repository.taxonomy_window_counts(
            subject=subject,
            generated_at=now,
            window_days=window_days,
            excluded_statuses=RANKING_EXCLUDED_STATUSES,
        )
        current_total = counts.current_total
        baseline_total = counts.baseline_total
        items: list[dict[str, object]] = []
        missing: list[dict[str, str]] = []

        for row in counts.items:
            sample_size = row.current_count + row.baseline_count
            confidence = sample_confidence(sample_size=sample_size)
            if subject == "topic":
                score = growth_rate_delta(
                    current_count=row.current_count,
                    baseline_count=row.baseline_count,
                    window_days=window_days,
                )
            else:
                score = adoption_share_delta(
                    current_count=row.current_count,
                    current_total=current_total,
                    baseline_count=row.baseline_count,
                    baseline_total=baseline_total,
                )
                if score is None:
                    missing.append(
                        {
                            "normalized_name": row.normalized_name,
                            "signal": "publication_window_denominator",
                            "reason": (
                                "current_and_baseline_windows_require_data"
                            ),
                        }
                    )
                    continue
            items.append(
                {
                    "id": row.id,
                    "name": row.name,
                    "normalized_name": row.normalized_name,
                    "score": float(score),
                    "current_count": row.current_count,
                    "baseline_count": row.baseline_count,
                    "confidence": {
                        "level": confidence.level,
                        "explanation": confidence.explanation,
                    },
                }
            )

        items.sort(
            key=lambda item: (
                -float(item["score"]),
                str(item["normalized_name"]),
            )
        )
        overall_confidence = sample_confidence(
            sample_size=current_total + baseline_total
        )
        ranking_name = (
            "topic_growth" if subject == "topic" else "method_adoption"
        )
        formula_version = (
            TOPIC_GROWTH_FORMULA_VERSION
            if subject == "topic"
            else METHOD_ADOPTION_FORMULA_VERSION
        )
        explanation = (
            "Score is the change in publications per day between the "
            "current window and the immediately preceding equal baseline "
            "window."
            if subject == "topic"
            else "Score is current publication share minus baseline "
            "publication share over an immediately preceding equal window."
        )
        return {
            "ranking_name": ranking_name,
            "formula_version": formula_version,
            "window_days": window_days,
            "generated_at": now,
            "coverage": {
                "eligible_subjects": len(counts.items),
                "ranked_subjects": len(items),
                "current_total": current_total,
                "baseline_total": baseline_total,
                "confidence": overall_confidence.level,
                "confidence_explanation": (
                    overall_confidence.explanation
                ),
            },
            "missing_signals": missing,
            "explanation": explanation,
            "items": items[:limit],
        }

    def _latest(
        self,
        *,
        now: datetime,
        window_days: int,
        limit: int,
    ) -> dict[str, object]:
        works = self.repository.rankable_works(
            excluded_statuses=RANKING_EXCLUDED_STATUSES,
        )
        cutoff = (now - timedelta(days=window_days)).date()
        candidates = [
            work
            for work in works
            if work.publication_date is not None
            and cutoff <= work.publication_date <= now.date()
        ]
        candidates.sort(
            key=lambda work: (
                -work.publication_date.toordinal(),
                work.canonical_key,
            )
        )
        items = [
            self.repository.paper_trend_item(
                work,
                score=float(work.publication_date.toordinal()),
                details={"publication_date": work.publication_date.isoformat()},
            )
            for work in candidates[:limit]
        ]
        return {
            "ranking_name": "latest",
            "formula_version": LATEST_FORMULA_VERSION,
            "window_days": window_days,
            "generated_at": now,
            "coverage": {
                "eligible_subjects": len(candidates),
                "ranked_subjects": len(items),
                "total_in_scope": len(works),
                "signal_coverage": (
                    len(candidates) / len(works) if works else 1.0
                ),
            },
            "missing_signals": [
                self.repository.paper_missing_signal(
                    work,
                    signal="publication_date",
                    reason="publication_date_is_missing",
                )
                for work in works
                if work.publication_date is None
            ],
            "explanation": (
                "Ordered by publication date descending, then canonical "
                "key ascending. No quality or popularity score is mixed in."
            ),
            "items": items,
        }

    def _citation_velocity(
        self,
        *,
        now: datetime,
        window_days: int,
        limit: int,
    ) -> dict[str, object]:
        works = self.repository.rankable_works(
            excluded_statuses=RANKING_EXCLUDED_STATUSES,
        )
        by_id = {work.id: work for work in works}
        types = self.repository.projection_assertion_values(
            tuple(by_id),
            field_name="type",
        )
        observations = self.repository.work_metric_observations(
            work_ids=tuple(by_id),
            metric_name="citation_count",
            start=now - timedelta(days=window_days),
            end=now,
        )
        series: list[CitationSeries] = []
        missing: list[dict[str, object]] = []
        for work in works:
            work_type = types.get(work.id)
            if work.publication_date is None:
                missing.append(
                    self.repository.paper_missing_signal(
                        work,
                        signal="publication_date",
                        reason="publication_month_is_required_for_cohort",
                    )
                )
                continue
            if not isinstance(work_type, str) or not work_type:
                missing.append(
                    self.repository.paper_missing_signal(
                        work,
                        signal="work_type",
                        reason="work_type_is_required_for_cohort",
                    )
                )
                continue
            series.append(
                CitationSeries(
                    subject_id=work.id,
                    publication_month=work.publication_date.strftime("%Y-%m"),
                    work_type=work_type,
                    observations=tuple(
                        MetricObservation(
                            measured_at=item.measured_at,
                            value=item.value,
                        )
                        for item in observations.get(work.id, ())
                    ),
                )
            )
        calculation = calculate_citation_velocity(series)
        for item in calculation.missing_signals:
            work = by_id[UUID(item["subject_id"])]
            missing.append(
                self.repository.paper_missing_signal(
                    work,
                    signal=item["signal"],
                    reason=item["reason"],
                )
            )
        items = [
            self.repository.paper_trend_item(
                by_id[item.subject_id],
                score=float(item.velocity_per_day),
                percentile=float(item.percentile),
                details={
                    "cohort_size": item.cohort_size,
                    "publication_month": item.publication_month,
                    "work_type": item.work_type,
                },
            )
            for item in calculation.items[:limit]
        ]
        return {
            "ranking_name": "citation_velocity",
            "formula_version": CITATION_VELOCITY_FORMULA_VERSION,
            "window_days": window_days,
            "generated_at": now,
            "coverage": {
                "eligible_subjects": len(works),
                "ranked_subjects": len(calculation.items),
                "signal_coverage": (
                    len(calculation.items) / len(works) if works else 1.0
                ),
            },
            "missing_signals": missing,
            "explanation": (
                "Score is (latest citation count - earliest citation count) "
                "/ elapsed days using at least two snapshots in the window. "
                "Percentile is the empirical CDF within publication month "
                "and work type."
            ),
            "items": items,
        }

    def _code_growth(
        self,
        *,
        now: datetime,
        window_days: int,
        limit: int,
    ) -> dict[str, object]:
        works = self.repository.rankable_works(
            excluded_statuses=RANKING_EXCLUDED_STATUSES,
            load_code=True,
        )
        repository_ids = tuple(
            repository.id
            for work in works
            for repository in work.code_repositories
        )
        observations = self.repository.repository_metric_observations(
            repository_ids=repository_ids,
            metric_name="stars",
            start=now - timedelta(days=window_days),
            end=now,
        )
        ranked: list[tuple[object, Decimal, int, int]] = []
        missing: list[dict[str, object]] = []
        for work in works:
            if not work.code_repositories:
                missing.append(
                    self.repository.paper_missing_signal(
                        work,
                        signal="code_repository",
                        reason="no_code_repository",
                    )
                )
                continue
            velocities = [
                velocity
                for repository in work.code_repositories
                if (
                    velocity := metric_velocity(
                        tuple(
                            MetricObservation(
                                measured_at=item.measured_at,
                                value=item.value,
                            )
                            for item in observations.get(repository.id, ())
                        )
                    )
                )
                is not None
            ]
            if not velocities:
                missing.append(
                    self.repository.paper_missing_signal(
                        work,
                        signal="stars",
                        reason=(
                            "requires_at_least_two_distinct_snapshots"
                        ),
                    )
                )
                continue
            ranked.append(
                (
                    work,
                    sum(velocities, Decimal("0")),
                    len(velocities),
                    len(work.code_repositories),
                )
            )
        ranked.sort(key=lambda row: (-row[1], row[0].canonical_key))
        items = [
            self.repository.paper_trend_item(
                work,
                score=float(score),
                details={
                    "repositories_with_signal": paired,
                    "repositories_total": total,
                    "metric": "stars",
                },
            )
            for work, score, paired, total in ranked[:limit]
        ]
        return {
            "ranking_name": "code_growth",
            "formula_version": CODE_GROWTH_FORMULA_VERSION,
            "window_days": window_days,
            "generated_at": now,
            "coverage": {
                "eligible_subjects": len(works),
                "ranked_subjects": len(ranked),
                "signal_coverage": (
                    len(ranked) / len(works) if works else 1.0
                ),
            },
            "missing_signals": missing,
            "explanation": (
                "Score is the sum of per-repository star deltas per elapsed "
                "day. Each repository requires at least two snapshots; "
                "missing signals are never replaced by zero."
            ),
            "items": items,
        }
