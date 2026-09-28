"""In-process repository for tests and local experiments."""

from dataclasses import replace
from uuid import UUID

from spin_transactions.domain.errors import TransactionNotFoundError
from spin_transactions.domain.models import Transaction, TransactionFilter


class TransactionAlreadyExistsError(Exception):
    pass


class InMemoryTransactionRepository:
    def __init__(self) -> None:
        self._items: dict[UUID, Transaction] = {}

    async def create(self, transaction: Transaction) -> None:
        if transaction.id in self._items:
            raise TransactionAlreadyExistsError(str(transaction.id))
        self._items[transaction.id] = replace(transaction)

    async def update(self, transaction: Transaction) -> None:
        if transaction.id not in self._items:
            raise TransactionNotFoundError(str(transaction.id))
        self._items[transaction.id] = replace(transaction)

    async def get(self, transaction_id: UUID) -> Transaction:
        try:
            return replace(self._items[transaction_id])
        except KeyError:
            raise TransactionNotFoundError(str(transaction_id)) from None

    async def search(self, criteria: TransactionFilter) -> list[Transaction]:
        matches = [
            replace(tx)
            for tx in self._items.values()
            if (criteria.account_id is None or tx.account_id == criteria.account_id)
            and (criteria.status is None or tx.status == criteria.status)
            and (criteria.type is None or tx.type == criteria.type)
        ]
        matches.sort(key=lambda tx: (tx.created_at, tx.id.bytes), reverse=True)
        return matches[criteria.offset : criteria.offset + criteria.limit]
