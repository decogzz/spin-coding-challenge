"""Translate exceptions into the API's ``{code, message, details}`` error body."""

from collections.abc import Mapping
from http import HTTPStatus

from fastapi import FastAPI, Request, Response
from fastapi.exceptions import RequestValidationError
from pydantic_core import ErrorDetails
from starlette.exceptions import HTTPException

from spin_transactions.domain.errors import TransactionNotFoundError, ValidationFailedError, Violation

from .responses import ExactJSONResponse
from .schemas import ErrorBody


class InvalidRequestBodyError(Exception):
    """The body could not be read as a JSON object."""


class PayloadTooLargeError(Exception):
    def __init__(self, limit: int) -> None:
        super().__init__(f"request body must not exceed {limit} bytes")


# pydantic error type -> (API code, message template).
_VIOLATIONS: dict[str, tuple[str, str]] = {
    "missing": ("REQUIRED", "{field} is required"),
    "extra_forbidden": ("UNKNOWN_FIELD", "{field} is not a known field"),
    "string_type": ("INVALID_TYPE", "{field} must be a string"),
    "string_too_long": ("TOO_LONG", "{field} is too long"),
    "int_parsing": ("INVALID_INTEGER", "{field} must be an integer"),
    "int_type": ("INVALID_INTEGER", "{field} must be an integer"),
    "int_from_float": ("INVALID_INTEGER", "{field} must be an integer"),
    "uuid_parsing": ("INVALID_ID", "{field} must be a UUID"),
    "uuid_type": ("INVALID_ID", "{field} must be a UUID"),
    "invalid_amount": ("INVALID_AMOUNT", "{message}"),
}


def to_violation(error: ErrorDetails) -> Violation:
    """Describe a pydantic error in the API's terms, without leaking pydantic's wording."""
    location = [part for part in error["loc"] if part not in {"body", "query", "path"}]
    field = ".".join(str(part) for part in location) or "body"
    code, template = _VIOLATIONS.get(error["type"], ("INVALID_VALUE", "{message}"))
    return Violation(field, code, template.format(field=field, message=error["msg"]))


def error_response(
    status: int, code: str, message: str, headers: Mapping[str, str] | None = None
) -> ExactJSONResponse:
    return ExactJSONResponse(ErrorBody(code=code, message=message).to_wire(), status, headers=headers)


def validation_response(violations: list[Violation]) -> ExactJSONResponse:
    return ExactJSONResponse(ErrorBody.validation_failed(violations).to_wire(), 400)


def register_error_handlers(app: FastAPI) -> None:
    @app.exception_handler(ValidationFailedError)
    async def validation_failed(_: Request, error: ValidationFailedError) -> Response:
        return validation_response(error.violations)

    @app.exception_handler(RequestValidationError)
    async def request_validation(_: Request, error: RequestValidationError) -> Response:
        return validation_response([to_violation(detail) for detail in error.errors()])

    @app.exception_handler(InvalidRequestBodyError)
    async def invalid_body(_: Request, error: InvalidRequestBodyError) -> Response:
        return error_response(400, "INVALID_REQUEST_BODY", str(error))

    @app.exception_handler(PayloadTooLargeError)
    async def payload_too_large(_: Request, error: PayloadTooLargeError) -> Response:
        return error_response(413, "PAYLOAD_TOO_LARGE", str(error))

    @app.exception_handler(TransactionNotFoundError)
    async def not_found(_: Request, __: TransactionNotFoundError) -> Response:
        return error_response(404, "NOT_FOUND", "transaction not found")

    @app.exception_handler(HTTPException)
    async def http_error(_: Request, error: HTTPException) -> Response:
        status = HTTPStatus(error.status_code)
        return error_response(error.status_code, status.name, status.phrase.lower(), headers=error.headers)
