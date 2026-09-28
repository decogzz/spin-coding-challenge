import pytest
from alembic.autogenerate import compare_metadata
from alembic.runtime.migration import MigrationContext
from sqlalchemy.engine import Connection
from sqlalchemy.ext.asyncio import AsyncEngine

from spin_transactions.adapters.postgres import PostgresTransactionRepository, metadata
from tests.repository_contract import RepositoryContract

pytestmark = pytest.mark.integration


class TestPostgresRepository(RepositoryContract):
    @pytest.fixture
    def repository(self, engine: AsyncEngine) -> PostgresTransactionRepository:
        return PostgresTransactionRepository(engine)


async def test_migrations_match_the_table_definition(engine: AsyncEngine) -> None:
    def diff(connection: Connection) -> list[object]:
        return list(compare_metadata(MigrationContext.configure(connection), metadata))

    async with engine.connect() as connection:
        assert await connection.run_sync(diff) == []


async def test_ping(engine: AsyncEngine) -> None:
    await PostgresTransactionRepository(engine).ping()
