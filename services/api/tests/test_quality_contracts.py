from datetime import UTC, datetime
from pathlib import Path
from uuid import uuid4

import pytest
from pydantic import SecretStr, ValidationError
from sqlalchemy import CheckConstraint, inspect


def _provenance() -> dict[str, object]:
    return {
        "source": "openalex",
        "retrieved_at": datetime(2026, 7, 15, 8, 30, tzinfo=UTC),
    }


@pytest.mark.parametrize(
    "raw",
    [
        "10.1000/foo bar",
        "https://doi.org/10.1000/foo\tbar",
        "doi:10.1000/foo\nbar",
    ],
)
def test_doi_internal_whitespace_is_rejected(raw: str) -> None:
    from paper_hub.normalization import normalize_doi

    assert normalize_doi(raw) is None


@pytest.mark.parametrize(
    "raw",
    [
        "2401.01 234v1",
        "arXiv:2401.01234 v3",
        "not-an-arxiv-id",
    ],
)
def test_invalid_arxiv_ids_are_rejected(raw: str) -> None:
    from paper_hub.normalization import normalize_arxiv_id

    assert normalize_arxiv_id(raw) is None


@pytest.mark.parametrize(
    "record",
    [
        {"doi": "not-a-doi"},
        {"openreview_forum_id": "forum id"},
        {"openalex_id": "A123"},
        {"semantic_scholar_paper_id": "abc123"},
    ],
)
def test_invalid_external_identifiers_do_not_generate_identity(
    record: dict[str, str],
) -> None:
    from paper_hub.normalization import canonical_identity

    assert canonical_identity(record) is None


@pytest.mark.parametrize(
    ("raw", "expected"),
    [
        ("DOI:10.1000/ABC", "doi:10.1000/abc"),
        ("arXiv:2401.01234V3", "arxiv:2401.01234"),
        (
            "OPENALEX:https://openalex.org/w1234567890",
            "openalex:W1234567890",
        ),
        (
            "S2:A0B1C2D3E4F5678901234567890ABCDEFFEDCBA9",
            "s2:a0b1c2d3e4f5678901234567890abcdeffedcba9",
        ),
    ],
)
def test_work_schema_normalizes_canonical_key(
    raw: str,
    expected: str,
) -> None:
    from paper_hub.schemas import WorkCreate

    work = WorkCreate(
        canonical_key=raw,
        title="Planning Agents with Tool Use",
        **_provenance(),
    )

    assert work.canonical_key == expected


@pytest.mark.parametrize(
    "canonical_key",
    [
        "doi:not-a-doi",
        "doi:10.1000/foo bar",
        "arxiv:not-an-id",
        "openreview:forum id",
        "openalex:A123",
        "s2:abc123",
    ],
)
def test_work_schema_rejects_malformed_canonical_keys(
    canonical_key: str,
) -> None:
    from paper_hub.schemas import WorkCreate

    with pytest.raises(ValidationError):
        WorkCreate(
            canonical_key=canonical_key,
            title="Planning Agents with Tool Use",
            **_provenance(),
        )


def test_schema_forbids_extra_fields_and_naive_datetimes() -> None:
    from paper_hub.schemas import PaperVersionCreate, WorkCreate

    with pytest.raises(ValidationError):
        WorkCreate(
            canonical_key="doi:10.1000/example",
            title="Planning Agents with Tool Use",
            source="openalex",
            retrieved_at=datetime(2026, 7, 15, 8, 30),
        )

    submitted_at = datetime(2026, 7, 14, 8, 30, tzinfo=UTC)
    version = PaperVersionCreate(
        work_id=uuid4(),
        version_label="v1",
        version_number=1,
        version_type="preprint",
        submitted_at=submitted_at,
        source="arxiv",
        retrieved_at=datetime(2026, 7, 15, 8, 30, tzinfo=UTC),
    )
    assert version.submitted_at == submitted_at

    with pytest.raises(ValidationError):
        PaperVersionCreate(
            work_id=uuid4(),
            version_label="v1",
            version_type="preprint",
            submitted_at=datetime(2026, 7, 14, 8, 30),
            source="arxiv",
            retrieved_at=datetime(2026, 7, 15, 8, 30, tzinfo=UTC),
        )

    with pytest.raises(ValidationError):
        WorkCreate(
            canonical_key="doi:10.1000/example",
            title="Planning Agents with Tool Use",
            unexpected="forbidden",
            **_provenance(),
        )


def test_field_assertion_schema_uses_uuid_and_json_safe_value() -> None:
    from paper_hub.schemas import FieldAssertionCreate

    source_record_id = uuid4()
    assertion = FieldAssertionCreate(
        source_record_id=source_record_id,
        field_name="authors",
        value={"names": ["Ada", "Grace"], "verified": True},
        parser_version="openalex-v1",
        **_provenance(),
    )
    assert assertion.source_record_id == source_record_id

    with pytest.raises(ValidationError):
        FieldAssertionCreate(
            source_record_id="W123",
            field_name="title",
            value="Planning Agents",
            parser_version="openalex-v1",
            **_provenance(),
        )

    for unsafe_value in ({1, 2}, object()):
        with pytest.raises(ValidationError):
            FieldAssertionCreate(
                source_record_id=source_record_id,
                field_name="unsafe",
                value=unsafe_value,
                parser_version="openalex-v1",
                **_provenance(),
            )


def test_database_url_is_required_secret_and_stable(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    from paper_hub.config import Settings, get_settings

    monkeypatch.delenv("DATABASE_URL", raising=False)
    get_settings.cache_clear()
    with pytest.raises(ValidationError):
        Settings(_env_file=None)

    password = "do-not-leak"
    settings = Settings(
        database_url=(
            f"postgresql+psycopg://paper_hub:{password}@db/paper_hub"
        ),
        _env_file=None,
    )
    assert isinstance(settings.database_url, SecretStr)
    assert password not in repr(settings)
    assert Path(Settings.model_config["env_file"]).is_absolute()
    assert Settings.model_config["hide_input_in_errors"] is True

    with pytest.raises(ValidationError) as exc_info:
        Settings(
            database_url=f"sqlite://paper_hub:{password}@db/paper_hub",
            _env_file=None,
        )
    assert password not in str(exc_info.value)


def test_alembic_requires_database_url_and_compares_server_defaults() -> None:
    service_root = Path(__file__).resolve().parents[1]
    alembic_ini = (service_root / "alembic.ini").read_text()
    env_source = (service_root / "alembic" / "env.py").read_text()

    assert "sqlalchemy.url" not in alembic_ini
    assert env_source.count("compare_server_default=True") == 2


def test_ownership_constraints_prevent_cross_work_pollution() -> None:
    from paper_hub.models import (
        Base,
        ExternalIdentifier,
        PaperVersion,
        SourceRecord,
        Work,
    )

    source_record = Base.metadata.tables["source_record"]
    external_identifier = Base.metadata.tables["external_identifier"]
    field_assertion = Base.metadata.tables["field_assertion"]

    assert {"source_record_id"} == {
        foreign_key.parent.name for foreign_key in field_assertion.foreign_keys
    }
    assert {
        (foreign_key.parent.name, foreign_key.target_fullname)
        for foreign_key in source_record.foreign_keys
    } == {
        ("work_id", "work.id"),
        ("paper_version_id", "paper_version.id"),
    }
    assert all(
        len(constraint.elements) == 1
        for constraint in source_record.foreign_key_constraints
    )

    source_record_checks = {
        constraint.name
        for constraint in source_record.constraints
        if isinstance(constraint, CheckConstraint)
    }
    assert "ck_source_record_single_owner" in source_record_checks

    assert inspect(Work).relationships.source_records.remote_side == {
        source_record.c.work_id
    }
    assert inspect(PaperVersion).relationships.source_records.remote_side == {
        source_record.c.paper_version_id
    }
    assert inspect(SourceRecord).relationships.work.local_columns == {
        source_record.c.work_id
    }
    assert inspect(SourceRecord).relationships.paper_version.local_columns == {
        source_record.c.paper_version_id
    }
    assert {
        (foreign_key.parent.name, foreign_key.target_fullname)
        for foreign_key in external_identifier.foreign_keys
    } == {("work_id", "work.id")}
    assert set(inspect(ExternalIdentifier).relationships.keys()) == {"work"}
    assert "external_identifiers" not in inspect(PaperVersion).relationships
    assert "external_identifiers" not in inspect(SourceRecord).relationships


def test_model_check_constraints_cover_numeric_domains() -> None:
    from paper_hub.models import Base

    expected = {
        "paper_version": {"ck_paper_version_version_number_positive"},
        "source_record": {"ck_source_record_single_owner"},
        "field_assertion": {"ck_field_assertion_confidence_range"},
        "metric_snapshot": {
            "ck_metric_snapshot_single_target",
            "ck_metric_snapshot_window_days_positive",
        },
        "ranking_snapshot": {
            "ck_ranking_snapshot_exactly_one_subject",
            "ck_ranking_snapshot_rank_position_positive",
            "ck_ranking_snapshot_window_days_positive",
        },
    }

    for table_name, expected_names in expected.items():
        actual_names = {
            constraint.name
            for constraint in Base.metadata.tables[table_name].constraints
            if isinstance(constraint, CheckConstraint)
        }
        assert expected_names <= actual_names


def test_relationship_delete_configuration_matches_database_cascades() -> None:
    from paper_hub.models import (
        CodeRepository,
        Method,
        PaperVersion,
        SourceRecord,
        Topic,
        Work,
    )

    required_delete_orphan = {
        inspect(Work).relationships.versions,
        inspect(Work).relationships.external_identifiers,
        inspect(Work).relationships.code_repositories,
        inspect(Work).relationships.metric_snapshots,
        inspect(Work).relationships.ranking_snapshots,
        inspect(SourceRecord).relationships.field_assertions,
        inspect(CodeRepository).relationships.metric_snapshots,
        inspect(Topic).relationships.ranking_snapshots,
        inspect(Method).relationships.ranking_snapshots,
    }
    for relationship in required_delete_orphan:
        assert relationship.passive_deletes is True
        assert "delete" in relationship.cascade
        assert "delete-orphan" in relationship.cascade

    for relationship in (
        inspect(Work).relationships.source_records,
        inspect(PaperVersion).relationships.source_records,
    ):
        assert relationship.passive_deletes is True
        assert "delete" not in relationship.cascade
