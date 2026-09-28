"""End-to-end smoke test against a running stack (``docker compose up``).

Exercises every provider scenario, listing and the circuit breaker cycle.
Takes about 40 seconds because it waits for the breaker's open timeout.

    uv run python scripts/smoke.py [base_url]
"""

import sys
import time

import httpx

BREAKER_OPEN_TIMEOUT = 30

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


def main() -> None:
    executed = post("acc-smoke")
    assert executed.status_code == 201, executed.text
    assert b'"balanceAfter":8500.00' in executed.content, executed.text
    assert post("acc-smoke", "CREDIT", "123456789.01").status_code == 201
    assert post("acc-smoke", amount="1.00").status_code == 400
    assert post("acc-smoke", amount="10000.01").status_code == 400
    assert post("acc-smoke", currency="USD").status_code == 400
    assert post("acc-insufficient").status_code == 422
    assert post("acc-blocked").status_code == 422
    assert post("acc-flaky").status_code == 201
    assert post("acc-slow").status_code == 502

    page = api.get("/transactions", params={"accountId": "acc-smoke", "limit": 1}).json()
    assert page["pagination"]["hasMore"] is True
    rejected = api.get("/transactions", params={"status": "REJECTED"}).json()["data"]
    assert {tx["failureCode"] for tx in rejected} >= {"INSUFFICIENT_FUNDS", "ACCOUNT_BLOCKED"}
    assert api.get(executed.headers["Location"]).json()["id"] == executed.json()["id"]

    print("-- opening the circuit breaker")
    for _ in range(5):
        post("acc-error")
    assert post("acc-smoke").status_code == 502, "circuit should be open"
    print(f"-- waiting {BREAKER_OPEN_TIMEOUT + 1}s for the half-open probe")
    time.sleep(BREAKER_OPEN_TIMEOUT + 1)
    assert post("acc-smoke").status_code == 201, "circuit should have closed"
    print("smoke test passed")


if __name__ == "__main__":
    main()
