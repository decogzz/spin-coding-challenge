"""Wire format of the API.

The response models are what the endpoints serialize, so the OpenAPI document
at /docs cannot drift from the actual responses.
"""

from datetime import datetime
from decimal import Decimal
from typing import Annotated, Any, Self
from uuid import UUID

from pydantic import BaseModel, BeforeValidator, ConfigDict, Field, WithJsonSchema
from pydantic.alias_generators import to_camel
from pydantic_core import PydanticCustomError

from spin_transactions.domain.errors import Violation
from spin_transactions.domain.models import (
    ExecuteCommand,
    ListQuery,
    Page,
    Transaction,
    TransactionStatus,
    TransactionType,
)


def _require_json_number(value: object) -> object:
    if isinstance(value, bool) or not isinstance(value, int | Decimal):
        raise PydanticCustomError("invalid_amount", "amount must be a JSON number")
    return value


MONEY_SCHEMA = WithJsonSchema({"type": "number", "multipleOf": 0.01, "examples": [1500.00]})

RequestMoney = Annotated[Decimal, BeforeValidator(_require_json_number), MONEY_SCHEMA]
Money = Annotated[Decimal, MONEY_SCHEMA]


class WireModel(BaseModel):
    model_config = ConfigDict(alias_generator=to_camel, validate_by_name=True, frozen=True)

    def to_wire(self) -> dict[str, Any]:
        """Python objects keyed by the camelCase names, ready for jsonutil.dumps."""
        return self.model_dump(by_alias=True)


class CreateTransactionRequest(WireModel):
    """Business rules (limits, currency, type) are checked by the domain, not here."""

    # Clients must use the camelCase names; ``account_id`` is an unknown field.
    model_config = ConfigDict(extra="forbid", validate_by_name=False)

    account_id: str
    type: str
    amount: RequestMoney
    currency: str
    description: str = ""

    def to_command(self) -> ExecuteCommand:
        return ExecuteCommand(
            account_id=self.account_id,
            type=self.type,
            amount=self.amount,
            currency=self.currency,
            description=self.description,
        )


class ListTransactionsParams(WireModel):
    """Query parameters of GET /transactions. Values are validated by the domain."""

    account_id: str | None = Field(None, max_length=64)
    status: str | None = Field(None, json_schema_extra={"enum": [s.value for s in TransactionStatus]})
    type: str | None = Field(None, json_schema_extra={"enum": [t.value for t in TransactionType]})
    page: int | None = Field(None, description="1-based page number (default 1)")
    limit: int | None = Field(None, description="Page size, 1 to 100 (default 20)")

    def to_query(self) -> ListQuery:
        return ListQuery(
            account_id=self.account_id, status=self.status, type=self.type, page=self.page, limit=self.limit
        )


class TransactionBody(WireModel):
    id: UUID
    account_id: str
    type: TransactionType
    amount: Money
    currency: str
    description: str
    status: TransactionStatus
    provider_transaction_id: str | None
    balance_after: Money | None
    failure_code: str | None
    failure_message: str | None
    created_at: datetime
    updated_at: datetime

    @classmethod
    def from_domain(cls, transaction: Transaction) -> Self:
        return cls(
            id=transaction.id,
            account_id=transaction.account_id,
            type=transaction.type,
            amount=transaction.amount,
            currency=transaction.currency,
            description=transaction.description,
            status=transaction.status,
            provider_transaction_id=transaction.provider_transaction_id,
            balance_after=transaction.balance_after,
            failure_code=transaction.failure_code,
            failure_message=transaction.failure_message,
            created_at=transaction.created_at,
            updated_at=transaction.updated_at,
        )


class Pagination(WireModel):
    page: int
    limit: int
    has_more: bool


class TransactionPage(WireModel):
    data: list[TransactionBody]
    pagination: Pagination

    @classmethod
    def from_domain(cls, page: Page) -> Self:
        return cls(
            data=[TransactionBody.from_domain(tx) for tx in page.items],
            pagination=Pagination(page=page.page, limit=page.limit, has_more=page.has_more),
        )


class ViolationBody(WireModel):
    field: str
    code: str
    message: str


class ErrorBody(WireModel):
    code: str
    message: str
    details: list[ViolationBody] | None = None

    @classmethod
    def validation_failed(cls, violations: list[Violation]) -> Self:
        return cls(
            code="VALIDATION_FAILED",
            message="the request is invalid; see details",
            details=[ViolationBody(field=v.field, code=v.code, message=v.message) for v in violations],
        )

    def to_wire(self) -> dict[str, Any]:
        return self.model_dump(by_alias=True, exclude_none=True)
