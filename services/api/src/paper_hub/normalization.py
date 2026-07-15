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


def canonical_identity(record: Mapping[str, Any]) -> str | None:
    doi = normalize_doi(_string_value(record.get("doi")))
    if doi is not None:
        return f"doi:{doi}"

    arxiv_id = normalize_arxiv_id(
        _string_value(record.get("arxiv_id") or record.get("arxiv"))
    )
    if arxiv_id is not None:
        return f"arxiv:{arxiv_id}"

    return None


def _string_value(value: Any) -> str | None:
    return value if isinstance(value, str) else None
