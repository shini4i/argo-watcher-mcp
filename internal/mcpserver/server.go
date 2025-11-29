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

// Server wraps an MCP server instance and its tool registrations.
type Server struct {
	impl   *mcp.Server
	clock  clock.Clock
	svc    domain.DeploymentService
	logger *slog.Logger
}

// Options configure the server.
type Options struct {
	// Name identifies the MCP implementation exposed to clients.
	Name string
	// Version advertises the semantic version reported via MCP metadata.
	Version string
	// Service fetches deployment data backing the MCP tool responses.
	Service domain.DeploymentService
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

	handler := &getDeploymentsHandler{
		clock:   opts.Clock,
		svc:     opts.Service,
		logger:  logger.With("tool", "get_deployments"),
		metrics: metrics,
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_deployments",
		Description: "Retrieve deployment tasks from Argo Watcher.",
	}, handler.Handle)

	return &Server{
		impl:   srv,
		clock:  handler.clock,
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

type getDeploymentsInput struct {
	// The name of the application to filter by. Optional.
	App *string `json:"app,omitempty"`

	// How many days of history to search. Defaults to 30 when no explicit start is provided.
	// Ignored if `from_timestamp` is supplied. Must be non-negative.
	DaysHistory *int `json:"days_history,omitempty"`

	// The start of the time range (Unix timestamp).
	// If provided, overrides `days_history`.
	FromUnix *int64 `json:"from_timestamp,omitempty"`

	// The end of the time range (Unix timestamp).
	// Defaults to the current time.
	ToUnix *int64 `json:"to_timestamp,omitempty"`
}

type getDeploymentsHandler struct {
	clock   clock.Clock
	svc     domain.DeploymentService
	logger  *slog.Logger
	metrics telemetry.MCPRequestMetrics
}

// Handle executes the get_deployments tool logic, emits tracing spans, and logs the processed request.
func (h *getDeploymentsHandler) Handle(ctx context.Context, req *mcp.CallToolRequest, input getDeploymentsInput) (*mcp.CallToolResult, any, error) {
	if sc := trace.SpanContextFromContext(ctx); !sc.IsValid() && req != nil && req.Extra != nil && req.Extra.Header != nil {
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(req.Extra.Header))
	}
	span := trace.SpanFromContext(ctx)
	createdSpan := false
	if !span.IsRecording() {
		ctx, span = otel.Tracer("github.com/shini4i/argo-watcher-mcp/mcpserver").Start(ctx, "tool.get_deployments",
			trace.WithSpanKind(trace.SpanKindServer))
		createdSpan = true
	}
	if createdSpan {
		defer span.End()
	}

	logger := loggerFromContext(ctx, h.logger)
	now := h.nowUnix()

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
			h.metrics.RecordInvalid(ctx)
			err := fmt.Errorf("days_history must be non-negative")
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, nil, err
		}
		from = *to - int64(days)*24*60*60
	}

	if from > *to {
		h.metrics.RecordInvalid(ctx)
		err := fmt.Errorf("from_timestamp cannot be greater than to_timestamp")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, nil, err
	}

	filter := domain.DeploymentFilter{
		App:           input.App,
		FromTimestamp: from,
		ToTimestamp:   *to,
	}

	if logger != nil {
		logger.LogAttrs(ctx, slog.LevelInfo, "get_deployments request", h.requestAttrs(filter)...)
	}
	span.SetAttributes(
		attribute.Int64("argo_watcher.from_timestamp", filter.FromTimestamp),
		attribute.Int64("argo_watcher.to_timestamp", filter.ToTimestamp),
	)
	if filter.App != nil {
		span.SetAttributes(attribute.String("argo_watcher.app", *filter.App))
	}

	result, err := h.svc.ListDeployments(ctx, filter)
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
		attrs := append(h.requestAttrs(filter), slog.Int("count", len(result)))
		logger.LogAttrs(ctx, slog.LevelInfo, "get_deployments completed", attrs...)
	}
	h.metrics.RecordSuccess(ctx)
	span.SetAttributes(attribute.Int("argo_watcher.result_count", len(result)))
	span.SetStatus(codes.Ok, "completed")

	return nil, result, nil
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
	}
	if filter.App != nil {
		attrs = append(attrs, slog.String("app", *filter.App))
	}
	return attrs
}

func loggerFromContext(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	return httpserver.LoggerFromContext(ctx, fallback)
}

func ptrOf[T any](v T) *T {
	return &v
}
