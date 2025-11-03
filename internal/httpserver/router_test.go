package httpserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubChecker struct {
	err error
}

func (s *stubChecker) Check(_ context.Context) error {
	return s.err
}

func TestHealthEndpoints(t *testing.T) {
	router := NewRouter(nil, &stubChecker{}, nil, false)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"status":"up"`) {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestReadinessFailures(t *testing.T) {
	router := NewRouter(nil, &stubChecker{err: errors.New("downstream failed")}, nil, false)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatalf("ready request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

func TestRouterWithMCPHandler(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	router := NewRouter(nil, &stubChecker{}, handler, true)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("mcp handler request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", resp.StatusCode)
	}
}

func TestRouterWithMCPHandlerDisabled(t *testing.T) {
	var called bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	})

	router := NewRouter(nil, &stubChecker{}, handler, false)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	if called {
		t.Fatalf("mcp handler should not be invoked when disabled")
	}
}

func TestRouterWithoutMCPHandler(t *testing.T) {
	router := NewRouter(nil, &stubChecker{}, nil, false)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/unknown")
	if err != nil {
		t.Fatalf("not found request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"error":"not found"`) {
		t.Fatalf("unexpected body: %s", string(body))
	}
}

func TestReadyzNilChecker(t *testing.T) {
	router := NewRouter(nil, nil, nil, false)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/readyz")
	if err != nil {
		t.Fatalf("ready request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
}

type failingWriter struct {
	status int
}

func (f *failingWriter) Header() http.Header { return make(http.Header) }
func (f *failingWriter) WriteHeader(status int) { f.status = status }
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteJSONErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(slog.New(slog.NewJSONHandler(io.Discard, nil)), rec, http.StatusOK, make(chan int))

	if rec.Result().StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Result().StatusCode)
	}

	rec2 := httptest.NewRecorder()
	writeJSON(nil, rec2, http.StatusCreated, map[string]string{"status": "ok"})
	if rec2.Result().StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", rec2.Result().StatusCode)
	}

	writer := &failingWriter{}
	writeJSON(slog.New(slog.NewJSONHandler(io.Discard, nil)), writer, http.StatusOK, map[string]string{"foo": "bar"})
	if writer.status != http.StatusOK {
		t.Fatalf("expected status written despite write failure, got %d", writer.status)
	}
}
