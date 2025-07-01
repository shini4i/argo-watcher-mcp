# argo_watcher_mcp/health.py

from typing import Annotated

import httpx
from fastapi import APIRouter, Depends, HTTPException, status
from pydantic import BaseModel

from argo_watcher_mcp.client import ArgoWatcherClient
from argo_watcher_mcp.dependencies import get_argo_client


class HealthStatus(BaseModel):
    status: str

router = APIRouter()

@router.get("/live", response_model=HealthStatus)
def liveness_check() -> HealthStatus:
    """
    Checks if the application is running.
    """
    return HealthStatus(status="alive")


@router.get("/ready", response_model=HealthStatus)
def readiness_check(argo_client: Annotated[ArgoWatcherClient, Depends(get_argo_client)]):
    """
    Checks if the application is ready to serve traffic, including
    checking connectivity to downstream services like Argo Watcher.
    """
    try:
        # A lightweight check to ensure we can communicate with the downstream service.
        # Getting tasks from a 1-second window is cheap.
        argo_client.get_tasks(from_timestamp=0, to_timestamp=1)
    except httpx.RequestError as e:
        raise HTTPException(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            detail=f"Downstream service argo-watcher is unreachable. Reason: {e}",
        ) from e

    return HealthStatus(status="ready")
