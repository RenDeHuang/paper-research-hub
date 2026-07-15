from __future__ import annotations

from datetime import date, datetime
from decimal import Decimal
from enum import Enum
from typing import Any
from uuid import UUID, uuid4

from sqlalchemy import (
    Boolean,
    CheckConstraint,
    Column,
    Date,
    DateTime,
    Enum as SqlEnum,
    ForeignKey,
    Index,
    Integer,
    Numeric,
    String,
    Table,
    Text,
    UniqueConstraint,
    Uuid,
    func,
)
from sqlalchemy.dialects.postgresql import JSONB
from sqlalchemy.orm import Mapped, mapped_column, relationship

from paper_hub.db import Base


class RecordStatus(str, Enum):
    ACTIVE = "active"
    CORRECTED = "corrected"
    EXPRESSION_OF_CONCERN = "expression_of_concern"
    RETRACTED = "retracted"
    WITHDRAWN = "withdrawn"
    DESK_REJECTED = "desk_rejected"
    SUPERSEDED = "superseded"


RECORD_STATUS_ENUM = SqlEnum(
    RecordStatus,
    name="record_status",
    values_callable=lambda enum_class: [member.value for member in enum_class],
)


class UUIDPrimaryKeyMixin:
    id: Mapped[UUID] = mapped_column(Uuid, primary_key=True, default=uuid4)


class TimestampMixin:
    created_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True),
        nullable=False,
        server_default=func.now(),
    )
    updated_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True),
        nullable=False,
        server_default=func.now(),
        onupdate=func.now(),
    )


class ProvenanceMixin:
    source: Mapped[str] = mapped_column(String(64), nullable=False)
    source_url: Mapped[str | None] = mapped_column(Text)
    retrieved_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True),
        nullable=False,
        server_default=func.now(),
    )
    source_license: Mapped[str | None] = mapped_column(String(128))
    content_license: Mapped[str | None] = mapped_column(String(128))
    status: Mapped[RecordStatus] = mapped_column(
        RECORD_STATUS_ENUM,
        nullable=False,
        default=RecordStatus.ACTIVE,
        server_default=RecordStatus.ACTIVE.value,
    )


work_topic = Table(
    "work_topic",
    Base.metadata,
    Column(
        "work_id",
        Uuid,
        ForeignKey("work.id", ondelete="CASCADE"),
        primary_key=True,
    ),
    Column(
        "topic_id",
        Uuid,
        ForeignKey("topic.id", ondelete="CASCADE"),
        primary_key=True,
    ),
)

work_method = Table(
    "work_method",
    Base.metadata,
    Column(
        "work_id",
        Uuid,
        ForeignKey("work.id", ondelete="CASCADE"),
        primary_key=True,
    ),
    Column(
        "method_id",
        Uuid,
        ForeignKey("method.id", ondelete="CASCADE"),
        primary_key=True,
    ),
)

work_dataset = Table(
    "work_dataset",
    Base.metadata,
    Column(
        "work_id",
        Uuid,
        ForeignKey("work.id", ondelete="CASCADE"),
        primary_key=True,
    ),
    Column(
        "dataset_id",
        Uuid,
        ForeignKey("dataset.id", ondelete="CASCADE"),
        primary_key=True,
    ),
)

work_benchmark = Table(
    "work_benchmark",
    Base.metadata,
    Column(
        "work_id",
        Uuid,
        ForeignKey("work.id", ondelete="CASCADE"),
        primary_key=True,
    ),
    Column(
        "benchmark_id",
        Uuid,
        ForeignKey("benchmark.id", ondelete="CASCADE"),
        primary_key=True,
    ),
)


class Work(UUIDPrimaryKeyMixin, ProvenanceMixin, TimestampMixin, Base):
    __tablename__ = "work"
    __table_args__ = (
        UniqueConstraint("canonical_key", name="uq_work_canonical_key"),
    )

    canonical_key: Mapped[str] = mapped_column(String(512), nullable=False)
    title: Mapped[str] = mapped_column(Text, nullable=False)
    abstract: Mapped[str | None] = mapped_column(Text)
    publication_date: Mapped[date | None] = mapped_column(Date)

    versions: Mapped[list[PaperVersion]] = relationship(
        back_populates="work",
        cascade="all, delete-orphan",
    )
    source_records: Mapped[list[SourceRecord]] = relationship(
        back_populates="work",
    )
    external_identifiers: Mapped[list[ExternalIdentifier]] = relationship(
        back_populates="work",
        cascade="all, delete-orphan",
    )
    field_assertions: Mapped[list[FieldAssertion]] = relationship(
        back_populates="work",
        cascade="all, delete-orphan",
    )
    topics: Mapped[list[Topic]] = relationship(
        secondary=work_topic,
        back_populates="works",
    )
    methods: Mapped[list[Method]] = relationship(
        secondary=work_method,
        back_populates="works",
    )
    datasets: Mapped[list[Dataset]] = relationship(
        secondary=work_dataset,
        back_populates="works",
    )
    benchmarks: Mapped[list[Benchmark]] = relationship(
        secondary=work_benchmark,
        back_populates="works",
    )
    code_repositories: Mapped[list[CodeRepository]] = relationship(
        back_populates="work",
        cascade="all, delete-orphan",
    )
    metric_snapshots: Mapped[list[MetricSnapshot]] = relationship(
        back_populates="work",
    )


class PaperVersion(UUIDPrimaryKeyMixin, ProvenanceMixin, TimestampMixin, Base):
    __tablename__ = "paper_version"
    __table_args__ = (
        UniqueConstraint(
            "work_id",
            "version_label",
            name="uq_paper_version_work_label",
        ),
    )

    work_id: Mapped[UUID] = mapped_column(
        ForeignKey("work.id", ondelete="CASCADE"),
        nullable=False,
    )
    version_label: Mapped[str] = mapped_column(String(64), nullable=False)
    version_number: Mapped[int | None] = mapped_column(Integer)
    version_type: Mapped[str] = mapped_column(String(32), nullable=False)
    title: Mapped[str | None] = mapped_column(Text)
    abstract: Mapped[str | None] = mapped_column(Text)
    submitted_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    published_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True))
    version_url: Mapped[str | None] = mapped_column(Text)

    work: Mapped[Work] = relationship(back_populates="versions")
    source_records: Mapped[list[SourceRecord]] = relationship(
        back_populates="paper_version",
    )
    external_identifiers: Mapped[list[ExternalIdentifier]] = relationship(
        back_populates="paper_version",
    )
    field_assertions: Mapped[list[FieldAssertion]] = relationship(
        back_populates="paper_version",
    )


class SourceRecord(UUIDPrimaryKeyMixin, ProvenanceMixin, TimestampMixin, Base):
    __tablename__ = "source_record"
    __table_args__ = (
        UniqueConstraint(
            "source",
            "source_record_id",
            "content_hash",
            name="uq_source_record_source_record_hash",
        ),
    )

    work_id: Mapped[UUID | None] = mapped_column(
        ForeignKey("work.id", ondelete="SET NULL"),
    )
    paper_version_id: Mapped[UUID | None] = mapped_column(
        ForeignKey("paper_version.id", ondelete="SET NULL"),
    )
    source_record_id: Mapped[str] = mapped_column(String(255), nullable=False)
    content_hash: Mapped[str] = mapped_column(String(64), nullable=False)
    raw_payload: Mapped[dict[str, Any]] = mapped_column(JSONB, nullable=False)
    http_status: Mapped[int | None] = mapped_column(Integer)

    work: Mapped[Work | None] = relationship(back_populates="source_records")
    paper_version: Mapped[PaperVersion | None] = relationship(
        back_populates="source_records",
    )
    external_identifiers: Mapped[list[ExternalIdentifier]] = relationship(
        back_populates="source_record",
    )
    field_assertions: Mapped[list[FieldAssertion]] = relationship(
        back_populates="source_record",
    )


class ExternalIdentifier(
    UUIDPrimaryKeyMixin,
    ProvenanceMixin,
    TimestampMixin,
    Base,
):
    __tablename__ = "external_identifier"
    __table_args__ = (
        UniqueConstraint(
            "scheme",
            "normalized_value",
            name="uq_external_identifier_scheme_value",
        ),
    )

    work_id: Mapped[UUID] = mapped_column(
        ForeignKey("work.id", ondelete="CASCADE"),
        nullable=False,
    )
    paper_version_id: Mapped[UUID | None] = mapped_column(
        ForeignKey("paper_version.id", ondelete="CASCADE"),
    )
    source_record_id: Mapped[UUID | None] = mapped_column(
        ForeignKey("source_record.id", ondelete="SET NULL"),
    )
    scheme: Mapped[str] = mapped_column(String(32), nullable=False)
    normalized_value: Mapped[str] = mapped_column(String(512), nullable=False)
    raw_value: Mapped[str] = mapped_column(String(512), nullable=False)

    work: Mapped[Work] = relationship(back_populates="external_identifiers")
    paper_version: Mapped[PaperVersion | None] = relationship(
        back_populates="external_identifiers",
    )
    source_record: Mapped[SourceRecord | None] = relationship(
        back_populates="external_identifiers",
    )


class FieldAssertion(
    UUIDPrimaryKeyMixin,
    ProvenanceMixin,
    TimestampMixin,
    Base,
):
    __tablename__ = "field_assertion"
    __table_args__ = (
        UniqueConstraint(
            "source_record_id",
            "field_name",
            "parser_version",
            name="uq_field_assertion_record_field_parser",
        ),
    )

    work_id: Mapped[UUID] = mapped_column(
        ForeignKey("work.id", ondelete="CASCADE"),
        nullable=False,
    )
    paper_version_id: Mapped[UUID | None] = mapped_column(
        ForeignKey("paper_version.id", ondelete="CASCADE"),
    )
    source_record_id: Mapped[UUID] = mapped_column(
        ForeignKey("source_record.id", ondelete="CASCADE"),
        nullable=False,
    )
    field_name: Mapped[str] = mapped_column(String(128), nullable=False)
    value: Mapped[Any] = mapped_column(JSONB, nullable=False)
    parser_version: Mapped[str] = mapped_column(String(64), nullable=False)
    confidence: Mapped[Decimal | None] = mapped_column(Numeric(5, 4))

    work: Mapped[Work] = relationship(back_populates="field_assertions")
    paper_version: Mapped[PaperVersion | None] = relationship(
        back_populates="field_assertions",
    )
    source_record: Mapped[SourceRecord] = relationship(
        back_populates="field_assertions",
    )


class Topic(UUIDPrimaryKeyMixin, ProvenanceMixin, TimestampMixin, Base):
    __tablename__ = "topic"
    __table_args__ = (
        UniqueConstraint("normalized_name", name="uq_topic_normalized_name"),
    )

    name: Mapped[str] = mapped_column(String(255), nullable=False)
    normalized_name: Mapped[str] = mapped_column(String(255), nullable=False)
    description: Mapped[str | None] = mapped_column(Text)

    works: Mapped[list[Work]] = relationship(
        secondary=work_topic,
        back_populates="topics",
    )


class Method(UUIDPrimaryKeyMixin, ProvenanceMixin, TimestampMixin, Base):
    __tablename__ = "method"
    __table_args__ = (
        UniqueConstraint("normalized_name", name="uq_method_normalized_name"),
    )

    name: Mapped[str] = mapped_column(String(255), nullable=False)
    normalized_name: Mapped[str] = mapped_column(String(255), nullable=False)
    description: Mapped[str | None] = mapped_column(Text)

    works: Mapped[list[Work]] = relationship(
        secondary=work_method,
        back_populates="methods",
    )


class Dataset(UUIDPrimaryKeyMixin, ProvenanceMixin, TimestampMixin, Base):
    __tablename__ = "dataset"
    __table_args__ = (
        UniqueConstraint("normalized_name", name="uq_dataset_normalized_name"),
    )

    name: Mapped[str] = mapped_column(String(255), nullable=False)
    normalized_name: Mapped[str] = mapped_column(String(255), nullable=False)
    description: Mapped[str | None] = mapped_column(Text)
    homepage_url: Mapped[str | None] = mapped_column(Text)

    works: Mapped[list[Work]] = relationship(
        secondary=work_dataset,
        back_populates="datasets",
    )


class Benchmark(UUIDPrimaryKeyMixin, ProvenanceMixin, TimestampMixin, Base):
    __tablename__ = "benchmark"
    __table_args__ = (
        UniqueConstraint("normalized_name", name="uq_benchmark_normalized_name"),
    )

    name: Mapped[str] = mapped_column(String(255), nullable=False)
    normalized_name: Mapped[str] = mapped_column(String(255), nullable=False)
    description: Mapped[str | None] = mapped_column(Text)
    homepage_url: Mapped[str | None] = mapped_column(Text)

    works: Mapped[list[Work]] = relationship(
        secondary=work_benchmark,
        back_populates="benchmarks",
    )


class CodeRepository(
    UUIDPrimaryKeyMixin,
    ProvenanceMixin,
    TimestampMixin,
    Base,
):
    __tablename__ = "code_repository"
    __table_args__ = (
        UniqueConstraint(
            "normalized_url",
            name="uq_code_repository_normalized_url",
        ),
    )

    work_id: Mapped[UUID] = mapped_column(
        ForeignKey("work.id", ondelete="CASCADE"),
        nullable=False,
    )
    provider: Mapped[str] = mapped_column(String(32), nullable=False)
    repository_name: Mapped[str] = mapped_column(String(255), nullable=False)
    repository_url: Mapped[str] = mapped_column(Text, nullable=False)
    normalized_url: Mapped[str] = mapped_column(String(512), nullable=False)
    is_official: Mapped[bool] = mapped_column(
        Boolean,
        nullable=False,
        server_default="false",
    )

    work: Mapped[Work] = relationship(back_populates="code_repositories")
    metric_snapshots: Mapped[list[MetricSnapshot]] = relationship(
        back_populates="code_repository",
    )


class MetricSnapshot(
    UUIDPrimaryKeyMixin,
    ProvenanceMixin,
    TimestampMixin,
    Base,
):
    __tablename__ = "metric_snapshot"
    __table_args__ = (
        CheckConstraint(
            "(work_id IS NOT NULL AND code_repository_id IS NULL) OR "
            "(work_id IS NULL AND code_repository_id IS NOT NULL)",
            name="single_target",
        ),
        UniqueConstraint(
            "work_id",
            "metric_name",
            "measured_at",
            "source",
            name="uq_metric_snapshot_work_metric_time_source",
        ),
        UniqueConstraint(
            "code_repository_id",
            "metric_name",
            "measured_at",
            "source",
            name="uq_metric_snapshot_repo_metric_time_source",
        ),
        Index(
            "ix_metric_snapshot_work_metric_time",
            "work_id",
            "metric_name",
            "measured_at",
        ),
    )

    work_id: Mapped[UUID | None] = mapped_column(
        ForeignKey("work.id", ondelete="CASCADE"),
    )
    code_repository_id: Mapped[UUID | None] = mapped_column(
        ForeignKey("code_repository.id", ondelete="CASCADE"),
    )
    metric_name: Mapped[str] = mapped_column(String(64), nullable=False)
    metric_value: Mapped[Decimal] = mapped_column(Numeric(20, 6), nullable=False)
    measured_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True),
        nullable=False,
    )
    window_days: Mapped[int | None] = mapped_column(Integer)
    details: Mapped[dict[str, Any] | None] = mapped_column("metadata", JSONB)

    work: Mapped[Work | None] = relationship(back_populates="metric_snapshots")
    code_repository: Mapped[CodeRepository | None] = relationship(
        back_populates="metric_snapshots",
    )


class RankingSnapshot(
    UUIDPrimaryKeyMixin,
    ProvenanceMixin,
    TimestampMixin,
    Base,
):
    __tablename__ = "ranking_snapshot"
    __table_args__ = (
        UniqueConstraint(
            "ranking_name",
            "subject_type",
            "subject_id",
            "window_days",
            "computed_at",
            name="uq_ranking_snapshot_subject_window_time",
        ),
        UniqueConstraint(
            "ranking_name",
            "window_days",
            "computed_at",
            "rank_position",
            name="uq_ranking_snapshot_position",
        ),
    )

    ranking_name: Mapped[str] = mapped_column(String(64), nullable=False)
    subject_type: Mapped[str] = mapped_column(String(32), nullable=False)
    subject_id: Mapped[UUID] = mapped_column(Uuid, nullable=False)
    rank_position: Mapped[int] = mapped_column(Integer, nullable=False)
    score: Mapped[Decimal] = mapped_column(Numeric(20, 6), nullable=False)
    window_days: Mapped[int] = mapped_column(Integer, nullable=False)
    computed_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True),
        nullable=False,
    )
    formula_version: Mapped[str] = mapped_column(String(64), nullable=False)
    coverage: Mapped[dict[str, Any]] = mapped_column(JSONB, nullable=False)
    explanation: Mapped[str | None] = mapped_column(Text)
