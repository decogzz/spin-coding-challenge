"""PostgreSQL repository built on SQLAlchemy Core with the asyncpg driver."""

from typing import Any
from uuid import UUID

from sqlalchemy import (
    CheckConstraint,
    Column,
    DateTime,
    Index,
    MetaData,
    Numeric,
    String,
    Table,
    Text,
    Uuid,
    insert,
    select,
    text,
    update,
)
from sqlalchemy.exc import IntegrityError
from sqlalchemy.ext.asyncio import AsyncEngine, create_async_engine

from spin_transactions.domain.errors import TransactionAlreadyExistsError, TransactionNotFoundError
from spin_transactions.domain.models import Transaction, TransactionFilter, TransactionStatus, TransactionType

UNIQUE_VIOLATION = "23505"

metadata = MetaData()

# Money is NUMERIC(17, 2): exact, and wide enough for 999,999,999,999,999.99.
transactions = Table(
    "transactions",
    metadata,
    Column("id", Uuid(as_uuid=True), primary_key=True),
    Column("account_id", String(64), nullable=False),
    Column("type", Text, nullable=False),
    Column("amount", Numeric(17, 2, asdecimal=True), nullable=False),
    Column("currency", String(3), nullable=False),
    Column("description", String(255), nullable=False, server_default=""),
    Column("status", Text, nullable=False),
    Column("provider_transaction_id", Text),
    Column("balance_after", Numeric(17, 2, asdecimal=True)),
    Column("failure_code", Text),
    Column("failure_message", Text),
    Column("created_at", DateTime(timezone=True), nullable=False),
    Column("updated_at", DateTime(timezone=True), nullable=False),
    CheckConstraint("type IN ('CREDIT', 'DEBIT')", name="transactions_type_check"),
    CheckConstraint(
        "status IN ('PENDING', 'EXECUTED', 'REJECTED', 'FAILED')", name="transactions_status_check"
    ),
    CheckConstraint("amount > 0", name="transactions_amount_check"),
)

# Each index matches a GET /transactions filter plus its newest-first ordering,
# so a page is read straight off the index without sorting.
Index(
    "transactions_account_created_idx",
    transactions.c.account_id,
    transactions.c.created_at.desc(),
    transactions.c.id.desc(),
)
Index(
    "transactions_status_created_idx",
    transactions.c.status,
    transactions.c.created_at.desc(),
    transactions.c.id.desc(),
)
Index("transactions_created_idx", transactions.c.created_at.desc(), transactions.c.id.desc())


def async_url(url: str) -> str:
    """Accept plain postgres:// URLs and point SQLAlchemy at the asyncpg driver."""
    for prefix in ("postgres://", "postgresql://"):
        if url.startswith(prefix):
            return "postgresql+asyncpg://" + url.removeprefix(prefix)
    return url


def create_engine(
    url: str,
    *,
    pool_size: int = 10,
    max_overflow: int = 10,
    pool_timeout: float = 5.0,
    pool_recycle: float = 1800.0,
) -> AsyncEngine:
    return create_async_engine(
        async_url(url),
        pool_size=pool_size,
        max_overflow=max_overflow,
        pool_timeout=pool_timeout,
        pool_recycle=pool_recycle,
        pool_pre_ping=True,
    )


class PostgresTransactionRepository:
    def __init__(self, engine: AsyncEngine) -> None:
        self._engine = engine

    async def create(self, transaction: Transaction) -> None:
        try:
            async with self._engine.begin() as connection:
                await connection.execute(insert(transactions).values(_to_row(transaction)))
        except IntegrityError as error:
            if getattr(error.orig, "sqlstate", None) == UNIQUE_VIOLATION:
                raise TransactionAlreadyExistsError(str(transaction.id)) from error
            raise

    async def update(self, transaction: Transaction) -> None:
        statement = (
            update(transactions)
            .where(transactions.c.id == transaction.id)
            .values(
                status=transaction.status.value,
                provider_transaction_id=transaction.provider_transaction_id,
                balance_after=transaction.balance_after,
                failure_code=transaction.failure_code,
                failure_message=transaction.failure_message,
                updated_at=transaction.updated_at,
            )
        )
        async with self._engine.begin() as connection:
            result = await connection.execute(statement)
        if result.rowcount == 0:
            raise TransactionNotFoundError(str(transaction.id))

    async def get(self, transaction_id: UUID) -> Transaction:
        async with self._engine.connect() as connection:
            result = await connection.execute(select(transactions).where(transactions.c.id == transaction_id))
            row = result.mappings().one_or_none()
        if row is None:
            raise TransactionNotFoundError(str(transaction_id))
        return _from_row(row)

    async def search(self, criteria: TransactionFilter) -> list[Transaction]:
        statement = select(transactions)
        if criteria.account_id is not None:
            statement = statement.where(transactions.c.account_id == criteria.account_id)
        if criteria.status is not None:
            statement = statement.where(transactions.c.status == criteria.status.value)
        if criteria.type is not None:
            statement = statement.where(transactions.c.type == criteria.type.value)
        statement = (
            statement.order_by(transactions.c.created_at.desc(), transactions.c.id.desc())
            .limit(criteria.limit)
            .offset(criteria.offset)
        )
        async with self._engine.connect() as connection:
            result = await connection.execute(statement)
            return [_from_row(row) for row in result.mappings()]

    async def ping(self) -> None:
        async with self._engine.connect() as connection:
            await connection.execute(text("SELECT 1"))


def _to_row(transaction: Transaction) -> dict[str, Any]:
    return {
        "id": transaction.id,
        "account_id": transaction.account_id,
        "type": transaction.type.value,
        "amount": transaction.amount,
        "currency": transaction.currency,
        "description": transaction.description,
        "status": transaction.status.value,
        "provider_transaction_id": transaction.provider_transaction_id,
        "balance_after": transaction.balance_after,
        "failure_code": transaction.failure_code,
        "failure_message": transaction.failure_message,
        "created_at": transaction.created_at,
        "updated_at": transaction.updated_at,
    }


def _from_row(row: Any) -> Transaction:
    return Transaction(
        id=row["id"],
        account_id=row["account_id"],
        type=TransactionType(row["type"]),
        amount=row["amount"],
        currency=row["currency"],
        description=row["description"],
        status=TransactionStatus(row["status"]),
        created_at=row["created_at"],
        updated_at=row["updated_at"],
        provider_transaction_id=row["provider_transaction_id"],
        balance_after=row["balance_after"],
        failure_code=row["failure_code"],
        failure_message=row["failure_message"],
    )
