from datetime import date, datetime
from decimal import Decimal
from uuid import UUID

from pydantic import (
    AwareDatetime,
    BaseModel,
    ConfigDict,
    Field,
    JsonValue,
    field_validator,
)

from paper_hub.models import RecordStatus
from paper_hub.normalization import normalize_canonical_key


class ProvenanceSchema(BaseModel):
    model_config = ConfigDict(extra="forbid")

    source: str = Field(min_length=1, max_length=64)
    source_url: str | None = None
    retrieved_at: AwareDatetime
    source_license: str | None = None
    content_license: str | None = None
    status: RecordStatus = RecordStatus.ACTIVE


class FieldAssertionCreate(ProvenanceSchema):
    field_name: str = Field(min_length=1, max_length=128)
    value: JsonValue
    source_record_id: UUID
    parser_version: str = Field(min_length=1, max_length=64)
    confidence: Decimal | None = Field(default=None, ge=0, le=1)


class WorkCreate(ProvenanceSchema):
    canonical_key: str = Field(min_length=1, max_length=512)
    title: str = Field(min_length=1)
    abstract: str | None = None
    publication_date: date | None = None

    @field_validator("canonical_key", mode="before")
    @classmethod
    def normalize_approved_canonical_key(cls, value: object) -> str:
        if not isinstance(value, str):
            raise ValueError("canonical_key must be a string")
        normalized = normalize_canonical_key(value)
        if normalized is None:
            raise ValueError(
                "canonical_key must be a normalized approved external identifier"
            )
        return normalized


class WorkRead(WorkCreate):
    model_config = ConfigDict(from_attributes=True, extra="forbid")

    id: UUID
    created_at: AwareDatetime
    updated_at: AwareDatetime


class PaperVersionCreate(ProvenanceSchema):
    work_id: UUID
    version_label: str = Field(min_length=1, max_length=64)
    version_number: int | None = Field(default=None, ge=1)
    version_type: str = Field(min_length=1, max_length=32)
    title: str | None = None
    abstract: str | None = None
    submitted_at: AwareDatetime | None = None
    published_at: AwareDatetime | None = None
    version_url: str | None = None


class PaperVersionRead(PaperVersionCreate):
    model_config = ConfigDict(from_attributes=True, extra="forbid")

    id: UUID
    created_at: AwareDatetime
    updated_at: AwareDatetime
