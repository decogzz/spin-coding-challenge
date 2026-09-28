from decimal import Decimal

import uvicorn
from pydantic import Field, PositiveFloat
from pydantic_settings import BaseSettings, SettingsConfigDict

from spin_transactions.logs import configure_logging
from spin_transactions.providermock.app import create_app


class MockSettings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="PROVIDER_MOCK_", frozen=True)

    host: str = "0.0.0.0"  # noqa: S104 - listens inside its container
    port: int = Field(8081, ge=1, le=65535)
    initial_balance: Decimal = Decimal("10000.00")
    slow_delay: PositiveFloat = 5.0
    log_level: str = "INFO"


def run() -> None:
    settings = MockSettings()
    configure_logging(settings.log_level)
    app = create_app(initial_balance=settings.initial_balance, slow_delay=settings.slow_delay)
    uvicorn.run(app, host=settings.host, port=settings.port, log_config=None, access_log=False)


if __name__ == "__main__":
    run()
