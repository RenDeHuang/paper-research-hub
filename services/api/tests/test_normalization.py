from datetime import UTC, datetime
from io import StringIO
from pathlib import Path

import pytest
from pydantic import ValidationError


@pytest.mark.parametrize(
    ("raw", "expected"),
    [
        (" https://doi.org/10.1000/ABC ", "10.1000/abc"),
        ("HTTPS://DOI.ORG/ 10.1000/ABC", "10.1000/abc"),
        (" doi: 10.1000/ABC ", "10.1000/abc"),
        ("10.1000/ABC", "10.1000/abc"),
    ],
)
def test_doi_normalization_removes_prefixes_whitespace_and_case(
    raw: str,
    expected: str,
) -> None:
    from paper_hub.normalization import normalize_doi

    assert normalize_doi(raw) == expected


@pytest.mark.parametrize(
    ("raw", "expected"),
    [
        ("2401.01234v1", "2401.01234"),
        ("2401.01234v3", "2401.01234"),
        (" arXiv:2401.01234V12 ", "2401.01234"),
    ],
)
def test_arxiv_normalization_removes_version_suffix(
    raw: str,
    expected: str,
) -> None:
    from paper_hub.normalization import normalize_arxiv_id

    assert normalize_arxiv_id(raw) == expected


def test_identical_dois_share_a_canonical_identity() -> None:
    from paper_hub.normalization import canonical_identity

    first = canonical_identity({"doi": "https://doi.org/10.1000/ABC"})
    second = canonical_identity({"doi": "doi:10.1000/abc"})

    assert first == second == "doi:10.1000/abc"


def test_arxiv_versions_share_a_canonical_identity() -> None:
    from paper_hub.normalization import canonical_identity

    first = canonical_identity({"arxiv_id": "2401.01234v1"})
    second = canonical_identity({"arxiv_id": "2401.01234v3"})

    assert first == second == "arxiv:2401.01234"


def test_similar_titles_do_not_generate_a_canonical_identity() -> None:
    from paper_hub.normalization import canonical_identity

    first = canonical_identity({"title": "Planning Agents with Tool Use"})
    second = canonical_identity({"title": "Planning Agent with Tool-Use"})

    assert first is None
    assert second is None


def test_field_assertion_schema_preserves_provenance_and_licenses() -> None:
    from paper_hub.schemas import FieldAssertionCreate

    retrieved_at = datetime(2026, 7, 15, 8, 30, tzinfo=UTC)

    assertion = FieldAssertionCreate(
        field_name="title",
        value="Planning Agents with Tool Use",
        source="openalex",
        source_record_id="W123",
        source_url="https://example.test/works/W123",
        retrieved_at=retrieved_at,
        source_license="CC0",
        content_license="CC BY 4.0",
        parser_version="openalex-v1",
    )

    assert assertion.source == "openalex"
    assert assertion.source_record_id == "W123"
    assert assertion.retrieved_at == retrieved_at
    assert assertion.source_license == "CC0"
    assert assertion.content_license == "CC BY 4.0"
    assert assertion.parser_version == "openalex-v1"


def test_database_configuration_rejects_non_postgresql_urls() -> None:
    from paper_hub.config import Settings

    settings = Settings(
        database_url=(
            "postgresql+psycopg://paper_hub:paper_hub@localhost:5432/paper_hub"
        ),
        _env_file=None,
    )

    assert settings.database_url.startswith("postgresql")
    with pytest.raises(ValidationError):
        Settings(database_url="sqlite:///paper_hub.db", _env_file=None)


def test_core_models_have_provenance_status_constraints_and_relationships() -> None:
    from sqlalchemy import UniqueConstraint

    from paper_hub.models import Base

    required_tables = {
        "work",
        "paper_version",
        "source_record",
        "external_identifier",
        "field_assertion",
        "topic",
        "method",
        "dataset",
        "benchmark",
        "code_repository",
        "metric_snapshot",
        "ranking_snapshot",
    }
    provenance_columns = {
        "source",
        "source_license",
        "content_license",
        "retrieved_at",
        "status",
    }

    assert required_tables <= set(Base.metadata.tables)
    for table_name in required_tables:
        table = Base.metadata.tables[table_name]
        assert provenance_columns <= set(table.columns.keys())
        assert any(
            isinstance(constraint, UniqueConstraint)
            for constraint in table.constraints
        ), f"{table_name} must define a unique constraint"

    assert {
        foreign_key.target_fullname
        for foreign_key in Base.metadata.tables["paper_version"].foreign_keys
    } == {"work.id"}
    assert {
        foreign_key.target_fullname
        for foreign_key in Base.metadata.tables["field_assertion"].foreign_keys
    } >= {"work.id", "source_record.id"}
    assert {
        foreign_key.target_fullname
        for foreign_key in Base.metadata.tables["code_repository"].foreign_keys
    } >= {"work.id"}

    external_identifier = Base.metadata.tables["external_identifier"]
    assert any(
        {column.name for column in constraint.columns}
        == {"scheme", "normalized_value"}
        for constraint in external_identifier.constraints
        if isinstance(constraint, UniqueConstraint)
    )


def test_model_metadata_compiles_with_postgresql_dialect() -> None:
    from sqlalchemy.dialects import postgresql
    from sqlalchemy.schema import CreateTable

    from paper_hub.models import Base

    dialect = postgresql.dialect()

    for table in Base.metadata.sorted_tables:
        sql = str(CreateTable(table).compile(dialect=dialect))
        assert "CREATE TABLE" in sql

    source_record_sql = str(
        CreateTable(Base.metadata.tables["source_record"]).compile(dialect=dialect)
    )
    assert "JSONB" in source_record_sql


def test_alembic_initial_migration_generates_postgresql_sql() -> None:
    from alembic import command
    from alembic.config import Config

    service_root = Path(__file__).resolve().parents[1]
    output = StringIO()
    config = Config(service_root / "alembic.ini", output_buffer=output)

    command.upgrade(config, "head", sql=True)

    migration_sql = output.getvalue()
    assert "CREATE TYPE record_status" in migration_sql
    assert "CREATE TABLE work" in migration_sql
    assert "CREATE TABLE paper_version" in migration_sql
    assert "CREATE TABLE ranking_snapshot" in migration_sql
