# argo_watcher_mcp/app.py

import os
from contextlib import asynccontextmanager

import httpx
from fastapi import FastAPI

# Import the shared state and routers
from argo_watcher_mcp.dependencies import app_state
from argo_watcher_mcp.health import router as health_router
from argo_watcher_mcp.tools import mcp


@asynccontextmanager
async def lifespan(app: FastAPI):
    """
    Manages the application's startup and shutdown events.
    """
    # On startup, create the httpx client and configure the shared state.
    argo_watcher_url = os.environ.get("ARGO_WATCHER_URL")
    if not argo_watcher_url:
        raise RuntimeError("ARGO_WATCHER_URL environment variable is not set.")

    app_state.http_client = httpx.Client()
    app_state.argo_watcher_url = argo_watcher_url

    print("Application startup complete. HTTPX client created.")
    yield

    # On shutdown, close the client.
    if isinstance(app_state.http_client, httpx.Client):
        app_state.http_client.close()
    print("Application shutdown complete. HTTPX client closed.")


def create_app() -> FastAPI:
    """
    Application factory.
    """
    app = FastAPI(
        title="Argo Watcher MCP",
        version="0.1.0",
        lifespan=lifespan,
    )

    # Include the healthcheck routes
    app.include_router(health_router, prefix="/health", tags=["Health"])

    # Mount the correct, complete MCP application
    app.mount("/", mcp.sse_app())

    return app

