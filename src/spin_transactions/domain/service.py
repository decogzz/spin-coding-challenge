import asyncio
import logging
import uuid
from collections.abc import Callable, Sequence
from datetime import UTC, datetime
from uuid import UUID

from spin_transactions.domain.errors import ProviderRejectedError, ValidationFailedError, Violation
from spin_transactions.domain.models import (
    FAILURE_PROVIDER_UNAVAILABLE,
    ExecuteCommand,
    ListQuery,
    Page,
    Transaction,
    TransactionFilter,
    TransactionStatus,
    TransactionType,
)
from spin_transactions.domain.money import to_money
from spin_transactions.domain.ports import ExecutionRequest, TransactionProvider, TransactionRepository
from spin_transactions.domain.rules import DEFAULT_RULES, Rule, check_rules

DEFAULT_PAGE_SIZE = 20
MAX_PAGE_SIZE = 100

logger = logging.getLogger(__name__)


def utc_now() -> datetime:
    return datetime.now(UTC)


class TransactionService:
    def __init__(
        self,
        repository: TransactionRepository,
        provider: TransactionProvider,
        *,
        rules: Sequence[Rule] = DEFAULT_RULES,
        clock: Callable[[], datetime] = utc_now,
        id_factory: Callable[[], UUID] = uuid.uuid7,
    ) -> None:
        self._repository = repository
        self._provider = provider
        self._rules = rules
        self._clock = clock
        self._id_factory = id_factory
        self._in_flight: set[asyncio.Task[Transaction]] = set()

    async def execute(self, command: ExecuteCommand) -> Transaction:
        """Validate, record as PENDING, run against the provider and record the outcome.

        A provider rejection or failure is not an exception: it is reflected in
        the returned transaction's status.
        """
        check_rules(self._rules, command)

        now = self._clock()
        transaction = Transaction(
            id=self._id_factory(),
            account_id=command.account_id,
            type=TransactionType(command.type),
            amount=to_money(command.amount),
            currency=command.currency,
            description=command.description,
            status=TransactionStatus.PENDING,
            created_at=now,
            updated_at=now,
        )
        await self._repository.create(transaction)

        # Once the transaction is recorded, a client disconnect (which cancels
        # the request task) must not abort the provider call or the final
        # update halfway. The shielded task runs to completion regardless, and
        # drain() waits for it on shutdown.
        task = asyncio.create_task(self._execute_with_provider(transaction))
        self._in_flight.add(task)
        task.add_done_callback(self._in_flight.discard)
        return await asyncio.shield(task)

    async def _execute_with_provider(self, transaction: Transaction) -> Transaction:
        request = ExecutionRequest(
            idempotency_key=str(transaction.id),
            account_id=transaction.account_id,
            type=transaction.type,
            amount=transaction.amount,
            currency=transaction.currency,
        )
        try:
            result = await self._provider.execute(request)
        except ProviderRejectedError as rejection:
            transaction.mark_rejected(rejection.code, rejection.message, self._clock())
        except Exception:
            logger.exception("provider execution failed", extra={"transaction_id": str(transaction.id)})
            transaction.mark_failed(
                FAILURE_PROVIDER_UNAVAILABLE,
                "the provider could not be reached; the transaction was not confirmed",
                self._clock(),
            )
        else:
            transaction.mark_executed(result, self._clock())

        await self._repository.update(transaction)
        return transaction

    async def drain(self) -> None:
        """Wait for executions still talking to the provider, e.g. during shutdown.

        Callers bound the wait with ``asyncio.timeout``; cancelling it leaves the
        executions themselves untouched.
        """
        if self._in_flight:
            await asyncio.wait(set(self._in_flight))

    async def get(self, transaction_id: UUID) -> Transaction:
        return await self._repository.get(transaction_id)

    async def search(self, query: ListQuery) -> Page:
        """Return one page, newest first.

        The repository is asked for one extra row to know whether another page
        exists without running a COUNT query.
        """
        criteria, page, limit = _to_filter(query)
        items = await self._repository.search(criteria)
        has_more = len(items) > limit
        return Page(page=page, limit=limit, has_more=has_more, items=items[:limit])


def _to_filter(query: ListQuery) -> tuple[TransactionFilter, int, int]:
    page = query.page if query.page is not None else 1
    limit = query.limit if query.limit is not None else DEFAULT_PAGE_SIZE

    violations: list[Violation] = []
    if query.status is not None and query.status not in TransactionStatus:
        violations.append(
            Violation(
                "status", "UNSUPPORTED_STATUS", "status must be one of PENDING, EXECUTED, REJECTED, FAILED"
            )
        )
    if query.type is not None and query.type not in TransactionType:
        violations.append(Violation("type", "UNSUPPORTED_TYPE", "type must be CREDIT or DEBIT"))
    if page < 1:
        violations.append(Violation("page", "OUT_OF_RANGE", "page must be 1 or greater"))
    if not 1 <= limit <= MAX_PAGE_SIZE:
        violations.append(Violation("limit", "OUT_OF_RANGE", f"limit must be between 1 and {MAX_PAGE_SIZE}"))
    if violations:
        raise ValidationFailedError(violations)

    criteria = TransactionFilter(
        limit=limit + 1,
        offset=(page - 1) * limit,
        account_id=query.account_id,
        status=TransactionStatus(query.status) if query.status is not None else None,
        type=TransactionType(query.type) if query.type is not None else None,
    )
    return criteria, page, limit
