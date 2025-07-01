from datetime import datetime, timedelta, timezone
from typing import List, Optional

from mcp.server.fastmcp import FastMCP

from argo_watcher_mcp.client import Task
from argo_watcher_mcp.dependencies import get_argo_client


APP_TITLE = "ArgoWatcherMCP"
APP_DESCRIPTION = "This server provides tools to query an argo-watcher instance."
APP_VERSION = "0.1.0"

mcp = FastMCP(
    name=APP_TITLE,
    description=APP_DESCRIPTION,
    version=APP_VERSION,
)


@mcp.tool()
def get_deployments(
        app: Optional[str] = None,
        days_history: int = 30,
        from_timestamp: Optional[int] = None,
        to_timestamp: Optional[int] = None,
) -> List[Task]:
    """Retrieves deployment tasks from argo-watcher.

    This tool allows filtering for deployment tasks by application name and
    a specified time range.

    Args:
        app: The name of the application to filter by. (optional)
        days_history: How many days of history to search. Defaults to 30.
            This is ignored if 'from_timestamp' is provided.
        from_timestamp: The start of the time range (Unix timestamp).
            If provided, it overrides 'days_history'.
        to_timestamp: The end of the time range (Unix timestamp).
            Defaults to the current time.

    Returns:
        A list of Task objects representing the deployments.
    """
    argo_client = get_argo_client()

    if to_timestamp is None:
        to_timestamp = int(datetime.now(timezone.utc).timestamp())

    if from_timestamp is None:
        now = datetime.fromtimestamp(to_timestamp, tz=timezone.utc)
        from_timestamp = int((now - timedelta(days=days_history)).timestamp())

    return argo_client.get_tasks(
        from_timestamp=from_timestamp,
        to_timestamp=to_timestamp,
        app=app,
    )
