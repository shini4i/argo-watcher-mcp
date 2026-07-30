<div align="center">

# Argo Watcher MCP Server

A simple service that exposes an [argo-watcher](https://github.com/shini4i/argo-watcher) instance as a set of tools via the Model Context Protocol (MCP), allowing AI agents and other clients to query deployment history.

![GitHub Actions](https://img.shields.io/github/actions/workflow/status/shini4i/argo-watcher-mcp/go-tests.yml?branch=main&style=plastic)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/shini4i/argo-watcher-mcp?style=plastic)
![GitHub release (latest by date)](https://img.shields.io/github/v/release/shini4i/argo-watcher-mcp?style=plastic)
![codecov](https://img.shields.io/codecov/c/github/shini4i/argo-watcher-mcp?style=plastic&token=E61B6OYPFX)
![license](https://img.shields.io/github/license/shini4i/argo-watcher-mcp?style=plastic)

</div>

> [!IMPORTANT]
> This project is currently a Proof of Concept (PoC). It was built to explore the integration between Argo Watcher and the Model Context Protocol (MCP). As such, it may be subject to significant changes or be abandoned in the future. Please use it with this understanding.

## Features

- Exposes argo-watcher's read-only API as MCP tools, so an agent can answer what
  was deployed, when, by whom, and whether it succeeded.
- Filter deployment history by application, status, and time range, with
  pagination and an explicit truncation signal.
- Surfaces the deploy lock, dependency reachability, and instance configuration.
- Runs over stdio and HTTP (streamable SSE) via the official MCP Go SDK.
- Packaged as a production-ready Docker container.
- Modular, testable Go architecture.

> [!NOTE]
> Only read operations are exposed. This server cannot create deployments or
> change the deploy lock, by design.

## Tools

| Tool | Answers | Upstream endpoint |
|------|---------|-------------------|
| `get_deployments` | What was deployed, when, by whom, with which image tags, and whether it succeeded, failed, or was a rollback. | `GET /api/v1/tasks` |
| `get_deploy_lock` | Are deployments currently frozen (manually or by a scheduled lockdown)? | `GET /api/v1/deploy-lock` |
| `get_reachability` | Can argo-watcher currently reach ArgoCD and its state backend? | `GET /api/v1/reachability` |
| `get_server_info` | Which argo-watcher version is running, and how is it configured? | `GET /api/v1/version`, `GET /api/v1/config` |

### Deployment history and pagination

`get_deployments` accepts `app`, `status`, `days_history` (or `from_timestamp` /
`to_timestamp`), `limit`, and `offset`. Results are ordered newest first.

`limit` defaults to **50** and may not exceed **1000**, the cap argo-watcher
enforces on its task endpoint. Every response reports:

- `total` — how many deployments matched the filter in full, ignoring pagination.
- `truncated` — `true` when deployments remain *after this page*. Note an
  `offset` past the end reports `false`: nothing remains, even though `total` is
  larger than the page.

Check `truncated` before counting or aggregating; a page that silently stopped at
the cap otherwise reads as a complete history.

An **empty result is ambiguous**. argo-watcher's task query returns HTTP 200 with
an empty list both when nothing matched and when its database is unreachable, so
`get_deployments` cannot tell the two apart. Call `get_reachability` before
concluding that nothing was deployed — it reports `cannot connect to database`
in the outage case.

Valid `status` values are those argo-watcher accepts: `in progress`, `deployed`,
`failed`, `cancelled`, `aborted`, `accepted`, `app not found`,
`argocd is unavailable`, `failed to login to argocd`, and
`cannot connect to database`.

argo-watcher does not support filtering by author, so questions about a specific
person require fetching the relevant window and filtering the results.

`get_server_info` forwards an explicit allowlist of configuration fields rather
than the whole payload. argo-watcher already excludes every secret from
`/api/v1/config`, so the allowlist is not guarding today's response — it guards
the next one, so that a field added upstream cannot reach an LLM's context
without a deliberate change here. Notification integrations are reduced to their
`enabled` flag; their URLs and channel IDs are not forwarded.

### Required argo-watcher version

The tools depend on upstream features added at different times. Against an older
argo-watcher, unknown query parameters are **silently ignored** rather than
rejected, so a filter can appear to apply when it did not.

| Feature | Minimum argo-watcher |
|---------|----------------------|
| `total` (accurate counts), `get_deploy_lock` | v0.10.0 |
| `status` filter | v0.10.2 |
| `is_rollback` / `rollback_target_id` | v0.11.0 |
| `limit` / `offset` pagination | v0.12.0 |
| `get_reachability` | v0.13.0 |

**v0.12.0 or newer is recommended** for everything except `get_reachability`,
which needs the `/api/v1/reachability` endpoint. That endpoint is not in any
stable argo-watcher release yet — it currently ships only in
`v0.13.0-pre.20260726` — so `get_reachability` will fail against a stable
upstream until v0.13.0 lands.

## Prerequisites

- Go 1.26+ (for local builds and development).
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

   - Choose the transport via the `TRANSPORT_MODE` environment variable (`stdio` or `http`).
   - In `stdio` mode the server communicates over stdin/stdout; in `http` mode it serves a streamable endpoint on `HTTP_LISTEN_ADDR` (default `:8000`).

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
| `TRANSPORT_MODE` | `stdio` | Selects transport: `stdio` for CLI usage or `http` for SSE over HTTP. |
| `REQUEST_TIMEOUT` | `15s` | HTTP timeout for downstream argo-watcher requests. |
| `OTEL_ENABLED` | `true` | Enables OpenTelemetry instrumentation; set to `false` to disable all telemetry exports. |
| `OTEL_SERVICE_NAME` | inherits `APP_NAME` | Overrides the OpenTelemetry `service.name` resource attribute reported by the server. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | _none_ | Optional OTLP gRPC endpoint (`host:port`) used for exporting metrics and traces. |
| `OTEL_EXPORTER_OTLP_INSECURE` | `false` | Set to `true` to allow plaintext OTLP connections when the collector does not use TLS. |
| `APP_NAME` | `argo-watcher-mcp` | Metadata surfaced via MCP implementation info. |
| `APP_VERSION` | `0.0.1-dev` | Version surfaced via MCP implementation info. |

### Telemetry & Metrics

When telemetry is enabled, the HTTP transport exposes Prometheus metrics at `/metrics`. Key series:

- `argo_watcher_mcp_requests_total{result="success|invalid|failed"}` – MCP tool call counter partitioned by result label; filter by `result` to obtain invalid or failed counts.
- `argo_watcher_reachable` – gauge reporting downstream reachability (`1` when Argo Watcher responded successfully, `0` otherwise).
- Standard Go runtime (`go_*`) and process metrics are registered on the Prometheus endpoint to aid capacity monitoring.

## Development

Common tasks are exposed via the included `Taskfile`:

- `task test` – run unit tests.
- `task build` – compile the server binary into `bin/argo-watcher-mcp`.
- `task fmt` – format the Go sources.
- `task tidy` – tidy module dependencies.
- `task vet` – execute `go vet ./...`.
- `task run` – start the server locally using the default transport mode (`stdio` unless `TRANSPORT_MODE` is set).

Module and build caches live inside `.cache/` and are ignored by git.

Releases are produced with [GoReleaser](https://goreleaser.com/) and publish multi-architecture container images (`linux/amd64` and `linux/arm64`) to GHCR when tags matching `v*.*.*` are pushed.

## Contributing

As this is a PoC, formal contributions are not the primary focus. However, if you find a bug or have a suggestion, feel free to open an issue.
