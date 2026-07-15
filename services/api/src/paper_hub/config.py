from functools import lru_cache

from pydantic import field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


LOCAL_POSTGRES_DATABASE_URL = (
    "postgresql+psycopg://paper_hub:paper_hub@localhost:5432/paper_hub"
)


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
        case_sensitive=False,
    )

    database_url: str = LOCAL_POSTGRES_DATABASE_URL

    @field_validator("database_url")
    @classmethod
    def require_postgresql(cls, value: str) -> str:
        database_url = value.strip()
        if database_url.startswith("postgresql://"):
            database_url = database_url.replace(
                "postgresql://",
                "postgresql+psycopg://",
                1,
            )
        if not database_url.startswith("postgresql+psycopg://"):
            raise ValueError("DATABASE_URL must use PostgreSQL with the psycopg driver")
        return database_url


@lru_cache(maxsize=1)
def get_settings() -> Settings:
    return Settings()
