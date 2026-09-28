"""Pure ASGI middleware.

Written against the raw ASGI interface rather than BaseHTTPMiddleware, which
runs the endpoint in a separate task and buffers streaming responses.
"""

import logging
import re
import time
import uuid

from starlette.datastructures import Headers, MutableHeaders
from starlette.types import ASGIApp, Message, Receive, Scope, Send

from spin_transactions import jsonutil
from spin_transactions.logs import request_id_var

REQUEST_ID_HEADER = "X-Request-ID"
# A caller-supplied id is reused only if it is short and safe to log.
_VALID_REQUEST_ID = re.compile(r"[A-Za-z0-9._:\-]{1,128}")

logger = logging.getLogger(__name__)
access_logger = logging.getLogger("spin_transactions.access")


class RequestIdMiddleware:
    """Assigns every request an id, exposes it to logs and echoes it in the response."""

    def __init__(self, app: ASGIApp) -> None:
        self._app = app

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        if scope["type"] != "http":
            await self._app(scope, receive, send)
            return

        request_id = Headers(scope=scope).get(REQUEST_ID_HEADER)
        if request_id is None or not _VALID_REQUEST_ID.fullmatch(request_id):
            request_id = uuid.uuid4().hex

        async def send_with_request_id(message: Message) -> None:
            if message["type"] == "http.response.start":
                MutableHeaders(scope=message).append(REQUEST_ID_HEADER, request_id)
            await send(message)

        token = request_id_var.set(request_id)
        try:
            await self._app(scope, receive, send_with_request_id)
        finally:
            request_id_var.reset(token)


class AccessLogMiddleware:
    """Logs one line per request and turns unhandled exceptions into a JSON 500."""

    def __init__(self, app: ASGIApp) -> None:
        self._app = app

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        if scope["type"] != "http":
            await self._app(scope, receive, send)
            return

        started = time.perf_counter()
        status = 500
        response_started = False

        async def send_tracking_status(message: Message) -> None:
            nonlocal status, response_started
            if message["type"] == "http.response.start":
                status = message["status"]
                response_started = True
            await send(message)

        try:
            await self._app(scope, receive, send_tracking_status)
        except Exception:
            logger.exception("unhandled error while serving request")
            if response_started:
                raise
            await _send_internal_error(send)
        finally:
            access_logger.info(
                "request served",
                extra={
                    "method": scope["method"],
                    "path": scope["path"],
                    "status": status,
                    "duration_ms": round((time.perf_counter() - started) * 1000, 2),
                },
            )


async def _send_internal_error(send: Send) -> None:
    body = jsonutil.dumps({"code": "INTERNAL_ERROR", "message": "an unexpected error occurred"}).encode()
    await send(
        {
            "type": "http.response.start",
            "status": 500,
            "headers": [(b"content-type", b"application/json"), (b"content-length", str(len(body)).encode())],
        }
    )
    await send({"type": "http.response.body", "body": body})
