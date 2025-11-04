package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"golang.org/x/sync/errgroup"

	"github.com/shini4i/argo-watcher-mcp/internal/argowatcher"
	"github.com/shini4i/argo-watcher-mcp/internal/clock"
	"github.com/shini4i/argo-watcher-mcp/internal/config"
	"github.com/shini4i/argo-watcher-mcp/internal/httpserver"
	"github.com/shini4i/argo-watcher-mcp/internal/mcpserver"
	"github.com/shini4i/argo-watcher-mcp/internal/telemetry"
)

type mcpRunner interface {
	RunStdio(context.Context) error
	StreamableHandler() http.Handler
}

type httpServer interface {
	ListenAndServe() error
	Shutdown(context.Context) error
}

var (
	newMCPServer = func(opts mcpserver.Options) (mcpRunner, error) {
		return mcpserver.New(opts)
	}
	newHTTPRouter = httpserver.NewRouter
	newHTTPServer = func(addr string, handler http.Handler) httpServer {
		return &http.Server{
			Addr:    addr,
			Handler: handler,
		}
	}
)

// Application wires transports and dependencies for the MCP server.
type Application struct {
	cfg    config.Config
	logger *slog.Logger
	clock  clock.Clock
}

// New constructs an Application instance.
func New(cfg config.Config, logger *slog.Logger) *Application {
	if logger == nil {
		logger = slog.Default()
	}

	return &Application{
		cfg:    cfg,
		logger: logger,
		clock:  clock.SystemClock{},
	}
}

// WithClock allows overriding the clock, useful in tests.
func (a *Application) WithClock(c clock.Clock) *Application {
	if c != nil {
		a.clock = c
	}
	return a
}

// Run starts the MCP transports and HTTP server until the context is cancelled.
func (a *Application) Run(ctx context.Context) error {
	a.logger.Info("starting argo-watcher-mcp server",
		slog.String("transport_mode", a.cfg.TransportMode),
		slog.String("http_addr", a.cfg.HTTPListenAddr),
	)

	provider, err := telemetry.NewProvider(ctx, a.cfg, a.logger)
	if err != nil {
		return fmt.Errorf("init telemetry: %w", err)
	}
	if provider != nil {
		defer func() {
			if shutdownErr := provider.Shutdown(context.Background()); shutdownErr != nil {
				a.logger.Error("telemetry shutdown failed", "err", shutdownErr)
			}
		}()
	}

	httpClient := &http.Client{
		Timeout:   a.cfg.RequestTimeout,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
	defer httpClient.CloseIdleConnections()

	requestMetrics := telemetry.NoopMCPRequestMetrics()
	if metrics, err := telemetry.NewMCPRequestMetrics(); err != nil {
		a.logger.Error("init MCP request metrics", "err", err)
	} else {
		requestMetrics = metrics
	}

	reachabilityMetrics := telemetry.NoopArgoWatcherReachability()
	if tracker, err := telemetry.NewArgoWatcherReachability(); err != nil {
		a.logger.Error("init Argo Watcher reachability metrics", "err", err)
	} else {
		reachabilityMetrics = tracker
	}

	argoClient := argowatcher.New(
		a.cfg.ArgoWatcherBaseURL,
		httpClient,
		a.logger,
		argowatcher.WithReachabilityMetrics(reachabilityMetrics),
	)

	mcpSrv, err := newMCPServer(mcpserver.Options{
		Name:    a.cfg.Name,
		Version: a.cfg.Version,
		Service: argoClient,
		Clock:   a.clock,
		Logger:  a.logger,
		Metrics: requestMetrics,
	})
	if err != nil {
		return fmt.Errorf("create mcp server: %w", err)
	}

	group, groupCtx := errgroup.WithContext(ctx)

	switch a.cfg.TransportMode {
	case config.TransportModeStdio:
		group.Go(func() error {
			if err := mcpSrv.RunStdio(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("stdio transport: %w", err)
			}
			return nil
		})
	case config.TransportModeHTTP:
		handler := mcpSrv.StreamableHandler()
		var promHandler http.Handler
		if provider != nil {
			promHandler = provider.PrometheusHandler
		}
		router := newHTTPRouter(a.logger, argoClient, handler, true, promHandler)
		httpSrv := newHTTPServer(a.cfg.HTTPListenAddr, router)

		group.Go(func() error {
			err := httpSrv.ListenAndServe()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("http server: %w", err)
			}
			return nil
		})

		group.Go(func() error {
			<-groupCtx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return httpSrv.Shutdown(shutdownCtx)
		})
	default:
		return fmt.Errorf("unsupported transport mode: %s", a.cfg.TransportMode)
	}

	if err := group.Wait(); err != nil {
		return err
	}

	a.logger.Info("server shutdown complete")
	return nil
}
