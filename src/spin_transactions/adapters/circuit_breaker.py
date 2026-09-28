"""A minimal asyncio circuit breaker.

After ``failure_threshold`` consecutive failures the circuit opens and calls
fail immediately with CircuitOpenError. Once ``open_timeout`` seconds pass, a
single probe call is let through: success closes the circuit, failure opens it
again. State changes happen between awaits on one event loop, so no lock is needed.
"""

import logging
import time
from collections.abc import Awaitable, Callable
from enum import StrEnum

logger = logging.getLogger(__name__)


class CircuitState(StrEnum):
    CLOSED = "closed"
    OPEN = "open"
    HALF_OPEN = "half-open"


class CircuitOpenError(Exception):
    def __init__(self, name: str) -> None:
        super().__init__(f"circuit breaker {name!r} is open")


class CircuitBreaker:
    def __init__(
        self,
        name: str,
        *,
        failure_threshold: int,
        open_timeout: float,
        is_failure: Callable[[Exception], bool] = lambda _: True,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self._name = name
        self._failure_threshold = failure_threshold
        self._open_timeout = open_timeout
        self._is_failure = is_failure
        self._clock = clock
        self._state = CircuitState.CLOSED
        self._consecutive_failures = 0
        self._opened_at = 0.0
        self._probe_in_flight = False

    @property
    def state(self) -> CircuitState:
        if self._state is CircuitState.OPEN and self._clock() - self._opened_at >= self._open_timeout:
            return CircuitState.HALF_OPEN
        return self._state

    async def call[T](self, operation: Callable[[], Awaitable[T]]) -> T:
        self._before_call()
        try:
            result = await operation()
        except Exception as error:
            if self._is_failure(error):
                self._on_failure()
            else:
                self._on_success()
            raise
        except BaseException:
            # Cancelled: no verdict about the dependency's health.
            self._probe_in_flight = False
            raise
        self._on_success()
        return result

    def _before_call(self) -> None:
        state = self.state
        if state is CircuitState.OPEN or (state is CircuitState.HALF_OPEN and self._probe_in_flight):
            raise CircuitOpenError(self._name)
        if state is CircuitState.HALF_OPEN:
            self._transition(CircuitState.HALF_OPEN)
            self._probe_in_flight = True

    def _on_success(self) -> None:
        self._consecutive_failures = 0
        self._probe_in_flight = False
        if self._state is not CircuitState.CLOSED:
            self._transition(CircuitState.CLOSED)

    def _on_failure(self) -> None:
        self._probe_in_flight = False
        self._consecutive_failures += 1
        if self._state is CircuitState.HALF_OPEN or self._consecutive_failures >= self._failure_threshold:
            self._opened_at = self._clock()
            self._transition(CircuitState.OPEN)

    def _transition(self, to: CircuitState) -> None:
        if to is not self._state:
            logger.warning(
                "circuit breaker state changed",
                extra={"breaker": self._name, "from_state": str(self._state), "to_state": str(to)},
            )
            self._state = to
