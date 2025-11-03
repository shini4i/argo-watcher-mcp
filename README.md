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
- Runs over stdio and HTTP (streamable SSE) via the official MCP Go SDK.
- Packaged as a production-ready Docker container.
- Modular, testable Go architecture.

## Prerequisites

- Go 1.25+ (for local builds and development).
- Docker 24+ (optional, for containerized deployment).
- [Task](https://taskfile.dev) 3.x (optional, to use the provided Taskfile).
- A running instance of [argo-watcher](https://github.com/shini4i/argo-watcher).

## Quickstart

1. **Ensure argo-watcher is running**

   The MCP server reads deployment data from an existing argo-watcher instance. You can bootstrap one quickly using the [official docker-compose file](https://github.com/shini4i/argo-watcher/blob/main/docker-compose.yml).

   ```bash
   # In the argo-watcher repository
   docker compose up
   ```

2. **Configure the MCP server**

   Export the required environment variables:

   ```bash
   export ARGO_WATCHER_URL="http://localhost:8001"  # adjust to your deployment
   export HTTP_LISTEN_ADDR=":8000"
   ```

3. **Run the server locally**

   ```bash
   task run
   ```

   The process exposes:

   - An MCP streamable HTTP endpoint on `HTTP_LISTEN_ADDR` (default `:8000`).
   - stdio transport on the launched process stdin/stdout (usable by MCP-compatible CLIs).

4. **(Optional) Run via Docker**

   ```bash
   GOOS=linux GOARCH=amd64 go build -o argo-watcher-mcp ./cmd/server
   docker build -t argo-watcher-mcp .
   docker run --rm -p 8000:8000 \
     -e ARGO_WATCHER_URL="http://host.docker.internal:8001" \
     argo-watcher-mcp
   ```

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `ARGO_WATCHER_URL` | _none_ (required) | Base URL of the upstream argo-watcher service. |
| `HTTP_LISTEN_ADDR` | `:8000` | Address for the HTTP/SSE transport. |
| `ENABLE_HTTP_TRANSPORT` | `true` | Toggle the HTTP transport while keeping stdio available. |
| `REQUEST_TIMEOUT` | `15s` | HTTP timeout for downstream argo-watcher requests. |
| `APP_NAME` | `argo-watcher-mcp` | Metadata surfaced via MCP implementation info. |
| `APP_VERSION` | `0.0.1-dev` | Version surfaced via MCP implementation info. |

## Development

Common tasks are exposed via the included `Taskfile`:

- `task test` – run unit tests.
- `task build` – compile the server binary into `bin/argo-watcher-mcp`.
- `task fmt` – format the Go sources.
- `task tidy` – tidy module dependencies.
- `task vet` – execute `go vet ./...`.
- `task run` – start the server locally with stdio + HTTP transports.

Module and build caches live inside `.cache/` and are ignored by git.

Releases are produced with [GoReleaser](https://goreleaser.com/) and publish multi-architecture container images (`linux/amd64` and `linux/arm64`) to GHCR when tags matching `v*.*.*` are pushed.

## Contributing

As this is a PoC, formal contributions are not the primary focus. However, if you find a bug or have a suggestion, feel free to open an issue.
