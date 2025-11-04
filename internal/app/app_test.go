package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shini4i/argo-watcher-mcp/internal/clock"
	"github.com/shini4i/argo-watcher-mcp/internal/config"
	"github.com/shini4i/argo-watcher-mcp/internal/domain"
	"github.com/shini4i/argo-watcher-mcp/internal/mcpserver"
)

func TestNewUsesDefaultLogger(t *testing.T) {
	a := New(config.Config{}, nil)
	if a.logger != slog.Default() {
		t.Fatalf("expected slog.Default logger, got %#v", a.logger)
	}
}

func TestWithClockOverridesClock(t *testing.T) {
	a := New(config.Config{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	fakeClock := clock.FixedClock{At: time.Now()}
	a.WithClock(fakeClock)
	if _, ok := a.clock.(clock.FixedClock); !ok {
		t.Fatalf("expected clock to be FixedClock, got %T", a.clock)
	}
}

func TestWithClockNil(t *testing.T) {
	a := New(config.Config{}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	a.WithClock(nil)

	if _, ok := a.clock.(clock.SystemClock); !ok {
		t.Fatalf("expected clock to remain SystemClock, got %T", a.clock)
	}
}

type stubMCPServer struct {
	runCalled      bool
	runErr         error
	streamCalled   bool
	handlerToServe http.Handler
}

func (s *stubMCPServer) RunStdio(context.Context) error {
	s.runCalled = true
	return s.runErr
}

func (s *stubMCPServer) StreamableHandler() http.Handler {
	s.streamCalled = true
	if s.handlerToServe != nil {
		return s.handlerToServe
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
}

type stubHTTPServer struct {
	listenCalled   bool
	shutdownCalled bool
	listenErr      error
	shutdownErr    error
}

func (s *stubHTTPServer) ListenAndServe() error {
	s.listenCalled = true
	return s.listenErr
}

func (s *stubHTTPServer) Shutdown(context.Context) error {
	s.shutdownCalled = true
	return s.shutdownErr
}

func TestRunHTTPModeStartsHTTPServer(t *testing.T) {
	origNewMCP := newMCPServer
	origRouter := newHTTPRouter
	origHTTPServer := newHTTPServer
	t.Cleanup(func() {
		newMCPServer = origNewMCP
		newHTTPRouter = origRouter
		newHTTPServer = origHTTPServer
	})

	stubMCP := &stubMCPServer{runErr: context.Canceled}
	stubHTTP := &stubHTTPServer{listenErr: http.ErrServerClosed}

	newMCPServer = func(opts mcpserver.Options) (mcpRunner, error) {
		if opts.Service == nil {
			t.Fatal("expected service to be set")
		}
		return stubMCP, nil
	}

	var capturedHandler http.Handler
	newHTTPRouter = func(logger *slog.Logger, checker domain.HealthChecker, handler http.Handler, enable bool) http.Handler {
		if !enable {
			t.Fatal("expected HTTP mode to enable MCP handler")
		}
		capturedHandler = handler
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	}

	newHTTPServer = func(addr string, handler http.Handler) httpServer {
		return stubHTTP
	}

	a := New(config.Config{
		ArgoWatcherBaseURL: "https://example.com",
		HTTPListenAddr:     "127.0.0.1:0",
		TransportMode:      config.TransportModeHTTP,
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	a = a.WithClock(clock.FixedClock{At: time.Now()})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := a.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if stubMCP.runCalled {
		t.Fatal("expected stdio transport not to run in HTTP mode")
	}
	if !stubMCP.streamCalled {
		t.Fatal("expected StreamableHandler to be invoked")
	}
	if capturedHandler == nil {
		t.Fatal("expected handler to be passed to router")
	}
	if !stubHTTP.listenCalled {
		t.Fatal("expected HTTP server to listen")
	}
	if !stubHTTP.shutdownCalled {
		t.Fatal("expected HTTP server shutdown to be called")
	}
}

func TestRunStdioModeStartsOnlyStdio(t *testing.T) {
	stubMCP := &stubMCPServer{runErr: context.Canceled}
	stubHTTP := &stubHTTPServer{listenErr: http.ErrServerClosed}

	setupRunTest(t, stubMCP, stubHTTP)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	a := New(config.Config{
		ArgoWatcherBaseURL: "https://example.com",
		HTTPListenAddr:     "127.0.0.1:0",
		TransportMode:      config.TransportModeStdio,
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))

	if err := a.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !stubMCP.runCalled {
		t.Fatal("expected RunStdio to be invoked")
	}
	if stubMCP.streamCalled {
		t.Fatal("expected StreamableHandler not to be invoked in stdio mode")
	}
	if stubHTTP.listenCalled {
		t.Fatal("expected HTTP server not to listen in stdio mode")
	}
	if stubHTTP.shutdownCalled {
		t.Fatal("expected HTTP server shutdown not to be called in stdio mode")
	}
}

func TestRunPropagatesErrors(t *testing.T) {
	origNewMCP := newMCPServer
	t.Cleanup(func() { newMCPServer = origNewMCP })

	errBoom := errors.New("boom")
	newMCPServer = func(opts mcpserver.Options) (mcpRunner, error) {
		return nil, errBoom
	}
	a := New(config.Config{
		ArgoWatcherBaseURL: "https://example.com",
		TransportMode:      config.TransportModeStdio,
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))

	if err := a.Run(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("expected error %v, got %v", errBoom, err)
	}
}

func TestRunPropagatesStdioError(t *testing.T) {
	stubMCP := &stubMCPServer{runErr: errors.New("stdio failed")}
	stubHTTP := &stubHTTPServer{listenErr: http.ErrServerClosed}

	setupRunTest(t, stubMCP, stubHTTP)

	a := New(config.Config{
		ArgoWatcherBaseURL:  "https://example.com",
		TransportMode:       config.TransportModeStdio,
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))

	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stdio failed") {
		t.Fatalf("expected stdio failure, got %v", err)
	}
}

func TestRunPropagatesHTTPListenError(t *testing.T) {
	stubMCP := &stubMCPServer{runErr: context.Canceled}
	stubHTTP := &stubHTTPServer{listenErr: errors.New("listen failed")}

	setupRunTest(t, stubMCP, stubHTTP)

	a := New(config.Config{
		ArgoWatcherBaseURL: "https://example.com",
		TransportMode:      config.TransportModeHTTP,
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))

	err := a.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "listen failed") {
		t.Fatalf("expected listen failure, got %v", err)
	}
}

func TestRunPropagatesHTTPShutdownError(t *testing.T) {
	stubMCP := &stubMCPServer{runErr: context.Canceled}
	stubHTTP := &stubHTTPServer{
		listenErr:   http.ErrServerClosed,
		shutdownErr: errors.New("shutdown failed"),
	}

	setupRunTest(t, stubMCP, stubHTTP)

	a := New(config.Config{
		ArgoWatcherBaseURL: "https://example.com",
		TransportMode:      config.TransportModeHTTP,
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := a.Run(ctx)
	if err == nil || !errors.Is(err, stubHTTP.shutdownErr) {
		t.Fatalf("expected shutdown failure, got %v", err)
	}
}

func setupRunTest(t *testing.T, stubMCP *stubMCPServer, stubHTTP *stubHTTPServer) {
	origNewMCP := newMCPServer
	origRouter := newHTTPRouter
	origHTTPServer := newHTTPServer

	t.Cleanup(func() {
		newMCPServer = origNewMCP
		newHTTPRouter = origRouter
		newHTTPServer = origHTTPServer
	})

	newMCPServer = func(opts mcpserver.Options) (mcpRunner, error) {
		if opts.Service == nil {
			t.Fatal("expected service to be configured")
		}
		return stubMCP, nil
	}

	newHTTPRouter = func(logger *slog.Logger, checker domain.HealthChecker, handler http.Handler, enable bool) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	}

	newHTTPServer = func(addr string, handler http.Handler) httpServer {
		return stubHTTP
	}
}
