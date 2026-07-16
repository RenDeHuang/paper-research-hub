from __future__ import annotations

from datetime import UTC, date, datetime
from typing import Annotated, Literal

from fastapi import APIRouter, Depends, HTTPException, Query
from pydantic import AwareDatetime, StringConstraints
from sqlalchemy.orm import Session

from paper_hub.api.dependencies import get_public_read_session
from paper_hub.config import get_settings
from paper_hub.models import RecordStatus
from paper_hub.repositories import (
    PaperRepository,
    PaperSearchFilters,
    SearchSnapshotInvalidatedError,
)
from paper_hub.schemas import (
    ErrorResponse,
    PaperDetailResponse,
    PaperListResponse,
    StatsResponse,
    TaxonomyListResponse,
)


router = APIRouter()
ShortFilterValue = Annotated[
    str,
    StringConstraints(strip_whitespace=True, min_length=1, max_length=64),
]
TaxonomyFilterValue = Annotated[
    str,
    StringConstraints(strip_whitespace=True, min_length=1, max_length=255),
]
SearchQueryValue = Annotated[
    str,
    StringConstraints(strip_whitespace=True, min_length=1, max_length=200),
]
ERROR_RESPONSES = {
    404: {"model": ErrorResponse},
    422: {"model": ErrorResponse},
}


def _repository(session: Session) -> PaperRepository:
    return PaperRepository(
        session,
        current_scope_rule_version=(
            get_settings().current_scope_rule_version
        ),
    )


@router.get(
    "/papers",
    response_model=PaperListResponse,
    responses={
        409: {"model": ErrorResponse},
        422: {"model": ErrorResponse},
    },
)
def list_papers(
    q: Annotated[SearchQueryValue | None, Query()] = None,
    paper_type: Annotated[
        list[ShortFilterValue] | None,
        Query(alias="type", max_length=64),
    ] = None,
    topic: Annotated[
        list[TaxonomyFilterValue] | None,
        Query(max_length=255),
    ] = None,
    method: Annotated[
        list[TaxonomyFilterValue] | None,
        Query(max_length=255),
    ] = None,
    has_code: bool | None = None,
    date_from: date | None = None,
    date_to: date | None = None,
    status: Annotated[list[RecordStatus] | None, Query()] = None,
    source: Annotated[
        list[ShortFilterValue] | None,
        Query(max_length=64),
    ] = None,
    page: Annotated[int, Query(ge=1)] = 1,
    page_size: Annotated[int, Query(ge=1, le=100)] = 20,
    snapshot_at: Annotated[AwareDatetime | None, Query()] = None,
    snapshot_revision: Annotated[int | None, Query(ge=0)] = None,
    sort: Literal[
        "published_desc",
        "published_asc",
        "title_asc",
        "title_desc",
    ] = "published_desc",
    session: Session = Depends(get_public_read_session),
) -> dict[str, object]:
    if date_from is not None and date_to is not None and date_from > date_to:
        raise HTTPException(
            status_code=422,
            detail={
                "code": "validation_error",
                "message": "date_from must not be after date_to",
            },
        )
    if (snapshot_at is None) != (snapshot_revision is None):
        raise HTTPException(
            status_code=422,
            detail={
                "code": "validation_error",
                "message": (
                    "snapshot_at and snapshot_revision "
                    "must be provided together"
                ),
            },
        )
    if page > 1 and snapshot_at is None:
        raise HTTPException(
            status_code=422,
            detail={
                "code": "validation_error",
                "message": (
                    "page > 1 requires snapshot_at "
                    "and snapshot_revision"
                ),
            },
        )
    filters = PaperSearchFilters(
        query=q,
        work_types=tuple(paper_type or ()),
        topics=tuple(topic or ()),
        methods=tuple(method or ()),
        has_code=has_code,
        date_from=date_from,
        date_to=date_to,
        statuses=tuple(status or ()),
        sources=tuple(source or ()),
        page=page,
        page_size=page_size,
        sort=sort,
        snapshot_at=snapshot_at,
        snapshot_revision=snapshot_revision,
    )
    try:
        return _repository(session).search(filters)
    except SearchSnapshotInvalidatedError as error:
        raise HTTPException(
            status_code=409,
            detail={
                "code": "search_snapshot_invalidated",
                "message": (
                    "Paper search results changed; restart from page 1"
                ),
            },
        ) from error


@router.get(
    "/papers/{slug}",
    response_model=PaperDetailResponse,
    responses=ERROR_RESPONSES,
)
def paper_detail(
    slug: str,
    session: Session = Depends(get_public_read_session),
) -> dict[str, object]:
    paper = _repository(session).detail(slug)
    if paper is None:
        raise HTTPException(
            status_code=404,
            detail={
                "code": "paper_not_found",
                "message": "Paper does not exist in the current public scope",
            },
        )
    return paper


@router.get(
    "/topics",
    response_model=TaxonomyListResponse,
    responses={422: {"model": ErrorResponse}},
)
def list_topics(
    q: Annotated[SearchQueryValue | None, Query()] = None,
    limit: Annotated[int, Query(ge=1, le=100)] = 100,
    session: Session = Depends(get_public_read_session),
) -> dict[str, object]:
    return _repository(session).taxonomy_list(
        subject="topic",
        query=q,
        limit=limit,
    )


@router.get(
    "/methods",
    response_model=TaxonomyListResponse,
    responses={422: {"model": ErrorResponse}},
)
def list_methods(
    q: Annotated[SearchQueryValue | None, Query()] = None,
    limit: Annotated[int, Query(ge=1, le=100)] = 100,
    session: Session = Depends(get_public_read_session),
) -> dict[str, object]:
    return _repository(session).taxonomy_list(
        subject="method",
        query=q,
        limit=limit,
    )


@router.get(
    "/stats",
    response_model=StatsResponse,
)
def stats(
    session: Session = Depends(get_public_read_session),
) -> dict[str, object]:
    return _repository(session).stats(
        generated_at=datetime.now(UTC),
    )
