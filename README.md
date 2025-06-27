<div align="center">

# Argo Watcher MCP Server

A simple service that exposes an [argo-watcher](https://github.com/shini4i/argo-watcher) instance as a set of tools via the Model Context Protocol (MCP), allowing AI agents and other clients to query deployment history.

![GitHub Actions](https://img.shields.io/github/actions/workflow/status/shini4i/argo-watcher-mcp/run-tests.yml?branch=main&style=plastic)
![codecov](https://img.shields.io/codecov/c/github/shini4i/argo-watcher-mcp?style=plastic&token=E61B6OYPFX)
![GitHub release (latest by date)](https://img.shields.io/github/v/release/shini4i/argo-watcher-mcp?style=plastic)
![license](https://img.shields.io/github/license/shini4i/argo-watcher-mcp?style=plastic)

</div>

> [!IMPORTANT]
> This project is currently a Proof of Concept (PoC). It was built to explore the integration between Argo Watcher and the Model Context Protocol (MCP). As such, it may be subject to significant changes or be abandoned in the future. Please use it with this understanding.

## Features

- Exposes argo-watcher deployment tasks as an MCP tool.
- Filter deployments by application name and time range.
- Packaged as a production-ready Docker container.
- Simple, dependency-isolated architecture.

## Prerequisites

- Python 3.13+
- Poetry for dependency management.
- Docker for containerized deployment.
- A running instance of argo-watcher.

## Usage

This section outlines the full process for setting up the environment and running the interactive AI chat client.

1.  **Bootstrap `argo-watcher` Service**

    The MCP server depends on a running `argo-watcher` instance. You can quickly bootstrap this service using the official `docker-compose.yml` from the [argo-watcher repository](https://github.com/shini4i/argo-watcher/blob/main/docker-compose.yml).

    ```bash
    # In a separate terminal, from the argo-watcher project directory:
    docker-compose up
    ```

2.  **Start `argo-watcher-mcp` Server**

    With `argo-watcher` running, start the MCP server. This project includes a convenience task for this purpose.

    ```bash
    # From this project's root directory:
    task run
    ```

3.  **Configure OpenAI Credentials**

    The AI chat client requires an OpenAI API key. Export it as an environment variable in the terminal where you plan to run the chat.

    ```bash
    export OPENAI_API_KEY="sk-..."
    ```

4.  **Launch the Interactive AI Chat**

    Finally, run the AI chat client using its pre-configured task.

    ```bash
    # This will start the interactive chat session.
    task chat
    ```

5.  **Ask Questions**

    The script will enter an interactive loop. You can now ask questions about your deployments in natural language.

<div align="center">

<img src="https://raw.githubusercontent.com/shini4i/assets/main/src/argo-watcher-mcp/argo-watcher-mcp-chat.png" alt="Showcase" width="680" height="380">

</div>

## Contributing

As this is a PoC, formal contributions are not the primary focus. However, if you find a bug or have a suggestion, feel free to open an issue.
