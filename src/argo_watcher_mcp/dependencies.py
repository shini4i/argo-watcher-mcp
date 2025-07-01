# argo_watcher_mcp/dependencies.py

import httpx
from argo_watcher_mcp.client import ArgoWatcherClient, Task

# A simple class to hold shared state. This avoids using a global dict.
class AppState:
    http_client: httpx.Client | None = None
    argo_watcher_url: str | None = None

# A single instance of the state that will be populated on app startup.
app_state = AppState()

def get_argo_client() -> ArgoWatcherClient:
    """
    Dependency to get an ArgoWatcherClient.

    This function relies on the `app_state` object being populated
    by the application's lifespan manager.
    """
    if not isinstance(app_state.http_client, httpx.Client) or not app_state.argo_watcher_url:
        # This error should never happen in a running app, but it's good practice
        # to ensure the state was initialized correctly.
        raise RuntimeError("Application state not initialized. Lifespan manager did not run.")

    return ArgoWatcherClient(
        base_url=app_state.argo_watcher_url,
        client=app_state.http_client,
    )
