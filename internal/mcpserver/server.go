package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/shini4i/argo-watcher-mcp/internal/clock"
	"github.com/shini4i/argo-watcher-mcp/internal/domain"
	"github.com/shini4i/argo-watcher-mcp/internal/httpserver"
	"github.com/shini4i/argo-watcher-mcp/internal/telemetry"
)

const tracerName = "github.com/shini4i/argo-watcher-mcp/mcpserver"

// defaultDeploymentLimit bounds how many deployments a get_deployments call
// returns when the caller does not ask for a specific page size. Argo Watcher
// itself would return up to maxDeploymentLimit rows, which is far more history
// than a model needs to answer a typical question and would crowd out its
// context. Callers that need more can raise the limit or page through.
const defaultDeploymentLimit = 50

// maxDeploymentLimit mirrors the page-size cap Argo Watcher enforces on
// /api/v1/tasks. Requests above it are rejected here rather than silently
// clamped upstream, so the caller learns the real ceiling.
const maxDeploymentLimit = 1000

// readOnly marks every tool this server exposes. The server wraps only GET
// endpoints, and declaring that in the protocol lets hosts skip write
// confirmation prompts — and makes a future write tool stand out as the one
// registration that had to opt out.
var readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true}

// Server wraps an MCP server instance and its tool registrations.
type Server struct {
	impl   *mcp.Server
	clock  clock.Clock
	svc    domain.ArgoWatcher
	logger *slog.Logger
}

// Options configure the server.
type Options struct {
	// Name identifies the MCP implementation exposed to clients.
	Name string
	// Version advertises the semantic version reported via MCP metadata.
	Version string
	// Service provides the read-only Argo Watcher data backing every tool.
	Service domain.ArgoWatcher
	// Clock supplies time readings for defaulting request ranges. When unset the
	// system clock is used.
	Clock clock.Clock
	// Logger records tool activity. When nil, slog.Default is used.
	Logger *slog.Logger
	// Metrics records MCP tool request outcomes. Defaults to a no-op recorder.
	Metrics telemetry.MCPRequestMetrics
}

// New constructs an MCP server with all tools registered.
func New(opts Options) (*Server, error) {
	if opts.Service == nil {
		return nil, fmt.Errorf("deployment service is required")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "mcpserver")

	metrics := opts.Metrics
	if metrics == nil {
		metrics = telemetry.NoopMCPRequestMetrics()
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    opts.Name,
		Version: opts.Version,
	}, nil)

	deployments := &getDeploymentsHandler{
		clock:   opts.Clock,
		svc:     opts.Service,
		logger:  logger.With("tool", "get_deployments"),
		metrics: metrics,
	}

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: readOnly,
		Name:        "get_deployments",
		Description: "Search Argo Watcher deployment history. Answers what was deployed, " +
			"when, by whom, with which image tags, and whether it succeeded, failed, or was " +
			"rolled back. Results are ordered newest first.\n\n" +
			"Results are paginated. `total` reports how many deployments matched the filter " +
			"in full, ignoring pagination, and `truncated` is true when deployments remain " +
			"after this page — advance `offset` or raise `limit` to reach them. Never treat " +
			"a truncated page as the complete history when counting or aggregating.\n\n" +
			"An empty result is ambiguous: Argo Watcher also answers with an empty list when " +
			"its database is unreachable. Before concluding that nothing was deployed in a " +
			"window, call get_reachability to rule out an outage.\n\n" +
			"Argo Watcher supports filtering only by application and status. To answer a " +
			"question about a specific author, fetch the relevant window and filter the " +
			"results yourself, checking `truncated` first.",
	}, deployments.Handle)

	deployLock := &getDeployLockHandler{
		svc:     opts.Service,
		logger:  logger.With("tool", "get_deploy_lock"),
		metrics: metrics,
	}

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: readOnly,
		Name:        "get_deploy_lock",
		Description: "Report whether Argo Watcher is currently refusing new deployments. " +
			"The lock may have been set manually or by a scheduled lockdown window. Use this " +
			"to explain why deployments are being rejected right now.",
	}, deployLock.Handle)

	reachability := &getReachabilityHandler{
		svc:     opts.Service,
		logger:  logger.With("tool", "get_reachability"),
		metrics: metrics,
	}

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: readOnly,
		Name:        "get_reachability",
		Description: "Report whether Argo Watcher can currently reach ArgoCD and its state " +
			"backend. When `available` is false, `reason` names the unreachable subsystem. " +
			"Use this to distinguish an outage from an absence of deployments.",
	}, reachability.Handle)

	serverInfo := &getServerInfoHandler{
		svc:     opts.Service,
		logger:  logger.With("tool", "get_server_info"),
		metrics: metrics,
	}

	mcp.AddTool(srv, &mcp.Tool{
		Annotations: readOnly,
		Name:        "get_server_info",
		Description: "Return the upstream Argo Watcher version and its non-sensitive " +
			"configuration: the ArgoCD URL, deployment timeout, state backend type, " +
			"scheduled lockdown window, and which optional integrations (SSO, webhook " +
			"and Mattermost notifications) are enabled. Useful for building links to " +
			"ArgoCD and for explaining instance behaviour. Secrets and tokens are never " +
			"included, and integration endpoints are not reported — only whether each " +
			"is turned on.",
	}, serverInfo.Handle)

	return &Server{
		impl:   srv,
		clock:  deployments.clock,
		svc:    opts.Service,
		logger: logger,
	}, nil
}

// MCP returns the underlying server instance.
func (s *Server) MCP() *mcp.Server {
	return s.impl
}

// RunStdio serves the MCP server over STDIO.
func (s *Server) RunStdio(ctx context.Context) error {
	return s.impl.Run(ctx, &mcp.StdioTransport{})
}

// StreamableHandler returns an HTTP handler for SSE/Streamable transport.
func (s *Server) StreamableHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server {
		return s.impl
	}, nil)
}

// startToolSpan continues the caller's trace when the MCP request carried
// propagation headers, and otherwise starts a fresh server span for the tool
// call. The returned func ends the span only when this call created it.
func startToolSpan(ctx context.Context, req *mcp.CallToolRequest, name string) (context.Context, trace.Span, func()) {
	if sc := trace.SpanContextFromContext(ctx); !sc.IsValid() && req != nil && req.Extra != nil && req.Extra.Header != nil {
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(req.Extra.Header))
	}

	span := trace.SpanFromContext(ctx)
	if span.IsRecording() {
		return ctx, span, func() {}
	}

	ctx, span = otel.Tracer(tracerName).Start(ctx, name, trace.WithSpanKind(trace.SpanKindServer))
	return ctx, span, func() { span.End() }
}

type getDeploymentsInput struct {
	// Application name to filter by. Must match exactly; Argo Watcher does not
	// substring-match. Omit to include every application.
	App *string `json:"app,omitempty"`

	// Deployment status to filter by. Must be one of: "in progress", "deployed",
	// "failed", "cancelled", "aborted", "accepted", "app not found",
	// "argocd is unavailable", "failed to login to argocd",
	// "cannot connect to database". Matching is case-sensitive and lowercase, so
	// "Deployed" is not accepted. Argo Watcher rejects any other value with an
	// error rather than returning an empty result. Omit to include every status.
	Status *string `json:"status,omitempty"`

	// How many days of history to search. Defaults to 30 when no explicit start is provided.
	// Ignored if `from_timestamp` is supplied. Must be non-negative.
	DaysHistory *int `json:"days_history,omitempty"`

	// The start of the time range (Unix timestamp).
	// If provided, overrides `days_history`.
	FromUnix *int64 `json:"from_timestamp,omitempty"`

	// The end of the time range (Unix timestamp).
	// Defaults to the current time.
	ToUnix *int64 `json:"to_timestamp,omitempty"`

	// Maximum number of deployments to return, newest first. Defaults to 50 and
	// may not exceed 1000. Check `truncated` in the response before treating a
	// page as the whole result set.
	Limit *int `json:"limit,omitempty"`

	// Number of matching deployments to skip before returning results, newest
	// first. Use with `limit` to page through history. Must be non-negative.
	Offset *int `json:"offset,omitempty"`
}

type getDeploymentsHandler struct {
	clock   clock.Clock
	svc     domain.ArgoWatcher
	logger  *slog.Logger
	metrics telemetry.MCPRequestMetrics
}

// Handle executes the get_deployments tool logic, emits tracing spans, and logs the processed request.
func (h *getDeploymentsHandler) Handle(ctx context.Context, req *mcp.CallToolRequest, input getDeploymentsInput) (*mcp.CallToolResult, any, error) {
	ctx, span, end := startToolSpan(ctx, req, "tool.get_deployments")
	defer end()

	logger := loggerFromContext(ctx, h.logger)
	now := h.nowUnix()

	invalid := func(err error) (*mcp.CallToolResult, any, error) {
		h.metrics.RecordInvalid(ctx)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, nil, err
	}

	to := input.ToUnix
	if to == nil {
		to = ptrOf(now)
	}

	var from int64
	switch {
	case input.FromUnix != nil:
		from = *input.FromUnix
	default:
		days := 30
		if input.DaysHistory != nil {
			days = *input.DaysHistory
		}
		if days < 0 {
			return invalid(fmt.Errorf("days_history must be non-negative"))
		}
		from = *to - int64(days)*24*60*60
	}

	if from > *to {
		return invalid(fmt.Errorf("from_timestamp cannot be greater than to_timestamp"))
	}

	limit := defaultDeploymentLimit
	if input.Limit != nil {
		limit = *input.Limit
		if limit <= 0 {
			return invalid(fmt.Errorf("limit must be positive"))
		}
		if limit > maxDeploymentLimit {
			return invalid(fmt.Errorf("limit must not exceed %d", maxDeploymentLimit))
		}
	}

	offset := 0
	if input.Offset != nil {
		offset = *input.Offset
		if offset < 0 {
			return invalid(fmt.Errorf("offset must be non-negative"))
		}
	}

	filter := domain.DeploymentFilter{
		App:           input.App,
		Status:        input.Status,
		FromTimestamp: from,
		ToTimestamp:   *to,
		Limit:         limit,
		Offset:        offset,
	}

	if logger != nil {
		logger.LogAttrs(ctx, slog.LevelInfo, "get_deployments request", h.requestAttrs(filter)...)
	}
	span.SetAttributes(
		attribute.Int64("argo_watcher.from_timestamp", filter.FromTimestamp),
		attribute.Int64("argo_watcher.to_timestamp", filter.ToTimestamp),
		attribute.Int("argo_watcher.limit", filter.Limit),
		attribute.Int("argo_watcher.offset", filter.Offset),
	)
	if filter.App != nil {
		span.SetAttributes(attribute.String("argo_watcher.app", *filter.App))
	}
	if filter.Status != nil {
		span.SetAttributes(attribute.String("argo_watcher.status", *filter.Status))
	}

	page, err := h.svc.ListDeployments(ctx, filter)
	if err != nil {
		if logger != nil {
			attrs := append(h.requestAttrs(filter), slog.Any("error", err))
			logger.LogAttrs(ctx, slog.LevelError, "get_deployments failed", attrs...)
		}
		h.metrics.RecordFailure(ctx)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, nil, err
	}

	if logger != nil {
		attrs := append(h.requestAttrs(filter),
			slog.Int("count", len(page.Deployments)),
			slog.Int64("total", page.Total),
			slog.Bool("truncated", page.Truncated),
		)
		logger.LogAttrs(ctx, slog.LevelInfo, "get_deployments completed", attrs...)
	}
	h.metrics.RecordSuccess(ctx)
	span.SetAttributes(
		attribute.Int("argo_watcher.result_count", len(page.Deployments)),
		attribute.Int64("argo_watcher.total", page.Total),
		attribute.Bool("argo_watcher.truncated", page.Truncated),
	)
	span.SetStatus(codes.Ok, "completed")

	return nil, page, nil
}

func (h *getDeploymentsHandler) nowUnix() int64 {
	switch {
	case h.clock != nil:
		return h.clock.Now().Unix()
	default:
		return time.Now().UTC().Unix()
	}
}

func (h *getDeploymentsHandler) requestAttrs(filter domain.DeploymentFilter) []slog.Attr {
	attrs := []slog.Attr{
		slog.Int64("from_timestamp", filter.FromTimestamp),
		slog.Int64("to_timestamp", filter.ToTimestamp),
		slog.Int("limit", filter.Limit),
		slog.Int("offset", filter.Offset),
	}
	if filter.App != nil {
		attrs = append(attrs, slog.String("app", *filter.App))
	}
	if filter.Status != nil {
		attrs = append(attrs, slog.String("status", *filter.Status))
	}
	return attrs
}

// noInput is the argument type for tools that take no parameters. The MCP SDK
// requires a struct or map so the inferred schema is a JSON object.
type noInput struct{}

type getDeployLockHandler struct {
	svc     domain.ArgoWatcher
	logger  *slog.Logger
	metrics telemetry.MCPRequestMetrics
}

// Handle executes the get_deploy_lock tool, reporting whether deployments are
// currently frozen.
func (h *getDeployLockHandler) Handle(ctx context.Context, req *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	ctx, span, end := startToolSpan(ctx, req, "tool.get_deploy_lock")
	defer end()

	logger := loggerFromContext(ctx, h.logger)

	state, err := h.svc.GetDeployLock(ctx)
	if err != nil {
		if logger != nil {
			logger.LogAttrs(ctx, slog.LevelError, "get_deploy_lock failed", slog.Any("error", err))
		}
		h.metrics.RecordFailure(ctx)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, nil, err
	}

	if logger != nil {
		logger.LogAttrs(ctx, slog.LevelInfo, "get_deploy_lock completed", slog.Bool("locked", state.Locked))
	}
	h.metrics.RecordSuccess(ctx)
	span.SetAttributes(attribute.Bool("argo_watcher.deploy_locked", state.Locked))
	span.SetStatus(codes.Ok, "completed")

	return nil, state, nil
}

type getReachabilityHandler struct {
	svc     domain.ArgoWatcher
	logger  *slog.Logger
	metrics telemetry.MCPRequestMetrics
}

// Handle executes the get_reachability tool, reporting whether Argo Watcher can
// reach its own dependencies.
func (h *getReachabilityHandler) Handle(ctx context.Context, req *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	ctx, span, end := startToolSpan(ctx, req, "tool.get_reachability")
	defer end()

	logger := loggerFromContext(ctx, h.logger)

	reachability, err := h.svc.GetReachability(ctx)
	if err != nil {
		if logger != nil {
			logger.LogAttrs(ctx, slog.LevelError, "get_reachability failed", slog.Any("error", err))
		}
		h.metrics.RecordFailure(ctx)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, nil, err
	}

	if logger != nil {
		logger.LogAttrs(ctx, slog.LevelInfo, "get_reachability completed",
			slog.Bool("available", reachability.Available),
			slog.String("reason", reachability.Reason),
		)
	}
	h.metrics.RecordSuccess(ctx)
	span.SetAttributes(attribute.Bool("argo_watcher.available", reachability.Available))
	span.SetStatus(codes.Ok, "completed")

	return nil, reachability, nil
}

type getServerInfoHandler struct {
	svc     domain.ArgoWatcher
	logger  *slog.Logger
	metrics telemetry.MCPRequestMetrics
}

// Handle executes the get_server_info tool, returning the upstream version and
// non-sensitive configuration.
func (h *getServerInfoHandler) Handle(ctx context.Context, req *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	ctx, span, end := startToolSpan(ctx, req, "tool.get_server_info")
	defer end()

	logger := loggerFromContext(ctx, h.logger)

	info, err := h.svc.GetServerInfo(ctx)
	if err != nil {
		if logger != nil {
			logger.LogAttrs(ctx, slog.LevelError, "get_server_info failed", slog.Any("error", err))
		}
		h.metrics.RecordFailure(ctx)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, nil, err
	}

	if logger != nil {
		logger.LogAttrs(ctx, slog.LevelInfo, "get_server_info completed", slog.String("version", info.Version))
	}
	h.metrics.RecordSuccess(ctx)
	span.SetAttributes(attribute.String("argo_watcher.version", info.Version))
	span.SetStatus(codes.Ok, "completed")

	return nil, info, nil
}

func loggerFromContext(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	return httpserver.LoggerFromContext(ctx, fallback)
}

func ptrOf[T any](v T) *T {
	return &v
}
