from typing import Annotated

import httpx
from fastapi import APIRouter
from fastapi import Depends
from fastapi import HTTPException
from fastapi import status
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
    return HealthStatus(status="up")


@router.get("/ready", response_model=HealthStatus)
def readiness_check(argo_client: Annotated[ArgoWatcherClient, Depends(get_argo_client)]):
    """
    Checks if the application is ready to serve traffic by checking connectivity
    to the downstream argo-watcher service.
    """
    try:
        argo_client.check_health()
    except httpx.RequestError as e:
        raise HTTPException(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            detail=f"Downstream service argo-watcher is unreachable. Reason: {e}",
        ) from e
    except httpx.HTTPStatusError as e:
        raise HTTPException(
            status_code=status.HTTP_503_SERVICE_UNAVAILABLE,
            detail=f"Downstream service argo-watcher is not healthy. Reason: {e}",
        ) from e

    return HealthStatus(status="ready")
