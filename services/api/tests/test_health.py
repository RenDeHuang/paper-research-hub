from fastapi.testclient import TestClient

from paper_hub.main import app


def test_health_reports_service_identity() -> None:
    response = TestClient(app).get("/health")

    assert response.status_code == 200
    assert response.json() == {"status": "ok", "service": "paper-hub-api"}
