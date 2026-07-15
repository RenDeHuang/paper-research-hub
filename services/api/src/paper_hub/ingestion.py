from __future__ import annotations

from dataclasses import asdict, dataclass, is_dataclass
from datetime import UTC, date, datetime
from decimal import Decimal
from typing import Any, Literal
from uuid import UUID

from sqlalchemy import select, text
from sqlalchemy.dialects.postgresql import insert as pg_insert
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
    RecordStatus,
    ScopeAssessment,
    SourceRecord,
    Topic,
    Work,
)
from paper_hub.normalization import APPROVED_CANONICAL_PREFIXES


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
        self._acquire_identity_locks(record, parsed)
        scope_evidence = tuple(
            {
                "field": evidence.field,
                "term": evidence.term,
                "matched_text": evidence.matched_text,
            }
            for evidence in parsed.scope.evidence
        )
        existing_snapshot = self.session.scalar(
            select(SourceRecord).where(
                SourceRecord.source == record.source,
                SourceRecord.source_record_id == record.source_record_id,
                SourceRecord.content_hash == record.content_hash,
            )
        )
        if existing_snapshot is not None:
            existing_assessment = self.session.scalar(
                select(ScopeAssessment).where(
                    ScopeAssessment.source_record_id
                    == existing_snapshot.id,
                    ScopeAssessment.rule_version
                    == parsed.scope.rule_version,
                )
            )
            if existing_assessment is not None:
                if (
                    existing_assessment.included
                    and self._projection_requires_rebuild(
                        existing_assessment
                    )
                ):
                    return self._include_snapshot(
                        existing_snapshot,
                        record,
                        parsed,
                        scope_evidence,
                        persist_scope_assessment=False,
                        force_projection=True,
                    )
                return self._existing_assessment_result(
                    existing_snapshot,
                    existing_assessment,
                )
            if not parsed.scope.included:
                self._persist_scope_assessment(
                    existing_snapshot,
                    parsed,
                    work=None,
                )
                self.session.flush()
                return IngestionResult(
                    status="excluded",
                    source_record_id=existing_snapshot.id,
                    reason=parsed.scope.reason,
                    scope_rule_version=parsed.scope.rule_version,
                    scope_evidence=scope_evidence,
                )
            return self._include_snapshot(
                existing_snapshot,
                record,
                parsed,
                scope_evidence,
            )
        if not parsed.scope.included:
            source_record = self._create_source_record(
                work=None,
                record=record,
                parsed=parsed,
            )
            self._persist_scope_assessment(
                source_record,
                parsed,
                work=None,
            )
            self.session.flush()
            return IngestionResult(
                status="excluded",
                source_record_id=source_record.id,
                reason=parsed.scope.reason,
                scope_rule_version=parsed.scope.rule_version,
                scope_evidence=scope_evidence,
            )

        return self._include_snapshot(
            None,
            record,
            parsed,
            scope_evidence,
        )

    def _include_snapshot(
        self,
        source_record: SourceRecord | None,
        record: ConnectorRecord[ParsedWork],
        parsed: ParsedWork,
        scope_evidence: tuple[dict[str, str], ...],
        *,
        persist_scope_assessment: bool = True,
        force_projection: bool = False,
    ) -> IngestionResult:
        work = self._resolve_work(parsed)
        if source_record is not None and source_record.work_id is not None:
            source_work = self.session.get(Work, source_record.work_id)
            if source_work is None:
                raise IdentityConflictError(
                    "source record references a missing canonical work"
                )
            if work is None:
                work = source_work
            elif work.id != source_work.id:
                raise IdentityConflictError(
                    "source record and controlled identifiers point "
                    "to different works"
                )
        is_new_work = work is None
        if work is None:
            work = Work(
                canonical_key=parsed.canonical_key,
                title=parsed.title,
                abstract=parsed.abstract,
                publication_date=parsed.publication_date,
                projection_source=record.source,
                projection_source_record_id=record.source_record_id,
                projection_source_updated_at=record.source_updated_at,
                status=self._work_status(parsed),
                **self._provenance(record, parsed),
            )
            self.session.add(work)
            self.session.flush()
        else:
            self._upgrade_canonical_key(work, parsed)
            self._update_work_projection(
                work,
                record,
                parsed,
                force=force_projection,
            )

        if source_record is None:
            source_record = self._create_source_record(
                work=work,
                record=record,
                parsed=parsed,
            )
        elif source_record.work_id is None:
            source_record.work_id = work.id
            self.session.flush()
        elif source_record.work_id != work.id:
            raise IdentityConflictError(
                "source record is already linked to another work"
            )

        self._persist_external_identifiers(work, record, parsed)
        self._persist_field_assertions(source_record, record, parsed)
        self._persist_topics(work, record, parsed)
        self._persist_code_repositories(work, record, parsed)
        self._persist_citation_metric(work, source_record, record, parsed)
        if persist_scope_assessment:
            self._persist_scope_assessment(
                source_record,
                parsed,
                work=work,
            )
        self.session.flush()

        return IngestionResult(
            status="inserted" if is_new_work else "updated",
            work_id=work.id,
            source_record_id=source_record.id,
            scope_rule_version=parsed.scope.rule_version,
            scope_evidence=scope_evidence,
        )

    def _create_source_record(
        self,
        *,
        work: Work | None,
        record: ConnectorRecord[ParsedWork],
        parsed: ParsedWork,
    ) -> SourceRecord:
        source_record = SourceRecord(
            work_id=work.id if work is not None else None,
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
        return source_record

    def _persist_scope_assessment(
        self,
        source_record: SourceRecord,
        parsed: ParsedWork,
        *,
        work: Work | None,
    ) -> None:
        self.session.add(
            ScopeAssessment(
                source_record_id=source_record.id,
                rule_version=parsed.scope.rule_version,
                included=parsed.scope.included,
                reason=parsed.scope.reason,
                evidence=_json_value(parsed.scope.evidence),
                evaluated_at=datetime.now(UTC),
                work_id=work.id if work is not None else None,
            )
        )

    @staticmethod
    def _existing_assessment_result(
        source_record: SourceRecord,
        assessment: ScopeAssessment,
    ) -> IngestionResult:
        evidence = tuple(
            {
                "field": str(item["field"]),
                "term": str(item["term"]),
                "matched_text": str(item["matched_text"]),
            }
            for item in assessment.evidence
        )
        if assessment.included:
            return IngestionResult(
                status="unchanged",
                work_id=assessment.work_id,
                source_record_id=source_record.id,
                scope_rule_version=assessment.rule_version,
                scope_evidence=evidence,
            )
        return IngestionResult(
            status="excluded",
            source_record_id=source_record.id,
            reason=assessment.reason,
            scope_rule_version=assessment.rule_version,
            scope_evidence=evidence,
        )

    def _resolve_work(self, parsed: ParsedWork) -> Work | None:
        matched_works: dict[UUID, Work] = {}
        for identifier in parsed.external_identifiers:
            if identifier.scheme not in APPROVED_CANONICAL_PREFIXES:
                continue
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

    def _projection_requires_rebuild(
        self,
        assessment: ScopeAssessment,
    ) -> bool:
        if assessment.work_id is None:
            raise IdentityConflictError(
                "included scope assessment has no canonical work"
            )
        work = self.session.get(Work, assessment.work_id)
        if work is None:
            raise IdentityConflictError(
                "included scope assessment references a missing work"
            )
        return any(
            value is None
            for value in (
                work.projection_source,
                work.projection_source_record_id,
                work.projection_source_updated_at,
            )
        )

    def _acquire_identity_locks(
        self,
        record: ConnectorRecord[ParsedWork],
        parsed: ParsedWork,
    ) -> None:
        lock_keys = {
            f"source:{record.source}:{record.source_record_id}",
            *(
                "external:"
                f"{identifier.scheme}:{identifier.normalized_value}"
                for identifier in parsed.external_identifiers
            ),
        }
        for lock_key in sorted(lock_keys):
            self.session.execute(
                text(
                    """
                    SELECT pg_advisory_xact_lock(
                        hashtextextended(:lock_key, 0)
                    )
                    """
                ),
                {"lock_key": lock_key},
            )

    def _upgrade_canonical_key(
        self,
        work: Work,
        parsed: ParsedWork,
    ) -> None:
        priority = {
            scheme: index
            for index, scheme in enumerate(APPROVED_CANONICAL_PREFIXES)
        }
        current_scheme = work.canonical_key.split(":", maxsplit=1)[0]
        candidate_scheme = parsed.canonical_key.split(":", maxsplit=1)[0]
        if priority[candidate_scheme] >= priority[current_scheme]:
            return
        owner = self.session.scalar(
            select(Work).where(
                Work.canonical_key == parsed.canonical_key,
                Work.id != work.id,
            )
        )
        if owner is not None:
            raise IdentityConflictError(
                "stronger canonical key is already owned by another work"
            )
        work.canonical_key = parsed.canonical_key

    def _update_work_projection(
        self,
        work: Work,
        record: ConnectorRecord[ParsedWork],
        parsed: ParsedWork,
        *,
        force: bool = False,
    ) -> None:
        if (
            not force
            and work.projection_source_updated_at is not None
            and record.source_updated_at
            <= work.projection_source_updated_at
        ):
            return
        work.title = parsed.title
        work.abstract = parsed.abstract
        work.publication_date = parsed.publication_date
        work.source = record.source
        work.source_url = _source_url(record)
        work.retrieved_at = record.retrieved_at
        work.source_license = OPENALEX_SOURCE_LICENSE
        work.content_license = parsed.license
        work.status = self._work_status(parsed)
        work.projection_source = record.source
        work.projection_source_record_id = record.source_record_id
        work.projection_source_updated_at = record.source_updated_at

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
            "is_retracted": parsed.is_retracted,
            "topics": parsed.topics,
            "open_access": parsed.open_access,
            "license": parsed.license,
            "primary_location": parsed.primary_location,
            "best_oa_location": parsed.best_oa_location,
            "citation_count": parsed.citation_count,
            "source_updated_at": record.source_updated_at,
            "code_repositories": parsed.code_repositories,
        }
        existing_field_names = set(
            self.session.scalars(
                select(FieldAssertion.field_name).where(
                    FieldAssertion.source_record_id == source_record.id,
                    FieldAssertion.parser_version == record.parser_version,
                )
            ).all()
        )
        for field_name, value in assertions.items():
            if field_name in existing_field_names:
                continue
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
        for parsed_topic in sorted(
            parsed.topics,
            key=lambda topic: _normalize_name(topic.display_name),
        ):
            normalized_name = _normalize_name(parsed_topic.display_name)
            topic_id = self.session.scalar(
                pg_insert(Topic)
                .values(
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
                .on_conflict_do_nothing(
                    constraint="uq_topic_normalized_name"
                )
                .returning(Topic.id)
            )
            if topic_id is None:
                topic = self.session.scalar(
                    select(Topic).where(
                        Topic.normalized_name == normalized_name
                    )
                )
            else:
                topic = self.session.get(Topic, topic_id)
            if topic is None:
                raise IngestionError(
                    "topic upsert did not return or resolve a row"
                )
            if topic.id not in existing_topic_ids:
                work.topics.append(topic)
                existing_topic_ids.add(topic.id)

    def _persist_code_repositories(
        self,
        work: Work,
        record: ConnectorRecord[ParsedWork],
        parsed: ParsedWork,
    ) -> None:
        for parsed_repository in sorted(
            parsed.code_repositories,
            key=lambda repository: repository.normalized_url,
        ):
            self.session.execute(
                pg_insert(CodeRepository)
                .values(
                    work_id=work.id,
                    provider=parsed_repository.provider,
                    repository_name=(
                        parsed_repository.repository_name
                    ),
                    repository_url=(
                        parsed_repository.repository_url
                    ),
                    normalized_url=(
                        parsed_repository.normalized_url
                    ),
                    is_official=False,
                    **self._provenance(record, parsed),
                )
                .on_conflict_do_nothing(
                    constraint="uq_code_repository_normalized_url"
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

    @staticmethod
    def _work_status(parsed: ParsedWork) -> RecordStatus:
        return (
            RecordStatus.RETRACTED
            if parsed.is_retracted
            else RecordStatus.ACTIVE
        )


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
