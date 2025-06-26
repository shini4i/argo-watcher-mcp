import os
from datetime import datetime
from datetime import timedelta
from datetime import timezone
from typing import List
from typing import Optional

import httpx
from mcp.server.fastmcp import FastMCP

from argo_watcher_mcp.client import ArgoWatcherClient
from argo_watcher_mcp.client import Task

APP_TITLE = "ArgoWatcherMCP"
APP_DESCRIPTION = "This server provides tools to query an argo-watcher instance."
APP_VERSION = "0.1.0"

mcp = FastMCP(
    name=APP_TITLE,
    description=APP_DESCRIPTION,
    version=APP_VERSION,
    host="0.0.0.0",  # nosec B104
    port=8000,
)


def get_argo_client() -> ArgoWatcherClient:
    """
    Creates and returns an ArgoWatcherClient instance.
    """
    argo_watcher_url = os.environ.get("ARGO_WATCHER_URL")
    if not argo_watcher_url:
        raise RuntimeError("ARGO_WATCHER_URL environment variable is not set.")

    http_client = httpx.Client()
    return ArgoWatcherClient(base_url=argo_watcher_url, client=http_client)


@mcp.tool()
def get_deployments(
    app: Optional[str] = None,
    days_history: int = 30,
    from_timestamp: Optional[int] = None,
    to_timestamp: Optional[int] = None,
) -> List[Task]:
    """
    Retrieves deployment tasks from argo-watcher.

    Allows filtering by application name and project.

    Args:
        app: The name of the application to filter by.
        days_history: How many days back to search for deployments. Defaults to 30.
        from_timestamp: The start of the time range (Unix timestamp). Overrides days_history.
        to_timestamp: The end of the time range (Unix timestamp). Defaults to now.
    """
    argo_client = get_argo_client()

    if to_timestamp is None:
        to_timestamp = int(datetime.now(timezone.utc).timestamp())

    if from_timestamp is None:
        now = datetime.fromtimestamp(to_timestamp, tz=timezone.utc)
        from_timestamp = int((now - timedelta(days=days_history)).timestamp())

    tasks = argo_client.get_tasks(
        from_timestamp=from_timestamp,
        to_timestamp=to_timestamp,
        app=app,
    )

    return tasks
