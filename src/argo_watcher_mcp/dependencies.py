import httpx

from argo_watcher_mcp.client import ArgoWatcherClient


class AppState:
    http_client: httpx.Client | None = None
    argo_watcher_url: str | None = None


app_state = AppState()


def get_argo_client() -> ArgoWatcherClient:
    """
    Dependency to get an ArgoWatcherClient.

    This function relies on the `app_state` object being populated
    by the application's lifespan manager.
    """
    if not isinstance(app_state.http_client, httpx.Client) or not app_state.argo_watcher_url:
        raise RuntimeError("Application state not initialized. Lifespan manager did not run.")

    return ArgoWatcherClient(
        base_url=app_state.argo_watcher_url,
        client=app_state.http_client,
    )
