import io
import json
import logging

import httpx
import pytest
from pydantic import ValidationError

from spin_transactions.config import Settings
from spin_transactions.logs import JSONFormatter, request_id_var
from spin_transactions.main import build_app

REQUIRED = {
    "DATABASE_URL": "postgresql://spin:spin@localhost:5432/spin",
    "PROVIDER_BASE_URL": "http://provider:8081",
}


def test_settings_defaults(monkeypatch: pytest.MonkeyPatch) -> None:
    for name, value in REQUIRED.items():
        monkeypatch.setenv(name, value)
    monkeypatch.setenv("LOG_LEVEL", "debug")

    settings = Settings()

    assert settings.log_level == "DEBUG"
    assert settings.provider().attempt_timeout == 2.0
    assert settings.provider().max_attempts == 3


def test_settings_report_every_problem(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("DATABASE_URL", raising=False)
    monkeypatch.delenv("PROVIDER_BASE_URL", raising=False)
    monkeypatch.setenv("PROVIDER_MAX_ATTEMPTS", "0")

    with pytest.raises(ValidationError) as raised:
        Settings()

    assert {error["loc"][0] for error in raised.value.errors()} == {
        "database_url",
        "provider_base_url",
        "provider_max_attempts",
    }


def test_json_logs_carry_the_request_id_and_extras() -> None:
    stream = io.StringIO()
    handler = logging.StreamHandler(stream)
    handler.setFormatter(JSONFormatter())
    logger = logging.getLogger("test.json")
    logger.addHandler(handler)
    logger.propagate = False

    token = request_id_var.set("req-42")
    try:
        logger.warning("provider attempt failed", extra={"attempt": 2})
    finally:
        request_id_var.reset(token)

    entry = json.loads(stream.getvalue())
    assert entry["message"] == "provider attempt failed"
    assert entry["level"] == "WARNING"
    assert entry["request_id"] == "req-42"
    assert entry["attempt"] == 2
    assert entry["time"].endswith("Z")


async def test_app_wiring_starts_and_stops_without_a_database(monkeypatch: pytest.MonkeyPatch) -> None:
    for name, value in REQUIRED.items():
        monkeypatch.setenv(name, value)
    app = build_app(Settings())

    async with app.router.lifespan_context(app):
        transport = httpx.ASGITransport(app=app)
        async with httpx.AsyncClient(transport=transport, base_url="http://api.test") as client:
            assert (await client.get("/healthz")).status_code == 200
