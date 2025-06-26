import httpx
import pytest
import respx
from pydantic import ValidationError

from argo_watcher_mcp.client import ArgoWatcherClient
from argo_watcher_mcp.client import Image
from argo_watcher_mcp.client import Task

BASE_URL = "http://test-argo-watcher.com"


@pytest.fixture
def argo_client() -> ArgoWatcherClient:
    """Provides a test instance of the ArgoWatcherClient with a real httpx.Client."""
    return ArgoWatcherClient(base_url=BASE_URL, client=httpx.Client())


def test_image_model():
    """Tests the basic instantiation of the Image model."""
    image = Image(image="my-repo/my-image", tag="v1.0.0")
    assert image.image == "my-repo/my-image"
    assert image.tag == "v1.0.0"


@pytest.mark.parametrize(
    "task_data, expected_validated, expected_reason, expected_timeout",
    [
        (
            {
                "id": "full-id",
                "app": "test-app",
                "author": "tester",
                "project": "proj",
                "images": [{"image": "img", "tag": "v1"}],
                "status": "deployed",
                "created": 1.0,
                "updated": 2.0,
                "status_reason": "Success",
                "validated": True,
                "timeout": 300,
            },
            True,
            "Success",
            300,
        ),
        (
            {
                "id": "minimal-id",
                "app": "test-app",
                "author": "tester",
                "project": "proj",
                "images": [],
                "status": "pending",
                "created": 1.0,
                "updated": 2.0,
            },
            False,  # Default value
            None,  # Default value
            None,  # Default value
        ),
    ],
)
def test_task_model(task_data, expected_validated, expected_reason, expected_timeout):
    """
    Tests the Task model with both a full and minimal set of fields
    to ensure default values are handled correctly.
    """
    task = Task.model_validate(task_data)
    assert task.id == task_data["id"]
    assert task.validated is expected_validated
    assert task.status_reason == expected_reason
    assert task.timeout == expected_timeout


def test_task_model_raises_validation_error_on_missing_required_field():
    """Tests that Pydantic raises a ValidationError if a required field is missing."""
    invalid_data = {"id": "1", "author": "tester"}  # Missing app, project, etc.
    with pytest.raises(ValidationError):
        Task.model_validate(invalid_data)


# --- Client Tests ---


@respx.mock
def test_get_tasks_success(argo_client: ArgoWatcherClient):
    """
    Tests the successful retrieval and parsing of a full task from the API.
    """
    mock_response = {
        "tasks": [
            {
                "id": "1",
                "app": "app-1",
                "author": "tester",
                "project": "TestProject",
                "images": [{"image": "img", "tag": "v1"}],
                "status": "deployed",
                "created": 1.0,
                "updated": 2.0,
                "status_reason": "Success",
                "validated": True,
                "timeout": 600,
            }
        ]
    }
    respx.get(f"{BASE_URL}/api/v1/tasks").mock(return_value=httpx.Response(200, json=mock_response))

    tasks = argo_client.get_tasks(from_timestamp=0)

    assert len(tasks) == 1
    task = tasks[0]
    assert isinstance(task, Task)
    assert task.id == "1"
    assert task.app == "app-1"
    assert task.validated is True
    assert task.timeout == 600


@respx.mock
@pytest.mark.parametrize(
    "mock_response, expected_tasks_len",
    [
        ({"tasks": []}, 0),  # Empty list
        ({}, 0),  # Missing 'tasks' key
    ],
)
def test_get_tasks_handles_empty_or_malformed_responses(
    argo_client: ArgoWatcherClient, mock_response, expected_tasks_len
):
    """
    Tests that the client correctly handles an empty tasks list or a missing 'tasks' key.
    """
    respx.get(f"{BASE_URL}/api/v1/tasks").mock(return_value=httpx.Response(200, json=mock_response))
    tasks = argo_client.get_tasks(from_timestamp=0)
    assert len(tasks) == expected_tasks_len
    assert tasks == []


@respx.mock
@pytest.mark.parametrize(
    "kwargs, expected_query_params",
    [
        (
            {"from_timestamp": 123, "to_timestamp": 456, "app": "my-app"},
            "from_timestamp=123&to_timestamp=456&app=my-app",
        ),
        (
            {"from_timestamp": 123, "to_timestamp": None, "app": "my-app"},
            "from_timestamp=123&app=my-app",
        ),
        ({"from_timestamp": 123, "app": None}, "from_timestamp=123"),
        ({"from_timestamp": 123, "app": ""}, "from_timestamp=123&app="),
    ],
)
def test_get_tasks_builds_url_correctly(
    argo_client: ArgoWatcherClient, kwargs, expected_query_params
):
    """
    Tests that the client builds the request URL with the correct query parameters,
    correctly omitting None values but including empty strings.
    """
    route = respx.get(url__regex=rf"{BASE_URL}/api/v1/tasks\?.*").mock(
        return_value=httpx.Response(200, json={"tasks": []})
    )

    argo_client.get_tasks(**kwargs)

    assert route.called
    request_url = str(route.calls.last.request.url)
    assert request_url == f"{BASE_URL}/api/v1/tasks?{expected_query_params}"


@respx.mock
def test_get_tasks_raises_for_http_error(argo_client: ArgoWatcherClient):
    """
    Tests that the client raises an HTTPStatusError for non-2xx responses.
    """
    respx.get(f"{BASE_URL}/api/v1/tasks").mock(return_value=httpx.Response(500))

    with pytest.raises(httpx.HTTPStatusError, match="500 Internal Server Error"):
        argo_client.get_tasks(from_timestamp=0)
