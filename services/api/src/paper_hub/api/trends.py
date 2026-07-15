from __future__ import annotations

from enum import Enum
from typing import Annotated, Literal

from fastapi import APIRouter, Depends, Query
from sqlalchemy.orm import Session

from paper_hub.db import get_session
from paper_hub.rankings import RankingService
from paper_hub.schemas import (
    ErrorResponse,
    PaperTrendResponse,
    TaxonomyTrendResponse,
)


router = APIRouter(prefix="/trends")


class WindowDays(str, Enum):
    DAYS_7 = "7"
    DAYS_30 = "30"
    DAYS_90 = "90"


@router.get(
    "/papers",
    response_model=PaperTrendResponse,
    responses={422: {"model": ErrorResponse}},
)
def paper_trends(
    ranking: Literal[
        "latest",
        "citation_velocity",
        "code_growth",
    ] = "latest",
    window_days: WindowDays = WindowDays.DAYS_30,
    limit: Annotated[int, Query(ge=1, le=100)] = 20,
    session: Session = Depends(get_session),
) -> dict[str, object]:
    return RankingService(session).paper_ranking(
        ranking_name=ranking,
        window_days=int(window_days.value),
        limit=limit,
    )


@router.get(
    "/topics",
    response_model=TaxonomyTrendResponse,
    responses={422: {"model": ErrorResponse}},
)
def topic_trends(
    window_days: WindowDays = WindowDays.DAYS_30,
    limit: Annotated[int, Query(ge=1, le=100)] = 20,
    session: Session = Depends(get_session),
) -> dict[str, object]:
    return RankingService(session).taxonomy_ranking(
        subject="topic",
        window_days=int(window_days.value),
        limit=limit,
    )


@router.get(
    "/methods",
    response_model=TaxonomyTrendResponse,
    responses={422: {"model": ErrorResponse}},
)
def method_trends(
    window_days: WindowDays = WindowDays.DAYS_30,
    limit: Annotated[int, Query(ge=1, le=100)] = 20,
    session: Session = Depends(get_session),
) -> dict[str, object]:
    return RankingService(session).taxonomy_ranking(
        subject="method",
        window_days=int(window_days.value),
        limit=limit,
    )
