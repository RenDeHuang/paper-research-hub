import re
from collections.abc import Mapping
from typing import Any


_DOI_PREFIX = re.compile(
    r"^(?:https?://(?:dx\.)?doi\.org/|doi\s*:)",
    flags=re.IGNORECASE,
)
_ARXIV_PREFIX = re.compile(
    r"^(?:https?://arxiv\.org/(?:abs|pdf)/|arxiv\s*:)",
    flags=re.IGNORECASE,
)
_ARXIV_VERSION = re.compile(r"v\d+$", flags=re.IGNORECASE)
_OPENALEX_PREFIX = re.compile(
    r"^https?://openalex\.org/",
    flags=re.IGNORECASE,
)
APPROVED_CANONICAL_PREFIXES = (
    "doi",
    "arxiv",
    "openreview",
    "openalex",
    "s2",
)
CANONICAL_KEY_PATTERN = re.compile(
    rf"^(?:{'|'.join(APPROVED_CANONICAL_PREFIXES)}):\S+$"
)
CANONICAL_KEY_SQL_CHECK = (
    "canonical_key ~ "
    "'^(doi|arxiv|openreview|openalex|s2):[^[:space:]]+$'"
)


def normalize_doi(value: str | None) -> str | None:
    if value is None:
        return None
    normalized = _DOI_PREFIX.sub("", value.strip(), count=1)
    normalized = re.sub(r"\s+", "", normalized).lower()
    return normalized or None


def normalize_arxiv_id(value: str | None) -> str | None:
    if value is None:
        return None
    normalized = _ARXIV_PREFIX.sub("", value.strip(), count=1)
    normalized = re.sub(r"\s+", "", normalized).lower()
    normalized = normalized.removesuffix(".pdf")
    normalized = _ARXIV_VERSION.sub("", normalized)
    return normalized or None


def normalize_openreview_forum_id(value: str | None) -> str | None:
    return _normalize_opaque_external_id(value)


def normalize_openalex_id(value: str | None) -> str | None:
    if value is None:
        return None
    normalized = _OPENALEX_PREFIX.sub("", value.strip(), count=1)
    normalized = _normalize_opaque_external_id(normalized)
    return normalized.upper() if normalized is not None else None


def normalize_semantic_scholar_paper_id(value: str | None) -> str | None:
    normalized = _normalize_opaque_external_id(value)
    return normalized.lower() if normalized is not None else None


def is_approved_canonical_key(value: str) -> bool:
    return CANONICAL_KEY_PATTERN.fullmatch(value) is not None


def canonical_identity(record: Mapping[str, Any]) -> str | None:
    doi = normalize_doi(_string_value(record.get("doi")))
    if doi is not None:
        return f"doi:{doi}"

    arxiv_id = normalize_arxiv_id(
        _string_value(record.get("arxiv_id") or record.get("arxiv"))
    )
    if arxiv_id is not None:
        return f"arxiv:{arxiv_id}"

    openreview_forum_id = normalize_openreview_forum_id(
        _first_string(record, "openreview_forum_id", "openreview_id")
    )
    if openreview_forum_id is not None:
        return f"openreview:{openreview_forum_id}"

    openalex_id = normalize_openalex_id(_first_string(record, "openalex_id"))
    if openalex_id is not None:
        return f"openalex:{openalex_id}"

    semantic_scholar_paper_id = normalize_semantic_scholar_paper_id(
        _first_string(
            record,
            "semantic_scholar_paper_id",
            "semantic_scholar_id",
            "s2_paper_id",
        )
    )
    if semantic_scholar_paper_id is not None:
        return f"s2:{semantic_scholar_paper_id}"

    return None


def _string_value(value: Any) -> str | None:
    return value if isinstance(value, str) else None


def _first_string(record: Mapping[str, Any], *keys: str) -> str | None:
    for key in keys:
        value = _string_value(record.get(key))
        if value is not None:
            return value
    return None


def _normalize_opaque_external_id(value: str | None) -> str | None:
    if value is None:
        return None
    normalized = value.strip()
    if not normalized or re.search(r"\s", normalized):
        return None
    return normalized
