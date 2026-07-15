from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass
from datetime import date, datetime
import hashlib
import json
from typing import Any, Generic, TypeVar


ParsedRecord = TypeVar("ParsedRecord")


def canonical_content_hash(raw_payload: Mapping[str, Any]) -> str:
    return hashlib.sha256(
        json.dumps(
            raw_payload,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
    ).hexdigest()


class ConnectorError(RuntimeError):
    """Base error for an explicit connector failure."""


class ConnectorParseError(ConnectorError):
    """Raised when a source payload violates its documented contract."""


@dataclass(frozen=True)
class ExternalIdentifierData:
    scheme: str
    normalized_value: str
    raw_value: str


@dataclass(frozen=True)
class InstitutionData:
    openalex_id: str | None
    display_name: str
    ror: str | None
    country_code: str | None
    institution_type: str | None


@dataclass(frozen=True)
class AuthorData:
    openalex_id: str | None
    display_name: str
    orcid: str | None
    position: str | None
    is_corresponding: bool
    institutions: tuple[InstitutionData, ...]


@dataclass(frozen=True)
class TopicData:
    openalex_id: str | None
    display_name: str
    score: float | None


@dataclass(frozen=True)
class LocationData:
    is_oa: bool | None
    landing_page_url: str | None
    pdf_url: str | None
    license: str | None
    version: str | None
    source_name: str | None


@dataclass(frozen=True)
class OpenAccessData:
    is_oa: bool
    status: str | None
    url: str | None
    repository_has_fulltext: bool | None


@dataclass(frozen=True)
class CodeRepositoryData:
    provider: str
    repository_name: str
    repository_url: str
    normalized_url: str


@dataclass(frozen=True)
class ScopeEvidence:
    field: str
    term: str
    matched_text: str


@dataclass(frozen=True)
class ScopeDecision:
    included: bool
    rule_version: str
    evidence: tuple[ScopeEvidence, ...]
    reason: str | None


@dataclass(frozen=True)
class ParsedWork:
    canonical_key: str
    external_identifiers: tuple[ExternalIdentifierData, ...]
    title: str
    abstract: str | None
    authors: tuple[AuthorData, ...]
    institutions: tuple[InstitutionData, ...]
    publication_date: date | None
    work_type: str | None
    is_retracted: bool
    topics: tuple[TopicData, ...]
    open_access: OpenAccessData
    license: str | None
    primary_location: LocationData | None
    best_oa_location: LocationData | None
    citation_count: int
    updated_at: datetime
    code_repositories: tuple[CodeRepositoryData, ...]
    scope: ScopeDecision


@dataclass(frozen=True)
class ConnectorRecord(Generic[ParsedRecord]):
    source: str
    source_record_id: str
    retrieved_at: datetime
    content_hash: str
    raw_payload: dict[str, Any]
    source_updated_at: datetime
    http_status: int | None
    parser_version: str
    parsed: ParsedRecord
