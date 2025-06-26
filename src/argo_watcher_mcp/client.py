from typing import List
from typing import Optional

import httpx
from pydantic import BaseModel


class Image(BaseModel):
    """Represents a container image."""

    image: str
    tag: str


class Task(BaseModel):
    """Represents a task from the argo-watcher API."""

    id: str
    app: str
    author: str
    project: str
    images: List[Image]
    status: str
    created: float
    updated: float
    status_reason: Optional[str] = None
    validated: bool = False
    timeout: Optional[int] = None


class ArgoWatcherClient:
    """A client for the argo-watcher API."""

    def __init__(self, base_url: str, client: httpx.Client):
        self._base_url = base_url
        self._client = client

    def get_tasks(
        self,
        from_timestamp: int,
        to_timestamp: Optional[int] = None,
        app: Optional[str] = None,
    ) -> List[Task]:
        """
        Fetches tasks from the argo-watcher /api/v1/tasks endpoint.
        """
        params = {
            "from_timestamp": from_timestamp,
            "to_timestamp": to_timestamp,
            "app": app,
        }

        request_params = {k: v for k, v in params.items() if v is not None}

        response = self._client.get(f"{self._base_url}/api/v1/tasks", params=request_params)
        response.raise_for_status()

        response_data = response.json()
        tasks_list = response_data.get("tasks", [])

        return [Task.model_validate(task) for task in tasks_list]
