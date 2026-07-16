from __future__ import annotations

import base64
from collections import defaultdict
from collections.abc import Iterable, Sequence
from dataclasses import dataclass
from datetime import UTC, date, datetime, timedelta
from decimal import Decimal
import json
from math import ceil
from typing import Any
from uuid import UUID

from sqlalchemy import (
    DateTime,
    Numeric,
    Select,
    and_,
    case,
    cast,
    desc,
    distinct,
    exists,
    func,
    literal,
    or_,
    select,
    text,
    tuple_,
    union_all,
)
from sqlalchemy.dialects.postgresql import ARRAY, JSONB
from sqlalchemy.orm import Session, aliased, selectinload
from sqlalchemy.sql.sqltypes import Text

from paper_hub.models import (
    Benchmark,
    CodeRepository,
    Dataset,
    ExternalIdentifier,
    FieldAssertion,
    Method,
    MetricSnapshot,
    PaperSearchChange,
    PaperSearchSnapshotState,
    PaperVersion,
    RecordStatus,
    ScopeAssessment,
    SourceRecord,
    Topic,
    Work,
    work_benchmark,
    work_code_repository,
    work_dataset,
    work_method,
    work_topic,
)
from paper_hub.normalization import (
    normalize_arxiv_id,
    normalize_canonical_key,
    normalize_doi,
    normalize_openalex_id,
    normalize_openreview_forum_id,
    normalize_semantic_scholar_paper_id,
)


@dataclass(frozen=True)
class PaperSearchFilters:
    query: str | None = None
    work_types: tuple[str, ...] = ()
    topics: tuple[str, ...] = ()
    methods: tuple[str, ...] = ()
    has_code: bool | None = None
    date_from: date | None = None
    date_to: date | None = None
    statuses: tuple[RecordStatus, ...] = ()
    sources: tuple[str, ...] = ()
    page: int = 1
    page_size: int = 20
    sort: str = "published_desc"
    snapshot_at: datetime | None = None
    snapshot_revision: int | None = None


class SearchSnapshotInvalidatedError(RuntimeError):
    """A prior paper-list snapshot can no longer be read consistently."""


@dataclass(frozen=True)
class MetricValue:
    measured_at: datetime
    value: Any


@dataclass(frozen=True)
class TaxonomyRankingItem:
    id: UUID
    name: str
    normalized_name: str
    score: Decimal
    current_count: int
    baseline_count: int
    current_total: int
    baseline_total: int


@dataclass(frozen=True)
class BoundedTaxonomyRanking:
    items: tuple[TaxonomyRankingItem, ...]
    missing_signals: tuple[dict[str, str], ...]
    current_total: int
    baseline_total: int
    eligible_subjects: int
    ranked_subjects: int


@dataclass(frozen=True)
class LatestWorkItem:
    work: Work
    effective_at: datetime
    publication_at: datetime | None
    version_at: datetime | None
    projection_at: datetime | None


@dataclass(frozen=True)
class LatestWorkRanking:
    items: tuple[LatestWorkItem, ...]
    total_in_scope: int
    subjects_with_signal: int
    ranked_subjects: int


@dataclass(frozen=True)
class BoundedPaperRanking:
    items: tuple[dict[str, object], ...]
    missing_signals: tuple[dict[str, object], ...]
    total_in_scope: int
    ranked_subjects: int


def slug_for_canonical_key(canonical_key: str) -> str:
    encoded = base64.urlsafe_b64encode(
        canonical_key.encode("utf-8")
    ).decode("ascii")
    return f"paper-{encoded.rstrip('=')}"


def canonical_key_from_slug(slug: str) -> str | None:
    prefix = "paper-"
    if not slug.startswith(prefix):
        return None
    payload = slug[len(prefix) :]
    if not payload:
        return None
    try:
        padding = "=" * (-len(payload) % 4)
        decoded = base64.b64decode(
            payload + padding,
            altchars=b"-_",
            validate=True,
        ).decode("utf-8")
    except (ValueError, UnicodeDecodeError):
        return None
    if slug_for_canonical_key(decoded) != slug:
        return None
    return decoded


def public_work_predicate():
    scope = aliased(ScopeAssessment)
    source = aliased(SourceRecord)
    latest_scope = aliased(ScopeAssessment)
    latest_source = aliased(SourceRecord)
    latest_assessment_id = (
        select(latest_scope.id)
        .select_from(latest_scope)
        .join(
            latest_source,
            latest_source.id == latest_scope.source_record_id,
        )
        .where(
            latest_scope.work_id == Work.id,
            latest_source.source == source.source,
            latest_source.source_record_id == source.source_record_id,
        )
        .order_by(
            latest_source.source_updated_at.desc().nullslast(),
            latest_source.retrieved_at.desc(),
            latest_scope.evaluated_at.desc(),
            latest_source.id.desc(),
            latest_scope.rule_version.desc(),
            latest_scope.id.desc(),
        )
        .limit(1)
        .correlate(Work, source)
        .scalar_subquery()
    )
    return exists(
        select(literal(1))
        .select_from(scope)
        .join(source, source.id == scope.source_record_id)
        .where(
            scope.work_id == Work.id,
            scope.included.is_(True),
            scope.id == latest_assessment_id,
        )
        .correlate(Work)
    )


class PaperRepository:
    def __init__(
        self,
        session: Session,
        *,
        current_scope_rule_version: str = "agent-llm-v1",
    ) -> None:
        self.session = session
        self.current_scope_rule_version = current_scope_rule_version

    def search(self, filters: PaperSearchFilters) -> dict[str, object]:
        if (
            filters.snapshot_at is None
            and filters.snapshot_revision is None
        ):
            snapshot_at, snapshot_revision = (
                self._capture_search_snapshot()
            )
        elif (
            filters.snapshot_at is not None
            and filters.snapshot_revision is not None
        ):
            snapshot_at = filters.snapshot_at
            snapshot_revision = filters.snapshot_revision
            self._validate_search_snapshot(
                snapshot_at=snapshot_at,
                snapshot_revision=snapshot_revision,
            )
        else:
            raise ValueError(
                "snapshot_at and snapshot_revision must be provided together"
            )
        predicates = self._search_predicates(
            filters,
            snapshot_at=snapshot_at,
        )
        total = int(
            self.session.scalar(
                select(func.count())
                .select_from(Work)
                .where(*predicates)
            )
            or 0
        )
        statement = (
            select(Work)
            .where(*predicates)
            .options(
                selectinload(Work.topics),
                selectinload(Work.methods),
                selectinload(Work.code_repositories),
            )
            .order_by(*self._ordering(filters.sort))
            .offset((filters.page - 1) * filters.page_size)
            .limit(filters.page_size)
        )
        works = list(self.session.scalars(statement).all())
        work_ids = tuple(work.id for work in works)
        assertion_values = self._projection_assertion_map(
            work_ids,
            field_names=("authors", "institutions", "type"),
        )
        citations = self._latest_work_metric_values(
            work_ids,
            metric_name="citation_count",
        )
        return {
            "items": [
                self._paper_summary(
                    work,
                    assertions=assertion_values.get(work.id, {}),
                    citation_count=citations.get(work.id),
                )
                for work in works
            ],
            "total": total,
            "page": filters.page,
            "page_size": filters.page_size,
            "total_pages": ceil(total / filters.page_size) if total else 0,
            "snapshot_at": snapshot_at,
            "snapshot_revision": snapshot_revision,
            "facets": self._facets(predicates, total=total),
        }

    def _capture_search_snapshot(self) -> tuple[datetime, int]:
        row = self.session.execute(
            select(
                func.clock_timestamp().label("snapshot_at"),
                func.coalesce(
                    func.max(PaperSearchChange.revision),
                    0,
                ).label("snapshot_revision"),
            )
        ).one()
        return row.snapshot_at, int(row.snapshot_revision)

    def _validate_search_snapshot(
        self,
        *,
        snapshot_at: datetime,
        snapshot_revision: int,
    ) -> None:
        valid_from = self.session.scalar(
            select(PaperSearchSnapshotState.valid_from).where(
                PaperSearchSnapshotState.id.is_(True)
            )
        )
        if valid_from is None:
            raise RuntimeError("paper search snapshot state is missing")
        binding = self.session.execute(
            select(
                func.clock_timestamp().label("current_time"),
                func.coalesce(
                    func.max(PaperSearchChange.revision).filter(
                        PaperSearchChange.changed_at <= snapshot_at
                    ),
                    0,
                ).label("expected_revision"),
            )
        ).one()
        if (
            snapshot_at > binding.current_time
            or snapshot_revision != int(binding.expected_revision)
        ):
            raise SearchSnapshotInvalidatedError(
                "paper search snapshot identity is invalid"
            )
        changed = self.session.scalar(
            select(
                exists(
                    select(literal(1))
                    .select_from(PaperSearchChange)
                    .where(
                        PaperSearchChange.revision
                        > snapshot_revision,
                        PaperSearchChange.work_created_at
                        <= snapshot_at,
                    )
                )
            )
        )
        if snapshot_at < valid_from or bool(changed):
            raise SearchSnapshotInvalidatedError(
                "paper search snapshot was invalidated by a work update"
            )

    def detail(self, slug: str) -> dict[str, object] | None:
        canonical_key = canonical_key_from_slug(slug)
        if canonical_key is None:
            return None
        work = self.session.scalar(
            select(Work)
            .where(
                Work.canonical_key == canonical_key,
                public_work_predicate(),
            )
            .options(
                selectinload(Work.versions),
                selectinload(Work.external_identifiers),
                selectinload(Work.topics),
                selectinload(Work.methods),
                selectinload(Work.datasets),
                selectinload(Work.benchmarks),
                selectinload(Work.code_repositories).selectinload(
                    CodeRepository.metric_snapshots
                ),
                selectinload(Work.metric_snapshots),
                selectinload(Work.source_records),
                selectinload(Work.scope_assessments).selectinload(
                    ScopeAssessment.source_record
                ),
            )
        )
        if work is None:
            return None

        assertions = self._projection_assertion_map(
            (work.id,),
            field_names=("authors", "institutions", "type"),
        ).get(work.id, {})
        latest_citations = self._latest_work_metric_values(
            (work.id,),
            metric_name="citation_count",
        )
        summary = self._paper_summary(
            work,
            assertions=assertions,
            citation_count=latest_citations.get(work.id),
        )
        current_scope = self._current_scope_state(work)
        work_metrics = [
            self._metric_snapshot(snapshot, target="work")
            for snapshot in work.metric_snapshots
        ]
        repository_metrics = [
            self._metric_snapshot(
                snapshot,
                target=repository.normalized_url,
            )
            for repository in work.code_repositories
            for snapshot in repository.metric_snapshots
        ]
        return {
            **summary,
            "versions": [
                {
                    "id": version.id,
                    "version_label": version.version_label,
                    "version_number": version.version_number,
                    "version_type": version.version_type,
                    "title": version.title,
                    "abstract": version.abstract,
                    "submitted_at": version.submitted_at,
                    "published_at": version.published_at,
                    "version_url": version.version_url,
                    "status": version.status,
                    "source": version.source,
                }
                for version in sorted(
                    work.versions,
                    key=lambda item: (
                        item.version_number is None,
                        item.version_number or 0,
                        item.version_label,
                    ),
                )
            ],
            "identifiers": [
                {
                    "scheme": identifier.scheme,
                    "normalized_value": identifier.normalized_value,
                    "raw_value": identifier.raw_value,
                    "source": identifier.source,
                }
                for identifier in sorted(
                    work.external_identifiers,
                    key=lambda item: (
                        item.scheme,
                        item.normalized_value,
                    ),
                )
            ],
            "institutions": self._institutions(assertions),
            "datasets": [
                self._named_resource(item) for item in sorted(
                    work.datasets,
                    key=lambda value: value.normalized_name,
                )
            ],
            "benchmarks": [
                self._named_resource(item) for item in sorted(
                    work.benchmarks,
                    key=lambda value: value.normalized_name,
                )
            ],
            "code_repositories": [
                self._code_repository(repository)
                for repository in sorted(
                    work.code_repositories,
                    key=lambda item: item.normalized_url,
                )
            ],
            "provenance": self._provenance(work),
            "license": {
                "source": work.source_license,
                "content": work.content_license,
            },
            "source_records": [
                {
                    "id": source.id,
                    "source": source.source,
                    "source_record_id": source.source_record_id,
                    "content_hash": source.content_hash,
                    "source_url": source.source_url,
                    "source_updated_at": source.source_updated_at,
                    "retrieved_at": source.retrieved_at,
                    "http_status": source.http_status,
                    "source_license": source.source_license,
                    "content_license": source.content_license,
                    "status": source.status,
                }
                for source in sorted(
                    self._associated_source_records(work),
                    key=lambda item: (
                        item.source_updated_at or item.retrieved_at,
                        item.retrieved_at,
                        str(item.id),
                    ),
                    reverse=True,
                )
            ],
            "metric_snapshots": sorted(
                work_metrics + repository_metrics,
                key=lambda item: (
                    item["measured_at"],
                    item["metric_name"],
                    item["target"],
                ),
                reverse=True,
            ),
            "scope_state": current_scope,
        }

    def taxonomy_list(
        self,
        *,
        subject: str,
        query: str | None,
        limit: int,
    ) -> dict[str, object]:
        model, association = self._taxonomy(subject)
        conditions = [public_work_predicate()]
        if query:
            escaped = self._escape_like(query.strip())
            conditions.append(model.name.ilike(f"%{escaped}%", escape="\\"))
        rows = self.session.execute(
            select(
                model.id,
                model.name,
                model.normalized_name,
                model.description,
                func.count(distinct(Work.id)).label("paper_count"),
            )
            .select_from(model)
            .join(
                association,
                association.c[f"{subject}_id"] == model.id,
            )
            .join(Work, Work.id == association.c.work_id)
            .where(*conditions)
            .group_by(
                model.id,
                model.name,
                model.normalized_name,
                model.description,
            )
            .order_by(
                desc("paper_count"),
                model.normalized_name.asc(),
            )
            .limit(limit)
        ).all()
        return {
            "items": [
                {
                    "id": row.id,
                    "name": row.name,
                    "normalized_name": row.normalized_name,
                    "description": row.description,
                    "paper_count": int(row.paper_count),
                }
                for row in rows
            ]
        }

    def stats(self, *, generated_at: datetime) -> dict[str, object]:
        public_ids = (
            select(Work.id)
            .where(public_work_predicate())
            .cte("public_work_ids")
        )

        def association_count(association, column) -> int:
            return int(
                self.session.scalar(
                    select(func.count(distinct(column)))
                    .select_from(association)
                    .join(
                        public_ids,
                        public_ids.c.id == association.c.work_id,
                    )
                )
                or 0
            )

        status_rows = dict(
            self.session.execute(
                select(Work.status, func.count())
                .join(public_ids, public_ids.c.id == Work.id)
                .group_by(Work.status)
            ).all()
        )
        papers = sum(int(value) for value in status_rows.values())
        return {
            "papers": papers,
            "topics": association_count(work_topic, work_topic.c.topic_id),
            "methods": association_count(
                work_method,
                work_method.c.method_id,
            ),
            "datasets": association_count(
                work_dataset,
                work_dataset.c.dataset_id,
            ),
            "benchmarks": association_count(
                work_benchmark,
                work_benchmark.c.benchmark_id,
            ),
            "code_repositories": association_count(
                work_code_repository,
                work_code_repository.c.code_repository_id,
            ),
            "active": int(status_rows.get(RecordStatus.ACTIVE, 0)),
            "retracted": int(
                status_rows.get(RecordStatus.RETRACTED, 0)
            ),
            "withdrawn": int(
                status_rows.get(RecordStatus.WITHDRAWN, 0)
            ),
            "generated_at": generated_at,
        }

    def latest_work_ranking(
        self,
        *,
        generated_at: datetime,
        window_days: int,
        limit: int,
        excluded_statuses: set[RecordStatus],
    ) -> LatestWorkRanking:
        projection = aliased(SourceRecord)
        publication_at = cast(
            Work.publication_date,
            DateTime(timezone=True),
        )
        version_at = (
            select(
                func.max(
                    func.greatest(
                        PaperVersion.updated_at,
                        PaperVersion.submitted_at,
                        PaperVersion.published_at,
                    )
                )
            )
            .where(PaperVersion.work_id == Work.id)
            .correlate(Work)
            .scalar_subquery()
        )
        projection_at = func.greatest(
            projection.source_updated_at,
            projection.retrieved_at,
        )
        effective_at = func.greatest(
            publication_at,
            version_at,
            projection_at,
        )
        base = (
            select(
                Work.id.label("work_id"),
                effective_at.label("effective_at"),
            )
            .select_from(Work)
            .outerjoin(
                projection,
                projection.id == Work.projection_source_record_id,
            )
            .where(
                public_work_predicate(),
                Work.status.not_in(excluded_statuses),
            )
            .cte("latest_rankable_work")
        )
        cutoff = generated_at - timedelta(days=window_days)
        coverage = self.session.execute(
            select(
                func.count().label("total_in_scope"),
                func.count()
                .filter(base.c.effective_at.is_not(None))
                .label("subjects_with_signal"),
                func.count()
                .filter(
                    base.c.effective_at >= cutoff,
                    base.c.effective_at <= generated_at,
                )
                .label("ranked_subjects"),
            ).select_from(base)
        ).one()
        rows = self.session.execute(
            select(
                Work,
                publication_at.label("publication_at"),
                version_at.label("version_at"),
                projection_at.label("projection_at"),
                effective_at.label("effective_at"),
            )
            .select_from(Work)
            .outerjoin(
                projection,
                projection.id == Work.projection_source_record_id,
            )
            .where(
                public_work_predicate(),
                Work.status.not_in(excluded_statuses),
                effective_at >= cutoff,
                effective_at <= generated_at,
            )
            .order_by(
                effective_at.desc(),
                Work.canonical_key.asc(),
            )
            .limit(limit)
        ).all()
        return LatestWorkRanking(
            items=tuple(
                LatestWorkItem(
                    work=row[0],
                    publication_at=row.publication_at,
                    version_at=row.version_at,
                    projection_at=row.projection_at,
                    effective_at=row.effective_at,
                )
                for row in rows
            ),
            total_in_scope=int(coverage.total_in_scope),
            subjects_with_signal=int(coverage.subjects_with_signal),
            ranked_subjects=int(coverage.ranked_subjects),
        )

    def code_growth_ranking(
        self,
        *,
        generated_at: datetime,
        window_days: int,
        limit: int,
        excluded_statuses: set[RecordStatus],
    ) -> BoundedPaperRanking:
        rankable = (
            select(
                Work.id,
                Work.canonical_key,
                Work.title,
                Work.publication_date,
                Work.status,
            )
            .where(
                public_work_predicate(),
                Work.status.not_in(excluded_statuses),
            )
            .cte("code_growth_rankable_work")
        )
        repositories = (
            select(
                work_code_repository.c.work_id,
                work_code_repository.c.code_repository_id.label(
                    "repository_id"
                ),
                CodeRepository.normalized_url.label("repository_url"),
            )
            .join(
                rankable,
                rankable.c.id == work_code_repository.c.work_id,
            )
            .join(
                CodeRepository,
                CodeRepository.id
                == work_code_repository.c.code_repository_id,
            )
            .cte("code_growth_repository")
        )

        def boundary(at: datetime, name: str):
            ranked = (
                select(
                    MetricSnapshot.code_repository_id.label(
                        "repository_id"
                    ),
                    MetricSnapshot.measured_at,
                    MetricSnapshot.metric_value,
                    func.row_number()
                    .over(
                        partition_by=MetricSnapshot.code_repository_id,
                        order_by=(
                            MetricSnapshot.measured_at.desc(),
                            MetricSnapshot.id.desc(),
                        ),
                    )
                    .label("position"),
                )
                .join(
                    repositories,
                    repositories.c.repository_id
                    == MetricSnapshot.code_repository_id,
                )
                .where(
                    MetricSnapshot.metric_name == "stars",
                    MetricSnapshot.measured_at <= at,
                )
                .subquery()
            )
            return (
                select(
                    ranked.c.repository_id,
                    ranked.c.measured_at,
                    ranked.c.metric_value,
                )
                .where(ranked.c.position == 1)
                .cte(name)
            )

        baseline = boundary(
            generated_at - timedelta(days=window_days),
            "code_growth_baseline",
        )
        current = boundary(
            generated_at,
            "code_growth_current",
        )
        elapsed_days = (
            func.extract(
                "epoch",
                current.c.measured_at - baseline.c.measured_at,
            )
            / Decimal("86400")
        )
        repository_signal = (
            select(
                current.c.repository_id,
                (
                    (current.c.metric_value - baseline.c.metric_value)
                    / elapsed_days
                ).label("velocity"),
            )
            .join(
                baseline,
                baseline.c.repository_id == current.c.repository_id,
            )
            .where(elapsed_days > 0)
            .cte("code_growth_repository_signal")
        )
        work_signal = (
            select(
                repositories.c.work_id,
                func.count(distinct(repositories.c.repository_id)).label(
                    "repositories_total"
                ),
                func.count(
                    distinct(repository_signal.c.repository_id)
                ).label("repositories_with_signal"),
                func.coalesce(
                    func.sum(repository_signal.c.velocity),
                    0,
                ).label("score"),
            )
            .select_from(repositories)
            .outerjoin(
                repository_signal,
                repository_signal.c.repository_id
                == repositories.c.repository_id,
            )
            .group_by(repositories.c.work_id)
            .cte("code_growth_work_signal")
        )
        ranked = (
            select(
                rankable,
                work_signal.c.score,
                work_signal.c.repositories_total,
                work_signal.c.repositories_with_signal,
            )
            .join(
                work_signal,
                work_signal.c.work_id == rankable.c.id,
            )
            .where(work_signal.c.repositories_with_signal > 0)
            .cte("code_growth_ranked")
        )
        coverage = self.session.execute(
            select(
                select(func.count())
                .select_from(rankable)
                .scalar_subquery()
                .label("total_in_scope"),
                select(func.count())
                .select_from(ranked)
                .scalar_subquery()
                .label("ranked_subjects"),
            )
        ).one()
        ranked_limited = (
            select(ranked)
            .order_by(
                ranked.c.score.desc(),
                ranked.c.canonical_key.asc(),
            )
            .limit(limit)
            .cte("code_growth_ranked_limited")
        )
        rows = self.session.execute(
            select(ranked_limited).order_by(
                ranked_limited.c.score.desc(),
                ranked_limited.c.canonical_key.asc(),
            )
        ).all()
        no_repository_missing = (
            select(
                rankable.c.id,
                rankable.c.canonical_key,
                cast(literal(None), Work.id.type).label("repository_id"),
                cast(literal(None), Text).label("repository_url"),
                literal("code_repository").label("signal"),
                literal("no_code_repository").label("reason"),
            )
            .outerjoin(
                repositories,
                repositories.c.work_id == rankable.c.id,
            )
            .where(repositories.c.repository_id.is_(None))
        )
        repository_missing = (
            select(
                rankable.c.id,
                rankable.c.canonical_key,
                repositories.c.repository_id,
                repositories.c.repository_url,
                literal("stars").label("signal"),
                case(
                    (
                        work_signal.c.repositories_with_signal > 0,
                        "partial_repository_metric_coverage",
                    ),
                    else_="requires_at_least_two_distinct_snapshots",
                ).label("reason"),
            )
            .join(
                repositories,
                repositories.c.work_id == rankable.c.id,
            )
            .join(
                work_signal,
                work_signal.c.work_id == rankable.c.id,
            )
            .outerjoin(
                repository_signal,
                repository_signal.c.repository_id
                == repositories.c.repository_id,
            )
            .where(repository_signal.c.repository_id.is_(None))
        )
        missing = union_all(
            no_repository_missing,
            repository_missing,
        ).subquery()
        missing_subject_candidates = (
            select(
                missing.c.id,
                missing.c.canonical_key,
                case(
                    (ranked_limited.c.id.is_not(None), 0),
                    else_=1,
                ).label("priority"),
                ranked_limited.c.score.label("ranked_score"),
            )
            .select_from(
                missing.outerjoin(
                    ranked_limited,
                    ranked_limited.c.id == missing.c.id,
                )
            )
            .group_by(
                missing.c.id,
                missing.c.canonical_key,
                ranked_limited.c.id,
                ranked_limited.c.score,
            )
            .cte("code_growth_missing_subject_candidates")
        )
        missing_subjects = (
            select(
                missing_subject_candidates.c.id,
                missing_subject_candidates.c.canonical_key,
            )
            .order_by(
                missing_subject_candidates.c.priority.asc(),
                missing_subject_candidates.c.ranked_score.desc().nullslast(),
                missing_subject_candidates.c.canonical_key.asc(),
            )
            .limit(limit)
            .cte("code_growth_missing_subjects")
        )
        missing_rows = self.session.execute(
            select(missing)
            .join(
                missing_subjects,
                missing_subjects.c.id == missing.c.id,
            )
            .order_by(
                missing.c.canonical_key,
                missing.c.repository_url.asc().nullsfirst(),
            )
        ).all()

        items = tuple(
            {
                "slug": slug_for_canonical_key(row.canonical_key),
                "canonical_key": row.canonical_key,
                "title": row.title,
                "publication_date": row.publication_date,
                "status": row.status,
                "score": float(row.score),
                "percentile": None,
                "details": {
                    "repositories_with_signal": int(
                        row.repositories_with_signal
                    ),
                    "repositories_total": int(row.repositories_total),
                    "repositories_missing": int(
                        row.repositories_total
                        - row.repositories_with_signal
                    ),
                    "signal_status": (
                        "complete"
                        if row.repositories_total
                        == row.repositories_with_signal
                        else "partial"
                    ),
                    "metric": "stars",
                },
            }
            for row in rows
        )
        missing_signals = tuple(
            (
                {
                    "subject_id": str(row.id),
                    "canonical_key": row.canonical_key,
                    "slug": slug_for_canonical_key(
                        row.canonical_key
                    ),
                    "repository_id": row.repository_id,
                    "repository_url": row.repository_url,
                    "signal": row.signal,
                    "reason": row.reason,
                }
            )
            for row in missing_rows
        )
        return BoundedPaperRanking(
            items=items,
            missing_signals=missing_signals,
            total_in_scope=int(coverage.total_in_scope),
            ranked_subjects=int(coverage.ranked_subjects),
        )

    def citation_velocity_ranking(
        self,
        *,
        generated_at: datetime,
        window_days: int,
        limit: int,
        excluded_statuses: set[RecordStatus],
    ) -> BoundedPaperRanking:
        projection = aliased(SourceRecord)
        assertion = aliased(FieldAssertion)
        ranked_type = (
            select(
                Work.id.label("work_id"),
                assertion.value.label("raw_value"),
                assertion.value.op("#>>")(
                    cast(literal("{}"), ARRAY(Text))
                ).label("extracted_type"),
                func.row_number()
                .over(
                    partition_by=Work.id,
                    order_by=(
                        assertion.created_at.desc(),
                        assertion.parser_version.desc(),
                        assertion.id.desc(),
                    ),
                )
                .label("position"),
            )
            .join(
                projection,
                and_(
                    projection.work_id == Work.id,
                    projection.id == Work.projection_source_record_id,
                ),
            )
            .join(
                assertion,
                assertion.source_record_id == projection.id,
            )
            .where(assertion.field_name == "type")
            .subquery()
        )
        work_type = (
            select(
                ranked_type.c.work_id,
                case(
                    (
                        and_(
                            func.jsonb_typeof(
                                ranked_type.c.raw_value
                            )
                            == "string",
                            func.btrim(
                                ranked_type.c.extracted_type
                            )
                            != "",
                        ),
                        ranked_type.c.extracted_type,
                    ),
                    else_=None,
                ).label("work_type"),
            )
            .where(ranked_type.c.position == 1)
            .cte("citation_work_type")
        )

        def boundary(at: datetime, name: str):
            ranked = (
                select(
                    MetricSnapshot.work_id,
                    MetricSnapshot.measured_at,
                    MetricSnapshot.metric_value,
                    func.row_number()
                    .over(
                        partition_by=MetricSnapshot.work_id,
                        order_by=(
                            MetricSnapshot.measured_at.desc(),
                            MetricSnapshot.id.desc(),
                        ),
                    )
                    .label("position"),
                )
                .where(
                    MetricSnapshot.work_id.is_not(None),
                    MetricSnapshot.metric_name == "citation_count",
                    MetricSnapshot.measured_at <= at,
                )
                .subquery()
            )
            return (
                select(
                    ranked.c.work_id,
                    ranked.c.measured_at,
                    ranked.c.metric_value,
                )
                .where(ranked.c.position == 1)
                .cte(name)
            )

        baseline = boundary(
            generated_at - timedelta(days=window_days),
            "citation_baseline",
        )
        current = boundary(generated_at, "citation_current")
        elapsed_days = (
            func.extract(
                "epoch",
                current.c.measured_at - baseline.c.measured_at,
            )
            / Decimal("86400")
        )
        publication_month = func.to_char(
            Work.publication_date,
            "YYYY-MM",
        )
        velocity = (
            (current.c.metric_value - baseline.c.metric_value)
            / elapsed_days
        )
        velocity_work = (
            select(
                Work.id,
                Work.canonical_key,
                Work.title,
                Work.publication_date,
                Work.status,
                work_type.c.work_type,
                publication_month.label("publication_month"),
                velocity.label("velocity"),
            )
            .join(work_type, work_type.c.work_id == Work.id)
            .join(baseline, baseline.c.work_id == Work.id)
            .join(current, current.c.work_id == Work.id)
            .where(
                public_work_predicate(),
                Work.status.not_in(excluded_statuses),
                Work.publication_date.is_not(None),
                work_type.c.work_type.is_not(None),
                elapsed_days > 0,
            )
            .cte("citation_velocity_work")
        )
        topic_cohort = (
            select(
                velocity_work,
                Topic.normalized_name.label("topic"),
                func.cume_dist()
                .over(
                    partition_by=(
                        Topic.id,
                        velocity_work.c.publication_month,
                        velocity_work.c.work_type,
                    ),
                    order_by=velocity_work.c.velocity,
                )
                .label("topic_percentile"),
                func.count()
                .over(
                    partition_by=(
                        Topic.id,
                        velocity_work.c.publication_month,
                        velocity_work.c.work_type,
                    )
                )
                .label("cohort_size"),
            )
            .join(
                work_topic,
                work_topic.c.work_id == velocity_work.c.id,
            )
            .join(Topic, Topic.id == work_topic.c.topic_id)
            .cte("citation_topic_cohort")
        )
        ranked = (
            select(
                topic_cohort.c.id,
                topic_cohort.c.canonical_key,
                topic_cohort.c.title,
                topic_cohort.c.publication_date,
                topic_cohort.c.status,
                topic_cohort.c.work_type,
                topic_cohort.c.publication_month,
                func.max(topic_cohort.c.velocity).label("score"),
                func.percentile_cont(0.5)
                .within_group(topic_cohort.c.topic_percentile)
                .label("percentile"),
                func.jsonb_agg(
                    func.jsonb_build_object(
                        "topic",
                        topic_cohort.c.topic,
                        "cohort_size",
                        topic_cohort.c.cohort_size,
                        "percentile",
                        topic_cohort.c.topic_percentile,
                    )
                ).label("topic_cohorts"),
            )
            .group_by(
                topic_cohort.c.id,
                topic_cohort.c.canonical_key,
                topic_cohort.c.title,
                topic_cohort.c.publication_date,
                topic_cohort.c.status,
                topic_cohort.c.work_type,
                topic_cohort.c.publication_month,
            )
            .cte("citation_ranked")
        )
        total_in_scope = (
            select(func.count())
            .select_from(Work)
            .where(
                public_work_predicate(),
                Work.status.not_in(excluded_statuses),
            )
            .scalar_subquery()
        )
        coverage = self.session.execute(
            select(
                total_in_scope.label("total_in_scope"),
                select(func.count())
                .select_from(ranked)
                .scalar_subquery()
                .label("ranked_subjects"),
            )
        ).one()
        rows = self.session.execute(
            select(ranked)
            .order_by(
                ranked.c.score.desc(),
                ranked.c.canonical_key.asc(),
            )
            .limit(limit)
        ).all()

        topic_exists = exists(
            select(literal(1))
            .select_from(work_topic)
            .where(work_topic.c.work_id == Work.id)
        )
        missing_reason = case(
            (
                Work.publication_date.is_(None),
                "publication_month_is_required_for_cohort",
            ),
            (
                work_type.c.work_type.is_(None),
                "work_type_is_required_for_cohort",
            ),
            (
                ~topic_exists,
                "at_least_one_topic_is_required_for_cohort",
            ),
            else_="requires_at_least_two_distinct_snapshots",
        )
        missing_signal = case(
            (Work.publication_date.is_(None), "publication_date"),
            (work_type.c.work_type.is_(None), "work_type"),
            (~topic_exists, "topics"),
            else_="citation_count",
        )
        missing_rows = self.session.execute(
            select(
                Work.id,
                Work.canonical_key,
                missing_signal.label("signal"),
                missing_reason.label("reason"),
            )
            .outerjoin(work_type, work_type.c.work_id == Work.id)
            .outerjoin(baseline, baseline.c.work_id == Work.id)
            .outerjoin(current, current.c.work_id == Work.id)
            .where(
                public_work_predicate(),
                Work.status.not_in(excluded_statuses),
                or_(
                    Work.publication_date.is_(None),
                    work_type.c.work_type.is_(None),
                    ~topic_exists,
                    baseline.c.work_id.is_(None),
                    current.c.work_id.is_(None),
                    current.c.measured_at
                    <= baseline.c.measured_at,
                ),
            )
            .order_by(Work.canonical_key)
            .limit(limit)
        ).all()
        return BoundedPaperRanking(
            items=tuple(
                {
                    "slug": slug_for_canonical_key(row.canonical_key),
                    "canonical_key": row.canonical_key,
                    "title": row.title,
                    "publication_date": row.publication_date,
                    "status": row.status,
                    "score": float(row.score),
                    "percentile": float(row.percentile),
                    "details": {
                        "publication_month": row.publication_month,
                        "work_type": row.work_type,
                        "percentile_aggregation": (
                            "median_across_topics"
                        ),
                        "topic_cohorts": sorted(
                            row.topic_cohorts,
                            key=lambda item: item["topic"],
                        ),
                    },
                }
                for row in rows
            ),
            missing_signals=tuple(
                {
                    "subject_id": str(row.id),
                    "canonical_key": row.canonical_key,
                    "slug": slug_for_canonical_key(
                        row.canonical_key
                    ),
                    "signal": row.signal,
                    "reason": row.reason,
                }
                for row in missing_rows
            ),
            total_in_scope=int(coverage.total_in_scope),
            ranked_subjects=int(coverage.ranked_subjects),
        )

    def rankable_works(
        self,
        *,
        excluded_statuses: set[RecordStatus],
        load_code: bool = False,
        load_topics: bool = False,
    ) -> list[Work]:
        options = []
        if load_code:
            options.append(selectinload(Work.code_repositories))
        if load_topics:
            options.append(selectinload(Work.topics))
        statement = (
            select(Work)
            .where(
                public_work_predicate(),
                Work.status.not_in(excluded_statuses),
            )
            .order_by(Work.canonical_key)
        )
        if options:
            statement = statement.options(*options)
        return list(self.session.scalars(statement).all())

    def projection_assertion_values(
        self,
        work_ids: Sequence[UUID],
        *,
        field_name: str,
    ) -> dict[UUID, Any]:
        values = self._projection_assertion_map(
            work_ids,
            field_names=(field_name,),
        )
        return {
            work_id: fields[field_name]
            for work_id, fields in values.items()
            if field_name in fields
        }

    def work_metric_observations(
        self,
        *,
        work_ids: Sequence[UUID],
        metric_name: str,
        start: datetime,
        end: datetime,
    ) -> dict[UUID, tuple[MetricValue, ...]]:
        return self._boundary_metric_observations(
            target_column=MetricSnapshot.work_id,
            target_ids=work_ids,
            metric_name=metric_name,
            baseline_at=start,
            current_at=end,
        )

    def repository_metric_observations(
        self,
        *,
        repository_ids: Sequence[UUID],
        metric_name: str,
        start: datetime,
        end: datetime,
    ) -> dict[UUID, tuple[MetricValue, ...]]:
        return self._boundary_metric_observations(
            target_column=MetricSnapshot.code_repository_id,
            target_ids=repository_ids,
            metric_name=metric_name,
            baseline_at=start,
            current_at=end,
        )

    def _boundary_metric_observations(
        self,
        *,
        target_column,
        target_ids: Sequence[UUID],
        metric_name: str,
        baseline_at: datetime,
        current_at: datetime,
    ) -> dict[UUID, tuple[MetricValue, ...]]:
        if not target_ids:
            return {}

        def boundary(at: datetime):
            ranked = (
                select(
                    target_column.label("target_id"),
                    MetricSnapshot.measured_at,
                    MetricSnapshot.metric_value,
                    MetricSnapshot.id,
                    func.row_number()
                    .over(
                        partition_by=target_column,
                        order_by=(
                            MetricSnapshot.measured_at.desc(),
                            MetricSnapshot.id.desc(),
                        ),
                    )
                    .label("position"),
                )
                .where(
                    target_column.in_(target_ids),
                    MetricSnapshot.metric_name == metric_name,
                    MetricSnapshot.measured_at <= at,
                )
                .subquery()
            )
            return select(
                ranked.c.target_id,
                ranked.c.measured_at,
                ranked.c.metric_value,
                ranked.c.id,
            ).where(ranked.c.position == 1)

        combined = union_all(
            boundary(baseline_at),
            boundary(current_at),
        ).subquery()
        rows = self.session.execute(
            select(
                combined.c.target_id,
                combined.c.measured_at,
                combined.c.metric_value,
            )
            .distinct(
                combined.c.target_id,
                combined.c.measured_at,
            )
            .order_by(
                combined.c.target_id,
                combined.c.measured_at,
            )
        ).all()
        grouped: dict[UUID, list[MetricValue]] = defaultdict(list)
        for row in rows:
            grouped[row.target_id].append(
                MetricValue(
                    measured_at=row.measured_at,
                    value=row.metric_value,
                )
            )
        return {key: tuple(value) for key, value in grouped.items()}

    def taxonomy_window_ranking(
        self,
        *,
        subject: str,
        generated_at: datetime,
        window_days: int,
        limit: int,
        excluded_statuses: set[RecordStatus],
    ) -> BoundedTaxonomyRanking:
        model, association = self._taxonomy(subject)
        today = generated_at.date()
        current_start = (generated_at - timedelta(days=window_days)).date()
        baseline_start = (
            generated_at - timedelta(days=window_days * 2)
        ).date()
        eligible = (
            public_work_predicate(),
            Work.status.not_in(excluded_statuses),
            Work.publication_date.is_not(None),
            Work.publication_date > baseline_start,
            Work.publication_date <= today,
        )
        current_condition = and_(
            Work.publication_date > current_start,
            Work.publication_date <= today,
        )
        baseline_condition = and_(
            Work.publication_date > baseline_start,
            Work.publication_date <= current_start,
        )
        totals = self.session.execute(
            select(
                func.count()
                .filter(current_condition)
                .label("current_total"),
                func.count()
                .filter(baseline_condition)
                .label("baseline_total"),
            )
            .select_from(Work)
            .where(
                public_work_predicate(),
                Work.status.not_in(excluded_statuses),
                Work.publication_date.is_not(None),
                Work.publication_date > baseline_start,
                Work.publication_date <= today,
            )
        ).one()
        current_total = int(totals.current_total)
        baseline_total = int(totals.baseline_total)
        if subject == "method":
            counts = self._method_window_counts(
                eligible=eligible,
                excluded_statuses=excluded_statuses,
                current_start=current_start,
                baseline_start=baseline_start,
                today=today,
            )
            score = case(
                (
                    and_(
                        counts.c.current_total > 0,
                        counts.c.baseline_total > 0,
                    ),
                    cast(counts.c.current_count, Numeric)
                    / counts.c.current_total
                    - cast(counts.c.baseline_count, Numeric)
                    / counts.c.baseline_total,
                ),
                else_=None,
            )
        else:
            counts = (
                select(
                    model.id,
                    model.name,
                    model.normalized_name,
                    func.count(distinct(Work.id))
                    .filter(current_condition)
                    .label("current_count"),
                    func.count(distinct(Work.id))
                    .filter(baseline_condition)
                    .label("baseline_count"),
                    literal(current_total).label("current_total"),
                    literal(baseline_total).label("baseline_total"),
                )
                .select_from(model)
                .join(
                    association,
                    association.c[f"{subject}_id"] == model.id,
                )
                .join(Work, Work.id == association.c.work_id)
                .where(*eligible)
                .group_by(model.id, model.name, model.normalized_name)
                .cte("topic_window_counts")
            )
            score = (
                cast(
                    counts.c.current_count - counts.c.baseline_count,
                    Numeric,
                )
                / window_days
            )
        scored = (
            select(
                counts,
                score.label("score"),
            )
            .cte(f"{subject}_window_scored")
        )
        coverage = self.session.execute(
            select(
                func.count().label("eligible_subjects"),
                func.count()
                .filter(scored.c.score.is_not(None))
                .label("ranked_subjects"),
            ).select_from(scored)
        ).one()
        rows = self.session.execute(
            select(scored)
            .where(scored.c.score.is_not(None))
            .order_by(
                scored.c.score.desc(),
                scored.c.normalized_name.asc(),
            )
            .limit(limit)
        ).all()
        missing_rows = ()
        if subject == "method":
            missing_rows = self.session.execute(
                select(scored.c.normalized_name)
                .where(scored.c.score.is_(None))
                .order_by(scored.c.normalized_name)
                .limit(limit)
            ).all()
        return BoundedTaxonomyRanking(
            items=tuple(
                TaxonomyRankingItem(
                    id=row.id,
                    name=row.name,
                    normalized_name=row.normalized_name,
                    score=Decimal(row.score),
                    current_count=int(row.current_count),
                    baseline_count=int(row.baseline_count),
                    current_total=int(row.current_total),
                    baseline_total=int(row.baseline_total),
                )
                for row in rows
            ),
            missing_signals=tuple(
                {
                    "normalized_name": row.normalized_name,
                    "signal": "publication_window_denominator",
                    "reason": (
                        "current_and_baseline_windows_require_data"
                    ),
                }
                for row in missing_rows
            ),
            current_total=current_total,
            baseline_total=baseline_total,
            eligible_subjects=int(coverage.eligible_subjects),
            ranked_subjects=int(coverage.ranked_subjects),
        )

    def _method_window_counts(
        self,
        *,
        eligible: Sequence[Any],
        excluded_statuses: set[RecordStatus],
        current_start: date,
        baseline_start: date,
        today: date,
    ):
        public_method_work = (
            select(Work.id)
            .where(
                public_work_predicate(),
                Work.status.not_in(excluded_statuses),
            )
            .cte("public_method_work")
        )
        related_topics = (
            select(
                work_method.c.method_id,
                work_topic.c.topic_id,
            )
            .select_from(work_method)
            .join(
                public_method_work,
                public_method_work.c.id == work_method.c.work_id,
            )
            .join(
                work_topic,
                work_topic.c.work_id == work_method.c.work_id,
            )
            .distinct()
            .cte("method_related_topics")
        )
        eligible_work = (
            select(
                Work.id,
                Work.publication_date,
            )
            .where(*eligible)
            .cte("method_window_work")
        )
        numerators = (
            select(
                work_method.c.method_id,
                func.count(distinct(eligible_work.c.id))
                .filter(
                    eligible_work.c.publication_date
                    > current_start,
                    eligible_work.c.publication_date <= today,
                )
                .label("current_count"),
                func.count(distinct(eligible_work.c.id))
                .filter(
                    and_(
                        eligible_work.c.publication_date
                        > baseline_start,
                        eligible_work.c.publication_date
                        <= current_start,
                    )
                )
                .label("baseline_count"),
            )
            .select_from(work_method)
            .join(
                eligible_work,
                eligible_work.c.id == work_method.c.work_id,
            )
            .join(
                related_topics,
                related_topics.c.method_id == work_method.c.method_id,
            )
            .join(
                work_topic,
                and_(
                    work_topic.c.work_id == eligible_work.c.id,
                    work_topic.c.topic_id
                    == related_topics.c.topic_id,
                ),
            )
            .group_by(work_method.c.method_id)
            .cte("method_numerators")
        )
        denominators = (
            select(
                related_topics.c.method_id,
                func.count(distinct(eligible_work.c.id))
                .filter(
                    eligible_work.c.publication_date
                    > current_start,
                    eligible_work.c.publication_date <= today,
                )
                .label("current_total"),
                func.count(distinct(eligible_work.c.id))
                .filter(
                    and_(
                        eligible_work.c.publication_date
                        > baseline_start,
                        eligible_work.c.publication_date
                        <= current_start,
                    )
                )
                .label("baseline_total"),
            )
            .select_from(related_topics)
            .join(
                work_topic,
                work_topic.c.topic_id == related_topics.c.topic_id,
            )
            .join(
                eligible_work,
                eligible_work.c.id == work_topic.c.work_id,
            )
            .group_by(related_topics.c.method_id)
            .cte("method_denominators")
        )
        return (
            select(
                Method.id,
                Method.name,
                Method.normalized_name,
                func.coalesce(numerators.c.current_count, 0).label(
                    "current_count"
                ),
                func.coalesce(numerators.c.baseline_count, 0).label(
                    "baseline_count"
                ),
                func.coalesce(denominators.c.current_total, 0).label(
                    "current_total"
                ),
                func.coalesce(denominators.c.baseline_total, 0).label(
                    "baseline_total"
                ),
            )
            .join(
                denominators,
                denominators.c.method_id == Method.id,
            )
            .outerjoin(
                numerators,
                numerators.c.method_id == Method.id,
            )
            .cte("method_window_counts")
        )

    def paper_trend_item(
        self,
        work: Work,
        *,
        score: float,
        percentile: float | None = None,
        details: dict[str, Any] | None = None,
    ) -> dict[str, object]:
        return {
            "slug": slug_for_canonical_key(work.canonical_key),
            "canonical_key": work.canonical_key,
            "title": work.title,
            "publication_date": work.publication_date,
            "status": work.status,
            "score": score,
            "percentile": percentile,
            "details": details or {},
        }

    def paper_missing_signal(
        self,
        work: Work,
        *,
        signal: str,
        reason: str,
    ) -> dict[str, object]:
        return {
            "subject_id": str(work.id),
            "canonical_key": work.canonical_key,
            "slug": slug_for_canonical_key(work.canonical_key),
            "signal": signal,
            "reason": reason,
        }

    def _search_predicates(
        self,
        filters: PaperSearchFilters,
        *,
        snapshot_at: datetime,
    ) -> list[Any]:
        predicates: list[Any] = [
            public_work_predicate(),
            Work.created_at <= snapshot_at,
        ]
        if filters.query and filters.query.strip():
            query = filters.query.strip()
            identifiers = self._exact_identifier_candidates(query)
            if identifiers:
                canonical_keys = tuple(
                    f"{scheme}:{value}"
                    for scheme, value in identifiers
                )
                candidate_work = aliased(Work)
                candidate_work_ids = union_all(
                    select(
                        candidate_work.id.label("work_id")
                    ).where(
                        candidate_work.canonical_key.in_(canonical_keys)
                    ),
                    select(
                        ExternalIdentifier.work_id.label("work_id")
                    ).where(
                        tuple_(
                            ExternalIdentifier.scheme,
                            ExternalIdentifier.normalized_value,
                        ).in_(identifiers)
                    ),
                ).subquery()
                predicates.append(
                    Work.id.in_(
                        select(candidate_work_ids.c.work_id)
                    )
                )
            else:
                escaped = self._escape_like(query)
                pattern = f"%{escaped}%"
                predicates.append(
                    or_(
                        Work.title.ilike(pattern, escape="\\"),
                        Work.canonical_key.ilike(
                            pattern,
                            escape="\\",
                        ),
                        exists(
                            select(literal(1))
                            .select_from(ExternalIdentifier)
                            .where(
                                ExternalIdentifier.work_id == Work.id,
                                or_(
                                    ExternalIdentifier.normalized_value.ilike(
                                        pattern,
                                        escape="\\",
                                    ),
                                    ExternalIdentifier.raw_value.ilike(
                                        pattern,
                                        escape="\\",
                                    ),
                                ),
                            )
                        ),
                        self._projection_assertion_exists(
                            field_name="authors",
                            text_pattern=pattern,
                        ),
                    )
                )
        if filters.work_types:
            predicates.append(
                self._projection_assertion_exists(
                    field_name="type",
                    json_values=filters.work_types,
                )
            )
        if filters.topics:
            candidates = self._taxonomy_filter_values(filters.topics)
            predicates.append(
                exists(
                    select(literal(1))
                    .select_from(work_topic.join(Topic))
                    .where(
                        work_topic.c.work_id == Work.id,
                        Topic.normalized_name.in_(candidates),
                    )
                )
            )
        if filters.methods:
            candidates = self._taxonomy_filter_values(filters.methods)
            predicates.append(
                exists(
                    select(literal(1))
                    .select_from(work_method.join(Method))
                    .where(
                        work_method.c.work_id == Work.id,
                        Method.normalized_name.in_(candidates),
                    )
                )
            )
        if filters.has_code is not None:
            code_exists = exists(
                select(literal(1))
                .select_from(work_code_repository)
                .where(work_code_repository.c.work_id == Work.id)
            )
            predicates.append(
                code_exists if filters.has_code else ~code_exists
            )
        if filters.date_from is not None:
            predicates.append(
                Work.publication_date >= filters.date_from
            )
        if filters.date_to is not None:
            predicates.append(
                Work.publication_date <= filters.date_to
            )
        if filters.statuses:
            predicates.append(Work.status.in_(filters.statuses))
        if filters.sources:
            predicates.append(Work.source.in_(filters.sources))
        return predicates

    @staticmethod
    def _ordering(sort: str) -> tuple[Any, ...]:
        if sort == "published_asc":
            return (
                Work.publication_date.asc().nullslast(),
                Work.canonical_key.asc(),
            )
        if sort == "title_asc":
            return (
                func.lower(Work.title).asc(),
                Work.canonical_key.asc(),
            )
        if sort == "title_desc":
            return (
                func.lower(Work.title).desc(),
                Work.canonical_key.asc(),
            )
        return (
            Work.publication_date.desc().nullslast(),
            Work.canonical_key.asc(),
        )

    def _projection_assertion_exists(
        self,
        *,
        field_name: str,
        text_pattern: str | None = None,
        json_values: Sequence[str] = (),
    ):
        source = aliased(SourceRecord)
        assertion = aliased(FieldAssertion)
        current_assertion_id = self._current_assertion_id(
            source,
            field_name=field_name,
        )
        conditions = [
            source.work_id == Work.id,
            source.id == Work.projection_source_record_id,
            assertion.source_record_id == source.id,
            assertion.field_name == field_name,
            assertion.id == current_assertion_id,
        ]
        if text_pattern is not None:
            conditions.append(
                cast(assertion.value, Text).ilike(
                    text_pattern,
                    escape="\\",
                )
            )
        if json_values:
            conditions.append(
                cast(assertion.value, Text).in_(
                    tuple(json.dumps(value) for value in json_values)
                )
            )
        return exists(
            select(literal(1))
            .select_from(source)
            .join(assertion, assertion.source_record_id == source.id)
            .where(*conditions)
        )

    @staticmethod
    def _current_assertion_id(
        source,
        *,
        field_name: str,
    ):
        current = aliased(FieldAssertion)
        return (
            select(current.id)
            .where(
                current.source_record_id == source.id,
                current.field_name == field_name,
            )
            .order_by(
                current.created_at.desc(),
                current.parser_version.desc(),
                current.id.desc(),
            )
            .limit(1)
            .correlate(source)
            .scalar_subquery()
        )

    def _projection_assertion_map(
        self,
        work_ids: Sequence[UUID],
        *,
        field_names: Sequence[str],
    ) -> dict[UUID, dict[str, Any]]:
        if not work_ids:
            return {}
        source = aliased(SourceRecord)
        assertion = aliased(FieldAssertion)
        rows = self.session.execute(
            select(
                Work.id.label("work_id"),
                assertion.field_name,
                assertion.value,
            )
            .join(
                source,
                and_(
                    source.work_id == Work.id,
                    source.id == Work.projection_source_record_id,
                ),
            )
            .join(
                assertion,
                assertion.source_record_id == source.id,
            )
            .where(
                Work.id.in_(work_ids),
                assertion.field_name.in_(field_names),
            )
            .distinct(Work.id, assertion.field_name)
            .order_by(
                Work.id,
                assertion.field_name,
                assertion.created_at.desc(),
                assertion.parser_version.desc(),
                assertion.id.desc(),
            )
        ).all()
        result: dict[UUID, dict[str, Any]] = defaultdict(dict)
        for row in rows:
            result[row.work_id][row.field_name] = row.value
        return dict(result)

    def _latest_work_metric_values(
        self,
        work_ids: Sequence[UUID],
        *,
        metric_name: str,
    ) -> dict[UUID, float]:
        if not work_ids:
            return {}
        rows = self.session.execute(
            select(
                MetricSnapshot.work_id,
                MetricSnapshot.metric_value,
            )
            .where(
                MetricSnapshot.work_id.in_(work_ids),
                MetricSnapshot.metric_name == metric_name,
            )
            .distinct(MetricSnapshot.work_id)
            .order_by(
                MetricSnapshot.work_id,
                MetricSnapshot.measured_at.desc(),
                MetricSnapshot.id.desc(),
            )
        ).all()
        return {
            row.work_id: float(row.metric_value)
            for row in rows
        }

    def _facets(
        self,
        predicates: Sequence[Any],
        *,
        total: int,
    ) -> dict[str, list[dict[str, object]]]:
        filtered = (
            select(Work.id)
            .where(*predicates)
            .cte("filtered_work_ids")
            .prefix_with("MATERIALIZED")
        )
        source_counts = (
            select(
                Work.source.label("value"),
                func.count().label("count"),
            )
            .join(filtered, filtered.c.id == Work.id)
            .group_by(Work.source)
        )
        status_counts = (
            select(
                Work.status.label("value"),
                func.count().label("count"),
            )
            .join(filtered, filtered.c.id == Work.id)
            .group_by(Work.status)
        )
        source = aliased(SourceRecord)
        assertion = aliased(FieldAssertion)
        current_type_assertion_id = self._current_assertion_id(
            source,
            field_name="type",
        )
        type_counts = (
            select(
                assertion.value.op("#>>")(
                    cast(literal("{}"), ARRAY(Text))
                ).label("value"),
                func.count(distinct(Work.id)).label("count"),
            )
            .select_from(Work)
            .join(filtered, filtered.c.id == Work.id)
            .join(
                source,
                and_(
                    source.work_id == Work.id,
                    source.id == Work.projection_source_record_id,
                ),
            )
            .join(
                assertion,
                assertion.source_record_id == source.id,
            )
            .where(
                assertion.field_name == "type",
                assertion.id == current_type_assertion_id,
                func.jsonb_typeof(assertion.value) == "string",
                func.btrim(
                    assertion.value.op("#>>")(
                        cast(literal("{}"), ARRAY(Text))
                    )
                )
                != "",
            )
            .group_by(assertion.value)
        )
        topic_counts = (
            select(
                Topic.normalized_name.label("value"),
                Topic.name.label("label"),
                func.count(distinct(work_topic.c.work_id)).label("count"),
            )
            .select_from(Topic)
            .join(
                work_topic,
                work_topic.c.topic_id == Topic.id,
            )
            .join(
                filtered,
                filtered.c.id == work_topic.c.work_id,
            )
            .group_by(Topic.normalized_name, Topic.name)
        )
        method_counts = (
            select(
                Method.normalized_name.label("value"),
                Method.name.label("label"),
                func.count(distinct(work_method.c.work_id)).label("count"),
            )
            .select_from(Method)
            .join(
                work_method,
                work_method.c.method_id == Method.id,
            )
            .join(
                filtered,
                filtered.c.id == work_method.c.work_id,
            )
            .group_by(Method.normalized_name, Method.name)
        )

        empty_json = cast(literal("[]"), JSONB)

        def aggregate_counts(statement: Select):
            rows = statement.subquery()
            return (
                select(
                    func.coalesce(
                        func.jsonb_agg(
                            func.jsonb_build_object(
                                "value",
                                rows.c.value,
                                "count",
                                rows.c.count,
                            )
                        ),
                        empty_json,
                    )
                )
                .select_from(rows)
                .scalar_subquery()
            )

        def aggregate_labeled_counts(statement: Select):
            rows = statement.subquery()
            return (
                select(
                    func.coalesce(
                        func.jsonb_agg(
                            func.jsonb_build_object(
                                "value",
                                rows.c.value,
                                "label",
                                rows.c.label,
                                "count",
                                rows.c.count,
                            )
                        ),
                        empty_json,
                    )
                )
                .select_from(rows)
                .scalar_subquery()
            )

        with_code = (
            select(func.count(distinct(work_code_repository.c.work_id)))
            .select_from(work_code_repository)
            .join(
                filtered,
                filtered.c.id == work_code_repository.c.work_id,
            )
            .scalar_subquery()
        )
        row = self.session.execute(
            select(
                aggregate_counts(source_counts).label("sources"),
                aggregate_counts(status_counts).label("statuses"),
                aggregate_counts(type_counts).label("types"),
                aggregate_labeled_counts(topic_counts).label("topics"),
                aggregate_labeled_counts(method_counts).label("methods"),
                with_code.label("with_code"),
            )
        ).one()

        def sorted_counts(
            values: Sequence[dict[str, object]],
        ) -> list[dict[str, object]]:
            return sorted(
                (
                    {
                        **value,
                        "count": int(value["count"]),
                    }
                    for value in values
                ),
                key=lambda value: str(value["value"]),
            )

        with_code_count = int(row.with_code or 0)
        return {
            "types": sorted_counts(row.types),
            "topics": sorted_counts(row.topics),
            "methods": sorted_counts(row.methods),
            "statuses": sorted_counts(row.statuses),
            "sources": sorted_counts(row.sources),
            "has_code": [
                {"value": "true", "count": with_code_count},
                {"value": "false", "count": total - with_code_count},
            ],
        }

    def _paper_summary(
        self,
        work: Work,
        *,
        assertions: dict[str, Any],
        citation_count: float | None,
    ) -> dict[str, object]:
        authors = assertions.get("authors")
        if not isinstance(authors, list):
            authors = []
        work_type = assertions.get("type")
        return {
            "slug": slug_for_canonical_key(work.canonical_key),
            "canonical_key": work.canonical_key,
            "title": work.title,
            "abstract": work.abstract,
            "publication_date": work.publication_date,
            "type": work_type if isinstance(work_type, str) else None,
            "authors": authors,
            "topics": [
                self._taxonomy_tag(topic)
                for topic in sorted(
                    work.topics,
                    key=lambda item: item.normalized_name,
                )
            ],
            "methods": [
                self._taxonomy_tag(method)
                for method in sorted(
                    work.methods,
                    key=lambda item: item.normalized_name,
                )
            ],
            "has_code": bool(work.code_repositories),
            "status": work.status,
            "source": work.source,
            "citation_count": citation_count,
            "updated_at": work.updated_at,
        }

    @staticmethod
    def _taxonomy_tag(item: Topic | Method) -> dict[str, object]:
        return {
            "id": item.id,
            "name": item.name,
            "normalized_name": item.normalized_name,
        }

    @staticmethod
    def _named_resource(
        item: Dataset | Benchmark,
    ) -> dict[str, object]:
        return {
            "id": item.id,
            "name": item.name,
            "normalized_name": item.normalized_name,
            "description": item.description,
            "homepage_url": item.homepage_url,
            "source": item.source,
        }

    @staticmethod
    def _code_repository(
        repository: CodeRepository,
    ) -> dict[str, object]:
        return {
            "id": repository.id,
            "provider": repository.provider,
            "repository_name": repository.repository_name,
            "repository_url": repository.repository_url,
            "normalized_url": repository.normalized_url,
            "is_official": repository.is_official,
            "source": repository.source,
            "retrieved_at": repository.retrieved_at,
        }

    @staticmethod
    def _provenance(work: Work) -> dict[str, object]:
        return {
            "source": work.source,
            "source_url": work.source_url,
            "retrieved_at": work.retrieved_at,
            "source_license": work.source_license,
            "content_license": work.content_license,
            "projection_source": work.projection_source,
            "projection_source_record_id": (
                work.projection_source_record_id
            ),
            "projection_source_updated_at": (
                work.projection_source_updated_at
            ),
            "updated_at": work.updated_at,
        }

    @staticmethod
    def _metric_snapshot(
        snapshot: MetricSnapshot,
        *,
        target: str,
    ) -> dict[str, object]:
        return {
            "id": snapshot.id,
            "target": target,
            "metric_name": snapshot.metric_name,
            "metric_value": float(snapshot.metric_value),
            "measured_at": snapshot.measured_at,
            "window_days": snapshot.window_days,
            "details": snapshot.details,
            "source": snapshot.source,
        }

    @staticmethod
    def _institutions(
        assertions: dict[str, Any],
    ) -> list[dict[str, object]]:
        institutions = assertions.get("institutions")
        return institutions if isinstance(institutions, list) else []

    def _current_scope_state(
        self,
        work: Work,
    ) -> dict[str, object] | None:
        assessments = list(work.scope_assessments)
        if not assessments:
            return None
        current_by_logical_source: dict[
            tuple[str, str],
            ScopeAssessment,
        ] = {}
        for assessment in assessments:
            source = assessment.source_record
            identity = (source.source, source.source_record_id)
            current = current_by_logical_source.get(identity)
            if (
                current is None
                or PaperRepository._scope_assessment_order_key(assessment)
                > PaperRepository._scope_assessment_order_key(current)
            ):
                current_by_logical_source[identity] = assessment

        current_decisions = tuple(current_by_logical_source.values())
        evaluated_at = max(
            item.evaluated_at for item in current_decisions
        )
        included = any(item.included for item in current_decisions)
        return {
            "included": included,
            "rule_version": self.current_scope_rule_version,
            "reason": None if included else "all_logical_sources_excluded",
            "evidence": [
                {
                    "source": item.source_record.source,
                    "source_record_id": (
                        item.source_record.source_record_id
                    ),
                    "rule_version": item.rule_version,
                    "included": item.included,
                    "reason": item.reason,
                    "evaluated_at": (
                        item.evaluated_at.isoformat().replace(
                            "+00:00",
                            "Z",
                        )
                    ),
                }
                for item in sorted(
                    current_decisions,
                    key=lambda decision: (
                        decision.source_record.source,
                        decision.source_record.source_record_id,
                    ),
                )
            ],
            "evaluated_at": evaluated_at,
        }

    @staticmethod
    def _scope_assessment_order_key(
        assessment: ScopeAssessment,
    ) -> tuple[object, ...]:
        source = assessment.source_record
        return (
            source.source_updated_at is not None,
            source.source_updated_at or datetime.min.replace(tzinfo=UTC),
            source.retrieved_at,
            assessment.evaluated_at,
            str(source.id),
            assessment.rule_version,
            str(assessment.id),
        )

    @staticmethod
    def _associated_source_records(work: Work) -> list[SourceRecord]:
        by_id = {source.id: source for source in work.source_records}
        for assessment in work.scope_assessments:
            by_id[assessment.source_record.id] = assessment.source_record
        return list(by_id.values())

    @staticmethod
    def _taxonomy(subject: str):
        if subject == "topic":
            return Topic, work_topic
        if subject == "method":
            return Method, work_method
        raise ValueError(f"unsupported taxonomy subject: {subject}")

    @staticmethod
    def _escape_like(value: str) -> str:
        return (
            value.replace("\\", "\\\\")
            .replace("%", "\\%")
            .replace("_", "\\_")
        )

    @staticmethod
    def _taxonomy_filter_values(
        values: Iterable[str],
    ) -> tuple[str, ...]:
        normalized: set[str] = set()
        for value in values:
            collapsed = " ".join(value.casefold().split())
            normalized.add(collapsed)
            normalized.add(collapsed.replace("-", " "))
            normalized.add(collapsed.replace(" ", "-"))
        return tuple(sorted(normalized))

    @staticmethod
    def _exact_identifier_candidates(
        value: str,
    ) -> tuple[tuple[str, str], ...]:
        canonical = normalize_canonical_key(value)
        if canonical is not None:
            scheme, normalized = canonical.split(":", maxsplit=1)
            return ((scheme, normalized),)

        candidates: list[tuple[str, str]] = []
        for scheme, normalizer in (
            ("doi", normalize_doi),
            ("arxiv", normalize_arxiv_id),
            ("openalex", normalize_openalex_id),
            ("s2", normalize_semantic_scholar_paper_id),
        ):
            normalized = normalizer(value)
            if normalized is not None:
                candidates.append((scheme, normalized))
        if value.casefold().startswith("openreview:"):
            normalized = normalize_openreview_forum_id(
                value.split(":", maxsplit=1)[1]
            )
            if normalized is not None:
                candidates.append(("openreview", normalized))
        return tuple(candidates)
