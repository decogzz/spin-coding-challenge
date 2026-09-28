import asyncio
from collections.abc import Iterator
from datetime import UTC, datetime, timedelta
from decimal import Decimal
from uuid import uuid4

import pytest

from spin_transactions.adapters.memory import InMemoryTransactionRepository
from spin_transactions.domain.errors import (
    ProviderRejectedError,
    TransactionNotFoundError,
    ValidationFailedError,
)
from spin_transactions.domain.models import (
    FAILURE_PROVIDER_UNAVAILABLE,
    ExecuteCommand,
    ExecutionResult,
    ListQuery,
    Transaction,
    TransactionFilter,
    TransactionStatus,
)
from spin_transactions.domain.ports import ExecutionRequest, TransactionProvider
from spin_transactions.domain.service import DEFAULT_PAGE_SIZE, TransactionService
from tests.fakes import FakeProvider, approving, raising

FIXED_NOW = datetime(2025, 3, 15, 10, 30, tzinfo=UTC)

CREDIT = ExecuteCommand(
    account_id="acc-123456",
    type="CREDIT",
    amount=Decimal("1500.00"),
    currency="MXN",
    description="Transferencia recibida",
)


def make_service(
    provider: TransactionProvider, repository: InMemoryTransactionRepository | None = None
) -> tuple[TransactionService, InMemoryTransactionRepository]:
    repository = repository or InMemoryTransactionRepository()
    return TransactionService(repository, provider, clock=lambda: FIXED_NOW), repository


async def test_execute_approved() -> None:
    provider = approving("5500.00")
    service, repository = make_service(provider)

    transaction = await service.execute(CREDIT)

    assert transaction.status is TransactionStatus.EXECUTED
    assert transaction.provider_transaction_id == "txn-789"
    assert transaction.balance_after == Decimal("5500.00")
    assert transaction.created_at == FIXED_NOW
    assert transaction.id.version == 7

    [call] = provider.calls
    assert call.idempotency_key == str(transaction.id)
    assert (call.account_id, call.type, call.amount, call.currency) == (
        "acc-123456",
        "CREDIT",
        Decimal("1500.00"),
        "MXN",
    )

    assert await repository.get(transaction.id) == transaction


async def test_amount_is_normalized_to_cents() -> None:
    service, _ = make_service(approving())

    transaction = await service.execute(ExecuteCommand("acc-1", "CREDIT", Decimal("1E+3"), "MXN"))

    assert str(transaction.amount) == "1000.00"


async def test_execute_rejected_by_provider() -> None:
    service, repository = make_service(
        raising(ProviderRejectedError("INSUFFICIENT_FUNDS", "not enough balance"))
    )

    transaction = await service.execute(CREDIT)

    assert transaction.status is TransactionStatus.REJECTED
    assert transaction.failure_code == "INSUFFICIENT_FUNDS"
    assert transaction.balance_after is None
    assert (await repository.get(transaction.id)).status is TransactionStatus.REJECTED


async def test_execute_provider_unavailable() -> None:
    service, repository = make_service(raising(ConnectionError("connection refused")))

    transaction = await service.execute(CREDIT)

    assert transaction.status is TransactionStatus.FAILED
    assert transaction.failure_code == FAILURE_PROVIDER_UNAVAILABLE
    assert (await repository.get(transaction.id)).status is TransactionStatus.FAILED


async def test_rule_violation_never_reaches_provider_or_storage() -> None:
    provider = approving()
    service, _ = make_service(provider)

    with pytest.raises(ValidationFailedError):
        await service.execute(ExecuteCommand("acc-1", "DEBIT", Decimal("10000.01"), "MXN"))

    assert provider.calls == []
    assert (await service.search(ListQuery())).items == []


class FailingCreateRepository(InMemoryTransactionRepository):
    async def create(self, transaction: Transaction) -> None:
        raise ConnectionError("database is down")


async def test_provider_is_not_called_when_persistence_fails() -> None:
    provider = approving()
    service, _ = make_service(provider, FailingCreateRepository())

    with pytest.raises(ConnectionError):
        await service.execute(CREDIT)

    assert provider.calls == []


async def test_outcome_is_recorded_when_the_client_disconnects() -> None:
    provider_started = asyncio.Event()
    release_provider = asyncio.Event()

    async def slow_approval(_: ExecutionRequest) -> ExecutionResult:
        provider_started.set()
        await release_provider.wait()
        return ExecutionResult("txn-1", Decimal("100.00"), None)

    service, repository = make_service(FakeProvider(slow_approval))

    request = asyncio.create_task(service.execute(CREDIT))
    await provider_started.wait()
    request.cancel()
    with pytest.raises(asyncio.CancelledError):
        await request

    release_provider.set()
    async with asyncio.timeout(1):
        await service.drain()

    [stored] = await repository.search(TransactionFilter(limit=10))
    assert stored.status is TransactionStatus.EXECUTED


async def test_get_unknown_transaction() -> None:
    service, _ = make_service(approving())

    with pytest.raises(TransactionNotFoundError):
        await service.get(uuid4())


def ticking_clock() -> Iterator[datetime]:
    now = FIXED_NOW
    while True:
        now += timedelta(seconds=1)
        yield now


async def test_search_paginates_and_filters() -> None:
    clock = ticking_clock()
    repository = InMemoryTransactionRepository()
    service = TransactionService(repository, approving(), clock=lambda: next(clock))
    for i in range(5):
        account = "acc-other" if i % 2 else "acc-123456"
        await service.execute(ExecuteCommand(account, "CREDIT", Decimal("100.00"), "MXN"))

    first = await service.search(ListQuery(limit=2))
    assert (len(first.items), first.has_more, first.page) == (2, True, 1)
    assert first.items[0].created_at > first.items[1].created_at

    last = await service.search(ListQuery(page=3, limit=2))
    assert (len(last.items), last.has_more) == (1, False)

    filtered = await service.search(ListQuery(account_id="acc-other"))
    assert filtered.limit == DEFAULT_PAGE_SIZE
    assert [tx.account_id for tx in filtered.items] == ["acc-other", "acc-other"]


async def test_search_rejects_invalid_query() -> None:
    service, _ = make_service(approving())

    with pytest.raises(ValidationFailedError) as excinfo:
        await service.search(ListQuery(status="DONE", type="REFUND", page=0, limit=101))

    assert [v.field for v in excinfo.value.violations] == ["status", "type", "page", "limit"]
