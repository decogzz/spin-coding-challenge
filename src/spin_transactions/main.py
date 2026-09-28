"""Composition root: the only module that knows every concrete adapter."""

import asyncio
import logging
from collections.abc import AsyncIterator
from contextlib import asynccontextmanager

import uvicorn
from fastapi import FastAPI

from spin_transactions.adapters.postgres import PostgresTransactionRepository, create_engine
from spin_transactions.adapters.provider_client import HttpTransactionProvider, create_http_client
from spin_transactions.api.app import create_app
from spin_transactions.config import Settings
from spin_transactions.domain.service import TransactionService
from spin_transactions.logs import configure_logging

logger = logging.getLogger(__name__)


def build_app(settings: Settings) -> FastAPI:
    engine = create_engine(
        settings.database_url,
        pool_size=settings.db_pool_size,
        max_overflow=settings.db_max_overflow,
        pool_timeout=settings.db_pool_timeout,
    )
    repository = PostgresTransactionRepository(engine)
    provider_config = settings.provider()
    http_client = create_http_client(provider_config)
    service = TransactionService(repository, HttpTransactionProvider(provider_config, http_client))

    @asynccontextmanager
    async def lifespan(_: FastAPI) -> AsyncIterator[None]:
        logger.info("api started", extra={"port": settings.port})
        yield
        try:
            async with asyncio.timeout(settings.shutdown_timeout):
                await service.drain()
        except TimeoutError:
            logger.warning("shutdown timed out with executions still in flight; they remain PENDING")
        await http_client.aclose()
        await engine.dispose()
        logger.info("api stopped")

    return create_app(service, readiness=repository.ping, lifespan=lifespan)


def run() -> None:
    settings = Settings()
    configure_logging(settings.log_level)
    uvicorn.run(
        build_app(settings),
        host=settings.host,
        port=settings.port,
        log_config=None,
        access_log=False,
        timeout_graceful_shutdown=int(settings.shutdown_timeout) + 5,
    )


if __name__ == "__main__":
    run()
