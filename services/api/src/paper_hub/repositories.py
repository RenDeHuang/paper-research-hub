from __future__ import annotations

import base64
from collections import defaultdict
from collections.abc import Iterable, Sequence
from dataclasses import dataclass
from datetime import date, datetime, timedelta
import json
from math import ceil
from typing import Any
from uuid import UUID

from sqlalchemy import (
    Select,
    and_,
    cast,
    desc,
    distinct,
    exists,
    func,
    literal,
    or_,
    select,
)
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


@dataclass(frozen=True)
class MetricValue:
    measured_at: datetime
    value: Any


@dataclass(frozen=True)
class TaxonomyWindowItem:
    id: UUID
    name: str
    normalized_name: str
    current_count: int
    baseline_count: int


@dataclass(frozen=True)
class TaxonomyWindowCounts:
    items: tuple[TaxonomyWindowItem, ...]
    current_total: int
    baseline_total: int


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
    def __init__(self, session: Session) -> None:
        self.session = session

    def search(self, filters: PaperSearchFilters) -> dict[str, object]:
        predicates = self._search_predicates(filters)
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
            "facets": self._facets(predicates, total=total),
        }

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

    def rankable_works(
        self,
        *,
        excluded_statuses: set[RecordStatus],
        load_code: bool = False,
    ) -> list[Work]:
        options = []
        if load_code:
            options.append(selectinload(Work.code_repositories))
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
        if not work_ids:
            return {}
        rows = self.session.execute(
            select(
                MetricSnapshot.work_id,
                MetricSnapshot.measured_at,
                MetricSnapshot.metric_value,
            )
            .where(
                MetricSnapshot.work_id.in_(work_ids),
                MetricSnapshot.metric_name == metric_name,
                MetricSnapshot.measured_at >= start,
                MetricSnapshot.measured_at <= end,
            )
            .order_by(
                MetricSnapshot.work_id,
                MetricSnapshot.measured_at,
                MetricSnapshot.id,
            )
        ).all()
        grouped: dict[UUID, list[MetricValue]] = defaultdict(list)
        for row in rows:
            grouped[row.work_id].append(
                MetricValue(
                    measured_at=row.measured_at,
                    value=row.metric_value,
                )
            )
        return {key: tuple(value) for key, value in grouped.items()}

    def repository_metric_observations(
        self,
        *,
        repository_ids: Sequence[UUID],
        metric_name: str,
        start: datetime,
        end: datetime,
    ) -> dict[UUID, tuple[MetricValue, ...]]:
        if not repository_ids:
            return {}
        rows = self.session.execute(
            select(
                MetricSnapshot.code_repository_id,
                MetricSnapshot.measured_at,
                MetricSnapshot.metric_value,
            )
            .where(
                MetricSnapshot.code_repository_id.in_(repository_ids),
                MetricSnapshot.metric_name == metric_name,
                MetricSnapshot.measured_at >= start,
                MetricSnapshot.measured_at <= end,
            )
            .order_by(
                MetricSnapshot.code_repository_id,
                MetricSnapshot.measured_at,
                MetricSnapshot.id,
            )
        ).all()
        grouped: dict[UUID, list[MetricValue]] = defaultdict(list)
        for row in rows:
            grouped[row.code_repository_id].append(
                MetricValue(
                    measured_at=row.measured_at,
                    value=row.metric_value,
                )
            )
        return {key: tuple(value) for key, value in grouped.items()}

    def taxonomy_window_counts(
        self,
        *,
        subject: str,
        generated_at: datetime,
        window_days: int,
        excluded_statuses: set[RecordStatus],
    ) -> TaxonomyWindowCounts:
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
        current_total = int(
            self.session.scalar(
                select(func.count())
                .select_from(Work)
                .where(
                    public_work_predicate(),
                    Work.status.not_in(excluded_statuses),
                    current_condition,
                )
            )
            or 0
        )
        baseline_total = int(
            self.session.scalar(
                select(func.count())
                .select_from(Work)
                .where(
                    public_work_predicate(),
                    Work.status.not_in(excluded_statuses),
                    baseline_condition,
                )
            )
            or 0
        )
        rows = self.session.execute(
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
            )
            .select_from(model)
            .join(
                association,
                association.c[f"{subject}_id"] == model.id,
            )
            .join(Work, Work.id == association.c.work_id)
            .where(*eligible)
            .group_by(model.id, model.name, model.normalized_name)
            .order_by(model.normalized_name)
        ).all()
        return TaxonomyWindowCounts(
            items=tuple(
                TaxonomyWindowItem(
                    id=row.id,
                    name=row.name,
                    normalized_name=row.normalized_name,
                    current_count=int(row.current_count),
                    baseline_count=int(row.baseline_count),
                )
                for row in rows
            ),
            current_total=current_total,
            baseline_total=baseline_total,
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
    ) -> list[Any]:
        predicates: list[Any] = [public_work_predicate()]
        if filters.query and filters.query.strip():
            escaped = self._escape_like(filters.query.strip())
            pattern = f"%{escaped}%"
            predicates.append(
                or_(
                    Work.title.ilike(pattern, escape="\\"),
                    Work.canonical_key.ilike(pattern, escape="\\"),
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
        conditions = [
            source.work_id == Work.id,
            source.source == Work.projection_source,
            source.source_record_id == Work.projection_source_record_id,
            source.source_updated_at.is_not_distinct_from(
                Work.projection_source_updated_at
            ),
            assertion.source_record_id == source.id,
            assertion.field_name == field_name,
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
                    source.source == Work.projection_source,
                    source.source_record_id
                    == Work.projection_source_record_id,
                    source.source_updated_at.is_not_distinct_from(
                        Work.projection_source_updated_at
                    ),
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
                source.retrieved_at.desc(),
                source.id.desc(),
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
        )
        source_rows = self.session.execute(
            select(Work.source, func.count())
            .join(filtered, filtered.c.id == Work.id)
            .group_by(Work.source)
            .order_by(Work.source)
        ).all()
        status_rows = self.session.execute(
            select(Work.status, func.count())
            .join(filtered, filtered.c.id == Work.id)
            .group_by(Work.status)
            .order_by(Work.status)
        ).all()
        type_rows = self._type_facets(filtered)
        topic_rows = self._taxonomy_facets(
            filtered,
            subject="topic",
        )
        method_rows = self._taxonomy_facets(
            filtered,
            subject="method",
        )
        with_code = int(
            self.session.scalar(
                select(func.count(distinct(work_code_repository.c.work_id)))
                .select_from(work_code_repository)
                .join(
                    filtered,
                    filtered.c.id == work_code_repository.c.work_id,
                )
            )
            or 0
        )
        return {
            "types": [
                {"value": str(value), "count": int(count)}
                for value, count in sorted(type_rows)
            ],
            "topics": topic_rows,
            "methods": method_rows,
            "statuses": [
                {
                    "value": (
                        value.value
                        if isinstance(value, RecordStatus)
                        else str(value)
                    ),
                    "count": int(count),
                }
                for value, count in status_rows
            ],
            "sources": [
                {"value": value, "count": int(count)}
                for value, count in source_rows
            ],
            "has_code": [
                {"value": "true", "count": with_code},
                {"value": "false", "count": total - with_code},
            ],
        }

    def _type_facets(self, filtered: Select) -> list[tuple[str, int]]:
        source = aliased(SourceRecord)
        assertion = aliased(FieldAssertion)
        rows = self.session.execute(
            select(
                assertion.value,
                func.count(distinct(Work.id)),
            )
            .select_from(Work)
            .join(filtered, filtered.c.id == Work.id)
            .join(
                source,
                and_(
                    source.work_id == Work.id,
                    source.source == Work.projection_source,
                    source.source_record_id
                    == Work.projection_source_record_id,
                    source.source_updated_at.is_not_distinct_from(
                        Work.projection_source_updated_at
                    ),
                ),
            )
            .join(
                assertion,
                assertion.source_record_id == source.id,
            )
            .where(assertion.field_name == "type")
            .group_by(assertion.value)
        ).all()
        return [
            (str(value), int(count))
            for value, count in rows
            if isinstance(value, str)
        ]

    def _taxonomy_facets(
        self,
        filtered: Select,
        *,
        subject: str,
    ) -> list[dict[str, object]]:
        model, association = self._taxonomy(subject)
        rows = self.session.execute(
            select(
                model.normalized_name,
                model.name,
                func.count(distinct(association.c.work_id)),
            )
            .select_from(model)
            .join(
                association,
                association.c[f"{subject}_id"] == model.id,
            )
            .join(
                filtered,
                filtered.c.id == association.c.work_id,
            )
            .group_by(model.normalized_name, model.name)
            .order_by(model.normalized_name)
        ).all()
        return [
            {
                "value": normalized_name,
                "label": name,
                "count": int(count),
            }
            for normalized_name, name, count in rows
        ]

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

    @staticmethod
    def _current_scope_state(work: Work) -> dict[str, object] | None:
        assessments = list(work.scope_assessments)
        if not assessments:
            return None
        current = max(
            assessments,
            key=lambda item: (
                item.source_record.source_updated_at is not None,
                (
                    item.source_record.source_updated_at
                    or item.source_record.retrieved_at
                ),
                item.source_record.retrieved_at,
                item.evaluated_at,
                str(item.source_record.id),
                item.rule_version,
                str(item.id),
            ),
        )
        return {
            "included": current.included,
            "rule_version": current.rule_version,
            "reason": current.reason,
            "evidence": current.evidence,
            "evaluated_at": current.evaluated_at,
        }

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
