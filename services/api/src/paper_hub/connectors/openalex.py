from __future__ import annotations

from collections.abc import Iterator, Mapping, Sequence
from copy import deepcopy
from datetime import UTC, date, datetime
import json
import re
from typing import Any
from urllib.parse import urlsplit, urlunsplit

import httpx
from pydantic import SecretStr

from paper_hub.connectors.base import (
    AuthorData,
    CodeRepositoryData,
    ConnectorError,
    ConnectorParseError,
    ConnectorRecord,
    ExternalIdentifierData,
    InstitutionData,
    LocationData,
    OpenAccessData,
    ParsedWork,
    ScopeDecision,
    ScopeEvidence,
    TopicData,
    canonical_content_hash,
)
from paper_hub.normalization import (
    canonical_identity,
    normalize_arxiv_id,
    normalize_doi,
    normalize_openalex_id,
    normalize_openreview_forum_id,
    normalize_semantic_scholar_paper_id,
)


OPENALEX_API_URL = "https://api.openalex.org/works"
OPENALEX_USER_AGENT_VERSION = "0.1.0"
OPENALEX_PARSER_VERSION = "openalex-v1"
OPENALEX_SCOPE_RULE_VERSION = "agent-llm-v1"
OPENALEX_MAX_RESULTS = 1000
OPENALEX_PAGE_SIZE = 100
OPENALEX_SOURCE_LICENSE = "CC0"

_EMAIL_PATTERN = re.compile(
    r"^[^@\s]+@[^@\s]+\.[^@\s]+$",
    flags=re.IGNORECASE,
)
_URL_PATTERN = re.compile(r"https?://[^\s<>{}\[\]\"']+")
_REPOSITORY_HOSTS = {
    "github.com": "github",
    "gitlab.com": "gitlab",
    "bitbucket.org": "bitbucket",
}
_SCOPE_TERMS: tuple[tuple[str, re.Pattern[str]], ...] = (
    (
        "large language model",
        re.compile(r"\blarge[\s-]+language[\s-]+models?\b", re.IGNORECASE),
    ),
    ("llm", re.compile(r"\bllms?\b", re.IGNORECASE)),
    (
        "llm agent",
        re.compile(r"\bllm[\s-]+agents?\b", re.IGNORECASE),
    ),
    (
        "ai agent",
        re.compile(r"\b(?:ai|artificial intelligence)[\s-]+agents?\b", re.IGNORECASE),
    ),
    (
        "autonomous agent",
        re.compile(r"\bautonomous[\s-]+agents?\b", re.IGNORECASE),
    ),
    ("agentic ai", re.compile(r"\bagentic[\s-]+ai\b", re.IGNORECASE)),
    ("agentic", re.compile(r"\bagentic\b", re.IGNORECASE)),
    ("multi-agent", re.compile(r"\bmulti[\s-]+agents?\b", re.IGNORECASE)),
    (
        "tool-using agent",
        re.compile(r"\btool[\s-]+(?:using|use)[\s-]+agents?\b", re.IGNORECASE),
    ),
)


class OpenAlexError(ConnectorError):
    """Base error for OpenAlex request failures."""


class OpenAlexTimeoutError(OpenAlexError):
    """OpenAlex did not respond within the configured timeout."""


class OpenAlexRateLimitError(OpenAlexError):
    def __init__(self, retry_after: str | None) -> None:
        self.retry_after = retry_after
        message = "OpenAlex request was rate limited with HTTP 429"
        if retry_after:
            message += f"; Retry-After={retry_after}"
        super().__init__(message)


class OpenAlexResponseError(OpenAlexError):
    """OpenAlex returned a non-success or malformed response."""


class OpenAlexParseError(OpenAlexError, ConnectorParseError):
    """An OpenAlex work cannot be parsed safely."""


class OpenAlexConnector:
    def __init__(
        self,
        *,
        contact_email: str,
        api_key: SecretStr | None = None,
        timeout: httpx.Timeout | float = httpx.Timeout(
            20.0,
            connect=5.0,
            read=20.0,
            write=10.0,
            pool=5.0,
        ),
        client: httpx.Client | None = None,
    ) -> None:
        normalized_email = contact_email.strip()
        if not _EMAIL_PATTERN.fullmatch(normalized_email):
            raise ValueError("a valid OpenAlex contact email is required")
        self.contact_email = normalized_email
        self.api_key = api_key
        self.timeout = timeout
        self._owns_client = client is None
        self.client = client or httpx.Client(timeout=timeout)

    def close(self) -> None:
        if self._owns_client:
            self.client.close()

    def __enter__(self) -> OpenAlexConnector:
        return self

    def __exit__(self, *_: object) -> None:
        self.close()

    def fetch_records(
        self,
        *,
        query: str,
        max_results: int,
        from_date: date | None = None,
    ) -> Iterator[ConnectorRecord[ParsedWork]]:
        normalized_query = query.strip()
        if not normalized_query:
            raise ValueError("query must not be empty")
        if isinstance(max_results, bool) or not (
            1 <= max_results <= OPENALEX_MAX_RESULTS
        ):
            raise ValueError(
                "max_results must be between 1 and "
                f"{OPENALEX_MAX_RESULTS}"
            )

        cursor = "*"
        seen_cursors: set[str] = set()
        yielded = 0
        while yielded < max_results:
            if cursor in seen_cursors:
                raise OpenAlexResponseError(
                    "OpenAlex returned a repeated pagination cursor"
                )
            seen_cursors.add(cursor)
            remaining = max_results - yielded
            per_page = min(OPENALEX_PAGE_SIZE, remaining)
            payload, status_code, retrieved_at = self._request_page(
                query=normalized_query,
                from_date=from_date,
                cursor=cursor,
                per_page=per_page,
            )
            results = payload.get("results")
            if not isinstance(results, list):
                raise OpenAlexResponseError(
                    "OpenAlex response field 'results' must be a list"
                )

            for raw_work in results[:remaining]:
                if not isinstance(raw_work, dict):
                    raise OpenAlexResponseError(
                        "OpenAlex returned a non-object work record"
                    )
                yield self.parse_record(
                    raw_work,
                    retrieved_at=retrieved_at,
                    http_status=status_code,
                )
                yielded += 1

            if yielded >= max_results or not results:
                break
            meta = payload.get("meta")
            next_cursor = (
                meta.get("next_cursor")
                if isinstance(meta, Mapping)
                else None
            )
            if not isinstance(next_cursor, str) or not next_cursor:
                break
            cursor = next_cursor

    def _request_page(
        self,
        *,
        query: str,
        from_date: date | None,
        cursor: str,
        per_page: int,
    ) -> tuple[dict[str, Any], int, datetime]:
        params: dict[str, str | int] = {
            "search": query,
            "cursor": cursor,
            "per-page": per_page,
            "mailto": self.contact_email,
        }
        if from_date is not None:
            params["filter"] = (
                f"from_publication_date:{from_date.isoformat()}"
            )
        if self.api_key is not None:
            params["api_key"] = self.api_key.get_secret_value()
        headers = {
            "User-Agent": (
                f"paper-hub/{OPENALEX_USER_AGENT_VERSION} "
                f"(mailto:{self.contact_email})"
            )
        }

        try:
            response = self.client.get(
                OPENALEX_API_URL,
                params=params,
                headers=headers,
                timeout=self.timeout,
            )
        except httpx.TimeoutException as exc:
            raise OpenAlexTimeoutError(
                "OpenAlex request timed out"
            ) from exc
        except httpx.RequestError as exc:
            raise OpenAlexError(
                f"OpenAlex request failed: {exc.__class__.__name__}"
            ) from exc

        if response.status_code == 429:
            raise OpenAlexRateLimitError(response.headers.get("Retry-After"))
        if not 200 <= response.status_code < 300:
            raise OpenAlexResponseError(
                "OpenAlex request failed with HTTP "
                f"{response.status_code}"
            )
        try:
            payload = response.json()
        except (json.JSONDecodeError, ValueError) as exc:
            raise OpenAlexResponseError(
                "OpenAlex returned invalid JSON"
            ) from exc
        if not isinstance(payload, dict):
            raise OpenAlexResponseError(
                "OpenAlex response must be a JSON object"
            )
        return payload, response.status_code, datetime.now(UTC)

    @classmethod
    def parse_record(
        cls,
        raw_payload: Mapping[str, Any],
        *,
        retrieved_at: datetime | None = None,
        http_status: int | None = None,
    ) -> ConnectorRecord[ParsedWork]:
        raw_copy = deepcopy(dict(raw_payload))
        source_record_id = normalize_openalex_id(
            _string(raw_payload.get("id"))
        )
        if source_record_id is None:
            source_record_id = normalize_openalex_id(
                _nested_string(raw_payload, "ids", "openalex")
            )
        if source_record_id is None:
            raise OpenAlexParseError(
                "OpenAlex work is missing a valid OpenAlex ID"
            )

        source_updated_at = _parse_datetime(
            raw_payload.get("updated_date"),
            field_name="updated_date",
        )
        external_identifiers = _external_identifiers(raw_payload)
        identity_values = {
            identifier.scheme: identifier.normalized_value
            for identifier in external_identifiers
        }
        identity_record = {
            "doi": identity_values.get("doi"),
            "arxiv_id": identity_values.get("arxiv"),
            "openreview_forum_id": identity_values.get("openreview"),
            "openalex_id": identity_values.get("openalex"),
            "semantic_scholar_paper_id": identity_values.get("s2"),
        }
        canonical_key = canonical_identity(identity_record)
        if canonical_key is None:
            raise OpenAlexParseError(
                "OpenAlex work has no controlled canonical identifier"
            )

        title = _required_title(raw_payload)
        abstract = _reconstruct_abstract(
            raw_payload.get("abstract_inverted_index")
        )
        authors, institutions = _authors_and_institutions(raw_payload)
        topics = _topics(raw_payload)
        primary_location = _location(raw_payload.get("primary_location"))
        best_oa_location = _location(raw_payload.get("best_oa_location"))
        open_access = _open_access(raw_payload.get("open_access"))
        license_value = (
            best_oa_location.license
            if best_oa_location is not None
            else None
        ) or (
            primary_location.license
            if primary_location is not None
            else None
        )
        scope = _scope_decision(title, abstract, topics)
        parsed = ParsedWork(
            canonical_key=canonical_key,
            external_identifiers=external_identifiers,
            title=title,
            abstract=abstract,
            authors=authors,
            institutions=institutions,
            publication_date=_parse_date(raw_payload.get("publication_date")),
            work_type=_string(raw_payload.get("type")),
            topics=topics,
            open_access=open_access,
            license=license_value,
            primary_location=primary_location,
            best_oa_location=best_oa_location,
            citation_count=_nonnegative_int(
                raw_payload.get("cited_by_count"),
                field_name="cited_by_count",
            ),
            updated_at=source_updated_at,
            code_repositories=_code_repositories(
                raw_payload,
                abstract=abstract,
            ),
            scope=scope,
        )
        content_hash = canonical_content_hash(raw_copy)
        resolved_retrieved_at = retrieved_at or datetime.now(UTC)
        if resolved_retrieved_at.tzinfo is None:
            raise ValueError("retrieved_at must be timezone-aware")
        return ConnectorRecord(
            source="openalex",
            source_record_id=source_record_id,
            retrieved_at=resolved_retrieved_at,
            content_hash=content_hash,
            raw_payload=raw_copy,
            source_updated_at=source_updated_at,
            http_status=http_status,
            parser_version=OPENALEX_PARSER_VERSION,
            parsed=parsed,
        )


def _required_title(raw_payload: Mapping[str, Any]) -> str:
    for key in ("display_name", "title"):
        value = _string(raw_payload.get(key))
        if value is not None and value.strip():
            return value.strip()
    raise OpenAlexParseError("OpenAlex work is missing a title")


def _reconstruct_abstract(value: Any) -> str | None:
    if value is None:
        return None
    if not isinstance(value, Mapping):
        raise OpenAlexParseError(
            "abstract_inverted_index must be an object or null"
        )
    positioned_words: dict[int, str] = {}
    for word, positions in value.items():
        if not isinstance(word, str) or not isinstance(positions, Sequence):
            raise OpenAlexParseError(
                "abstract_inverted_index contains invalid entries"
            )
        for position in positions:
            if (
                isinstance(position, bool)
                or not isinstance(position, int)
                or position < 0
            ):
                raise OpenAlexParseError(
                    "abstract_inverted_index positions must be nonnegative integers"
                )
            existing = positioned_words.get(position)
            if existing is not None and existing != word:
                raise OpenAlexParseError(
                    "abstract_inverted_index contains a position collision"
                )
            positioned_words[position] = word
    if not positioned_words:
        return None
    return " ".join(
        positioned_words[position] for position in sorted(positioned_words)
    )


def _external_identifiers(
    raw_payload: Mapping[str, Any],
) -> tuple[ExternalIdentifierData, ...]:
    ids = raw_payload.get("ids")
    id_map = ids if isinstance(ids, Mapping) else {}
    candidates: tuple[
        tuple[str, Any, Any],
        ...,
    ] = (
        (
            "openalex",
            raw_payload.get("id") or id_map.get("openalex"),
            normalize_openalex_id,
        ),
        (
            "doi",
            raw_payload.get("doi") or id_map.get("doi"),
            normalize_doi,
        ),
        (
            "arxiv",
            _first_value(
                id_map,
                "arxiv",
                "arxiv_id",
            )
            or _identifier_from_locations(raw_payload, "arxiv"),
            normalize_arxiv_id,
        ),
        (
            "openreview",
            _first_value(
                id_map,
                "openreview",
                "openreview_id",
                "openreview_forum_id",
            ),
            normalize_openreview_forum_id,
        ),
        (
            "s2",
            _first_value(
                id_map,
                "semantic_scholar",
                "semantic_scholar_id",
                "semantic_scholar_paper_id",
                "s2",
            ),
            normalize_semantic_scholar_paper_id,
        ),
    )
    identifiers: list[ExternalIdentifierData] = []
    for scheme, raw_value, normalizer in candidates:
        value = _string(raw_value)
        normalized = normalizer(value)
        if value is None or normalized is None:
            continue
        identifiers.append(
            ExternalIdentifierData(
                scheme=scheme,
                normalized_value=normalized,
                raw_value=value,
            )
        )
    return tuple(identifiers)


def _identifier_from_locations(
    raw_payload: Mapping[str, Any],
    scheme: str,
) -> str | None:
    location_values: list[Any] = [
        raw_payload.get("primary_location"),
        raw_payload.get("best_oa_location"),
    ]
    locations = raw_payload.get("locations")
    if isinstance(locations, list):
        location_values.extend(locations)
    for location in location_values:
        if not isinstance(location, Mapping):
            continue
        for key in ("landing_page_url", "pdf_url"):
            url = _string(location.get(key))
            if url is None:
                continue
            if scheme == "arxiv" and "arxiv.org/" in url.lower():
                return url
    return None


def _authors_and_institutions(
    raw_payload: Mapping[str, Any],
) -> tuple[tuple[AuthorData, ...], tuple[InstitutionData, ...]]:
    authorships = raw_payload.get("authorships")
    if authorships is None:
        return (), ()
    if not isinstance(authorships, list):
        raise OpenAlexParseError("authorships must be a list")

    authors: list[AuthorData] = []
    unique_institutions: dict[
        tuple[str | None, str],
        InstitutionData,
    ] = {}
    for authorship in authorships:
        if not isinstance(authorship, Mapping):
            raise OpenAlexParseError("authorship must be an object")
        author = authorship.get("author")
        if not isinstance(author, Mapping):
            raise OpenAlexParseError("authorship.author must be an object")
        display_name = _string(author.get("display_name"))
        if display_name is None or not display_name.strip():
            raise OpenAlexParseError("authorship author is missing a name")
        institutions = _institution_list(authorship.get("institutions"))
        for institution in institutions:
            unique_institutions[
                (institution.openalex_id, institution.display_name)
            ] = institution
        is_corresponding = authorship.get("is_corresponding", False)
        if not isinstance(is_corresponding, bool):
            raise OpenAlexParseError(
                "authorship.is_corresponding must be a boolean"
            )
        authors.append(
            AuthorData(
                openalex_id=_strip_openalex_url(
                    _string(author.get("id")),
                    expected_prefix="A",
                ),
                display_name=display_name.strip(),
                orcid=_strip_orcid(_string(author.get("orcid"))),
                position=_string(authorship.get("author_position")),
                is_corresponding=is_corresponding,
                institutions=institutions,
            )
        )
    return tuple(authors), tuple(unique_institutions.values())


def _institution_list(value: Any) -> tuple[InstitutionData, ...]:
    if value is None:
        return ()
    if not isinstance(value, list):
        raise OpenAlexParseError("authorship institutions must be a list")
    institutions: list[InstitutionData] = []
    for raw_institution in value:
        if not isinstance(raw_institution, Mapping):
            raise OpenAlexParseError("institution must be an object")
        name = _string(raw_institution.get("display_name"))
        if name is None or not name.strip():
            continue
        institutions.append(
            InstitutionData(
                openalex_id=_strip_openalex_url(
                    _string(raw_institution.get("id")),
                    expected_prefix="I",
                ),
                display_name=name.strip(),
                ror=_strip_ror(_string(raw_institution.get("ror"))),
                country_code=_string(raw_institution.get("country_code")),
                institution_type=_string(raw_institution.get("type")),
            )
        )
    return tuple(institutions)


def _topics(raw_payload: Mapping[str, Any]) -> tuple[TopicData, ...]:
    value = raw_payload.get("topics")
    if value is None:
        value = []
    if not isinstance(value, list):
        raise OpenAlexParseError("topics must be a list")
    topics: list[TopicData] = []
    seen: set[tuple[str | None, str]] = set()
    for raw_topic in value:
        if not isinstance(raw_topic, Mapping):
            raise OpenAlexParseError("topic must be an object")
        name = _string(raw_topic.get("display_name"))
        if name is None or not name.strip():
            continue
        topic = TopicData(
            openalex_id=_strip_openalex_url(
                _string(raw_topic.get("id")),
                expected_prefix="T",
            ),
            display_name=name.strip(),
            score=_optional_float(raw_topic.get("score")),
        )
        key = (topic.openalex_id, topic.display_name)
        if key not in seen:
            topics.append(topic)
            seen.add(key)
    return tuple(topics)


def _location(value: Any) -> LocationData | None:
    if value is None:
        return None
    if not isinstance(value, Mapping):
        raise OpenAlexParseError("location must be an object or null")
    source = value.get("source")
    source_name = (
        _string(source.get("display_name"))
        if isinstance(source, Mapping)
        else None
    )
    is_oa = value.get("is_oa")
    if is_oa is not None and not isinstance(is_oa, bool):
        raise OpenAlexParseError("location.is_oa must be a boolean or null")
    return LocationData(
        is_oa=is_oa,
        landing_page_url=_string(value.get("landing_page_url")),
        pdf_url=_string(value.get("pdf_url")),
        license=_string(value.get("license")),
        version=_string(value.get("version")),
        source_name=source_name,
    )


def _open_access(value: Any) -> OpenAccessData:
    if value is None:
        return OpenAccessData(
            is_oa=False,
            status=None,
            url=None,
            repository_has_fulltext=None,
        )
    if not isinstance(value, Mapping):
        raise OpenAlexParseError("open_access must be an object")
    is_oa = value.get("is_oa", False)
    repository_has_fulltext = value.get("any_repository_has_fulltext")
    if not isinstance(is_oa, bool):
        raise OpenAlexParseError("open_access.is_oa must be a boolean")
    if (
        repository_has_fulltext is not None
        and not isinstance(repository_has_fulltext, bool)
    ):
        raise OpenAlexParseError(
            "open_access.any_repository_has_fulltext must be a boolean or null"
        )
    return OpenAccessData(
        is_oa=is_oa,
        status=_string(value.get("oa_status")),
        url=_string(value.get("oa_url")),
        repository_has_fulltext=repository_has_fulltext,
    )


def _scope_decision(
    title: str,
    abstract: str | None,
    topics: tuple[TopicData, ...],
) -> ScopeDecision:
    searchable: list[tuple[str, str]] = [("title", title)]
    if abstract:
        searchable.append(("abstract", abstract))
    searchable.extend(("topic", topic.display_name) for topic in topics)

    evidence: list[ScopeEvidence] = []
    seen: set[tuple[str, str, str]] = set()
    for field, text in searchable:
        for term, pattern in _SCOPE_TERMS:
            for match in pattern.finditer(text):
                item = ScopeEvidence(
                    field=field,
                    term=term,
                    matched_text=match.group(0),
                )
                key = (item.field, item.term, item.matched_text.lower())
                if key not in seen:
                    evidence.append(item)
                    seen.add(key)
    included = bool(evidence)
    return ScopeDecision(
        included=included,
        rule_version=OPENALEX_SCOPE_RULE_VERSION,
        evidence=tuple(evidence),
        reason=None if included else "no_agent_llm_scope_term_match",
    )


def _code_repositories(
    raw_payload: Mapping[str, Any],
    *,
    abstract: str | None,
) -> tuple[CodeRepositoryData, ...]:
    text_values = [abstract or ""]
    for key in ("title", "display_name"):
        value = _string(raw_payload.get(key))
        if value:
            text_values.append(value)
    for location_key in ("primary_location", "best_oa_location"):
        location = raw_payload.get(location_key)
        if isinstance(location, Mapping):
            text_values.extend(
                value
                for value in (
                    _string(location.get("landing_page_url")),
                    _string(location.get("pdf_url")),
                )
                if value is not None
            )
    locations = raw_payload.get("locations")
    if isinstance(locations, list):
        for location in locations:
            if not isinstance(location, Mapping):
                continue
            text_values.extend(
                value
                for value in (
                    _string(location.get("landing_page_url")),
                    _string(location.get("pdf_url")),
                )
                if value is not None
            )

    repositories: dict[str, CodeRepositoryData] = {}
    for text_value in text_values:
        for match in _URL_PATTERN.finditer(text_value):
            normalized = _normalize_repository_url(match.group(0))
            if normalized is None:
                continue
            provider, normalized_url, repository_name = normalized
            repositories[normalized_url] = CodeRepositoryData(
                provider=provider,
                repository_name=repository_name,
                repository_url=match.group(0).rstrip(".,;:!?"),
                normalized_url=normalized_url,
            )
    return tuple(repositories.values())


def _normalize_repository_url(
    raw_url: str,
) -> tuple[str, str, str] | None:
    cleaned = raw_url.rstrip(".,;:!?")
    parsed = urlsplit(cleaned)
    host = (parsed.hostname or "").lower().removeprefix("www.")
    provider = _REPOSITORY_HOSTS.get(host)
    if provider is None:
        return None
    segments = [segment for segment in parsed.path.split("/") if segment]
    if len(segments) < 2:
        return None
    owner, repository = segments[0], segments[1].removesuffix(".git")
    if not owner or not repository:
        return None
    path = f"/{owner}/{repository}"
    normalized_url = urlunsplit(("https", host, path, "", ""))
    return provider, normalized_url, f"{owner}/{repository}"


def _parse_date(value: Any) -> date | None:
    if value is None:
        return None
    if not isinstance(value, str):
        raise OpenAlexParseError("publication_date must be a string or null")
    try:
        return date.fromisoformat(value)
    except ValueError as exc:
        raise OpenAlexParseError(
            "publication_date must be an ISO date"
        ) from exc


def _parse_datetime(value: Any, *, field_name: str) -> datetime:
    if not isinstance(value, str) or not value:
        raise OpenAlexParseError(f"{field_name} must be a non-empty ISO datetime")
    normalized = value.replace("Z", "+00:00")
    try:
        parsed = datetime.fromisoformat(normalized)
    except ValueError as exc:
        raise OpenAlexParseError(
            f"{field_name} must be an ISO datetime"
        ) from exc
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=UTC)
    return parsed.astimezone(UTC)


def _nonnegative_int(value: Any, *, field_name: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise OpenAlexParseError(
            f"{field_name} must be a nonnegative integer"
        )
    return value


def _optional_float(value: Any) -> float | None:
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise OpenAlexParseError("topic score must be numeric or null")
    return float(value)


def _strip_openalex_url(
    value: str | None,
    *,
    expected_prefix: str,
) -> str | None:
    if value is None:
        return None
    candidate = value.rstrip("/").rsplit("/", maxsplit=1)[-1]
    return candidate if candidate.startswith(expected_prefix) else None


def _strip_orcid(value: str | None) -> str | None:
    if value is None:
        return None
    return value.rstrip("/").rsplit("/", maxsplit=1)[-1]


def _strip_ror(value: str | None) -> str | None:
    if value is None:
        return None
    return value.rstrip("/").rsplit("/", maxsplit=1)[-1]


def _first_value(mapping: Mapping[str, Any], *keys: str) -> Any:
    for key in keys:
        value = mapping.get(key)
        if value is not None:
            return value
    return None


def _nested_string(
    mapping: Mapping[str, Any],
    parent: str,
    child: str,
) -> str | None:
    nested = mapping.get(parent)
    if not isinstance(nested, Mapping):
        return None
    return _string(nested.get(child))


def _string(value: Any) -> str | None:
    return value if isinstance(value, str) else None
