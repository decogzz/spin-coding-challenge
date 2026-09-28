import os
from collections.abc import AsyncIterator
from pathlib import Path

import pytest
from alembic import command
from alembic.config import Config
from sqlalchemy import text
from sqlalchemy.ext.asyncio import AsyncEngine

from spin_transactions.adapters.postgres import async_url, create_engine

ALEMBIC_INI = Path(__file__).parents[2] / "alembic.ini"


@pytest.fixture(scope="session")
def database_url() -> str:
    url = os.environ.get("TEST_DATABASE_URL")
    if not url:
        pytest.skip("set TEST_DATABASE_URL to run the integration tests")
    return async_url(url)


@pytest.fixture(scope="session")
def alembic_config(database_url: str) -> Config:
    config = Config(ALEMBIC_INI)
    config.set_main_option("sqlalchemy.url", database_url)
    config.attributes["configure_logger"] = False
    return config


@pytest.fixture(scope="session")
def migrated(alembic_config: Config) -> None:
    # Sync on purpose: the Alembic env starts its own event loop.
    command.downgrade(alembic_config, "base")
    command.upgrade(alembic_config, "head")


@pytest.fixture
async def engine(database_url: str, migrated: None) -> AsyncIterator[AsyncEngine]:
    engine = create_engine(database_url, pool_size=2, max_overflow=0)
    async with engine.begin() as connection:
        await connection.execute(text("TRUNCATE transactions"))
    yield engine
    await engine.dispose()
