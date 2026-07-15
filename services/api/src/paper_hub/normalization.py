import re
from collections.abc import Callable, Mapping
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
_DOI_VALUE = re.compile(r"^10\.\d{4,9}/\S+$", flags=re.IGNORECASE)
_ARXIV_VALUE = re.compile(
    r"^(?:\d{4}\.\d{4,5}|[a-z][a-z0-9.-]*/\d{7})$",
    flags=re.IGNORECASE,
)
_OPENREVIEW_VALUE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._~-]{0,254}$")
_OPENALEX_PREFIX = re.compile(
    r"^https?://openalex\.org/",
    flags=re.IGNORECASE,
)
_OPENALEX_VALUE = re.compile(r"^W\d+$")
_SEMANTIC_SCHOLAR_VALUE = re.compile(r"^[0-9a-f]{40}$")
APPROVED_CANONICAL_PREFIXES = (
    "doi",
    "arxiv",
    "openreview",
    "s2",
    "openalex",
)
_CANONICAL_KEY = re.compile(
    rf"^({'|'.join(APPROVED_CANONICAL_PREFIXES)}):(.*)$",
    flags=re.IGNORECASE,
)
CANONICAL_KEY_SQL_CHECK = (
    "("
    "(canonical_key ~ '^doi:10[.][0-9]{4,9}/[^[:space:]]+$' "
    "AND canonical_key = lower(canonical_key)) OR "
    "(canonical_key ~ "
    "'^arxiv:([0-9]{4}[.][0-9]{4,5}|[a-z][a-z0-9.-]*/[0-9]{7})$') OR "
    "(canonical_key ~ "
    "'^openreview:[A-Za-z0-9][A-Za-z0-9._~-]{0,254}$') OR "
    "(canonical_key ~ '^openalex:W[0-9]+$') OR "
    "(canonical_key ~ '^s2:[0-9a-f]{40}$')"
    ")"
)


def normalize_doi(value: str | None) -> str | None:
    if value is None:
        return None
    normalized = _DOI_PREFIX.sub("", value.strip(), count=1)
    normalized = normalized.strip()
    if re.search(r"\s", normalized):
        return None
    normalized = normalized.lower()
    return normalized if _DOI_VALUE.fullmatch(normalized) else None


def normalize_arxiv_id(value: str | None) -> str | None:
    if value is None:
        return None
    normalized = _ARXIV_PREFIX.sub("", value.strip(), count=1)
    normalized = normalized.strip()
    if re.search(r"\s", normalized):
        return None
    normalized = normalized.lower()
    normalized = normalized.removesuffix(".pdf")
    normalized = _ARXIV_VERSION.sub("", normalized)
    return normalized if _ARXIV_VALUE.fullmatch(normalized) else None


def normalize_openreview_forum_id(value: str | None) -> str | None:
    normalized = _normalize_external_id(value)
    if normalized is None:
        return None
    return normalized if _OPENREVIEW_VALUE.fullmatch(normalized) else None


def normalize_openalex_id(value: str | None) -> str | None:
    if value is None:
        return None
    normalized = _OPENALEX_PREFIX.sub("", value.strip(), count=1)
    normalized = _normalize_external_id(normalized)
    if normalized is None:
        return None
    normalized = normalized.upper()
    return normalized if _OPENALEX_VALUE.fullmatch(normalized) else None


def normalize_semantic_scholar_paper_id(value: str | None) -> str | None:
    normalized = _normalize_external_id(value)
    if normalized is None:
        return None
    normalized = normalized.lower()
    return (
        normalized
        if _SEMANTIC_SCHOLAR_VALUE.fullmatch(normalized)
        else None
    )


def is_approved_canonical_key(value: str) -> bool:
    return normalize_canonical_key(value) == value


def normalize_canonical_key(value: str | None) -> str | None:
    if value is None or value != value.strip():
        return None
    match = _CANONICAL_KEY.fullmatch(value)
    if match is None:
        return None

    prefix = match.group(1).lower()
    raw_identifier = match.group(2)
    normalizers = {
        "doi": normalize_doi,
        "arxiv": normalize_arxiv_id,
        "openreview": normalize_openreview_forum_id,
        "openalex": normalize_openalex_id,
        "s2": normalize_semantic_scholar_paper_id,
    }
    normalized_identifier = normalizers[prefix](raw_identifier)
    if normalized_identifier is None:
        return None
    return f"{prefix}:{normalized_identifier}"


def canonical_identity(record: Mapping[str, Any]) -> str | None:
    doi = normalize_doi(_string_value(record.get("doi")))
    if doi is not None:
        return f"doi:{doi}"

    arxiv_id = _first_normalized(
        record,
        normalize_arxiv_id,
        "arxiv_id",
        "arxiv",
    )
    if arxiv_id is not None:
        return f"arxiv:{arxiv_id}"

    openreview_forum_id = _first_normalized(
        record,
        normalize_openreview_forum_id,
        "openreview_forum_id",
        "openreview_id",
    )
    if openreview_forum_id is not None:
        return f"openreview:{openreview_forum_id}"

    semantic_scholar_paper_id = _first_normalized(
        record,
        normalize_semantic_scholar_paper_id,
        "semantic_scholar_paper_id",
        "semantic_scholar_id",
        "s2_paper_id",
    )
    if semantic_scholar_paper_id is not None:
        return f"s2:{semantic_scholar_paper_id}"

    openalex_id = _first_normalized(
        record,
        normalize_openalex_id,
        "openalex_id",
    )
    if openalex_id is not None:
        return f"openalex:{openalex_id}"

    return None


def _string_value(value: Any) -> str | None:
    return value if isinstance(value, str) else None


def _first_normalized(
    record: Mapping[str, Any],
    normalizer: Callable[[str | None], str | None],
    *keys: str,
) -> str | None:
    for key in keys:
        normalized = normalizer(_string_value(record.get(key)))
        if normalized is not None:
            return normalized
    return None


def _normalize_external_id(value: str | None) -> str | None:
    if value is None:
        return None
    normalized = value.strip()
    if not normalized or re.search(r"\s", normalized):
        return None
    return normalized
