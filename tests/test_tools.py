from datetime import datetime
from datetime import timedelta
from datetime import timezone
from unittest.mock import MagicMock
from unittest.mock import patch

import pytest
from freezegun import freeze_time

from argo_watcher_mcp.client import ArgoWatcherClient
from argo_watcher_mcp.client import Task
from argo_watcher_mcp.tools import get_deployments


@pytest.fixture
def mock_argo_client() -> MagicMock:
    """
    Provides a mock ArgoWatcherClient instance.
    """
    return MagicMock(spec=ArgoWatcherClient)


@pytest.fixture(autouse=True)
def patch_get_argo_client(mock_argo_client: MagicMock):
    """
    Automatically patches 'get_argo_client' in the tools module for all tests
    in this file, making it return the mock_argo_client.
    """
    with patch("argo_watcher_mcp.tools.get_argo_client", return_value=mock_argo_client) as mock:
        yield mock


def test_get_deployments_returns_tasks(mock_argo_client: MagicMock):
    """
    Tests that the tool correctly returns the list of tasks from the argo client.
    """
    sample_task = Task(
        id="1",
        app="app-1",
        author="tester",
        project="Test",
        images=[],
        status="deployed",
        created=1.0,
        updated=2.0,
    )
    mock_argo_client.get_tasks.return_value = [sample_task]

    result = get_deployments()

    assert result == [sample_task]
    mock_argo_client.get_tasks.assert_called_once()


@freeze_time("2023-10-27 10:00:00", tz_offset=timedelta(0))
def test_get_deployments_calculates_timestamps_from_days_history(mock_argo_client: MagicMock):
    """
    Tests that timestamps are correctly calculated from `days_history` when none are provided.
    """
    now = datetime.now(timezone.utc)
    expected_to = int(now.timestamp())
    expected_from = int((now - timedelta(days=15)).timestamp())

    get_deployments(days_history=15)

    mock_argo_client.get_tasks.assert_called_once()
    _, call_kwargs = mock_argo_client.get_tasks.call_args
    assert call_kwargs.get("from_timestamp") == expected_from
    assert call_kwargs.get("to_timestamp") == expected_to


def test_get_deployments_uses_provided_timestamps(mock_argo_client: MagicMock):
    """
    Tests that provided timestamps override any calculation.
    """
    from_ts, to_ts = 1698000000, 1698300000

    get_deployments(from_timestamp=from_ts, to_timestamp=to_ts, days_history=99)

    mock_argo_client.get_tasks.assert_called_once_with(
        from_timestamp=from_ts, to_timestamp=to_ts, app=None
    )


def test_get_deployments_passes_app_parameter(mock_argo_client: MagicMock):
    """
    Tests that the `app` parameter is correctly passed to the client.
    """
    app_name = "my-special-app"

    get_deployments(app=app_name)

    mock_argo_client.get_tasks.assert_called_once()
    _, call_kwargs = mock_argo_client.get_tasks.call_args
    assert call_kwargs.get("app") == app_name
