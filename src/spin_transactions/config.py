"""Settings of the API, read from environment variables. Durations are in seconds."""

from typing import Literal

from pydantic import Field, PositiveFloat, field_validator
from pydantic_settings import BaseSettings, SettingsConfigDict

from spin_transactions.adapters.provider_client import ProviderClientConfig

type LogLevel = Literal["DEBUG", "INFO", "WARNING", "ERROR"]


class Settings(BaseSettings):
    model_config = SettingsConfigDict(frozen=True)

    host: str = "0.0.0.0"  # noqa: S104 - listens inside its container
    port: int = Field(8080, ge=1, le=65535)
    log_level: LogLevel = "INFO"
    # How long shutdown waits for executions still talking to the provider.
    shutdown_timeout: PositiveFloat = 15.0

    database_url: str
    db_pool_size: int = Field(10, ge=1, le=200)
    db_max_overflow: int = Field(10, ge=0, le=200)
    db_pool_timeout: PositiveFloat = 5.0

    provider_base_url: str
    provider_attempt_timeout: PositiveFloat = 2.0
    provider_max_attempts: int = Field(3, ge=1, le=10)
    provider_base_backoff: PositiveFloat = 0.1
    provider_max_backoff: PositiveFloat = 1.0
    provider_breaker_failures: int = Field(5, ge=1, le=1000)
    provider_breaker_open_timeout: PositiveFloat = 30.0

    @field_validator("log_level", mode="before")
    @classmethod
    def _uppercase_level(cls, value: object) -> object:
        return value.upper() if isinstance(value, str) else value

    def provider(self) -> ProviderClientConfig:
        return ProviderClientConfig(
            base_url=self.provider_base_url,
            attempt_timeout=self.provider_attempt_timeout,
            max_attempts=self.provider_max_attempts,
            base_backoff=self.provider_base_backoff,
            max_backoff=self.provider_max_backoff,
            breaker_failures=self.provider_breaker_failures,
            breaker_open_timeout=self.provider_breaker_open_timeout,
        )
