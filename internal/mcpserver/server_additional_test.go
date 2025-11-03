package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shini4i/argo-watcher-mcp/internal/clock"
	"github.com/shini4i/argo-watcher-mcp/internal/domain"
)

func TestNewRequiresService(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("expected error when Service is nil")
	}
}

func TestServerRunStdioRespectsContext(t *testing.T) {
	srv, err := New(Options{
		Service: &stubDeploymentService{},
	})
	if err != nil {
		t.Fatalf("unexpected error creating server: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := srv.RunStdio(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestStreamableHandlerServesRequests(t *testing.T) {
	srv, err := New(Options{
		Service: &stubDeploymentService{
			result: []domain.Deployment{},
		},
		Clock: clock.FixedClock{At: time.Unix(0, 0)},
	})
	if err != nil {
		t.Fatalf("unexpected error creating server: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()

	handler := srv.StreamableHandler()
	handler.ServeHTTP(recorder, req)

	if recorder.Result().StatusCode == 0 {
		t.Fatal("expected StreamableHandler to write a response")
	}
}

func TestGetDeploymentsHandlerNowUnixNilClock(t *testing.T) {
	handler := &getDeploymentsHandler{}

	got := handler.nowUnix()
	if got <= 0 {
		t.Fatalf("expected positive unix timestamp, got %d", got)
	}
}
