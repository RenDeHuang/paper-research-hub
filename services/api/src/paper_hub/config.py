from functools import lru_cache
from pathlib import Path
import re

from pydantic import (
    Field,
    PostgresDsn,
    SecretStr,
    TypeAdapter,
    field_validator,
)
from pydantic_settings import BaseSettings, SettingsConfigDict


PROJECT_ROOT = Path(__file__).resolve().parents[4]
ROOT_ENV_FILE = PROJECT_ROOT / ".env"
POSTGRES_DSN_ADAPTER = TypeAdapter(PostgresDsn)
EMAIL_PATTERN = re.compile(r"^[^@\s]+@[^@\s]+\.[^@\s]+$")


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=ROOT_ENV_FILE,
        env_file_encoding="utf-8",
        extra="ignore",
        case_sensitive=False,
        hide_input_in_errors=True,
    )

    database_url: SecretStr
    openalex_contact_email: str | None = None
    openalex_api_key: SecretStr | None = None
    openalex_max_retries: int = Field(default=3, ge=0, le=10)
    openalex_retry_backoff_seconds: float = Field(
        default=1.0,
        ge=0,
        le=60,
    )
    openalex_max_retry_wait_seconds: float = Field(
        default=30.0,
        ge=0,
        le=300,
    )

    @field_validator("database_url")
    @classmethod
    def require_postgresql(cls, value: SecretStr) -> SecretStr:
        database_url = value.get_secret_value()
        parsed = POSTGRES_DSN_ADAPTER.validate_python(database_url)
        if parsed.scheme != "postgresql+psycopg":
            raise ValueError(
                "DATABASE_URL must use PostgreSQL with the psycopg driver"
            )
        return SecretStr(str(parsed))

    @field_validator("openalex_contact_email")
    @classmethod
    def validate_openalex_contact_email(
        cls,
        value: str | None,
    ) -> str | None:
        if value is None:
            return None
        normalized = value.strip()
        if not EMAIL_PATTERN.fullmatch(normalized):
            raise ValueError(
                "OPENALEX_CONTACT_EMAIL must be a valid email address"
            )
        return normalized

    @property
    def sqlalchemy_database_url(self) -> str:
        return self.database_url.get_secret_value()


@lru_cache(maxsize=1)
def get_settings() -> Settings:
    return Settings()
