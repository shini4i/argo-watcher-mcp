package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shini4i/argo-watcher-mcp/internal/clock"
	"github.com/shini4i/argo-watcher-mcp/internal/domain"
)

// Server wraps an MCP server instance and its tool registrations.
type Server struct {
	impl  *mcp.Server
	clock clock.Clock
	svc   domain.DeploymentService
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
}

// New constructs an MCP server with all tools registered.
func New(opts Options) (*Server, error) {
	if opts.Service == nil {
		return nil, fmt.Errorf("deployment service is required")
	}
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    opts.Name,
		Version: opts.Version,
	}, nil)

	handler := &getDeploymentsHandler{
		clock: opts.Clock,
		svc:   opts.Service,
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_deployments",
		Description: "Retrieve deployment tasks from Argo Watcher.",
	}, handler.Handle)

	return &Server{
		impl:  srv,
		clock: handler.clock,
		svc:   opts.Service,
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
	App         *string `json:"app,omitempty"`
	DaysHistory *int    `json:"days_history,omitempty"`
	FromUnix    *int64  `json:"from_timestamp,omitempty"`
	ToUnix      *int64  `json:"to_timestamp,omitempty"`
}

type getDeploymentsHandler struct {
	clock clock.Clock
	svc   domain.DeploymentService
}

// Handle executes the get_deployments tool logic.
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

	result, err := h.svc.ListDeployments(ctx, filter)
	if err != nil {
		return nil, nil, err
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

func ptrOf[T any](v T) *T {
	return &v
}
