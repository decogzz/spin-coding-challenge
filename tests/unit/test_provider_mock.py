"""The provider mock on its own, and the real HTTP client talking to it."""

from collections.abc import AsyncIterator
from datetime import UTC, datetime
from decimal import Decimal
from typing import Any

import httpx
import pytest

from spin_transactions import jsonutil
from spin_transactions.adapters.circuit_breaker import CircuitOpenError
from spin_transactions.adapters.provider_client import (
    HttpTransactionProvider,
    ProviderClientConfig,
    ProviderTransientError,
    create_http_client,
)
from spin_transactions.domain.errors import ProviderRejectedError
from spin_transactions.domain.models import TransactionType
from spin_transactions.domain.ports import ExecutionRequest
from spin_transactions.providermock.app import create_app

NOW = datetime(2026, 9, 26, 10, 0, 0, 500000, tzinfo=UTC)


@pytest.fixture
async def mock() -> AsyncIterator[httpx.AsyncClient]:
    app = create_app(initial_balance=Decimal("100.00"), slow_delay=0.3, clock=lambda: NOW)
    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://provider.test") as client:
        yield client


async def execute(client: httpx.AsyncClient, key: str = "", **overrides: Any) -> httpx.Response:
    body = {"accountId": "acc-1", "type": "DEBIT", "amount": Decimal("40.00"), "currency": "MXN", **overrides}
    headers = {"Idempotency-Key": key} if key else {}
    return await client.post("/provider/v1/execute", content=jsonutil.dumps(body), headers=headers)


async def test_approves_and_tracks_balances(mock: httpx.AsyncClient) -> None:
    debit = await execute(mock, "k1")
    credit = await execute(mock, "k2", type="CREDIT", amount=Decimal("0.50"))

    assert debit.status_code == 200
    assert debit.json() == {
        "transactionId": "txn-1",
        "status": "APPROVED",
        "balance": 60.0,
        "executedAt": "2026-09-26T10:00:00Z",
    }
    assert b'"balance":60.00' in debit.content
    assert b'"balance":60.50' in credit.content


async def test_replays_the_reply_for_a_repeated_idempotency_key(mock: httpx.AsyncClient) -> None:
    first = await execute(mock, "same")
    second = await execute(mock, "same")
    third = await execute(mock, "other")

    assert second.json() == first.json()
    assert third.json()["balance"] == 20.0


@pytest.mark.parametrize(
    ("account", "status", "code"),
    [
        ("acc-insufficient", 422, "INSUFFICIENT_FUNDS"),
        ("acc-blocked", 403, "ACCOUNT_BLOCKED"),
        ("acc-error", 500, "PROVIDER_ERROR"),
    ],
)
async def test_scenario_accounts(mock: httpx.AsyncClient, account: str, status: int, code: str) -> None:
    response = await execute(mock, "k", accountId=account)

    assert response.status_code == status
    assert response.json()["status"] == "REJECTED"
    assert response.json()["code"] == code


async def test_flaky_account_fails_only_the_first_attempt(mock: httpx.AsyncClient) -> None:
    first = await execute(mock, "k", accountId="acc-flaky")
    retry = await execute(mock, "k", accountId="acc-flaky")

    assert (first.status_code, retry.status_code) == (503, 200)


async def test_slow_account_answers_after_the_delay(mock: httpx.AsyncClient) -> None:
    response = await execute(mock, "k", accountId="acc-slow")

    assert response.status_code == 200


@pytest.mark.parametrize(
    "content",
    [
        b"not json",
        b'{"accountId":"acc-1","type":"DEBIT"}',
        b'{"accountId":"acc-1","type":"REFUND","amount":1}',
        b'{"accountId":"acc-1","type":"DEBIT","amount":"10"}',
        b'{"accountId":"acc-1","type":"DEBIT","amount":-5}',
        b'{"accountId":"","type":"DEBIT","amount":5}',
    ],
)
async def test_invalid_requests_are_rejected(mock: httpx.AsyncClient, content: bytes) -> None:
    response = await mock.post("/provider/v1/execute", content=content)

    assert response.status_code == 400
    assert response.json()["code"] == "INVALID_REQUEST"


def request(account_id: str, key: str, amount: str = "40.00") -> ExecutionRequest:
    return ExecutionRequest(
        idempotency_key=key,
        account_id=account_id,
        type=TransactionType.DEBIT,
        amount=Decimal(amount),
        currency="MXN",
    )


def real_client(**overrides: Any) -> HttpTransactionProvider:
    app = create_app(initial_balance=Decimal("100.00"), slow_delay=1.0)
    config = ProviderClientConfig(
        base_url="http://provider.test", attempt_timeout=0.2, base_backoff=0.001, **overrides
    )
    return HttpTransactionProvider(config, create_http_client(config, transport=httpx.ASGITransport(app=app)))


async def test_client_against_mock_end_to_end() -> None:
    client = real_client()

    approved = await client.execute(request("acc-1", "k1"))
    flaky = await client.execute(request("acc-flaky", "k2"))

    assert approved.balance == Decimal("60.00")
    assert approved.executed_at is not None
    assert flaky.balance == Decimal("60.00")
    with pytest.raises(ProviderRejectedError) as rejected:
        await client.execute(request("acc-insufficient", "k3"))
    assert rejected.value.code == "INSUFFICIENT_FUNDS"


async def test_client_times_out_on_the_slow_account_and_opens_the_circuit() -> None:
    client = real_client(max_attempts=2, breaker_failures=2)

    for key in ("k1", "k2"):
        with pytest.raises(ProviderTransientError):
            await client.execute(request("acc-slow", key))
    with pytest.raises(CircuitOpenError):
        await client.execute(request("acc-1", "k3"))
