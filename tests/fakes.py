"""Test doubles shared by the unit tests."""

from collections.abc import Awaitable, Callable
from decimal import Decimal

from spin_transactions.domain.models import ExecutionResult
from spin_transactions.domain.ports import ExecutionRequest

type Behaviour = Callable[[ExecutionRequest], Awaitable[ExecutionResult]]


class FakeProvider:
    def __init__(self, behaviour: Behaviour) -> None:
        self.calls: list[ExecutionRequest] = []
        self._behaviour = behaviour

    async def execute(self, request: ExecutionRequest) -> ExecutionResult:
        self.calls.append(request)
        return await self._behaviour(request)


def approving(balance: str = "5500.00") -> FakeProvider:
    async def approve(_: ExecutionRequest) -> ExecutionResult:
        return ExecutionResult(provider_transaction_id="txn-789", balance=Decimal(balance), executed_at=None)

    return FakeProvider(approve)


def raising(error: Exception) -> FakeProvider:
    async def fail(_: ExecutionRequest) -> ExecutionResult:
        raise error

    return FakeProvider(fail)
