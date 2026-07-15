from fastapi import APIRouter

from paper_hub.api.papers import router as papers_router
from paper_hub.api.trends import router as trends_router


api_router = APIRouter(prefix="/api/v1")
api_router.include_router(papers_router)
api_router.include_router(trends_router)
