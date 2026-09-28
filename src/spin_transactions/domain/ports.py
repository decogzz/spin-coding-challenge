"""Interfaces the domain depends on. Adapters implement them; the domain never imports adapters."""

from dataclasses import dataclass
from decimal import Decimal
from typing import Protocol
from uuid import UUID

from spin_transactions.domain.models import ExecutionResult, Transaction, TransactionFilter, TransactionType


class TransactionRepository(Protocol):
    async def create(self, transaction: Transaction) -> None:
        """Raises TransactionAlreadyExistsError if the id is taken."""
        ...

    async def update(self, transaction: Transaction) -> None:
        """Record the provider outcome. Raises TransactionNotFoundError if it does not exist."""
        ...

    async def get(self, transaction_id: UUID) -> Transaction:
        """Raises TransactionNotFoundError if it does not exist."""
        ...

    async def search(self, criteria: TransactionFilter) -> list[Transaction]: ...


@dataclass(frozen=True, slots=True)
class ExecutionRequest:
    # Lets the provider deduplicate retries of the same transaction.
    idempotency_key: str
    account_id: str
    type: TransactionType
    amount: Decimal
    currency: str


class TransactionProvider(Protocol):
    async def execute(self, request: ExecutionRequest) -> ExecutionResult:
        """Execute against the external provider.

        A business refusal must raise ProviderRejectedError; any other exception
        means the outcome could not be confirmed.
        """
        ...
