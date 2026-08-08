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

- Exposes argo-watcher's read-only API as MCP tools: what was deployed, when, by
  whom, and whether it succeeded.
- Also surfaces the deploy lock, dependency reachability, and instance config.
- Runs over stdio or HTTP (streamable SSE) via the official MCP Go SDK.
- Multi-platform distroless container image.

> [!NOTE]
> Read-only by design: this server cannot create deployments or change the deploy
> lock.

## Tools

| Tool | Answers | Upstream endpoint |
|------|---------|-------------------|
| `get_deployments` | What was deployed, when, by whom, with which image tags, and whether it succeeded, failed, or was a rollback. | `GET /api/v1/tasks` |
| `get_deploy_lock` | Are deployments currently frozen? | `GET /api/v1/deploy-lock` |
| `get_reachability` | Can argo-watcher reach ArgoCD and its state backend? | `GET /api/v1/reachability` |
| `get_server_info` | Which argo-watcher version is running, and how is it configured? | `GET /api/v1/version`, `GET /api/v1/config` |

Parameters and constraints live in each tool's MCP schema. Two things it does not
tell you:

- `get_deployments` returns 50 rows by default and 1000 at most, so check `total`
  and `truncated` before counting. An empty result can also mean argo-watcher's
  database is unreachable; `get_reachability` distinguishes the two.
- `get_server_info` forwards an allowlist of config fields, reduces `oidc`,
  `webhook` and `mattermost` to their `enabled` flag, and strips
  `user:password@` from URL-valued fields — argo-watcher does not redact
  `ARGO_URL_ALIAS` or `DOCKER_IMAGES_PROXY` itself.

### Required argo-watcher version

Older versions silently ignore unknown query parameters, so a filter can appear
to apply when it did not.

| Feature | Minimum argo-watcher |
|---------|----------------------|
| `total`, `get_deploy_lock` | v0.10.0 |
| `status` filter | v0.10.2 |
| `is_rollback` / `rollback_target_id` | v0.11.0 |
| `limit` / `offset` | v0.12.0 |
| `get_reachability` | v0.13.0 |
| `/readyz` (this server's own readiness check) | unreleased |

v0.13.0+ is recommended; v0.14.0 is the current stable upstream release.

This server's readiness check requires argo-watcher's `/livez` and `/readyz`,
which arrive with [the probe split](https://github.com/shini4i/argo-watcher/pull/535)
and are unreleased. Against v0.14.0 and older it reports `503` with
`no probe payload`, so give this server an upstream that serves them before
wiring a readiness probe to it. Every MCP tool works against v0.13.0+ regardless.

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
   GOOS=linux GOARCH=amd64 go build -o linux/amd64/argo-watcher-mcp ./cmd/server
   docker build --build-arg TARGETPLATFORM=linux/amd64 -t argo-watcher-mcp .
   docker run --rm -p 8000:8000 \
     -e ARGO_WATCHER_URL="http://host.docker.internal:8001" \
     argo-watcher-mcp
   ```

   For the real multi-platform images, use `goreleaser release --snapshot --clean`.

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
| `APP_VERSION` | build-stamped (`local` otherwise) | Version surfaced via MCP implementation info. Releases link it in; set this only to override. |

### Health endpoints

The HTTP transport serves two unauthenticated probe endpoints:

| Endpoint | Reports | Use as |
|----------|---------|--------|
| `/healthz` | This process is serving; checks nothing else | Liveness probe |
| `/readyz` | This process is serving **and** argo-watcher answers | Readiness probe |

`/readyz` deliberately stays `200` when argo-watcher answers but reports itself
unready — its state backend is down, or it is shutting down. Every replica of this
server shares one argo-watcher, so failing readiness there would withdraw all of
them at once and take `get_reachability`, the tool that names the cause, out of
reach with them. That verdict is reported in the body instead:

```json
{"status":"ready","argo_watcher":"not_ready","argo_watcher_reason":"state backend unreachable"}
```

Alert on `get_reachability` or on argo-watcher's own metrics for that condition.
`/readyz` returns `503` only when argo-watcher's process does not answer at all —
including against an upstream too old to serve `/livez`, per
[Required argo-watcher version](#required-argo-watcher-version).

### Telemetry & Metrics

When telemetry is enabled, the HTTP transport exposes Prometheus metrics at `/metrics`. Key series:

- `argo_watcher_mcp_requests_total{result="success|invalid|failed"}` – MCP tool call counter partitioned by result label; filter by `result` to obtain invalid or failed counts.
- `argo_watcher_reachable` – gauge reporting downstream reachability (`1` when argo-watcher answered, `0` otherwise). It tracks reachability, not health: an argo-watcher whose readiness probe reports it unready still reads `1`.
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
