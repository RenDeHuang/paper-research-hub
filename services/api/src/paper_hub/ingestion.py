from __future__ import annotations

from dataclasses import asdict, dataclass, is_dataclass
from datetime import date, datetime
from decimal import Decimal
from typing import Any, Literal
from uuid import UUID

from sqlalchemy import select
from sqlalchemy.orm import Session

from paper_hub.connectors.base import (
    ConnectorRecord,
    ParsedWork,
    canonical_content_hash,
)
from paper_hub.connectors.openalex import OPENALEX_SOURCE_LICENSE
from paper_hub.models import (
    CodeRepository,
    ExternalIdentifier,
    FieldAssertion,
    MetricSnapshot,
    SourceRecord,
    Topic,
    Work,
)


IngestionStatus = Literal[
    "inserted",
    "updated",
    "unchanged",
    "excluded",
]


class IngestionError(RuntimeError):
    """Base error for a record that cannot be ingested safely."""


class IdentityConflictError(IngestionError):
    """Controlled external identifiers point to different canonical works."""


@dataclass(frozen=True)
class IngestionResult:
    status: IngestionStatus
    work_id: UUID | None = None
    source_record_id: UUID | None = None
    reason: str | None = None
    scope_rule_version: str | None = None
    scope_evidence: tuple[dict[str, str], ...] = ()


@dataclass
class IngestionSummary:
    inserted: int = 0
    updated: int = 0
    excluded: int = 0
    failed: int = 0
    unchanged: int = 0

    def add(self, result: IngestionResult) -> None:
        setattr(self, result.status, getattr(self, result.status) + 1)

    def as_dict(self) -> dict[str, int]:
        return {
            "inserted": self.inserted,
            "updated": self.updated,
            "excluded": self.excluded,
            "failed": self.failed,
            "unchanged": self.unchanged,
        }


class IngestionService:
    def __init__(self, session: Session) -> None:
        self.session = session

    def ingest(
        self,
        record: ConnectorRecord[ParsedWork],
    ) -> IngestionResult:
        if canonical_content_hash(record.raw_payload) != record.content_hash:
            raise IngestionError(
                "raw payload no longer matches its content hash"
            )
        parsed = record.parsed
        scope_evidence = tuple(
            {
                "field": evidence.field,
                "term": evidence.term,
                "matched_text": evidence.matched_text,
            }
            for evidence in parsed.scope.evidence
        )
        if not parsed.scope.included:
            return IngestionResult(
                status="excluded",
                reason=parsed.scope.reason,
                scope_rule_version=parsed.scope.rule_version,
                scope_evidence=scope_evidence,
            )

        existing_snapshot = self.session.scalar(
            select(SourceRecord).where(
                SourceRecord.source == record.source,
                SourceRecord.source_record_id == record.source_record_id,
                SourceRecord.content_hash == record.content_hash,
            )
        )
        if existing_snapshot is not None:
            return IngestionResult(
                status="unchanged",
                work_id=existing_snapshot.work_id,
                source_record_id=existing_snapshot.id,
                scope_rule_version=parsed.scope.rule_version,
                scope_evidence=scope_evidence,
            )

        work = self._resolve_work(parsed)
        is_new_work = work is None
        if work is None:
            work = Work(
                canonical_key=parsed.canonical_key,
                title=parsed.title,
                abstract=parsed.abstract,
                publication_date=parsed.publication_date,
                **self._provenance(record, parsed),
            )
            self.session.add(work)
            self.session.flush()
        else:
            self._update_work_projection(work, record, parsed)

        source_record = SourceRecord(
            work_id=work.id,
            paper_version_id=None,
            source_record_id=record.source_record_id,
            content_hash=record.content_hash,
            raw_payload=record.raw_payload,
            source_updated_at=record.source_updated_at,
            http_status=record.http_status,
            **self._provenance(record, parsed),
        )
        self.session.add(source_record)
        self.session.flush()

        self._persist_external_identifiers(work, record, parsed)
        self._persist_field_assertions(source_record, record, parsed)
        self._persist_topics(work, record, parsed)
        self._persist_code_repositories(work, record, parsed)
        self._persist_citation_metric(work, source_record, record, parsed)
        self.session.flush()

        return IngestionResult(
            status="inserted" if is_new_work else "updated",
            work_id=work.id,
            source_record_id=source_record.id,
            scope_rule_version=parsed.scope.rule_version,
            scope_evidence=scope_evidence,
        )

    def _resolve_work(self, parsed: ParsedWork) -> Work | None:
        matched_works: dict[UUID, Work] = {}
        for identifier in parsed.external_identifiers:
            existing_identifier = self.session.scalar(
                select(ExternalIdentifier).where(
                    ExternalIdentifier.scheme == identifier.scheme,
                    ExternalIdentifier.normalized_value
                    == identifier.normalized_value,
                )
            )
            if existing_identifier is not None:
                matched_works[existing_identifier.work_id] = (
                    existing_identifier.work
                )

        canonical_work = self.session.scalar(
            select(Work).where(Work.canonical_key == parsed.canonical_key)
        )
        if canonical_work is not None:
            matched_works[canonical_work.id] = canonical_work
        if len(matched_works) > 1:
            raise IdentityConflictError(
                "controlled external identifiers point to multiple works"
            )
        return next(iter(matched_works.values()), None)

    def _update_work_projection(
        self,
        work: Work,
        record: ConnectorRecord[ParsedWork],
        parsed: ParsedWork,
    ) -> None:
        work.title = parsed.title
        work.abstract = parsed.abstract
        work.publication_date = parsed.publication_date
        work.source = record.source
        work.source_url = _source_url(record)
        work.retrieved_at = record.retrieved_at
        work.source_license = OPENALEX_SOURCE_LICENSE
        work.content_license = parsed.license

    def _persist_external_identifiers(
        self,
        work: Work,
        record: ConnectorRecord[ParsedWork],
        parsed: ParsedWork,
    ) -> None:
        for identifier in parsed.external_identifiers:
            existing = self.session.scalar(
                select(ExternalIdentifier).where(
                    ExternalIdentifier.scheme == identifier.scheme,
                    ExternalIdentifier.normalized_value
                    == identifier.normalized_value,
                )
            )
            if existing is not None:
                if existing.work_id != work.id:
                    raise IdentityConflictError(
                        "external identifier is already owned by another work"
                    )
                continue
            self.session.add(
                ExternalIdentifier(
                    work_id=work.id,
                    scheme=identifier.scheme,
                    normalized_value=identifier.normalized_value,
                    raw_value=identifier.raw_value,
                    **self._provenance(record, parsed),
                )
            )

    def _persist_field_assertions(
        self,
        source_record: SourceRecord,
        record: ConnectorRecord[ParsedWork],
        parsed: ParsedWork,
    ) -> None:
        assertions: dict[str, Any] = {
            "title": parsed.title,
            "abstract": parsed.abstract,
            "external_identifiers": parsed.external_identifiers,
            "authors": parsed.authors,
            "institutions": parsed.institutions,
            "publication_date": parsed.publication_date,
            "type": parsed.work_type,
            "topics": parsed.topics,
            "open_access": parsed.open_access,
            "license": parsed.license,
            "primary_location": parsed.primary_location,
            "best_oa_location": parsed.best_oa_location,
            "citation_count": parsed.citation_count,
            "source_updated_at": record.source_updated_at,
            "code_repositories": parsed.code_repositories,
            "scope": parsed.scope,
        }
        for field_name, value in assertions.items():
            self.session.add(
                FieldAssertion(
                    source_record_id=source_record.id,
                    field_name=field_name,
                    value=_json_value(value),
                    parser_version=record.parser_version,
                    **self._provenance(record, parsed),
                )
            )

    def _persist_topics(
        self,
        work: Work,
        record: ConnectorRecord[ParsedWork],
        parsed: ParsedWork,
    ) -> None:
        existing_topic_ids = {topic.id for topic in work.topics}
        for parsed_topic in parsed.topics:
            normalized_name = _normalize_name(parsed_topic.display_name)
            topic = self.session.scalar(
                select(Topic).where(
                    Topic.normalized_name == normalized_name
                )
            )
            if topic is None:
                topic = Topic(
                    name=parsed_topic.display_name,
                    normalized_name=normalized_name,
                    description=None,
                    source=record.source,
                    source_url=(
                        f"https://openalex.org/{parsed_topic.openalex_id}"
                        if parsed_topic.openalex_id
                        else None
                    ),
                    retrieved_at=record.retrieved_at,
                    source_license=OPENALEX_SOURCE_LICENSE,
                    content_license=None,
                )
                self.session.add(topic)
                self.session.flush()
            if topic.id not in existing_topic_ids:
                work.topics.append(topic)
                existing_topic_ids.add(topic.id)

    def _persist_code_repositories(
        self,
        work: Work,
        record: ConnectorRecord[ParsedWork],
        parsed: ParsedWork,
    ) -> None:
        for parsed_repository in parsed.code_repositories:
            repository = self.session.scalar(
                select(CodeRepository).where(
                    CodeRepository.normalized_url
                    == parsed_repository.normalized_url
                )
            )
            if repository is not None:
                if repository.work_id != work.id:
                    continue
                continue
            self.session.add(
                CodeRepository(
                    work_id=work.id,
                    provider=parsed_repository.provider,
                    repository_name=parsed_repository.repository_name,
                    repository_url=parsed_repository.repository_url,
                    normalized_url=parsed_repository.normalized_url,
                    is_official=False,
                    **self._provenance(record, parsed),
                )
            )

    def _persist_citation_metric(
        self,
        work: Work,
        source_record: SourceRecord,
        record: ConnectorRecord[ParsedWork],
        parsed: ParsedWork,
    ) -> None:
        existing = self.session.scalar(
            select(MetricSnapshot).where(
                MetricSnapshot.work_id == work.id,
                MetricSnapshot.metric_name == "citation_count",
                MetricSnapshot.measured_at == record.source_updated_at,
                MetricSnapshot.source == record.source,
            )
        )
        if existing is not None:
            return
        self.session.add(
            MetricSnapshot(
                work_id=work.id,
                code_repository_id=None,
                metric_name="citation_count",
                metric_value=Decimal(parsed.citation_count),
                measured_at=record.source_updated_at,
                window_days=None,
                details={
                    "source_record_id": str(source_record.id),
                    "content_hash": record.content_hash,
                },
                **self._provenance(record, parsed),
            )
        )

    @staticmethod
    def _provenance(
        record: ConnectorRecord[ParsedWork],
        parsed: ParsedWork,
    ) -> dict[str, Any]:
        return {
            "source": record.source,
            "source_url": _source_url(record),
            "retrieved_at": record.retrieved_at,
            "source_license": OPENALEX_SOURCE_LICENSE,
            "content_license": parsed.license,
        }


def _source_url(record: ConnectorRecord[ParsedWork]) -> str:
    return f"https://openalex.org/{record.source_record_id}"


def _normalize_name(value: str) -> str:
    return " ".join(value.casefold().split())


def _json_value(value: Any) -> Any:
    if value is None or isinstance(value, (str, int, float, bool)):
        return value
    if isinstance(value, (date, datetime)):
        return value.isoformat()
    if isinstance(value, Decimal):
        return str(value)
    if isinstance(value, UUID):
        return str(value)
    if is_dataclass(value):
        return _json_value(asdict(value))
    if isinstance(value, dict):
        return {
            str(key): _json_value(nested_value)
            for key, nested_value in value.items()
        }
    if isinstance(value, (list, tuple)):
        return [_json_value(item) for item in value]
    raise TypeError(f"value of type {type(value).__name__} is not JSON-safe")
