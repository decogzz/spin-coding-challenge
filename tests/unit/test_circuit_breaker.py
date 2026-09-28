import asyncio

import pytest

from spin_transactions.adapters.circuit_breaker import CircuitBreaker, CircuitOpenError, CircuitState


class Clock:
    def __init__(self) -> None:
        self.now = 0.0

    def __call__(self) -> float:
        return self.now


class BoomError(Exception):
    pass


class HarmlessError(Exception):
    pass


def breaker(clock: Clock, threshold: int = 2) -> CircuitBreaker:
    return CircuitBreaker(
        "test",
        failure_threshold=threshold,
        open_timeout=10,
        is_failure=lambda error: not isinstance(error, HarmlessError),
        clock=clock,
    )


async def ok() -> str:
    return "ok"


async def fail() -> str:
    raise BoomError


async def fail_harmlessly() -> str:
    raise HarmlessError


async def trip(cb: CircuitBreaker, times: int) -> None:
    for _ in range(times):
        with pytest.raises(BoomError):
            await cb.call(fail)


async def test_opens_after_consecutive_failures_and_fails_fast() -> None:
    cb = breaker(Clock())
    await trip(cb, 2)

    assert cb.state is CircuitState.OPEN
    calls = 0

    async def counted() -> str:
        nonlocal calls
        calls += 1
        return "ok"

    with pytest.raises(CircuitOpenError):
        await cb.call(counted)
    assert calls == 0


async def test_success_resets_the_failure_count() -> None:
    cb = breaker(Clock())
    await trip(cb, 1)
    assert await cb.call(ok) == "ok"
    await trip(cb, 1)

    assert cb.state is CircuitState.CLOSED


async def test_errors_that_are_not_failures_do_not_open_it() -> None:
    cb = breaker(Clock())
    for _ in range(5):
        with pytest.raises(HarmlessError):
            await cb.call(fail_harmlessly)

    assert cb.state is CircuitState.CLOSED


async def test_half_open_probe_success_closes_it() -> None:
    clock = Clock()
    cb = breaker(clock)
    await trip(cb, 2)

    observed = []
    for now in (9.9, 10):
        clock.now = now
        observed.append(cb.state)
    assert observed == [CircuitState.OPEN, CircuitState.HALF_OPEN]

    assert await cb.call(ok) == "ok"
    assert cb.state is CircuitState.CLOSED


async def test_half_open_probe_failure_reopens_it() -> None:
    clock = Clock()
    cb = breaker(clock)
    await trip(cb, 2)

    clock.now = 10
    await trip(cb, 1)
    assert cb.state is CircuitState.OPEN

    clock.now = 15
    with pytest.raises(CircuitOpenError):
        await cb.call(ok)


async def test_half_open_allows_a_single_probe() -> None:
    clock = Clock()
    cb = breaker(clock)
    await trip(cb, 2)
    clock.now = 10

    release = asyncio.Event()

    async def slow() -> str:
        await release.wait()
        return "ok"

    probe = asyncio.create_task(cb.call(slow))
    await asyncio.sleep(0)
    with pytest.raises(CircuitOpenError):
        await cb.call(ok)

    release.set()
    assert await probe == "ok"
    assert cb.state is CircuitState.CLOSED


async def test_cancelled_probe_lets_the_next_call_probe() -> None:
    clock = Clock()
    cb = breaker(clock)
    await trip(cb, 2)
    clock.now = 10

    probe = asyncio.create_task(cb.call(asyncio.Event().wait))
    await asyncio.sleep(0)
    probe.cancel()
    with pytest.raises(asyncio.CancelledError):
        await probe

    assert await cb.call(ok) == "ok"
    assert cb.state is CircuitState.CLOSED
