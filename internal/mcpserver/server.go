package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shini4i/argo-watcher-mcp/internal/clock"
	"github.com/shini4i/argo-watcher-mcp/internal/domain"
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

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    opts.Name,
		Version: opts.Version,
	}, nil)

	handler := &getDeploymentsHandler{
		clock:  opts.Clock,
		svc:    opts.Service,
		logger: logger.With("tool", "get_deployments"),
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
	clock  clock.Clock
	svc    domain.DeploymentService
	logger *slog.Logger
}

// Handle executes the get_deployments tool logic and logs the processed request.
func (h *getDeploymentsHandler) Handle(ctx context.Context, _ *mcp.CallToolRequest, input getDeploymentsInput) (*mcp.CallToolResult, any, error) {
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
			return nil, nil, fmt.Errorf("days_history must be non-negative")
		}
		from = *to - int64(days)*24*60*60
	}

	if from > *to {
		return nil, nil, fmt.Errorf("from_timestamp cannot be greater than to_timestamp")
	}

	filter := domain.DeploymentFilter{
		App:           input.App,
		FromTimestamp: from,
		ToTimestamp:   *to,
	}

	if h.logger != nil {
		h.logger.LogAttrs(ctx, slog.LevelInfo, "get_deployments request", h.requestAttrs(filter)...)
	}

	result, err := h.svc.ListDeployments(ctx, filter)
	if err != nil {
		if h.logger != nil {
			attrs := append(h.requestAttrs(filter), slog.Any("error", err))
			h.logger.LogAttrs(ctx, slog.LevelError, "get_deployments failed", attrs...)
		}
		return nil, nil, err
	}

	if h.logger != nil {
		attrs := append(h.requestAttrs(filter), slog.Int("count", len(result)))
		h.logger.LogAttrs(ctx, slog.LevelInfo, "get_deployments completed", attrs...)
	}

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

func ptrOf[T any](v T) *T {
	return &v
}
