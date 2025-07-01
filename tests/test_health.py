from unittest.mock import MagicMock

import httpx
import pytest
from fastapi.testclient import TestClient

# Import the factory function instead of the app instance
from argo_watcher_mcp.app import create_app
from argo_watcher_mcp.client import ArgoWatcherClient
from argo_watcher_mcp.dependencies import get_argo_client


@pytest.fixture
def app_with_mocked_client():
    """
    This fixture creates a new app instance for tests, creates a mock client,
    and applies the dependency override. It yields both the app and the mock.
    """
    app = create_app()
    mock = MagicMock(spec=ArgoWatcherClient)
    # noinspection PyUnresolvedReferences
    app.dependency_overrides[get_argo_client] = lambda: mock
    yield app, mock
    # noinspection PyUnresolvedReferences
    app.dependency_overrides.clear()


def test_liveness_check(app_with_mocked_client):
    """
    Tests the /healthz endpoint.
    """
    app, _ = app_with_mocked_client
    client = TestClient(app)
    response = client.get("/healthz")
    assert response.status_code == 200
    assert response.json() == {"status": "up"}


def test_readiness_check_success(app_with_mocked_client):
    """
    Tests the /readyz endpoint for a successful check.
    """
    app, mock_argo_client = app_with_mocked_client
    client = TestClient(app)
    mock_argo_client.check_health.return_value = None

    response = client.get("/readyz")

    assert response.status_code == 200
    assert response.json() == {"status": "ready"}
    mock_argo_client.check_health.assert_called_once()


@pytest.mark.parametrize(
    "exception_to_raise",
    [
        httpx.RequestError(message="Network error", request=None),
        httpx.HTTPStatusError(
            message="Server error", request=None, response=MagicMock(status_code=500)
        ),
    ],
)
def test_readiness_check_failure(app_with_mocked_client, exception_to_raise):
    """
    Tests that the /readyz endpoint fails correctly if the downstream
    service is unreachable or not healthy.
    """
    app, mock_argo_client = app_with_mocked_client
    client = TestClient(app)
    mock_argo_client.check_health.side_effect = exception_to_raise

    response = client.get("/readyz")

    assert response.status_code == 503
    assert "Downstream service argo-watcher" in response.json()["detail"]
    mock_argo_client.check_health.assert_called_once()
