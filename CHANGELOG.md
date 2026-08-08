# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

While the project is at `0.x`, a minor release may carry breaking changes.
Breaking entries are marked below.

## [Unreleased]

### Changed

- **Breaking:** the readiness check now probes argo-watcher's `/livez` and
  `/readyz` instead of `/healthz`, which
  [its probe split](https://github.com/shini4i/argo-watcher/pull/535) removes.
  Those endpoints are unreleased upstream: against v0.14.0 and older, `/readyz`
  here returns `503` with `no probe payload`, so do not wire a readiness probe to
  this server until its argo-watcher serves them. The MCP tools are unaffected and
  keep working against v0.13.0+. A pre-split argo-watcher answers `/livez` from
  the catch-all serving its Web UI, so the probe response is recognised by its
  JSON payload rather than its status code — an HTML shell at `200` is refused
  rather than read as healthy.
- **Breaking:** `/readyz` stays `200` when argo-watcher answers but reports itself
  unready — state backend down, or shutting down — and names the verdict in the
  body as `argo_watcher` and `argo_watcher_reason` instead. It previously returned
  `503`. Every replica shares one argo-watcher, so failing readiness on its state
  backend withdrew all of them at once and took `get_reachability`, the tool that
  explains the outage, out of reach with them. `503` is now reserved for an
  argo-watcher whose process does not answer at all. Alerts that watched this
  endpoint for a state-backend outage should watch `get_reachability` or
  argo-watcher's own metrics instead.
- **Breaking:** `argo_watcher_reachable` follows the same line: it now reads `1`
  for an argo-watcher that answers while reporting itself unready, where it
  previously read `0`.

## [0.3.0] - 2026-07-30

The server is now written in Go, and covers argo-watcher's read-only API rather
than deployment history alone. Write operations remain deliberately unexposed.

Upgrading from `0.2.0` requires reading the **Changed** and **Removed** sections:
the `get_deployments` response shape changed, two deployment fields are gone, and
container images are published under fewer tags. `ARGO_WATCHER_URL` is unchanged
and remains the only required setting.

### Added

- Three new read-only tools: `get_deploy_lock` reports whether deployments are
  currently frozen, `get_reachability` reports whether argo-watcher can reach
  ArgoCD and its state backend, and `get_server_info` returns the upstream
  version and non-sensitive configuration.
- Filter deployment history by `status`, and page through it with `limit` and
  `offset`. Responses carry `total` (how many deployments matched the filter in
  full) and `truncated` (whether any remain beyond the current page).
- Deployments now report `is_rollback` and `rollback_target_id`, so a rollback is
  distinguishable from a forward deploy and names what it returned to.
- Every tool advertises itself as read-only over the protocol, letting clients
  skip write-confirmation prompts.
- OpenTelemetry traces and Prometheus metrics, served at `/metrics` and
  configured through `OTEL_ENABLED`, `OTEL_SERVICE_NAME`,
  `OTEL_EXPORTER_OTLP_ENDPOINT` and `OTEL_EXPORTER_OTLP_INSECURE`. Metrics
  include `argo_watcher_mcp_requests_total{result="success|invalid|failed"}` and
  `argo_watcher_reachable`.
- A stdio transport, selectable with `TRANSPORT_MODE`, for running the server
  directly under an MCP client instead of over HTTP. `0.2.0` served HTTP/SSE
  only, on a port fixed in the container image; the listen address is now set
  with `HTTP_LISTEN_ADDR`.
- `REQUEST_TIMEOUT` bounds outbound calls to argo-watcher.
- Published container images now carry a software bill of materials.
- Released binaries report their own release version to MCP clients, so
  `APP_VERSION` only needs setting to override it.

> [!NOTE]
> `get_reachability` needs argo-watcher's `/api/v1/reachability` endpoint, which
> is not in any stable argo-watcher release yet. Against a current stable
> upstream this tool returns an error; the rest are unaffected. See the README
> for the minimum argo-watcher version each feature requires.

### Changed

- **Breaking:** `get_deployments` returns an object —
  `{deployments, total, limit, offset, truncated}` — instead of a bare list of
  deployments. Clients reading the previous list must now read `.deployments`.
- **Breaking:** `get_deployments` returns 50 deployments by default, and at most
  1000, where it previously returned everything argo-watcher would serve for the
  requested window. Raise `limit` or advance `offset` for more, and check
  `truncated` before treating a page as complete.
- Rewritten from Python to Go, and distributed as a static binary in a distroless
  image. Configuration is compatible: `ARGO_WATCHER_URL` keeps its name and
  meaning.
- Container images are now multi-platform: `:v0.3.0` and `:latest` each resolve to
  `linux/amd64` or `linux/arm64` automatically. Earlier tags were amd64 only.

### Removed

- **Breaking:** the `validated` and `timeout` fields no longer appear on
  deployments. argo-watcher does not serialise either one, so both always read as
  their zero value — reporting them invited treating `validated: false` as fact.

### Fixed

- Deployment counts and aggregations could be silently wrong. argo-watcher caps
  its task endpoint at 1000 rows, and the reported total was discarded, so a
  window with more history than that returned a truncated page indistinguishable
  from a complete one. Responses now report `total` and `truncated`.
- MCP requests larger than 1 KiB were truncated before reaching the tool handler
  and failed as malformed JSON. Request bodies are now passed through intact at
  any size.

### Security

- `get_server_info` no longer exposes credentials embedded in URL-valued
  configuration. argo-watcher takes `ARGO_URL_ALIAS` and `DOCKER_IMAGES_PROXY`
  verbatim from the environment and redacts neither, so a registry proxy behind
  basic authentication had its credentials in the configuration payload. Any
  `user:password@` component is now stripped, including from schemeless values.
- `get_server_info` forwards an explicit allowlist of configuration fields rather
  than the whole payload, and reduces the OIDC, webhook and Mattermost sections
  to whether each is enabled. A field added upstream in future cannot reach a
  client without a deliberate change here.
- The HTTP transport now bounds how long a client may take to send its request
  headers, so connections cannot be held open indefinitely to exhaust capacity.
- Built with Go 1.26.5, which resolves 20 reachable standard-library
  vulnerabilities present in the toolchain previously used, spanning
  `crypto/tls`, `crypto/x509`, `net/http` and `net/textproto`.

[Unreleased]: https://github.com/shini4i/argo-watcher-mcp/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/shini4i/argo-watcher-mcp/compare/v0.2.0...v0.3.0
