import pytest

from spin_transactions.adapters.memory import InMemoryTransactionRepository
from tests.repository_contract import RepositoryContract


class TestInMemoryRepository(RepositoryContract):
    @pytest.fixture
    def repository(self) -> InMemoryTransactionRepository:
        return InMemoryTransactionRepository()
