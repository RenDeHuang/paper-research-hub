from datetime import date
from decimal import Decimal
from typing import Literal
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


class ApiSchema(BaseModel):
    model_config = ConfigDict(extra="forbid", allow_inf_nan=False)


class ErrorDetailSchema(ApiSchema):
    code: str
    message: str
    details: JsonValue | None = None


class ErrorResponse(ApiSchema):
    error: ErrorDetailSchema


class TaxonomyTagResponse(ApiSchema):
    id: UUID
    name: str
    normalized_name: str


class InstitutionResponse(ApiSchema):
    openalex_id: str | None = None
    display_name: str
    ror: str | None = None
    country_code: str | None = None
    institution_type: str | None = None


class AuthorResponse(ApiSchema):
    openalex_id: str | None = None
    display_name: str
    orcid: str | None = None
    position: str | None = None
    is_corresponding: bool = False
    institutions: list[InstitutionResponse] = Field(default_factory=list)


class PaperSummaryResponse(ApiSchema):
    slug: str
    canonical_key: str
    title: str
    abstract: str | None = None
    publication_date: date | None = None
    type: str | None = None
    authors: list[AuthorResponse] = Field(default_factory=list)
    topics: list[TaxonomyTagResponse] = Field(default_factory=list)
    methods: list[TaxonomyTagResponse] = Field(default_factory=list)
    has_code: bool
    status: RecordStatus
    source: str
    citation_count: float | None = None
    updated_at: AwareDatetime


class FacetCountResponse(ApiSchema):
    value: str
    count: int = Field(ge=0)


class LabeledFacetCountResponse(FacetCountResponse):
    label: str


class PaperFacetsResponse(ApiSchema):
    types: list[FacetCountResponse]
    topics: list[LabeledFacetCountResponse]
    methods: list[LabeledFacetCountResponse]
    statuses: list[FacetCountResponse]
    sources: list[FacetCountResponse]
    has_code: list[FacetCountResponse]


class PaperListResponse(ApiSchema):
    items: list[PaperSummaryResponse]
    total: int = Field(ge=0)
    page: int = Field(ge=1)
    page_size: int = Field(ge=1, le=100)
    total_pages: int = Field(ge=0)
    snapshot_at: AwareDatetime
    snapshot_revision: int = Field(ge=0)
    facets: PaperFacetsResponse


class PaperVersionResponse(ApiSchema):
    id: UUID
    version_label: str
    version_number: int | None = None
    version_type: str
    title: str | None = None
    abstract: str | None = None
    submitted_at: AwareDatetime | None = None
    published_at: AwareDatetime | None = None
    version_url: str | None = None
    status: RecordStatus
    source: str


class IdentifierResponse(ApiSchema):
    scheme: str
    normalized_value: str
    raw_value: str
    source: str


class NamedResourceResponse(ApiSchema):
    id: UUID
    name: str
    normalized_name: str
    description: str | None = None
    homepage_url: str | None = None
    source: str


class CodeRepositoryResponse(ApiSchema):
    id: UUID
    provider: str
    repository_name: str
    repository_url: str
    normalized_url: str
    is_official: bool
    source: str
    retrieved_at: AwareDatetime


class WorkProvenanceResponse(ApiSchema):
    source: str
    source_url: str | None = None
    retrieved_at: AwareDatetime
    source_license: str | None = None
    content_license: str | None = None
    projection_source: str | None = None
    projection_source_record_id: UUID | None = None
    projection_source_updated_at: AwareDatetime | None = None
    updated_at: AwareDatetime


class LicenseResponse(ApiSchema):
    source: str | None = None
    content: str | None = None


class SourceRecordResponse(ApiSchema):
    id: UUID
    source: str
    source_record_id: str
    content_hash: str
    source_url: str | None = None
    source_updated_at: AwareDatetime | None = None
    retrieved_at: AwareDatetime
    http_status: int | None = None
    source_license: str | None = None
    content_license: str | None = None
    status: RecordStatus


class MetricSnapshotResponse(ApiSchema):
    id: UUID
    target: str
    metric_name: str
    metric_value: float
    measured_at: AwareDatetime
    window_days: int | None = None
    details: dict[str, JsonValue] | None = None
    source: str


class ScopeStateResponse(ApiSchema):
    included: bool
    rule_version: str
    reason: str | None = None
    evidence: list[dict[str, JsonValue]]
    evaluated_at: AwareDatetime


class PaperDetailResponse(PaperSummaryResponse):
    versions: list[PaperVersionResponse]
    identifiers: list[IdentifierResponse]
    institutions: list[InstitutionResponse]
    datasets: list[NamedResourceResponse]
    benchmarks: list[NamedResourceResponse]
    code_repositories: list[CodeRepositoryResponse]
    provenance: WorkProvenanceResponse
    license: LicenseResponse
    source_records: list[SourceRecordResponse]
    metric_snapshots: list[MetricSnapshotResponse]
    scope_state: ScopeStateResponse


class TaxonomyListItemResponse(ApiSchema):
    id: UUID
    name: str
    normalized_name: str
    description: str | None = None
    paper_count: int = Field(ge=0)


class TaxonomyListResponse(ApiSchema):
    items: list[TaxonomyListItemResponse]


class StatsResponse(ApiSchema):
    papers: int = Field(ge=0)
    topics: int = Field(ge=0)
    methods: int = Field(ge=0)
    datasets: int = Field(ge=0)
    benchmarks: int = Field(ge=0)
    code_repositories: int = Field(ge=0)
    active: int = Field(ge=0)
    retracted: int = Field(ge=0)
    withdrawn: int = Field(ge=0)
    generated_at: AwareDatetime


class MissingSignalResponse(ApiSchema):
    subject_id: str | None = None
    canonical_key: str | None = None
    slug: str | None = None
    normalized_name: str | None = None
    repository_id: UUID | None = None
    repository_url: str | None = None
    signal: str
    reason: str


class ConfidenceResponse(ApiSchema):
    level: Literal["low", "medium", "high"]
    sample_size: int = Field(ge=0)
    explanation: str


class PaperRankingCoverageResponse(ApiSchema):
    total_eligible: int = Field(ge=0)
    subjects_with_signal: int = Field(ge=0)
    ranked_subjects: int = Field(ge=0)
    returned_subjects: int = Field(ge=0)
    signal_coverage: float = Field(ge=0, le=1)
    confidence: ConfidenceResponse


class TaxonomyRankingCoverageResponse(ApiSchema):
    total_eligible: int = Field(ge=0)
    subjects_with_signal: int = Field(ge=0)
    ranked_subjects: int = Field(ge=0)
    returned_subjects: int = Field(ge=0)
    signal_coverage: float = Field(ge=0, le=1)
    confidence: ConfidenceResponse
    current_total: int = Field(ge=0)
    baseline_total: int = Field(ge=0)


class PaperTrendItemResponse(ApiSchema):
    slug: str
    canonical_key: str
    title: str
    publication_date: date | None = None
    status: RecordStatus
    score: float
    percentile: float | None = Field(default=None, ge=0, le=1)
    details: dict[str, JsonValue] = Field(default_factory=dict)


class TaxonomyTrendItemResponse(ApiSchema):
    id: UUID
    name: str
    normalized_name: str
    score: float
    current_count: int = Field(ge=0)
    baseline_count: int = Field(ge=0)
    current_total: int = Field(ge=0)
    baseline_total: int = Field(ge=0)
    confidence: ConfidenceResponse


class PaperTrendResponse(ApiSchema):
    ranking_name: Literal["latest", "citation_velocity", "code_growth"]
    formula_version: str
    window_days: Literal[7, 30, 90]
    generated_at: AwareDatetime
    coverage: PaperRankingCoverageResponse
    missing_signals: list[MissingSignalResponse]
    explanation: str
    items: list[PaperTrendItemResponse]


class TaxonomyTrendResponse(ApiSchema):
    ranking_name: Literal["topic_growth", "method_adoption"]
    formula_version: str
    window_days: Literal[7, 30, 90]
    generated_at: AwareDatetime
    coverage: TaxonomyRankingCoverageResponse
    missing_signals: list[MissingSignalResponse]
    explanation: str
    items: list[TaxonomyTrendItemResponse]
