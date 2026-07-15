from fastapi import FastAPI, HTTPException, Request
from fastapi.encoders import jsonable_encoder
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse

from paper_hub.api import api_router
from paper_hub.schemas import ErrorResponse

app = FastAPI(title="Paper Research Hub API")
app.include_router(api_router)


@app.exception_handler(RequestValidationError)
async def validation_exception_handler(
    _request: Request,
    exc: RequestValidationError,
) -> JSONResponse:
    payload = ErrorResponse(
        error={
            "code": "validation_error",
            "message": "Request validation failed",
            "details": jsonable_encoder(exc.errors()),
        }
    )
    return JSONResponse(
        status_code=422,
        content=payload.model_dump(mode="json"),
    )


@app.exception_handler(HTTPException)
async def http_exception_handler(
    _request: Request,
    exc: HTTPException,
) -> JSONResponse:
    if isinstance(exc.detail, dict):
        code = str(exc.detail.get("code", "http_error"))
        message = str(exc.detail.get("message", "Request failed"))
        details = exc.detail.get("details")
    else:
        code = "http_error"
        message = str(exc.detail)
        details = None
    payload = ErrorResponse(
        error={
            "code": code,
            "message": message,
            "details": details,
        }
    )
    return JSONResponse(
        status_code=exc.status_code,
        content=payload.model_dump(mode="json"),
        headers=exc.headers,
    )


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok", "service": "paper-hub-api"}
