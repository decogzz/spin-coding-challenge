import asyncio
import logging
from collections.abc import Awaitable, Callable
from typing import Annotated, Any, cast
from uuid import UUID

from fastapi import APIRouter, Depends, Query, Request, Response
from pydantic import ValidationError

from spin_transactions import jsonutil
from spin_transactions.domain.errors import ValidationFailedError
from spin_transactions.domain.models import TransactionStatus
from spin_transactions.domain.service import TransactionService

from .errors import InvalidRequestBodyError, PayloadTooLargeError, error_response, to_violation
from .responses import ExactJSONResponse
from .schemas import (
    CreateTransactionRequest,
    ErrorBody,
    ListTransactionsParams,
    TransactionBody,
    TransactionPage,
)

MAX_BODY_BYTES = 64 * 1024
READINESS_TIMEOUT = 2.0

# Every execution outcome returns the stored transaction; the status code says which one it was.
STATUS_BY_OUTCOME = {
    TransactionStatus.EXECUTED: 201,
    TransactionStatus.REJECTED: 422,
    TransactionStatus.FAILED: 502,
}

type Readiness = Callable[[], Awaitable[None]]

logger = logging.getLogger(__name__)
router = APIRouter()


def get_service(request: Request) -> TransactionService:
    return cast(TransactionService, request.app.state.service)


Service = Annotated[TransactionService, Depends(get_service)]

_ERRORS: dict[int | str, dict[str, Any]] = {
    400: {"model": ErrorBody, "description": "Invalid request or broken business rule"},
}

_CREATE_REQUEST_DOC = {
    "requestBody": {
        "required": True,
        "content": {
            "application/json": {
                "schema": CreateTransactionRequest.model_json_schema(by_alias=True),
                "example": {
                    "accountId": "acc-123",
                    "type": "DEBIT",
                    "amount": 1500.00,
                    "currency": "MXN",
                    "description": "rent",
                },
            }
        },
    }
}


async def _read_body(request: Request, limit: int) -> bytes:
    declared = request.headers.get("content-length", "")
    if declared.isdigit() and int(declared) > limit:
        raise PayloadTooLargeError(limit)
    body = bytearray()
    async for chunk in request.stream():
        body += chunk
        if len(body) > limit:
            raise PayloadTooLargeError(limit)
    return bytes(body)


async def read_create_request(request: Request) -> CreateTransactionRequest:
    """Parse the body ourselves: FastAPI's JSON parsing reads numbers as float and loses cents."""
    body = await _read_body(request, MAX_BODY_BYTES)
    if not body.strip():
        raise InvalidRequestBodyError("request body is empty")
    try:
        data = jsonutil.loads(body)
    except jsonutil.JSONDecodeFailedError:
        raise InvalidRequestBodyError("request body is not valid JSON") from None
    if not isinstance(data, dict):
        raise InvalidRequestBodyError("request body must be a JSON object")
    try:
        return CreateTransactionRequest.model_validate(data)
    except ValidationError as error:
        raise ValidationFailedError([to_violation(detail) for detail in error.errors()]) from None


@router.post(
    "/transactions",
    summary="Execute a transaction",
    description=(
        "Validates the business rules, executes against the provider and stores the outcome. "
        "The stored transaction is returned in every outcome: 201 EXECUTED, 422 REJECTED by the "
        "provider, 502 FAILED because the provider could not confirm it."
    ),
    status_code=201,
    openapi_extra=_CREATE_REQUEST_DOC,
    responses={
        201: {"model": TransactionBody, "description": "Executed"},
        422: {"model": TransactionBody, "description": "Rejected by the provider"},
        502: {"model": TransactionBody, "description": "Provider unavailable; outcome not confirmed"},
        413: {"model": ErrorBody, "description": "Body larger than 64 KiB"},
        **_ERRORS,
    },
    response_class=ExactJSONResponse,
)
async def create_transaction(request: Request, service: Service) -> Response:
    payload = await read_create_request(request)
    transaction = await service.execute(payload.to_command())
    return ExactJSONResponse(
        TransactionBody.from_domain(transaction).to_wire(),
        STATUS_BY_OUTCOME.get(transaction.status, 502),
        headers={"Location": f"/transactions/{transaction.id}"},
    )


@router.get(
    "/transactions",
    summary="List transactions, newest first",
    responses={200: {"model": TransactionPage}, **_ERRORS},
    response_class=ExactJSONResponse,
)
async def list_transactions(service: Service, params: Annotated[ListTransactionsParams, Query()]) -> Response:
    result = await service.search(params.to_query())
    return ExactJSONResponse(TransactionPage.from_domain(result).to_wire())


@router.get(
    "/transactions/{id}",
    summary="Get a transaction",
    responses={
        200: {"model": TransactionBody},
        404: {"model": ErrorBody, "description": "Not found"},
        **_ERRORS,
    },
    response_class=ExactJSONResponse,
)
async def get_transaction(id: UUID, service: Service) -> Response:
    transaction = await service.get(id)
    return ExactJSONResponse(TransactionBody.from_domain(transaction).to_wire())


@router.get("/healthz", summary="Liveness", response_class=ExactJSONResponse)
async def healthz() -> Response:
    return ExactJSONResponse({"status": "ok"})


@router.get(
    "/readyz",
    summary="Readiness: the database is reachable",
    responses={503: {"model": ErrorBody}},
    response_class=ExactJSONResponse,
)
async def readyz(request: Request) -> Response:
    readiness: Readiness | None = request.app.state.readiness
    if readiness is not None:
        try:
            async with asyncio.timeout(READINESS_TIMEOUT):
                await readiness()
        except Exception:
            logger.warning("readiness check failed", exc_info=True)
            return error_response(503, "NOT_READY", "database is unreachable")
    return ExactJSONResponse({"status": "ready"})
