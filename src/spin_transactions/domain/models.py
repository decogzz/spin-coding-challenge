from dataclasses import dataclass, field
from datetime import datetime
from decimal import Decimal
from enum import StrEnum
from uuid import UUID


class TransactionType(StrEnum):
    CREDIT = "CREDIT"
    DEBIT = "DEBIT"


class TransactionStatus(StrEnum):
    # Persisted before the provider is called, so an interrupted execution
    # still leaves a record that can be reconciled.
    PENDING = "PENDING"
    EXECUTED = "EXECUTED"
    REJECTED = "REJECTED"
    FAILED = "FAILED"


# Recorded when the provider could not be reached or kept failing, so the
# outcome of the execution is unconfirmed.
FAILURE_PROVIDER_UNAVAILABLE = "PROVIDER_UNAVAILABLE"


@dataclass(frozen=True, slots=True)
class ExecutionResult:
    provider_transaction_id: str
    balance: Decimal
    executed_at: datetime | None


@dataclass(slots=True)
class Transaction:
    id: UUID
    account_id: str
    type: TransactionType
    amount: Decimal
    currency: str
    description: str
    status: TransactionStatus
    created_at: datetime
    updated_at: datetime
    provider_transaction_id: str | None = None
    balance_after: Decimal | None = None
    failure_code: str | None = None
    failure_message: str | None = None

    def mark_executed(self, result: ExecutionResult, now: datetime) -> None:
        self.status = TransactionStatus.EXECUTED
        self.provider_transaction_id = result.provider_transaction_id
        self.balance_after = result.balance
        self.updated_at = now

    def mark_rejected(self, code: str, message: str, now: datetime) -> None:
        self.status = TransactionStatus.REJECTED
        self.failure_code = code
        self.failure_message = message
        self.updated_at = now

    def mark_failed(self, code: str, message: str, now: datetime) -> None:
        self.status = TransactionStatus.FAILED
        self.failure_code = code
        self.failure_message = message
        self.updated_at = now


@dataclass(frozen=True, slots=True)
class ExecuteCommand:
    """A request to execute a transaction, before any validation.

    ``type`` stays a plain string so an unsupported value is reported by the
    business rules together with every other violation.
    """

    account_id: str
    type: str
    amount: Decimal
    currency: str
    description: str = ""


@dataclass(frozen=True, slots=True)
class ListQuery:
    """Raw listing parameters; ``None`` page and limit mean the defaults."""

    account_id: str | None = None
    status: str | None = None
    type: str | None = None
    page: int | None = None
    limit: int | None = None


@dataclass(frozen=True, slots=True)
class TransactionFilter:
    """Selects transactions newest first. ``None`` fields match everything."""

    limit: int
    offset: int = 0
    account_id: str | None = None
    status: TransactionStatus | None = None
    type: TransactionType | None = None


@dataclass(frozen=True, slots=True)
class Page:
    page: int
    limit: int
    has_more: bool
    items: list[Transaction] = field(default_factory=list)
