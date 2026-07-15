from datetime import UTC, date, datetime
import json
from pathlib import Path

import httpx
from pydantic import SecretStr, ValidationError
import pytest


FIXTURE_PATH = Path(__file__).parent / "fixtures" / "openalex_works.json"
CONTACT_EMAIL = "research@example.com"
API_KEY = "openalex-secret-key"


def _fixture() -> dict:
    return json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))


def test_parse_record_maps_openalex_work_and_provenance() -> None:
    from paper_hub.connectors.openalex import (
        OPENALEX_PARSER_VERSION,
        OPENALEX_SCOPE_RULE_VERSION,
        OpenAlexConnector,
    )

    raw = _fixture()["results"][0]
    retrieved_at = datetime(2026, 7, 15, 9, 0, tzinfo=UTC)

    record = OpenAlexConnector.parse_record(
        raw,
        retrieved_at=retrieved_at,
        http_status=200,
    )
    work = record.parsed
    identifiers = {
        identifier.scheme: identifier.normalized_value
        for identifier in work.external_identifiers
    }

    assert record.source == "openalex"
    assert record.source_record_id == "W2741809807"
    assert record.retrieved_at == retrieved_at
    assert record.source_updated_at == datetime(
        2026,
        7,
        14,
        12,
        34,
        56,
        tzinfo=UTC,
    )
    assert record.http_status == 200
    assert record.raw_payload == raw
    assert len(record.content_hash) == 64
    assert record.parser_version == OPENALEX_PARSER_VERSION

    assert work.canonical_key == "doi:10.48550/arxiv.2607.12345"
    assert identifiers == {
        "openalex": "W2741809807",
        "doi": "10.48550/arxiv.2607.12345",
        "arxiv": "2607.12345",
        "openreview": "Forum_AbC123",
        "s2": "0123456789abcdef0123456789abcdef01234567",
    }
    assert work.title == "AgentBench: Evaluating LLM Agents with Tool Use"
    assert work.abstract == (
        "We study LLM agents with tool use and release code at "
        "https://github.com/example/agentbench."
    )
    assert work.publication_date == date(2026, 7, 10)
    assert work.work_type == "preprint"
    assert work.citation_count == 42
    assert work.updated_at == record.source_updated_at

    assert work.authors[0].display_name == "Ada Example"
    assert work.authors[0].orcid == "0000-0001-2345-6789"
    assert work.authors[0].is_corresponding is True
    assert work.authors[0].institutions[0].display_name == "Example University"
    assert {institution.display_name for institution in work.institutions} == {
        "Example University",
        "Sample AI Lab",
    }
    assert [topic.display_name for topic in work.topics] == [
        "Large Language Models",
        "Autonomous Agents",
    ]

    assert work.open_access.is_oa is True
    assert work.open_access.status == "green"
    assert work.license == "cc-by"
    assert work.primary_location.landing_page_url == (
        "https://arxiv.org/abs/2607.12345"
    )
    assert work.best_oa_location.pdf_url == (
        "https://arxiv.org/pdf/2607.12345"
    )
    assert work.code_repositories[0].provider == "github"
    assert work.code_repositories[0].normalized_url == (
        "https://github.com/example/agentbench"
    )

    assert work.scope.included is True
    assert work.scope.rule_version == OPENALEX_SCOPE_RULE_VERSION
    assert work.scope.evidence
    assert {evidence.field for evidence in work.scope.evidence} <= {
        "title",
        "abstract",
        "topic",
    }


def test_content_hash_is_deterministic_and_changes_with_payload() -> None:
    from paper_hub.connectors.openalex import OpenAlexConnector

    raw = _fixture()["results"][0]
    reordered = dict(reversed(list(raw.items())))
    changed = json.loads(json.dumps(raw))
    changed["cited_by_count"] = 43
    retrieved_at = datetime(2026, 7, 15, 9, 0, tzinfo=UTC)

    first = OpenAlexConnector.parse_record(raw, retrieved_at=retrieved_at)
    same = OpenAlexConnector.parse_record(reordered, retrieved_at=retrieved_at)
    updated = OpenAlexConnector.parse_record(changed, retrieved_at=retrieved_at)

    assert first.content_hash == same.content_hash
    assert updated.content_hash != first.content_hash


def test_scope_rule_excludes_non_agent_llm_record_with_reason() -> None:
    from paper_hub.connectors.openalex import (
        OPENALEX_SCOPE_RULE_VERSION,
        OpenAlexConnector,
    )

    record = OpenAlexConnector.parse_record(
        _fixture()["results"][1],
        retrieved_at=datetime(2026, 7, 15, 9, 0, tzinfo=UTC),
    )

    assert record.parsed.scope.included is False
    assert record.parsed.scope.rule_version == OPENALEX_SCOPE_RULE_VERSION
    assert record.parsed.scope.evidence == ()
    assert (
        record.parsed.scope.reason
        == "no_agent_llm_scope_term_match"
    )


def test_fetch_uses_contact_identity_api_key_and_cursor_pagination() -> None:
    from paper_hub.connectors.openalex import OpenAlexConnector

    fixture = _fixture()
    requests: list[httpx.Request] = []

    def handler(request: httpx.Request) -> httpx.Response:
        requests.append(request)
        cursor = request.url.params["cursor"]
        if cursor == "*":
            payload = {
                "meta": {"next_cursor": "next-page"},
                "results": [fixture["results"][0]],
            }
        else:
            payload = {
                "meta": {"next_cursor": None},
                "results": [fixture["results"][1]],
            }
        return httpx.Response(200, json=payload, request=request)

    client = httpx.Client(
        transport=httpx.MockTransport(handler),
        base_url="https://api.openalex.org",
    )
    connector = OpenAlexConnector(
        contact_email=CONTACT_EMAIL,
        api_key=SecretStr(API_KEY),
        client=client,
    )

    records = list(
        connector.fetch_records(
            query="LLM agent benchmark",
            from_date=date(2026, 1, 1),
            max_results=2,
        )
    )

    assert [record.source_record_id for record in records] == [
        "W2741809807",
        "W9988776655",
    ]
    assert len(requests) == 2
    assert requests[0].headers["user-agent"] == (
        f"paper-hub/0.1.0 (mailto:{CONTACT_EMAIL})"
    )
    assert requests[0].url.params["mailto"] == CONTACT_EMAIL
    assert requests[0].url.params["api_key"] == API_KEY
    assert requests[0].url.params["search"] == "LLM agent benchmark"
    assert requests[0].url.params["filter"] == (
        "from_publication_date:2026-01-01"
    )
    assert requests[0].url.params["cursor"] == "*"
    assert requests[1].url.params["cursor"] == "next-page"


def test_fetch_enforces_strict_max_results_limit() -> None:
    from paper_hub.connectors.openalex import (
        OPENALEX_MAX_RESULTS,
        OpenAlexConnector,
    )

    connector = OpenAlexConnector(
        contact_email=CONTACT_EMAIL,
        client=httpx.Client(transport=httpx.MockTransport(lambda request: None)),
    )

    with pytest.raises(ValueError, match="max_results"):
        list(
            connector.fetch_records(
                query="agents",
                max_results=OPENALEX_MAX_RESULTS + 1,
            )
        )


def test_timeout_raises_explicit_connector_error() -> None:
    from paper_hub.connectors.openalex import (
        OpenAlexConnector,
        OpenAlexTimeoutError,
    )

    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ReadTimeout("timed out", request=request)

    connector = OpenAlexConnector(
        contact_email=CONTACT_EMAIL,
        client=httpx.Client(transport=httpx.MockTransport(handler)),
    )

    with pytest.raises(OpenAlexTimeoutError, match="timed out"):
        list(connector.fetch_records(query="agents", max_results=1))


def test_429_and_non_2xx_errors_are_explicit_and_do_not_leak_api_key() -> None:
    from paper_hub.connectors.openalex import (
        OpenAlexConnector,
        OpenAlexRateLimitError,
        OpenAlexResponseError,
    )

    def rate_limited(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            429,
            headers={"Retry-After": "17"},
            json={"error": "rate limited"},
            request=request,
        )

    rate_connector = OpenAlexConnector(
        contact_email=CONTACT_EMAIL,
        api_key=SecretStr(API_KEY),
        client=httpx.Client(transport=httpx.MockTransport(rate_limited)),
    )
    with pytest.raises(OpenAlexRateLimitError) as rate_exc:
        list(rate_connector.fetch_records(query="agents", max_results=1))
    assert rate_exc.value.retry_after == "17"
    assert API_KEY not in str(rate_exc.value)

    def unavailable(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            503,
            text="upstream unavailable",
            request=request,
        )

    unavailable_connector = OpenAlexConnector(
        contact_email=CONTACT_EMAIL,
        api_key=SecretStr(API_KEY),
        client=httpx.Client(transport=httpx.MockTransport(unavailable)),
    )
    with pytest.raises(OpenAlexResponseError, match="503") as response_exc:
        list(unavailable_connector.fetch_records(query="agents", max_results=1))
    assert API_KEY not in str(response_exc.value)


def test_openalex_configuration_requires_email_and_hides_api_key() -> None:
    from paper_hub.config import Settings
    from paper_hub.connectors.openalex import OpenAlexConnector

    with pytest.raises(ValueError, match="contact email"):
        OpenAlexConnector(contact_email="")

    settings = Settings(
        database_url=(
            "postgresql+psycopg://paper_hub:paper_hub@localhost/paper_hub"
        ),
        openalex_contact_email=CONTACT_EMAIL,
        openalex_api_key=API_KEY,
        _env_file=None,
    )
    assert isinstance(settings.openalex_api_key, SecretStr)
    assert API_KEY not in repr(settings)

    with pytest.raises(ValidationError):
        Settings(
            database_url=(
                "postgresql+psycopg://paper_hub:paper_hub@localhost/paper_hub"
            ),
            openalex_contact_email="not-an-email",
            _env_file=None,
        )


def test_malformed_openalex_record_is_an_explicit_connector_error() -> None:
    from paper_hub.connectors.openalex import (
        OpenAlexConnector,
        OpenAlexError,
        OpenAlexParseError,
    )

    raw = _fixture()["results"][0]
    malformed = json.loads(json.dumps(raw))
    malformed["authorships"][0]["is_corresponding"] = "false"

    assert issubclass(OpenAlexParseError, OpenAlexError)
    with pytest.raises(
        OpenAlexParseError,
        match="is_corresponding",
    ):
        OpenAlexConnector.parse_record(malformed)
