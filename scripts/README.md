# ArgoWatcherMCP Test Script

This directory contains `test.py`, an integration test client for the ArgoWatcherMCP server.

## Purpose

The primary goal of this script is to provide an end-to-end test of the server's tools and functionality by simulating a client interaction. It serves as the canonical example for how to programmatically interact with an `mcp-server` using the `mcp==1.9.4` Python SDK, as the required process is non-trivial.

## Technical Implementation

The `mcp==1.9.4` library does not provide a single, high-level client object that handles both networking and protocol logic. Instead, the SDK is designed with a two-layer architecture that must be composed by the developer.

1.  **Transport Layer (`sse_client`):** Found in `mcp.client.sse`, this context manager handles the low-level network connection via Server-Sent Events (SSE). It manages the connection lifecycle and provides a pair of in-memory streams for reading and writing data.

2.  **Session Layer (`ClientSession`):** Found in `mcp.client.session`, this class implements the logical Model Context Protocol. It takes the I/O streams from the transport layer and exposes high-level methods like `.initialize()`, `.list_tools()`, and `.call_tool()`.

The `test.py` script correctly implements this two-layer architecture.

### Protocol Handshake

A successful connection requires a specific, multi-stage handshake which is handled automatically by the `ClientSession`'s `.initialize()` method:

1.  **Connect (`GET`):** The client opens a persistent SSE stream to `/sse` to receive a unique session URL.
2.  **Initialize (`POST`):** The client sends an `initialize` request to the session URL.
3.  **Confirm (`POST`):** After receiving a successful result, the client sends a final `notifications/initialized` notification to confirm the handshake.

Only after this sequence is complete can the session be used to execute commands like `tools/list` and `tools/call`.

## Usage

1.  Ensure all dependencies are installed and the poetry virtual environment is active.
2.  Start the ArgoWatcherMCP server in one terminal. (It will require running argo-watcher instance)
3.  From the project root, run the test script in a second terminal:

    ```bash
    ./scripts/test.py
    ```

The script will print its progress through each stage of the connection and handshake, then display the results of the tool calls. Any failure will be reported as a fatal error.

> By default, it will look for deployments of application "app".
