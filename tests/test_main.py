from datetime import datetime
from datetime import timedelta
from datetime import timezone
from unittest.mock import MagicMock
from unittest.mock import patch

import pytest
from freezegun import freeze_time

from argo_watcher_mcp.client import ArgoWatcherClient
from argo_watcher_mcp.client import Task
from argo_watcher_mcp.main import get_argo_client
from argo_watcher_mcp.main import get_deployments

# --- Tests for get_argo_client ---


def test_get_argo_client_success(monkeypatch):
    """
    Tests that get_argo_client successfully creates a client
    when the environment variable is set.
    """
    test_url = "http://test-url.com"
    monkeypatch.setenv("ARGO_WATCHER_URL", test_url)

    client = get_argo_client()

    assert isinstance(client, ArgoWatcherClient)
    # Note: Accessing a "private" member for a test is acceptable to verify state.
    assert client._base_url == test_url


def test_get_argo_client_raises_runtime_error(monkeypatch):
    """
    Tests that get_argo_client raises a RuntimeError
    if the environment variable is not set.
    """
    # Ensure the variable is not set
    monkeypatch.delenv("ARGO_WATCHER_URL", raising=False)

    with pytest.raises(RuntimeError, match="ARGO_WATCHER_URL environment variable is not set."):
        get_argo_client()


# --- Tests for get_deployments tool ---


@pytest.fixture
def mock_argo_client_factory():
    """
    Provides a factory to create a mock ArgoWatcherClient.
    This fixture patches the get_argo_client dependency within the main module.
    """
    # This is the mock object that will be "returned" by get_argo_client
    mock_client = MagicMock(spec=ArgoWatcherClient)
    # We also mock the method that will be called on the client instance
    mock_client.get_tasks = MagicMock(return_value=[])

    # The patch intercepts calls to `get_argo_client` within the `main` module
    # and returns our mock_client instead.
    with patch("argo_watcher_mcp.main.get_argo_client", return_value=mock_client) as mock_factory:
        yield mock_factory, mock_client


def test_get_deployments_returns_tasks(mock_argo_client_factory):
    """
    Tests that the tool correctly returns the list of tasks
    from the argo client.
    """
    _, mock_client = mock_argo_client_factory
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
    mock_client.get_tasks.return_value = [sample_task]

    result = get_deployments()

    assert result == [sample_task]
    mock_client.get_tasks.assert_called_once()


@freeze_time("2023-10-27 10:00:00", tz_offset=timedelta(0))
def test_get_deployments_calculates_timestamps_from_days_history(mock_argo_client_factory):
    """
    Tests that timestamps are correctly calculated from `days_history`
    when none are provided.
    """
    _, mock_client = mock_argo_client_factory
    now = datetime.now(timezone.utc)
    expected_to = int(now.timestamp())
    expected_from = int((now - timedelta(days=15)).timestamp())

    get_deployments(days_history=15)

    mock_client.get_tasks.assert_called_once()
    call_args, call_kwargs = mock_client.get_tasks.call_args
    assert call_kwargs.get("from_timestamp") == expected_from
    assert call_kwargs.get("to_timestamp") == expected_to


def test_get_deployments_uses_provided_timestamps(mock_argo_client_factory):
    """
    Tests that provided timestamps override any calculation.
    """
    _, mock_client = mock_argo_client_factory
    from_ts, to_ts = 1698000000, 1698300000

    get_deployments(from_timestamp=from_ts, to_timestamp=to_ts, days_history=99)

    mock_client.get_tasks.assert_called_once_with(
        from_timestamp=from_ts, to_timestamp=to_ts, app=None
    )


@freeze_time("2023-10-27 10:00:00", tz_offset=timedelta(0))
def test_get_deployments_calculates_to_timestamp_if_only_from_is_provided(mock_argo_client_factory):
    """
    Tests that `to_timestamp` defaults to now() if only `from_timestamp` is given.
    """
    _, mock_client = mock_argo_client_factory
    from_ts = 1698000000
    expected_to = int(datetime.now(timezone.utc).timestamp())

    get_deployments(from_timestamp=from_ts)

    mock_client.get_tasks.assert_called_once_with(
        from_timestamp=from_ts, to_timestamp=expected_to, app=None
    )


def test_get_deployments_calculates_from_timestamp_if_only_to_is_provided(mock_argo_client_factory):
    """
    Tests that `from_timestamp` is calculated from `days_history` if only
    `to_timestamp` is given.
    """
    _, mock_client = mock_argo_client_factory
    to_ts = 1698364800  # 2023-10-27 00:00:00 UTC
    to_dt = datetime.fromtimestamp(to_ts, tz=timezone.utc)
    expected_from = int((to_dt - timedelta(days=30)).timestamp())  # Default days_history

    get_deployments(to_timestamp=to_ts)

    mock_client.get_tasks.assert_called_once_with(
        from_timestamp=expected_from, to_timestamp=to_ts, app=None
    )


def test_get_deployments_passes_app_parameter(mock_argo_client_factory):
    """
    Tests that the `app` parameter is correctly passed to the client.
    """
    _, mock_client = mock_argo_client_factory
    app_name = "my-special-app"

    get_deployments(app=app_name)

    mock_client.get_tasks.assert_called_once()
    _, call_kwargs = mock_client.get_tasks.call_args
    assert call_kwargs.get("app") == app_name
