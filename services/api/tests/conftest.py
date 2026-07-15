from collections.abc import Generator
from pathlib import Path
import subprocess
import time
from uuid import uuid4

from alembic.config import Config
import psycopg
import pytest


SERVICE_ROOT = Path(__file__).resolve().parents[1]


@pytest.fixture(scope="session")
def postgres_container_url() -> Generator[str, None, None]:
    container_name = f"paper-hub-postgres-{uuid4().hex[:12]}"
    password = "paper_hub_test"
    subprocess.run(
        [
            "docker",
            "run",
            "--rm",
            "--detach",
            "--name",
            container_name,
            "--env",
            f"POSTGRES_PASSWORD={password}",
            "--env",
            "POSTGRES_DB=paper_hub_test",
            "--publish",
            "127.0.0.1::5432",
            "pgvector/pgvector:pg16",
        ],
        check=True,
        capture_output=True,
        text=True,
        timeout=180,
    )

    try:
        port_result = subprocess.run(
            ["docker", "port", container_name, "5432/tcp"],
            check=True,
            capture_output=True,
            text=True,
        )
        port = port_result.stdout.strip().rsplit(":", maxsplit=1)[1]
        database_url = (
            "postgresql+psycopg://postgres:"
            f"{password}@127.0.0.1:{port}/paper_hub_test"
        )
        psycopg_url = database_url.replace("postgresql+psycopg://", "postgresql://")

        deadline = time.monotonic() + 60
        while True:
            try:
                with psycopg.connect(psycopg_url):
                    break
            except psycopg.OperationalError:
                if time.monotonic() >= deadline:
                    logs = subprocess.run(
                        ["docker", "logs", container_name],
                        capture_output=True,
                        text=True,
                    )
                    raise RuntimeError(
                        f"PostgreSQL container did not become ready:\n{logs.stderr}"
                    )
                time.sleep(0.25)

        yield database_url
    finally:
        subprocess.run(
            ["docker", "rm", "--force", container_name],
            check=False,
            capture_output=True,
            text=True,
        )


@pytest.fixture
def clean_postgres_url(postgres_container_url: str) -> str:
    psycopg_url = postgres_container_url.replace(
        "postgresql+psycopg://",
        "postgresql://",
    )
    with psycopg.connect(psycopg_url, autocommit=True) as connection:
        connection.execute("DROP SCHEMA public CASCADE")
        connection.execute("CREATE SCHEMA public")
    return postgres_container_url


@pytest.fixture
def alembic_config(
    clean_postgres_url: str,
    monkeypatch: pytest.MonkeyPatch,
) -> Generator[Config, None, None]:
    from paper_hub.config import get_settings

    monkeypatch.setenv("DATABASE_URL", clean_postgres_url)
    get_settings.cache_clear()
    yield Config(SERVICE_ROOT / "alembic.ini")
    get_settings.cache_clear()
