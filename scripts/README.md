# Argo Watcher MCP - Client Scripts

This document describes the client scripts available in this repository for interacting with the Argo Watcher MCP server.

---

## Integration Test Script (`test.py`)

This script provides an end-to-end test of the server's tools and functionality by simulating a client interaction. It serves as the canonical example for how to programmatically interact with an `mcp-server` using the `mcp==1.9.4` Python SDK.

### Technical Implementation

The `mcp==1.9.4` library uses a two-layer architecture that must be composed by the developer:

1.  **Transport Layer (`sse_client`):** Handles the low-level network connection via Server-Sent Events (SSE).
2.  **Session Layer (`ClientSession`):** Implements the logical Model Context Protocol and exposes high-level methods like `.initialize()`, `.list_tools()`, and `.call_tool()`.

The `test.py` script correctly implements this architecture, including the mandatory multi-stage protocol handshake.

### Usage

1.  Ensure all dependencies are installed and the Poetry virtual environment is active.
2.  Start the ArgoWatcherMCP server in one terminal (this requires a running argo-watcher instance).
3.  From the project root, run the test script in a second terminal:

    ```bash
    ./scripts/test.py
    ```

The script will print its progress and the results of the tool calls.

---

## AI Chat Client (`ai-chat.py`)

This is an interactive command-line application that allows you to have a stateful conversation with the Argo Watcher MCP server using a powerful Large Language Model (OpenAI's GPT-4o) for natural language understanding.

### Purpose

The AI chat client demonstrates a more advanced use case where natural language questions are used to query the server. The LLM understands the user's intent, determines which tool to use from the server, and formulates a final answer based on the data returned by the tool.

### Technical Implementation

The script is built using a modern, object-oriented approach for a clean and maintainable codebase:

-   **`ArgoWatcherClient` Class:** Encapsulates all direct communication with the MCP server, including tool discovery and execution.
-   **`ChatManager` Class:** Orchestrates the entire interactive session. It maintains the conversation history, sends requests to the LLM, and uses the `ArgoWatcherClient` to perform tool calls.
-   **`click` and `rich`:** Used for creating a robust and user-friendly command-line interface with rich, formatted output (including Markdown).
-   **Stateful Conversation:** The chat history is maintained across multiple turns, allowing for follow-up questions and context-aware interactions.

### Usage

1.  **Set Environment Variables:**
    The script requires your OpenAI API key.
    ```bash
    export OPENAI_API_KEY="sk-..."
    ```

2.  **Start the ArgoWatcherMCP Server:**
    Ensure the server is running, either locally or in Docker. See the main project `README.md` for instructions.

3.  **Run the Chat Client:**
    From the project root, run the script. It will enter an interactive loop.
    ```bash
    ./scripts/ai-chat.py
    ```
    You can also enable verbose debugging output with the `--debug` flag:
    ```bash
    ./scripts/ai-chat.py --debug
    ```
    Once running, you can type your questions at the prompt. Type `exit` or `quit` to end the session.

