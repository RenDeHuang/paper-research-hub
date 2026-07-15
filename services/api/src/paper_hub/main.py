from fastapi import FastAPI

app = FastAPI(title="Paper Research Hub API")


@app.get("/health")
def health() -> dict[str, str]:
    return {"status": "ok", "service": "paper-hub-api"}
