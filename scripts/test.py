#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""
A high-level integration test script for the ArgoWatcherMCP server.

This script correctly uses the Model Context Protocol (MCP) v1.9.4 SDK to
perform end-to-end testing. It was created after a rigorous diagnostic process
which revealed that the library requires a specific two-layer architecture for
client-side communication.

This implementation serves as the canonical example for how to interact with an
MCP server using this version of the SDK.
"""

import asyncio
import json
import sys
from urllib.parse import urlencode

from mcp.client.session import ClientSession
from mcp.client.sse import sse_client

# --- Configuration ---
# The base URL of the running FastMCP server.
BASE_URL = "http://localhost:8000"
# The path for initiating the Server-Sent Events (SSE) connection.
SSE_INIT_PATH = "/sse"


async def main():
    """
    Executes the full test suite against the MCP server.

    This function demonstrates the correct, multi-stage process for
    communicating with the server using the intended SDK abstractions.
    """
    print(f"--- Testing MCP server at {BASE_URL} using the intended SDK architecture ---")

    # The MCP protocol requires an initial command to be passed as a URL query
    # parameter to begin a session. 'mcp.discovery.list_tools' is a safe,
    # read-only command suitable for this purpose.
    init_command = {"mcp_version": "1.0", "method": "mcp.discovery.list_tools"}
    query_params = urlencode({"p": json.dumps(init_command)})
    init_url = f"{BASE_URL.rstrip('/')}/{SSE_INIT_PATH.lstrip('/')}?{query_params}"

    try:
        # The SDK is designed with two layers: a transport layer and a session layer.
        # They must be used together as shown below.

        # 1. Transport Layer: The `sse_client` context manager handles the low-level
        #    network connection, SSE event parsing, and provides two in-memory
        #    streams for communication.
        async with sse_client(url=init_url) as (read_stream, write_stream):

            # 2. Session Layer: The `ClientSession` object takes the I/O streams from
            #    the transport layer and implements the high-level MCP logic.
            session = ClientSession(read_stream=read_stream, write_stream=write_stream)

            # The `async with session` block ensures the session's background
            # tasks for processing messages are started and properly shut down.
            async with session:
                print("ClientSession connection successful. Session is active.")
                print("-" * 50)

                # 3. Handshake: The `.initialize()` method performs the entire mandatory
                #    three-stage handshake (client sends initialize -> server replies ->
                #    client sends initialized notification).
                print("Initializing session...")
                init_result = await session.initialize()
                print("\n--- Initialization Result ---")
                print(json.dumps(init_result.model_dump(mode="json"), indent=2))
                print("-" * 50)

                # 4. Execution: With the handshake complete, we can now use the high-level
                #    methods to interact with the server's tools.
                print("Discovering tools...")
                tools = await session.list_tools()
                print("\n--- Discovery Result ---")
                print(json.dumps(tools.model_dump(mode="json"), indent=2))
                print("-" * 50)

                print("Calling 'get_deployments' tool...")
                # The arguments for the tool are passed as a dictionary. The session
                # object correctly formats this into the required JSON-RPC payload.
                deployment_params = {"days_history": 1, "app": "app"}
                deployment_tasks = await session.call_tool(
                    name="get_deployments", arguments=deployment_params
                )

                print("\n--- 'get_deployments' Execution Result ---")
                if deployment_tasks and deployment_tasks.content:
                    # The server may wrap tool results in a content object.
                    # The content itself may contain double-serialized JSON,
                    # which we parse here for clean, readable output.
                    for item in deployment_tasks.content:
                        if hasattr(item, "text") and isinstance(item.text, str):
                            print(json.dumps(json.loads(item.text), indent=2))
                        else:
                            print(json.dumps(item.model_dump(mode="json"), indent=2))
                else:
                    print("  -> No tasks found or empty content returned.")
                print("-" * 50)

    except Exception as e:
        print(f"\n[FATAL ERROR] An unexpected error occurred: {e}", file=sys.stderr)
        print("Please ensure the MCP server is running.", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    asyncio.run(main())
