from __future__ import annotations

from collections import defaultdict
from collections.abc import Sequence
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from decimal import Decimal
from statistics import median
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
    topics: tuple[str, ...]
    observations: tuple[MetricObservation, ...]


@dataclass(frozen=True)
class CitationTopicCohort:
    topic: str
    percentile: Decimal
    cohort_size: int


@dataclass(frozen=True)
class CitationVelocity:
    subject_id: UUID
    velocity_per_day: Decimal
    percentile: Decimal
    publication_month: str
    work_type: str
    topic_cohorts: tuple[CitationTopicCohort, ...]


@dataclass(frozen=True)
class CitationVelocityResult:
    items: tuple[CitationVelocity, ...]
    missing_signals: tuple[dict[str, str], ...]


@dataclass(frozen=True)
class Confidence:
    level: str
    sample_size: int
    explanation: str


def metric_velocity(
    observations: Sequence[MetricObservation],
    *,
    window_start: datetime | None = None,
    as_of: datetime | None = None,
) -> Decimal | None:
    if (window_start is None) != (as_of is None):
        raise ValueError(
            "window_start and as_of must be provided together"
        )
    ordered = sorted(
        observations,
        key=lambda observation: observation.measured_at,
    )
    if window_start is not None and as_of is not None:
        baseline_candidates = [
            observation
            for observation in ordered
            if observation.measured_at <= window_start
        ]
        current_candidates = [
            observation
            for observation in ordered
            if observation.measured_at <= as_of
        ]
        if not baseline_candidates or not current_candidates:
            return None
        ordered = [
            baseline_candidates[-1],
            current_candidates[-1],
        ]
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
        if not candidate.topics:
            missing.append(
                {
                    "subject_id": str(candidate.subject_id),
                    "signal": "topics",
                    "reason": "at_least_one_topic_is_required_for_cohort",
                }
            )
            continue
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

    cohorts: dict[tuple[str, str, str], list[Decimal]] = defaultdict(list)
    for candidate, velocity in measured:
        for topic in candidate.topics:
            cohorts[
                (
                    topic,
                    candidate.publication_month,
                    candidate.work_type,
                )
            ].append(velocity)

    items = []
    for candidate, velocity in measured:
        topic_cohorts = []
        for topic in sorted(set(candidate.topics)):
            cohort = cohorts[
                (
                    topic,
                    candidate.publication_month,
                    candidate.work_type,
                )
            ]
            topic_cohorts.append(
                CitationTopicCohort(
                    topic=topic,
                    percentile=Decimal(
                        sum(value <= velocity for value in cohort)
                    )
                    / Decimal(len(cohort)),
                    cohort_size=len(cohort),
                )
            )
        percentile = median(
            item.percentile for item in topic_cohorts
        )
        items.append(
            CitationVelocity(
                subject_id=candidate.subject_id,
                velocity_per_day=velocity,
                percentile=percentile,
                publication_month=candidate.publication_month,
                work_type=candidate.work_type,
                topic_cohorts=tuple(topic_cohorts),
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
            sample_size=sample_size,
            explanation=(
                "Low confidence: small sample; the score is descriptive "
                "and is not a significance claim."
            ),
        )
    if sample_size < 100:
        return Confidence(
            level="medium",
            sample_size=sample_size,
            explanation=(
                "Medium confidence: moderate sample; interpret rank "
                "differences cautiously."
            ),
        )
    return Confidence(
        level="high",
        sample_size=sample_size,
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
        ranking = self.repository.taxonomy_window_ranking(
            subject=subject,
            generated_at=now,
            window_days=window_days,
            limit=limit,
            excluded_statuses=RANKING_EXCLUDED_STATUSES,
        )
        current_total = ranking.current_total
        baseline_total = ranking.baseline_total
        items: list[dict[str, object]] = []

        for row in ranking.items:
            sample_size = (
                row.current_total + row.baseline_total
                if subject == "method"
                else row.current_count + row.baseline_count
            )
            confidence = sample_confidence(sample_size=sample_size)
            items.append(
                {
                    "id": row.id,
                    "name": row.name,
                    "normalized_name": row.normalized_name,
                    "score": float(row.score),
                    "current_count": row.current_count,
                    "baseline_count": row.baseline_count,
                    "current_total": row.current_total,
                    "baseline_total": row.baseline_total,
                    "confidence": {
                        "level": confidence.level,
                        "sample_size": confidence.sample_size,
                        "explanation": confidence.explanation,
                    },
                }
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
                "total_eligible": ranking.eligible_subjects,
                "subjects_with_signal": ranking.ranked_subjects,
                "ranked_subjects": ranking.ranked_subjects,
                "returned_subjects": len(items),
                "signal_coverage": (
                    ranking.ranked_subjects / ranking.eligible_subjects
                    if ranking.eligible_subjects
                    else 1.0
                ),
                "current_total": current_total,
                "baseline_total": baseline_total,
                "confidence": {
                    "level": overall_confidence.level,
                    "sample_size": overall_confidence.sample_size,
                    "explanation": overall_confidence.explanation,
                },
            },
            "missing_signals": list(ranking.missing_signals),
            "explanation": explanation,
            "items": items,
        }

    def _latest(
        self,
        *,
        now: datetime,
        window_days: int,
        limit: int,
    ) -> dict[str, object]:
        ranking = self.repository.latest_work_ranking(
            generated_at=now,
            window_days=window_days,
            limit=limit,
            excluded_statuses=RANKING_EXCLUDED_STATUSES,
        )
        items = []
        for item in ranking.items:
            sources = [
                name
                for name, value in (
                    ("publication", item.publication_at),
                    ("version", item.version_at),
                    ("projection", item.projection_at),
                )
                if value == item.effective_at
            ]
            items.append(
                self.repository.paper_trend_item(
                    item.work,
                    score=item.effective_at.timestamp(),
                    details={
                        "effective_at": item.effective_at.isoformat(),
                        "effective_source": (
                            sources[0] if len(sources) == 1 else "multiple"
                        ),
                        "effective_sources": sources,
                        "publication_at": (
                            item.publication_at.isoformat()
                            if item.publication_at is not None
                            else None
                        ),
                        "version_at": (
                            item.version_at.isoformat()
                            if item.version_at is not None
                            else None
                        ),
                        "projection_at": (
                            item.projection_at.isoformat()
                            if item.projection_at is not None
                            else None
                        ),
                    },
                )
            )
        return {
            "ranking_name": "latest",
            "formula_version": LATEST_FORMULA_VERSION,
            "window_days": window_days,
            "generated_at": now,
            "coverage": {
                "total_eligible": ranking.total_in_scope,
                "subjects_with_signal": ranking.subjects_with_signal,
                "ranked_subjects": ranking.ranked_subjects,
                "returned_subjects": len(items),
                "signal_coverage": (
                    ranking.subjects_with_signal / ranking.total_in_scope
                    if ranking.total_in_scope
                    else 1.0
                ),
                "confidence": self._confidence_payload(
                    sample_size=ranking.ranked_subjects
                ),
            },
            "missing_signals": [],
            "explanation": (
                "Ordered by the maximum of publication time, version update "
                "time and exact projection update time within the requested "
                "duration, then canonical key ascending. No quality or "
                "popularity score is mixed in."
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
        ranking = self.repository.citation_velocity_ranking(
            generated_at=now,
            window_days=window_days,
            limit=limit,
            excluded_statuses=RANKING_EXCLUDED_STATUSES,
        )
        items = list(ranking.items)
        return {
            "ranking_name": "citation_velocity",
            "formula_version": CITATION_VELOCITY_FORMULA_VERSION,
            "window_days": window_days,
            "generated_at": now,
            "coverage": {
                "total_eligible": ranking.total_in_scope,
                "subjects_with_signal": ranking.ranked_subjects,
                "ranked_subjects": ranking.ranked_subjects,
                "returned_subjects": len(items),
                "signal_coverage": (
                    ranking.ranked_subjects / ranking.total_in_scope
                    if ranking.total_in_scope
                    else 1.0
                ),
                "confidence": self._confidence_payload(
                    sample_size=ranking.ranked_subjects
                ),
            },
            "missing_signals": list(ranking.missing_signals),
            "explanation": (
                "Score uses the last citation snapshot at or before the "
                "window start and the last snapshot at or before generation "
                "time, divided by their exact elapsed days. Percentile is "
                "the median empirical CDF across Topic, publication month "
                "and work type cohorts."
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
        ranking = self.repository.code_growth_ranking(
            generated_at=now,
            window_days=window_days,
            limit=limit,
            excluded_statuses=RANKING_EXCLUDED_STATUSES,
        )
        items = list(ranking.items)
        return {
            "ranking_name": "code_growth",
            "formula_version": CODE_GROWTH_FORMULA_VERSION,
            "window_days": window_days,
            "generated_at": now,
            "coverage": {
                "total_eligible": ranking.total_in_scope,
                "subjects_with_signal": ranking.ranked_subjects,
                "ranked_subjects": ranking.ranked_subjects,
                "returned_subjects": len(items),
                "signal_coverage": (
                    ranking.ranked_subjects / ranking.total_in_scope
                    if ranking.total_in_scope
                    else 1.0
                ),
                "confidence": self._confidence_payload(
                    sample_size=ranking.ranked_subjects
                ),
            },
            "missing_signals": list(ranking.missing_signals),
            "explanation": (
                "Score is the sum of per-repository star deltas per elapsed "
                "day. Each repository requires at least two snapshots; "
                "missing signals are never replaced by zero."
            ),
            "items": items,
        }

    @staticmethod
    def _confidence_payload(*, sample_size: int) -> dict[str, object]:
        confidence = sample_confidence(sample_size=sample_size)
        return {
            "level": confidence.level,
            "sample_size": confidence.sample_size,
            "explanation": confidence.explanation,
        }
