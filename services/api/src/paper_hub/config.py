from functools import lru_cache
from pathlib import Path

from pydantic import PostgresDsn, SecretStr, TypeAdapter, field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict


PROJECT_ROOT = Path(__file__).resolve().parents[4]
ROOT_ENV_FILE = PROJECT_ROOT / ".env"
POSTGRES_DSN_ADAPTER = TypeAdapter(PostgresDsn)


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=ROOT_ENV_FILE,
        env_file_encoding="utf-8",
        extra="ignore",
        case_sensitive=False,
        hide_input_in_errors=True,
    )

    database_url: SecretStr

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

    @property
    def sqlalchemy_database_url(self) -> str:
        return self.database_url.get_secret_value()


@lru_cache(maxsize=1)
def get_settings() -> Settings:
    return Settings()
