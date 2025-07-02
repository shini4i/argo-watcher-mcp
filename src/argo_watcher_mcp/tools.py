import re
from datetime import datetime
from datetime import timedelta
from datetime import timezone
from typing import List
from typing import Optional

from mcp.server.fastmcp import FastMCP

from argo_watcher_mcp.client import Task
from argo_watcher_mcp.dependencies import get_argo_client

APP_TITLE = "ArgoWatcherMCP"
APP_DESCRIPTION = "This server provides tools to query an argo-watcher instance."
APP_VERSION = "0.2.0"

mcp = FastMCP(
    name=APP_TITLE,
    description=APP_DESCRIPTION,
    version=APP_VERSION,
)


def _parse_time_delta(time_delta_str: str) -> timedelta:
    """Parses a time delta string like '10m', '2h', or '7d'."""
    # FIX: Add anchors (^) and ($) for exact matching.
    match = re.match(r"^(\d+)([mhd])$", time_delta_str.lower())
    if not match:
        raise ValueError("Invalid time delta format. Use '10m', '2h', or '7d'.")
    value, unit = match.groups()
    if unit == "m":
        return timedelta(minutes=int(value))
    if unit == "h":
        return timedelta(hours=int(value))
    return timedelta(days=int(value))


@mcp.tool()
def get_deployments(
    app: Optional[str] = None,
    time_delta: Optional[str] = None,
    from_datetime: Optional[str] = None,  # Expects "YYYY-MM-DDTHH:MM:SS"
    to_datetime: Optional[str] = None,  # Expects "YYYY-MM-DDTHH:MM:SS"
) -> List[Task]:
    """
    Retrieves deployments. Supports relative time OR absolute datetime strings.
    Absolute datetimes take precedence.

    Args:
        app: The application name to filter by.
        time_delta: A relative time like '10m', '2h', or '7d'.
        from_datetime: An absolute start time in ISO format ("YYYY-MM-DDTHH:MM:SS").
        to_datetime: An absolute end time in ISO format ("YYYY-MM-DDTHH:MM:SS").
    """
    argo_client = get_argo_client()
    now = datetime.now(timezone.utc)
    to_timestamp = int(now.timestamp())

    # Prioritize absolute datetime strings
    if from_datetime:
        try:
            # FIX: Properly handle timezone-aware and naive datetime strings.
            start_dt = datetime.fromisoformat(from_datetime)
            if start_dt.tzinfo is None:
                start_dt = start_dt.replace(tzinfo=timezone.utc)
            else:
                start_dt = start_dt.astimezone(timezone.utc)
            from_timestamp = int(start_dt.timestamp())

            if to_datetime:
                end_dt = datetime.fromisoformat(to_datetime)
                if end_dt.tzinfo is None:
                    end_dt = end_dt.replace(tzinfo=timezone.utc)
                else:
                    end_dt = end_dt.astimezone(timezone.utc)
                to_timestamp = int(end_dt.timestamp())

        except ValueError:
            # FIX: Raise an exception instead of returning a string in the list.
            raise ValueError("Invalid datetime format. Please use YYYY-MM-DDTHH:MM:SS.")
    # Fall back to time_delta
    elif time_delta:
        try:
            delta = _parse_time_delta(time_delta)
            from_timestamp = int((now - delta).timestamp())
        except ValueError as e:
            # FIX: Raise the exception instead of returning a string.
            raise e
    # Default to 1 day
    else:
        delta = timedelta(days=1)
        from_timestamp = int((now - delta).timestamp())

    return argo_client.get_tasks(
        from_timestamp=from_timestamp,
        to_timestamp=to_timestamp,
        app=app,
    )
