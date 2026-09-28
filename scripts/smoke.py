"""End-to-end smoke test against a running stack (``docker compose up``).

Exercises every provider scenario, listing and the circuit breaker cycle.
Takes about 40 seconds because it waits for the breaker's open timeout.

    uv run python scripts/smoke.py [base_url]
"""

import sys
import time
import uuid

import httpx

BREAKER_OPEN_TIMEOUT = 30
# Fresh per run, so the expected balances hold against a mock that is already running.
ACCOUNT = f"acc-smoke-{uuid.uuid4().hex[:8]}"

api = httpx.Client(base_url=sys.argv[1] if len(sys.argv) > 1 else "http://localhost:8080", timeout=30)


def post(account: str, type: str = "DEBIT", amount: str = "1500.00", currency: str = "MXN") -> httpx.Response:
    body = f'{{"accountId":"{account}","type":"{type}","amount":{amount},"currency":"{currency}"}}'
    started = time.perf_counter()
    response = api.post("/transactions", content=body, headers={"Content-Type": "application/json"})
    data = response.json()
    outcome = data.get("status") or data.get("code")
    reason = data.get("failureCode") or ",".join(d["code"] for d in data.get("details", []))
    print(
        f"{account:17} {type:6} {amount:>13} {currency} -> {response.status_code} {outcome} {reason}"
        f" ({time.perf_counter() - started:.2f}s)"
    )
    return response


def check(condition: bool, message: str) -> None:
    # Not ``assert``: it must still fail under ``python -O``.
    if not condition:
        sys.exit(f"smoke test failed: {message}")


def expect_status(response: httpx.Response, status: int) -> None:
    check(response.status_code == status, f"expected {status}, got {response.status_code}: {response.text}")


def main() -> None:
    executed = post(ACCOUNT)
    expect_status(executed, 201)
    check(b'"balanceAfter":8500.00' in executed.content, f"balance not exact: {executed.text}")
    expect_status(post(ACCOUNT, "CREDIT", "123456789.01"), 201)
    expect_status(post(ACCOUNT, amount="1.00"), 400)
    expect_status(post(ACCOUNT, amount="10000.01"), 400)
    expect_status(post(ACCOUNT, currency="USD"), 400)
    expect_status(post("acc-insufficient"), 422)
    expect_status(post("acc-blocked"), 422)
    expect_status(post("acc-flaky"), 201)
    expect_status(post("acc-slow"), 502)

    page = api.get("/transactions", params={"accountId": ACCOUNT, "limit": 1}).json()
    check(page["pagination"]["hasMore"] is True, "listing should report another page")
    rejected = api.get("/transactions", params={"status": "REJECTED"}).json()["data"]
    codes = {tx["failureCode"] for tx in rejected}
    check(codes >= {"INSUFFICIENT_FUNDS", "ACCOUNT_BLOCKED"}, f"unexpected rejections: {codes}")
    fetched = api.get(executed.headers["Location"]).json()
    check(fetched["id"] == executed.json()["id"], "Location does not point at the transaction")

    print("-- opening the circuit breaker")
    for _ in range(5):
        post("acc-error")
    expect_status(post(ACCOUNT), 502)
    print(f"-- waiting {BREAKER_OPEN_TIMEOUT + 1}s for the half-open probe")
    time.sleep(BREAKER_OPEN_TIMEOUT + 1)
    expect_status(post(ACCOUNT), 201)
    print("smoke test passed")


if __name__ == "__main__":
    main()
