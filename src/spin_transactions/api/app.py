from collections.abc import Callable
from contextlib import AbstractAsyncContextManager

from fastapi import FastAPI

from spin_transactions.domain.service import TransactionService

from .errors import register_error_handlers
from .middleware import AccessLogMiddleware, RequestIdMiddleware
from .responses import ExactJSONResponse
from .routes import Readiness, router

type Lifespan = Callable[[FastAPI], AbstractAsyncContextManager[None]]


def create_app(
    service: TransactionService,
    *,
    readiness: Readiness | None = None,
    lifespan: Lifespan | None = None,
) -> FastAPI:
    """Build the API around an already wired service.

    ``readiness`` backs /readyz (e.g. a database ping); ``lifespan`` releases
    resources on shutdown.
    """
    app = FastAPI(
        title="Spin Transactions API",
        version="1.0.0",
        description="Executes transactions against an external provider and records their outcome.",
        default_response_class=ExactJSONResponse,
        lifespan=lifespan,
    )
    app.state.service = service
    app.state.readiness = readiness
    app.include_router(router)
    register_error_handlers(app)
    # Added last, so it runs first: every log line of a request carries its id.
    app.add_middleware(AccessLogMiddleware)
    app.add_middleware(RequestIdMiddleware)
    return app
