"""HTTP adapter for the external provider that owns balances and account locks."""

import asyncio
import logging
import random
from collections.abc import Awaitable, Callable
from dataclasses import dataclass
from datetime import datetime
from decimal import Decimal
from typing import Literal

import httpx
from pydantic import BaseModel, ValidationError

from spin_transactions import jsonutil
from spin_transactions.domain.errors import ProviderRejectedError
from spin_transactions.domain.models import ExecutionResult
from spin_transactions.domain.money import has_cents_precision
from spin_transactions.domain.ports import ExecutionRequest

from .circuit_breaker import CircuitBreaker

EXECUTE_PATH = "/provider/v1/execute"
CODE_REJECTED = "PROVIDER_REJECTED"

logger = logging.getLogger(__name__)


@dataclass(frozen=True, slots=True)
class ProviderClientConfig:
    base_url: str
    # Bounds a single HTTP attempt, not the whole retry sequence.
    attempt_timeout: float = 2.0
    # Counts the first attempt too; 1 disables retries.
    max_attempts: int = 3
    base_backoff: float = 0.1
    max_backoff: float = 1.0
    # Consecutive failed executions that open the circuit.
    breaker_failures: int = 5
    # Seconds the circuit stays open before a probe is allowed.
    breaker_open_timeout: float = 30.0


class ProviderTransientError(Exception):
    """A failure that another attempt might not repeat: network error, timeout, 5xx or 429."""


class ProviderProtocolError(Exception):
    """The provider answered with something the contract does not allow."""


class _ApprovedResponse(BaseModel):
    transactionId: str  # noqa: N815 - mirrors the provider contract
    status: Literal["APPROVED"]
    balance: Decimal
    executedAt: datetime | None = None  # noqa: N815


class _RejectedResponse(BaseModel):
    code: str
    message: str = ""


def create_http_client(
    config: ProviderClientConfig, transport: httpx.AsyncBaseTransport | None = None
) -> httpx.AsyncClient:
    # Keep enough idle connections to avoid reconnecting on every request under load.
    return httpx.AsyncClient(
        base_url=config.base_url,
        timeout=config.attempt_timeout,
        limits=httpx.Limits(max_connections=200, max_keepalive_connections=100),
        transport=transport,
    )


def _is_failure(error: Exception) -> bool:
    # A business rejection means the provider is healthy and answering.
    return not isinstance(error, ProviderRejectedError)


class HttpTransactionProvider:
    """Implements TransactionProvider with per-attempt timeouts, retries and a circuit breaker.

    Retries are safe because every attempt carries the same Idempotency-Key, so
    the provider can recognise a duplicate.
    """

    def __init__(
        self,
        config: ProviderClientConfig,
        http_client: httpx.AsyncClient,
        *,
        sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
    ) -> None:
        self._config = config
        self._http = http_client
        self._sleep = sleep
        self._breaker = CircuitBreaker(
            "provider",
            failure_threshold=config.breaker_failures,
            open_timeout=config.breaker_open_timeout,
            is_failure=_is_failure,
        )

    async def execute(self, request: ExecutionRequest) -> ExecutionResult:
        return await self._breaker.call(lambda: self._execute_with_retries(request))

    async def _execute_with_retries(self, request: ExecutionRequest) -> ExecutionResult:
        body = jsonutil.dumps(
            {
                "accountId": request.account_id,
                "type": request.type,
                "amount": request.amount,
                "currency": request.currency,
            }
        )
        attempt = 1
        while True:
            try:
                return await self._attempt(request.idempotency_key, body)
            except ProviderTransientError as error:
                if attempt >= self._config.max_attempts:
                    error.add_note(f"gave up after {attempt} attempts")
                    raise
                logger.warning(
                    "provider attempt failed",
                    extra={
                        "idempotency_key": request.idempotency_key,
                        "attempt": attempt,
                        "error": str(error),
                    },
                )
            await self._sleep(self.backoff(attempt))
            attempt += 1

    async def _attempt(self, idempotency_key: str, body: str) -> ExecutionResult:
        try:
            async with asyncio.timeout(self._config.attempt_timeout):
                response = await self._http.post(
                    EXECUTE_PATH,
                    content=body,
                    headers={
                        "Content-Type": "application/json",
                        "Accept": "application/json",
                        "Idempotency-Key": idempotency_key,
                    },
                )
        except TimeoutError as error:
            raise ProviderTransientError("provider attempt timed out") from error
        except httpx.TransportError as error:
            raise ProviderTransientError(f"could not reach the provider: {error!r}") from error

        status = response.status_code
        if status == httpx.codes.OK:
            return _decode_approved(response.content)
        if status == httpx.codes.TOO_MANY_REQUESTS or status >= httpx.codes.INTERNAL_SERVER_ERROR:
            raise ProviderTransientError(f"provider responded {status}: {response.text[:200]}")
        if status >= httpx.codes.BAD_REQUEST:
            raise _decode_rejection(status, response.content)
        raise ProviderProtocolError(f"unexpected provider status {status}")

    def backoff(self, retry: int) -> float:
        """Exponential delay with jitter, so concurrent retries don't hit the provider in lockstep."""
        delay = min(self._config.base_backoff * 2.0 ** (retry - 1), self._config.max_backoff)
        return delay / 2 + random.uniform(0, delay / 2)  # noqa: S311 - jitter needs no cryptographic source


def _decode_approved(content: bytes) -> ExecutionResult:
    # A malformed 200 is not retried: the provider may already have moved the money.
    try:
        body = _ApprovedResponse.model_validate(jsonutil.loads(content))
    except (jsonutil.JSONDecodeFailedError, ValidationError) as error:
        raise ProviderProtocolError(f"invalid successful response from provider: {error}") from error
    if not has_cents_precision(body.balance):
        raise ProviderProtocolError(f"provider returned an invalid balance: {body.balance}")
    return ExecutionResult(
        provider_transaction_id=body.transactionId,
        balance=body.balance,
        executed_at=body.executedAt,
    )


def _decode_rejection(status: int, content: bytes) -> ProviderRejectedError:
    try:
        body = _RejectedResponse.model_validate(jsonutil.loads(content))
    except jsonutil.JSONDecodeFailedError, ValidationError:
        return ProviderRejectedError(CODE_REJECTED, f"provider rejected the transaction with HTTP {status}")
    return ProviderRejectedError(body.code, body.message)
