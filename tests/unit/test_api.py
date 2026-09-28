import logging
import uuid
from collections.abc import AsyncIterator
from dataclasses import dataclass
from decimal import Decimal
from typing import Any

import httpx
import pytest

from spin_transactions import jsonutil
from spin_transactions.adapters.memory import InMemoryTransactionRepository
from spin_transactions.api.app import create_app
from spin_transactions.api.routes import MAX_BODY_BYTES, Readiness
from spin_transactions.domain.errors import ProviderRejectedError
from spin_transactions.domain.models import ExecutionResult, Transaction
from spin_transactions.domain.ports import ExecutionRequest
from spin_transactions.domain.service import TransactionService
from tests.fakes import FakeProvider

VALID = {"accountId": "acc-123", "type": "DEBIT", "amount": 1500.00, "currency": "MXN", "description": "rent"}


async def by_account(request: ExecutionRequest) -> ExecutionResult:
    match request.account_id:
        case "acc-rejected":
            raise ProviderRejectedError("INSUFFICIENT_FUNDS", "insufficient funds")
        case "acc-down":
            raise ConnectionError("provider unreachable")
        case _:
            return ExecutionResult("prv-1", Decimal("8500.00"), None)


@dataclass
class Api:
    client: httpx.AsyncClient
    provider: FakeProvider
    repository: InMemoryTransactionRepository

    async def create(self, **overrides: Any) -> httpx.Response:
        return await self.post({**VALID, **overrides})

    async def post(self, body: object) -> httpx.Response:
        return await self.client.post(
            "/transactions", content=jsonutil.dumps(body), headers={"Content-Type": "application/json"}
        )


def build_client(
    repository: InMemoryTransactionRepository, provider: FakeProvider, readiness: Readiness | None = None
) -> httpx.AsyncClient:
    app = create_app(TransactionService(repository, provider), readiness=readiness)
    return httpx.AsyncClient(transport=httpx.ASGITransport(app=app), base_url="http://api.test")


@pytest.fixture
async def api() -> AsyncIterator[Api]:
    repository = InMemoryTransactionRepository()
    provider = FakeProvider(by_account)
    async with build_client(repository, provider) as client:
        yield Api(client, provider, repository)


def detail_codes(response: httpx.Response) -> list[tuple[str, str]]:
    body = response.json()
    assert body["code"] == "VALIDATION_FAILED"
    return [(d["field"], d["code"]) for d in body["details"]]


async def test_executed_transaction_is_created(api: Api) -> None:
    response = await api.create()

    assert response.status_code == 201
    body = response.json()
    assert response.headers["Location"] == f"/transactions/{body['id']}"
    assert uuid.UUID(body["id"]).version == 7
    assert body["status"] == "EXECUTED"
    assert body["providerTransactionId"] == "prv-1"
    assert body["failureCode"] is None
    assert body["createdAt"].endswith("Z")
    assert b'"amount":1500.00' in response.content
    assert b'"balanceAfter":8500.00' in response.content


async def test_amounts_are_exact_end_to_end(api: Api) -> None:
    response = await api.post(
        {**VALID, "type": "CREDIT", "amount": Decimal("999999999999999.99")},
    )

    assert response.status_code == 201
    assert b'"amount":999999999999999.99' in response.content
    assert api.provider.calls[0].amount == Decimal("999999999999999.99")


async def test_integer_amounts_are_normalized_to_cents(api: Api) -> None:
    response = await api.create(amount=1500)

    assert b'"amount":1500.00' in response.content


async def test_provider_rejection_is_stored_and_returned_as_422(api: Api) -> None:
    response = await api.create(accountId="acc-rejected")

    assert response.status_code == 422
    body = response.json()
    assert (body["status"], body["failureCode"]) == ("REJECTED", "INSUFFICIENT_FUNDS")
    assert body["balanceAfter"] is None
    assert (await api.client.get(response.headers["Location"])).json()["status"] == "REJECTED"


async def test_unreachable_provider_is_stored_and_returned_as_502(api: Api) -> None:
    response = await api.create(accountId="acc-down")

    assert response.status_code == 502
    body = response.json()
    assert (body["status"], body["failureCode"]) == ("FAILED", "PROVIDER_UNAVAILABLE")


@pytest.mark.parametrize(
    ("overrides", "expected"),
    [
        pytest.param({"amount": 1.00}, [("amount", "AMOUNT_TOO_LOW")], id="minimum is exclusive"),
        pytest.param({"amount": 10000.01}, [("amount", "DEBIT_LIMIT_EXCEEDED")], id="debit limit"),
        pytest.param({"amount": 10.001}, [("amount", "INVALID_PRECISION")], id="three decimals"),
        pytest.param({"currency": "USD"}, [("currency", "UNSUPPORTED_CURRENCY")], id="currency"),
        pytest.param({"type": "REFUND"}, [("type", "UNSUPPORTED_TYPE")], id="type"),
        pytest.param(
            {"accountId": "", "amount": 0.5, "currency": "usd"},
            [("accountId", "REQUIRED"), ("amount", "AMOUNT_TOO_LOW"), ("currency", "UNSUPPORTED_CURRENCY")],
            id="all violations reported together",
        ),
    ],
)
async def test_business_rules_are_checked_before_the_provider(
    api: Api, overrides: dict[str, Any], expected: list[tuple[str, str]]
) -> None:
    response = await api.create(**overrides)

    assert response.status_code == 400
    assert detail_codes(response) == expected
    assert api.provider.calls == []
    assert (await api.client.get("/transactions")).json()["data"] == []


@pytest.mark.parametrize(
    ("body", "expected"),
    [
        pytest.param(
            {},
            [
                ("accountId", "REQUIRED"),
                ("type", "REQUIRED"),
                ("amount", "REQUIRED"),
                ("currency", "REQUIRED"),
            ],
            id="missing fields",
        ),
        pytest.param({**VALID, "fee": 1}, [("fee", "UNKNOWN_FIELD")], id="unknown field"),
        pytest.param(
            {**VALID, "account_id": "x"}, [("account_id", "UNKNOWN_FIELD")], id="snake case is not accepted"
        ),
        pytest.param({**VALID, "amount": "1500.00"}, [("amount", "INVALID_AMOUNT")], id="amount as string"),
        pytest.param({**VALID, "amount": True}, [("amount", "INVALID_AMOUNT")], id="amount as boolean"),
        pytest.param({**VALID, "accountId": 123}, [("accountId", "INVALID_TYPE")], id="number as string"),
    ],
)
async def test_malformed_fields_are_described(
    api: Api, body: dict[str, Any], expected: list[tuple[str, str]]
) -> None:
    response = await api.post(body)

    assert response.status_code == 400
    assert detail_codes(response) == expected


@pytest.mark.parametrize(
    ("content", "message"),
    [
        (b"", "request body is empty"),
        (b'{"accountId": ', "request body is not valid JSON"),
        (b'{"amount": NaN}', "request body is not valid JSON"),
        (b"[1, 2]", "request body must be a JSON object"),
    ],
)
async def test_unreadable_bodies_are_rejected(api: Api, content: bytes, message: str) -> None:
    response = await api.client.post("/transactions", content=content)

    assert response.status_code == 400
    assert response.json() == {"code": "INVALID_REQUEST_BODY", "message": message}


async def test_oversized_body_is_rejected(api: Api) -> None:
    response = await api.create(description="x" * MAX_BODY_BYTES)

    assert response.status_code == 413
    assert response.json()["code"] == "PAYLOAD_TOO_LARGE"


async def test_list_filters_and_paginates(api: Api) -> None:
    for _ in range(3):
        await api.create(accountId="acc-a")
    await api.create(accountId="acc-b")
    await api.create(accountId="acc-rejected")

    first = (await api.client.get("/transactions", params={"accountId": "acc-a", "limit": 2})).json()
    second = (
        await api.client.get("/transactions", params={"accountId": "acc-a", "limit": 2, "page": 2})
    ).json()
    rejected = (await api.client.get("/transactions", params={"status": "REJECTED", "type": "DEBIT"})).json()

    assert first["pagination"] == {"page": 1, "limit": 2, "hasMore": True}
    assert second["pagination"] == {"page": 2, "limit": 2, "hasMore": False}
    assert len(first["data"]) + len(second["data"]) == 3
    assert {tx["accountId"] for tx in first["data"] + second["data"]} == {"acc-a"}
    assert [tx["accountId"] for tx in rejected["data"]] == ["acc-rejected"]


async def test_list_defaults(api: Api) -> None:
    body = (await api.client.get("/transactions")).json()

    assert body == {"data": [], "pagination": {"page": 1, "limit": 20, "hasMore": False}}


@pytest.mark.parametrize(
    ("params", "expected"),
    [
        ({"page": "abc"}, [("page", "INVALID_INTEGER")]),
        ({"page": "0"}, [("page", "OUT_OF_RANGE")]),
        ({"limit": "101"}, [("limit", "OUT_OF_RANGE")]),
        ({"status": "DONE"}, [("status", "UNSUPPORTED_STATUS")]),
        ({"type": "debit"}, [("type", "UNSUPPORTED_TYPE")]),
    ],
)
async def test_list_rejects_invalid_parameters(
    api: Api, params: dict[str, str], expected: list[tuple[str, str]]
) -> None:
    response = await api.client.get("/transactions", params=params)

    assert response.status_code == 400
    assert detail_codes(response) == expected


async def test_get_by_id(api: Api) -> None:
    created = (await api.create()).json()

    response = await api.client.get(f"/transactions/{created['id']}")

    assert response.status_code == 200
    assert response.json() == created


async def test_get_unknown_id_is_404(api: Api) -> None:
    response = await api.client.get(f"/transactions/{uuid.uuid7()}")

    assert response.status_code == 404
    assert response.json() == {"code": "NOT_FOUND", "message": "transaction not found"}


async def test_get_malformed_id_is_400(api: Api) -> None:
    response = await api.client.get("/transactions/not-a-uuid")

    assert detail_codes(response) == [("id", "INVALID_ID")]


async def test_routing_errors_use_the_error_format(api: Api) -> None:
    missing = await api.client.get("/nope")
    wrong_method = await api.client.delete("/transactions")

    assert missing.json() == {"code": "NOT_FOUND", "message": "not found"}
    assert wrong_method.status_code == 405
    assert wrong_method.json()["code"] == "METHOD_NOT_ALLOWED"


async def test_request_id_is_echoed_or_generated(api: Api) -> None:
    echoed = await api.client.get("/healthz", headers={"X-Request-ID": "abc-123"})
    replaced = await api.client.get("/healthz", headers={"X-Request-ID": "bad id\twith spaces"})

    assert echoed.headers["X-Request-ID"] == "abc-123"
    assert len(replaced.headers["X-Request-ID"]) == 32


class BrokenRepository(InMemoryTransactionRepository):
    async def create(self, transaction: Transaction) -> None:
        raise RuntimeError("database is on fire")


async def test_unexpected_errors_return_a_generic_500(caplog: pytest.LogCaptureFixture) -> None:
    caplog.set_level(logging.INFO, logger="spin_transactions.access")
    async with build_client(BrokenRepository(), FakeProvider(by_account)) as client:
        response = await client.post("/transactions", content=jsonutil.dumps(VALID))

    assert response.status_code == 500
    assert response.json() == {"code": "INTERNAL_ERROR", "message": "an unexpected error occurred"}
    assert "X-Request-ID" in response.headers
    assert "database is on fire" not in response.text
    assert any(r.message == "request served" and r.__dict__["status"] == 500 for r in caplog.records)


async def test_health_and_readiness() -> None:
    healthy_calls = 0

    async def healthy() -> None:
        nonlocal healthy_calls
        healthy_calls += 1

    async def unreachable() -> None:
        raise ConnectionError("no database")

    provider = FakeProvider(by_account)
    async with build_client(InMemoryTransactionRepository(), provider, healthy) as client:
        assert (await client.get("/healthz")).json() == {"status": "ok"}
        assert (await client.get("/readyz")).json() == {"status": "ready"}
    async with build_client(InMemoryTransactionRepository(), provider, unreachable) as client:
        not_ready = await client.get("/readyz")

    assert healthy_calls == 1
    assert not_ready.status_code == 503
    assert not_ready.json()["code"] == "NOT_READY"


async def test_openapi_documents_amounts_as_numbers(api: Api) -> None:
    schema = (await api.client.get("/openapi.json")).json()

    body_schema = schema["paths"]["/transactions"]["post"]["requestBody"]["content"]["application/json"]
    assert body_schema["schema"]["properties"]["amount"]["type"] == "number"
    assert schema["components"]["schemas"]["TransactionBody"]["properties"]["balanceAfter"]


async def test_access_log_records_each_request(api: Api, caplog: pytest.LogCaptureFixture) -> None:
    caplog.set_level(logging.INFO, logger="spin_transactions.access")

    await api.client.get("/transactions", headers={"X-Request-ID": "req-1"})

    [record] = [r for r in caplog.records if r.name == "spin_transactions.access"]
    assert (record.__dict__["method"], record.__dict__["path"], record.__dict__["status"]) == (
        "GET",
        "/transactions",
        200,
    )
