from datetime import UTC, datetime
from io import StringIO
from pathlib import Path
from uuid import UUID

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


@pytest.mark.parametrize(
    ("record", "expected"),
    [
        (
            {"openreview_forum_id": "Forum_AbC123"},
            "openreview:Forum_AbC123",
        ),
        (
            {"openalex_id": "https://openalex.org/w1234567890"},
            "openalex:W1234567890",
        ),
        (
            {
                "semantic_scholar_paper_id": (
                    "A0B1C2D3E4F5678901234567890ABCDEFFEDCBA9"
                )
            },
            "s2:a0b1c2d3e4f5678901234567890abcdeffedcba9",
        ),
    ],
)
def test_explicit_external_ids_generate_controlled_canonical_identities(
    record: dict[str, str],
    expected: str,
) -> None:
    from paper_hub.normalization import canonical_identity

    assert canonical_identity(record) == expected


@pytest.mark.parametrize(
    ("record", "expected"),
    [
        (
            {
                "arxiv_id": "not-an-arxiv-id",
                "arxiv": "2401.01234v3",
            },
            "arxiv:2401.01234",
        ),
        (
            {
                "openreview_forum_id": " ",
                "openreview_id": "Forum_Backup123",
            },
            "openreview:Forum_Backup123",
        ),
        (
            {
                "semantic_scholar_paper_id": "invalid",
                "semantic_scholar_id": (
                    "A0B1C2D3E4F5678901234567890ABCDEFFEDCBA9"
                ),
            },
            "s2:a0b1c2d3e4f5678901234567890abcdeffedcba9",
        ),
    ],
)
def test_invalid_primary_alias_does_not_hide_valid_fallback(
    record: dict[str, str],
    expected: str,
) -> None:
    from paper_hub.normalization import canonical_identity

    assert canonical_identity(record) == expected


def test_similar_titles_do_not_generate_a_canonical_identity() -> None:
    from paper_hub.normalization import canonical_identity

    first = canonical_identity({"title": "Planning Agents with Tool Use"})
    second = canonical_identity({"title": "Planning Agent with Tool-Use"})

    assert first is None
    assert second is None


def test_arbitrary_canonical_key_is_not_accepted_as_identity_evidence() -> None:
    from paper_hub.normalization import canonical_identity

    assert canonical_identity({"canonical_key": "title:planning-agents"}) is None
    assert canonical_identity({"canonical_key": "custom:record-123"}) is None


def test_field_assertion_schema_preserves_provenance_and_licenses() -> None:
    from paper_hub.schemas import FieldAssertionCreate

    retrieved_at = datetime(2026, 7, 15, 8, 30, tzinfo=UTC)
    source_record_id = UUID("c67b8c4d-150f-43c4-9e2e-861695f0fa4e")

    assertion = FieldAssertionCreate(
        field_name="title",
        value="Planning Agents with Tool Use",
        source="openalex",
        source_record_id=source_record_id,
        source_url="https://example.test/works/W123",
        retrieved_at=retrieved_at,
        source_license="CC0",
        content_license="CC BY 4.0",
        parser_version="openalex-v1",
    )

    assert assertion.source == "openalex"
    assert assertion.source_record_id == source_record_id
    assert assertion.retrieved_at == retrieved_at
    assert assertion.source_license == "CC0"
    assert assertion.content_license == "CC BY 4.0"
    assert assertion.parser_version == "openalex-v1"


@pytest.mark.parametrize(
    "canonical_key",
    [
        "doi:10.1000/example",
        "arxiv:2401.01234",
        "openreview:Forum_AbC123",
        "openalex:W1234567890",
        "s2:a0b1c2d3e4f5678901234567890abcdeffedcba9",
    ],
)
def test_work_schema_accepts_only_approved_canonical_prefixes(
    canonical_key: str,
) -> None:
    from paper_hub.schemas import WorkCreate

    work = WorkCreate(
        canonical_key=canonical_key,
        title="Planning Agents with Tool Use",
        source="openalex",
        retrieved_at=datetime(2026, 7, 15, 8, 30, tzinfo=UTC),
    )

    assert work.canonical_key == canonical_key


@pytest.mark.parametrize(
    "canonical_key",
    [
        "title:planning-agents",
        "custom:record-123",
        "doi:",
        "openreview:",
        "openalex:   ",
        " s2:a0b1c2d3",
    ],
)
def test_work_schema_rejects_unapproved_or_empty_canonical_keys(
    canonical_key: str,
) -> None:
    from paper_hub.schemas import WorkCreate

    with pytest.raises(ValidationError):
        WorkCreate(
            canonical_key=canonical_key,
            title="Planning Agents with Tool Use",
            source="openalex",
            retrieved_at=datetime(2026, 7, 15, 8, 30, tzinfo=UTC),
        )


def test_work_table_enforces_approved_canonical_key_prefixes() -> None:
    from sqlalchemy import CheckConstraint

    from paper_hub.models import Base

    constraints = {
        constraint.name: str(constraint.sqltext)
        for constraint in Base.metadata.tables["work"].constraints
        if isinstance(constraint, CheckConstraint)
    }

    assert "ck_work_canonical_key_approved_prefix" in constraints
    assert "canonical_key" in constraints["ck_work_canonical_key_approved_prefix"]
    assert "openreview" in constraints["ck_work_canonical_key_approved_prefix"]
    assert "openalex" in constraints["ck_work_canonical_key_approved_prefix"]
    assert "s2" in constraints["ck_work_canonical_key_approved_prefix"]


def test_database_configuration_rejects_non_postgresql_urls() -> None:
    from paper_hub.config import Settings

    settings = Settings(
        database_url=(
            "postgresql+psycopg://paper_hub:paper_hub@localhost:5432/paper_hub"
        ),
        _env_file=None,
    )

    assert settings.database_url.get_secret_value().startswith("postgresql")
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
    } == {"source_record.id"}
    assert {
        foreign_key.target_fullname
        for foreign_key in Base.metadata.tables["code_repository"].foreign_keys
    } >= {"work.id"}

    external_identifier = Base.metadata.tables["external_identifier"]
    assert {
        foreign_key.target_fullname
        for foreign_key in external_identifier.foreign_keys
    } == {"work.id"}
    assert "paper_version_id" not in external_identifier.columns
    assert "source_record_id" not in external_identifier.columns
    assert any(
        {column.name for column in constraint.columns}
        == {"scheme", "normalized_value"}
        for constraint in external_identifier.constraints
        if isinstance(constraint, UniqueConstraint)
    )


def test_ranking_snapshot_uses_relational_exactly_one_subject_model() -> None:
    from sqlalchemy import CheckConstraint, UniqueConstraint, inspect

    from paper_hub.models import Base, RankingSnapshot

    table = Base.metadata.tables["ranking_snapshot"]
    assert {"work_id", "topic_id", "method_id"} <= set(table.columns.keys())
    assert "subject_id" not in table.columns
    assert "subject_type" not in table.columns
    assert {
        foreign_key.target_fullname for foreign_key in table.foreign_keys
    } == {"work.id", "topic.id", "method.id"}

    check_constraints = {
        constraint.name: str(constraint.sqltext)
        for constraint in table.constraints
        if isinstance(constraint, CheckConstraint)
    }
    exactly_one = check_constraints[
        "ck_ranking_snapshot_exactly_one_subject"
    ]
    assert "work_id IS NOT NULL" in exactly_one
    assert "topic_id IS NOT NULL" in exactly_one
    assert "method_id IS NOT NULL" in exactly_one

    unique_column_sets = {
        frozenset(column.name for column in constraint.columns)
        for constraint in table.constraints
        if isinstance(constraint, UniqueConstraint)
    }
    for subject_column in ("work_id", "topic_id", "method_id"):
        assert frozenset(
            {
                "ranking_name",
                subject_column,
                "window_days",
                "computed_at",
            }
        ) in unique_column_sets

    assert {"work", "topic", "method"} <= set(
        inspect(RankingSnapshot).relationships.keys()
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


def test_alembic_initial_migration_generates_postgresql_sql(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    from alembic import command
    from alembic.config import Config
    from paper_hub.config import get_settings

    service_root = Path(__file__).resolve().parents[1]
    output = StringIO()
    config = Config(service_root / "alembic.ini", output_buffer=output)
    monkeypatch.setenv(
        "DATABASE_URL",
        "postgresql+psycopg://paper_hub:test@db/paper_hub",
    )
    get_settings.cache_clear()

    command.upgrade(config, "head", sql=True)

    migration_sql = output.getvalue()
    assert "CREATE TYPE record_status" in migration_sql
    assert "CREATE TABLE work" in migration_sql
    assert "CREATE TABLE paper_version" in migration_sql
    assert "CREATE TABLE ranking_snapshot" in migration_sql
    assert "CONSTRAINT ck_work_canonical_key_approved_prefix CHECK" in migration_sql
    assert "0002_enforce_canonical_integrity" in migration_sql

    assert "ALTER TABLE ranking_snapshot DROP COLUMN subject_id" in migration_sql
    assert "ALTER TABLE ranking_snapshot DROP COLUMN subject_type" in migration_sql
    assert "ALTER TABLE ranking_snapshot ADD COLUMN work_id UUID" in migration_sql
    assert "ALTER TABLE ranking_snapshot ADD COLUMN topic_id UUID" in migration_sql
    assert "ALTER TABLE ranking_snapshot ADD COLUMN method_id UUID" in migration_sql
    assert (
        "FOREIGN KEY(work_id) REFERENCES work (id) ON DELETE CASCADE"
        in migration_sql
    )
    assert (
        "FOREIGN KEY(topic_id) REFERENCES topic (id) ON DELETE CASCADE"
        in migration_sql
    )
    assert (
        "FOREIGN KEY(method_id) REFERENCES method (id) ON DELETE CASCADE"
        in migration_sql
    )
    assert (
        "CONSTRAINT ck_ranking_snapshot_exactly_one_subject CHECK"
        in migration_sql
    )
