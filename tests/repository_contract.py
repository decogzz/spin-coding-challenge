"""Behaviour every TransactionRepository must have.

Subclass RepositoryContract as ``Test...`` and provide a ``repository`` fixture
returning an empty repository.
"""

import uuid
from dataclasses import replace
from datetime import UTC, datetime, timedelta
from decimal import Decimal

import pytest

from spin_transactions.domain.errors import TransactionAlreadyExistsError, TransactionNotFoundError
from spin_transactions.domain.models import (
    ExecutionResult,
    Transaction,
    TransactionFilter,
    TransactionStatus,
    TransactionType,
)
from spin_transactions.domain.ports import TransactionRepository

BASE_TIME = datetime(2026, 9, 26, 10, 0, 0, 123456, tzinfo=UTC)


def pending(
    *,
    account_id: str = "acc-1",
    type: TransactionType = TransactionType.DEBIT,
    amount: str = "1500.00",
    created_at: datetime = BASE_TIME,
) -> Transaction:
    return Transaction(
        id=uuid.uuid7(),
        account_id=account_id,
        type=type,
        amount=Decimal(amount),
        currency="MXN",
        description="rent",
        status=TransactionStatus.PENDING,
        created_at=created_at,
        updated_at=created_at,
    )


class RepositoryContract:
    async def test_created_transaction_can_be_read_back(self, repository: TransactionRepository) -> None:
        transaction = pending(amount="999999999999999.99")
        await repository.create(transaction)

        assert await repository.get(transaction.id) == transaction

    async def test_get_unknown_id_raises_not_found(self, repository: TransactionRepository) -> None:
        with pytest.raises(TransactionNotFoundError):
            await repository.get(uuid.uuid7())

    async def test_create_rejects_a_duplicate_id(self, repository: TransactionRepository) -> None:
        transaction = pending()
        await repository.create(transaction)

        with pytest.raises(TransactionAlreadyExistsError):
            await repository.create(replace(transaction, account_id="acc-2"))

    async def test_update_records_the_execution_outcome(self, repository: TransactionRepository) -> None:
        transaction = pending()
        await repository.create(transaction)

        executed_at = BASE_TIME + timedelta(seconds=1)
        transaction.mark_executed(ExecutionResult("prv-1", Decimal("8500.00"), None), executed_at)
        await repository.update(transaction)

        stored = await repository.get(transaction.id)
        assert stored.status is TransactionStatus.EXECUTED
        assert stored.provider_transaction_id == "prv-1"
        assert stored.balance_after == Decimal("8500.00")
        assert stored.updated_at == executed_at
        assert stored.created_at == BASE_TIME

    async def test_update_records_a_rejection(self, repository: TransactionRepository) -> None:
        transaction = pending()
        await repository.create(transaction)

        transaction.mark_rejected("INSUFFICIENT_FUNDS", "not enough", BASE_TIME)
        await repository.update(transaction)

        stored = await repository.get(transaction.id)
        assert (stored.status, stored.failure_code, stored.failure_message) == (
            TransactionStatus.REJECTED,
            "INSUFFICIENT_FUNDS",
            "not enough",
        )
        assert stored.balance_after is None

    async def test_update_unknown_id_raises_not_found(self, repository: TransactionRepository) -> None:
        with pytest.raises(TransactionNotFoundError):
            await repository.update(pending())

    async def test_callers_cannot_mutate_stored_state(self, repository: TransactionRepository) -> None:
        transaction = pending()
        await repository.create(transaction)
        transaction.status = TransactionStatus.EXECUTED

        read = await repository.get(transaction.id)
        read.status = TransactionStatus.FAILED

        assert (await repository.get(transaction.id)).status is TransactionStatus.PENDING

    async def test_search_returns_newest_first_with_id_as_tie_breaker(
        self, repository: TransactionRepository
    ) -> None:
        older = pending(created_at=BASE_TIME)
        tied = [pending(created_at=BASE_TIME + timedelta(seconds=1)) for _ in range(3)]
        for transaction in [older, *tied]:
            await repository.create(transaction)

        found = await repository.search(TransactionFilter(limit=10))

        expected = [*sorted(tied, key=lambda tx: tx.id.bytes, reverse=True), older]
        assert [tx.id for tx in found] == [tx.id for tx in expected]

    async def test_search_applies_every_filter(self, repository: TransactionRepository) -> None:
        match = pending(account_id="acc-1", type=TransactionType.CREDIT)
        match.mark_rejected("ACCOUNT_BLOCKED", "blocked", BASE_TIME)
        others = [
            pending(account_id="acc-2", type=TransactionType.CREDIT),
            pending(account_id="acc-1", type=TransactionType.DEBIT),
            pending(account_id="acc-1", type=TransactionType.CREDIT),
        ]
        others[0].mark_rejected("ACCOUNT_BLOCKED", "blocked", BASE_TIME)
        others[1].mark_rejected("ACCOUNT_BLOCKED", "blocked", BASE_TIME)
        for transaction in [match, *others]:
            await repository.create(transaction)

        found = await repository.search(
            TransactionFilter(
                limit=10,
                account_id="acc-1",
                status=TransactionStatus.REJECTED,
                type=TransactionType.CREDIT,
            )
        )

        assert [tx.id for tx in found] == [match.id]

    async def test_search_pages_with_limit_and_offset(self, repository: TransactionRepository) -> None:
        created = [pending(created_at=BASE_TIME + timedelta(seconds=i)) for i in range(5)]
        for transaction in created:
            await repository.create(transaction)
        newest_first = [tx.id for tx in reversed(created)]

        second_page = await repository.search(TransactionFilter(limit=2, offset=2))
        past_the_end = await repository.search(TransactionFilter(limit=2, offset=10))

        assert [tx.id for tx in second_page] == newest_first[2:4]
        assert past_the_end == []
