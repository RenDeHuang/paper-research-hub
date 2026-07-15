from datetime import date, datetime
from typing import Any
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field

from paper_hub.models import RecordStatus


class ProvenanceSchema(BaseModel):
    source: str = Field(min_length=1, max_length=64)
    source_url: str | None = None
    retrieved_at: datetime
    source_license: str | None = None
    content_license: str | None = None
    status: RecordStatus = RecordStatus.ACTIVE


class FieldAssertionCreate(ProvenanceSchema):
    field_name: str = Field(min_length=1, max_length=128)
    value: Any
    source_record_id: str = Field(min_length=1, max_length=255)
    parser_version: str = Field(min_length=1, max_length=64)


class WorkCreate(ProvenanceSchema):
    canonical_key: str = Field(min_length=1, max_length=512)
    title: str = Field(min_length=1)
    abstract: str | None = None
    publication_date: date | None = None


class WorkRead(WorkCreate):
    model_config = ConfigDict(from_attributes=True)

    id: UUID
    created_at: datetime
    updated_at: datetime


class PaperVersionCreate(ProvenanceSchema):
    work_id: UUID
    version_label: str = Field(min_length=1, max_length=64)
    version_number: int | None = Field(default=None, ge=1)
    version_type: str = Field(min_length=1, max_length=32)
    title: str | None = None
    abstract: str | None = None
    published_at: datetime | None = None
    version_url: str | None = None


class PaperVersionRead(PaperVersionCreate):
    model_config = ConfigDict(from_attributes=True)

    id: UUID
    created_at: datetime
    updated_at: datetime
