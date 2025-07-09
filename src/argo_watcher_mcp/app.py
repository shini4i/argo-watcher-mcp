import logging
import os
from contextlib import asynccontextmanager

import httpx
from fastapi import FastAPI

from argo_watcher_mcp.dependencies import app_state
from argo_watcher_mcp.health import router as health_router
from argo_watcher_mcp.tools import mcp

EXCLUDED_LOG_PATHS = ["/healthz", "/readyz"]


class AccessLogFilter(logging.Filter):
    """
    Filters out access logs for specified health check endpoints.
    """
    def filter(self, record: logging.LogRecord) -> bool:
        # The request path is the 3rd element in the args tuple.
        # e.g., ('127.0.0.1:8000', 'GET', '/healthz', '1.1', 200)
        request_path = record.args[2]
        return request_path not in EXCLUDED_LOG_PATHS


class HttpxLogFilter(logging.Filter):
    """
    Filters out httpx logs originating from health checks.
    """
    def filter(self, record: logging.LogRecord) -> bool:
        # For httpx, inspecting the message is more practical,
        # but we do it safely without swallowing all exceptions.
        message = record.getMessage()
        # Return True to keep the log, False to discard it.
        # We discard it if the name is 'httpx' and the message contains a health check path.
        if record.name.startswith("httpx"):
            return not any(path in message for path in EXCLUDED_LOG_PATHS)
        return True


@asynccontextmanager
async def lifespan(app: FastAPI):
    """
    Manages the application's startup and shutdown events.
    """
    argo_watcher_url = os.environ.get("ARGO_WATCHER_URL")
    if not argo_watcher_url:
        raise RuntimeError("ARGO_WATCHER_URL environment variable is not set.")

    app_state.http_client = httpx.AsyncClient()
    app_state.argo_watcher_url = argo_watcher_url

    print("Application startup complete. HTTPX client created.")
    yield

    if isinstance(app_state.http_client, httpx.AsyncClient):
        await app_state.http_client.aclose()
    print("Application shutdown complete. HTTPX client closed.")


def create_app() -> FastAPI:
    """
    Application factory.
    """
    app = FastAPI(
        title="Argo Watcher MCP",
        version="0.2.0",
        lifespan=lifespan,
    )

    # Configure logging to skip health check endpoints
    access_logger = logging.getLogger("uvicorn.access")
    for handler in access_logger.handlers:
        handler.addFilter(AccessLogFilter())

    # Suppress httpx logs from health checks
    httpx_logger = logging.getLogger("httpx")
    for handler in httpx_logger.handlers:
        handler.addFilter(HttpxLogFilter())


    app.include_router(health_router, tags=["Health"])
    app.mount("/", mcp.sse_app())

    return app
