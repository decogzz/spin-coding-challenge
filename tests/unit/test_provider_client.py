import asyncio
from collections.abc import Awaitable, Callable
from datetime import UTC, datetime
from decimal import Decimal

import httpx
import pytest

from spin_transactions.adapters.circuit_breaker import CircuitOpenError
from spin_transactions.adapters.provider_client import (
    EXECUTE_PATH,
    HttpTransactionProvider,
    ProviderClientConfig,
    ProviderProtocolError,
    ProviderTransientError,
    create_http_client,
)
from spin_transactions.domain.errors import ProviderRejectedError
from spin_transactions.domain.models import TransactionType
from spin_transactions.domain.ports import ExecutionRequest

type Handler = Callable[[httpx.Request], Awaitable[httpx.Response]]

REQUEST = ExecutionRequest(
    idempotency_key="0199b2a4-0000-7000-8000-000000000001",
    account_id="acc-1",
    type=TransactionType.DEBIT,
    amount=Decimal("1500.00"),
    currency="MXN",
)

APPROVED = (
    b'{"transactionId":"prv-1","status":"APPROVED","balance":8500.00,"executedAt":"2026-09-26T10:00:00Z"}'
)


class Recorder:
    """Serves scripted responses in order and records every request received."""

    def __init__(self, *responses: httpx.Response | Handler) -> None:
        self._responses = list(responses)
        self.requests: list[httpx.Request] = []

    async def __call__(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        response = self._responses[min(len(self.requests), len(self._responses)) - 1]
        if isinstance(response, httpx.Response):
            return response
        return await response(request)


def provider(recorder: Recorder, **overrides: float) -> tuple[HttpTransactionProvider, list[float]]:
    config = ProviderClientConfig(base_url="http://provider.test", **overrides)  # type: ignore[arg-type]
    sleeps: list[float] = []

    async def record_sleep(seconds: float) -> None:
        sleeps.append(seconds)

    client = create_http_client(config, transport=httpx.MockTransport(recorder))
    return HttpTransactionProvider(config, client, sleep=record_sleep), sleeps


def ok(content: bytes = APPROVED) -> httpx.Response:
    return httpx.Response(200, content=content)


async def test_approved_execution_is_decoded_exactly() -> None:
    recorder = Recorder(ok(b'{"transactionId":"prv-1","status":"APPROVED","balance":999999999999999.99}'))
    client, _ = provider(recorder)

    result = await client.execute(REQUEST)

    assert result.provider_transaction_id == "prv-1"
    assert result.balance == Decimal("999999999999999.99")
    assert result.executed_at is None


async def test_request_follows_the_provider_contract() -> None:
    recorder = Recorder(ok())
    client, _ = provider(recorder)

    result = await client.execute(REQUEST)

    [request] = recorder.requests
    assert request.method == "POST"
    assert request.url.path == EXECUTE_PATH
    assert request.headers["Idempotency-Key"] == REQUEST.idempotency_key
    assert request.headers["Content-Type"] == "application/json"
    assert request.content == b'{"accountId":"acc-1","type":"DEBIT","amount":1500.00,"currency":"MXN"}'
    assert result.executed_at == datetime(2026, 9, 26, 10, tzinfo=UTC)


@pytest.mark.parametrize(
    ("status", "body", "code", "message"),
    [
        (
            422,
            b'{"status":"REJECTED","code":"INSUFFICIENT_FUNDS","message":"not enough"}',
            "INSUFFICIENT_FUNDS",
            "not enough",
        ),
        (
            403,
            b'{"status":"REJECTED","code":"ACCOUNT_BLOCKED","message":"blocked"}',
            "ACCOUNT_BLOCKED",
            "blocked",
        ),
        (
            400,
            b"<html>bad request</html>",
            "PROVIDER_REJECTED",
            "provider rejected the transaction with HTTP 400",
        ),
    ],
)
async def test_client_errors_are_rejections_and_not_retried(
    status: int, body: bytes, code: str, message: str
) -> None:
    recorder = Recorder(httpx.Response(status, content=body))
    client, _ = provider(recorder)

    with pytest.raises(ProviderRejectedError) as raised:
        await client.execute(REQUEST)

    assert (raised.value.code, raised.value.message) == (code, message)
    assert len(recorder.requests) == 1


@pytest.mark.parametrize("status", [500, 502, 503, 429])
async def test_transient_status_is_retried_with_the_same_idempotency_key(status: int) -> None:
    recorder = Recorder(httpx.Response(status, content=b"{}"), ok())
    client, sleeps = provider(recorder)

    result = await client.execute(REQUEST)

    assert result.provider_transaction_id == "prv-1"
    assert len(recorder.requests) == 2
    assert {r.headers["Idempotency-Key"] for r in recorder.requests} == {REQUEST.idempotency_key}
    assert len(sleeps) == 1


async def test_network_errors_are_retried() -> None:
    async def refuse(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("connection refused", request=request)

    recorder = Recorder(refuse, ok())
    client, _ = provider(recorder)

    assert (await client.execute(REQUEST)).balance == Decimal("8500.00")


async def test_each_attempt_is_bounded_by_the_timeout() -> None:
    async def hang(request: httpx.Request) -> httpx.Response:
        await asyncio.sleep(10)
        return ok()

    recorder = Recorder(hang, ok())
    client, _ = provider(recorder, attempt_timeout=0.05)

    async with asyncio.timeout(1):
        result = await client.execute(REQUEST)

    assert result.provider_transaction_id == "prv-1"
    assert len(recorder.requests) == 2


async def test_gives_up_after_max_attempts() -> None:
    recorder = Recorder(httpx.Response(503, content=b"unavailable"))
    client, sleeps = provider(recorder, max_attempts=3)

    with pytest.raises(ProviderTransientError) as raised:
        await client.execute(REQUEST)

    assert len(recorder.requests) == 3
    assert len(sleeps) == 2
    assert "gave up after 3 attempts" in raised.value.__notes__


@pytest.mark.parametrize(
    "body",
    [
        b"not json",
        b'{"transactionId":"prv-1","status":"PENDING","balance":1.00}',
        b'{"status":"APPROVED","balance":1.00}',
        b'{"transactionId":"prv-1","status":"APPROVED","balance":1.001}',
    ],
)
async def test_malformed_success_is_a_protocol_error_and_not_retried(body: bytes) -> None:
    recorder = Recorder(ok(body))
    client, _ = provider(recorder)

    with pytest.raises(ProviderProtocolError):
        await client.execute(REQUEST)

    assert len(recorder.requests) == 1


async def test_circuit_opens_after_repeated_failures_and_ignores_rejections() -> None:
    rejection = httpx.Response(422, content=b'{"code":"INSUFFICIENT_FUNDS","message":"no"}')
    failure = httpx.Response(500, content=b"{}")
    recorder = Recorder(rejection, rejection, rejection, failure)
    client, _ = provider(recorder, max_attempts=1, breaker_failures=2)

    for _ in range(3):
        with pytest.raises(ProviderRejectedError):
            await client.execute(REQUEST)
    for _ in range(2):
        with pytest.raises(ProviderTransientError):
            await client.execute(REQUEST)
    with pytest.raises(CircuitOpenError):
        await client.execute(REQUEST)

    assert len(recorder.requests) == 5


def test_backoff_grows_exponentially_with_jitter_and_is_capped() -> None:
    client, _ = provider(Recorder(ok()), base_backoff=0.1, max_backoff=0.5)

    for retry, ceiling in [(1, 0.1), (2, 0.2), (3, 0.4), (4, 0.5), (10, 0.5)]:
        delay = client.backoff(retry)
        assert ceiling / 2 <= delay <= ceiling
