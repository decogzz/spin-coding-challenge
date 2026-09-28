"""Mock of the external provider.

It keeps balances in memory, honours Idempotency-Key and reserves a few
account ids that reproduce failure scenarios on demand. Any other account
starts with ``initial_balance``.
"""

import asyncio
import time
from collections.abc import Callable
from dataclasses import dataclass, field
from datetime import UTC, datetime
from decimal import Decimal
from typing import Any

from fastapi import FastAPI, Request, Response

from spin_transactions import jsonutil
from spin_transactions.domain.money import has_cents_precision

# Starts with a zero balance, so every DEBIT is rejected.
ACCOUNT_INSUFFICIENT = "acc-insufficient"
# Locked by the provider; rejects every transaction.
ACCOUNT_BLOCKED = "acc-blocked"
# Always answers 500.
ACCOUNT_ERROR = "acc-error"
# Answers after ``slow_delay``, longer than the client's per-attempt timeout.
ACCOUNT_SLOW = "acc-slow"
# Answers 503 to the first attempt of each idempotency key.
ACCOUNT_FLAKY = "acc-flaky"

MAX_BODY_BYTES = 64 * 1024


@dataclass(frozen=True, slots=True)
class Reply:
    status: int
    body: dict[str, Any]


def rejection(status: int, code: str, message: str) -> Reply:
    return Reply(status, {"status": "REJECTED", "code": code, "message": message})


@dataclass
class Ledger:
    initial_balance: Decimal
    clock: Callable[[], datetime] = lambda: datetime.now(UTC)
    balances: dict[str, Decimal] = field(default_factory=lambda: {ACCOUNT_INSUFFICIENT: Decimal("0.00")})
    replies: dict[str, Reply] = field(default_factory=dict)
    flaky_attempts: dict[str, int] = field(default_factory=dict)
    sequence: int = 0

    def process(self, key: str, account_id: str, type: str, amount: Decimal) -> Reply:
        """Runs without awaiting, so concurrent requests cannot interleave inside it."""
        if key and key in self.replies:
            return self.replies[key]

        if account_id == ACCOUNT_ERROR:
            return rejection(500, "PROVIDER_ERROR", "internal provider error")
        if account_id == ACCOUNT_FLAKY:
            self.flaky_attempts[key] = self.flaky_attempts.get(key, 0) + 1
            if self.flaky_attempts[key] == 1:
                return rejection(503, "TEMPORARILY_UNAVAILABLE", "try again later")

        reply = self._apply(account_id, type, amount)
        if key:
            self.replies[key] = reply
        return reply

    def _apply(self, account_id: str, type: str, amount: Decimal) -> Reply:
        if account_id == ACCOUNT_BLOCKED:
            return rejection(403, "ACCOUNT_BLOCKED", "The account is blocked and cannot operate")

        balance = self.balances.get(account_id, self.initial_balance)
        if type == "DEBIT":
            if amount > balance:
                return rejection(
                    422,
                    "INSUFFICIENT_FUNDS",
                    "The account does not have enough balance to complete the transaction",
                )
            balance -= amount
        else:
            balance += amount
        self.balances[account_id] = balance
        self.sequence += 1
        return Reply(
            200,
            {
                "transactionId": f"txn-{self.sequence}",
                "status": "APPROVED",
                "balance": balance,
                "executedAt": self.clock().replace(microsecond=0),
            },
        )


def _json(reply: Reply) -> Response:
    return Response(jsonutil.dumps(reply.body), reply.status, media_type="application/json")


def _parse(body: bytes) -> tuple[str, str, Decimal] | None:
    try:
        data = jsonutil.loads(body)
    except jsonutil.JSONDecodeFailedError:
        return None
    if not isinstance(data, dict):
        return None
    account_id, type, amount = data.get("accountId"), data.get("type"), data.get("amount")
    if not isinstance(account_id, str) or not account_id or type not in {"CREDIT", "DEBIT"}:
        return None
    if isinstance(amount, bool) or not isinstance(amount, int | Decimal):
        return None
    amount = Decimal(amount)
    if not has_cents_precision(amount) or amount <= 0:
        return None
    return account_id, type, amount


async def _wait_unless_disconnected(request: Request, delay: float) -> bool:
    """Sleep for ``delay``; False if the caller gave up first."""
    deadline = time.monotonic() + delay
    while (remaining := deadline - time.monotonic()) > 0:
        if await request.is_disconnected():
            return False
        await asyncio.sleep(min(remaining, 0.05))
    return True


def create_app(
    *,
    initial_balance: Decimal = Decimal("10000.00"),
    slow_delay: float = 5.0,
    clock: Callable[[], datetime] | None = None,
) -> FastAPI:
    ledger = Ledger(initial_balance, clock) if clock else Ledger(initial_balance)
    app = FastAPI(title="Provider mock", docs_url=None, redoc_url=None, openapi_url=None)
    app.state.ledger = ledger

    @app.post("/provider/v1/execute")
    async def execute(request: Request) -> Response:
        body = await request.body()
        parsed = _parse(body) if len(body) <= MAX_BODY_BYTES else None
        if parsed is None:
            return _json(
                rejection(400, "INVALID_REQUEST", "accountId, type and a positive amount are required")
            )
        account_id, type, amount = parsed

        # A request abandoned by the client while waiting is never applied.
        if account_id == ACCOUNT_SLOW and not await _wait_unless_disconnected(request, slow_delay):
            return Response(status_code=499)

        return _json(ledger.process(request.headers.get("Idempotency-Key", ""), account_id, type, amount))

    @app.get("/healthz")
    async def healthz() -> Response:
        return Response(jsonutil.dumps({"status": "ok"}), media_type="application/json")

    return app
