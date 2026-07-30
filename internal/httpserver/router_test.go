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

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type stubChecker struct {
	err error
}

func (s *stubChecker) Check(_ context.Context) error {
	return s.err
}

func TestHealthEndpoints(t *testing.T) {
	router := NewRouter(nil, &stubChecker{}, nil, false, nil)
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
	router := NewRouter(nil, &stubChecker{err: errors.New("downstream failed")}, nil, false, nil)
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

	router := NewRouter(nil, &stubChecker{}, handler, true, nil)
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

	router := NewRouter(nil, &stubChecker{}, handler, false, nil)
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
	router := NewRouter(nil, &stubChecker{}, nil, false, nil)
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

func TestMetricsEndpointWithPrometheusHandler(t *testing.T) {
	promHandler := promhttp.Handler()
	router := NewRouter(nil, &stubChecker{}, nil, false, promHandler)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := http.Get(server.URL + "/metrics")
	if err != nil {
		t.Fatalf("metrics request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed reading metrics body: %v", err)
	}
	if !strings.Contains(string(body), "# HELP") {
		t.Fatalf("expected Prometheus exposition format, got: %s", string(body))
	}
}

func TestReadyzNilChecker(t *testing.T) {
	router := NewRouter(nil, nil, nil, false, nil)
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

func (f *failingWriter) Header() http.Header       { return make(http.Header) }
func (f *failingWriter) WriteHeader(status int)    { f.status = status }
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteJSONErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(context.Background(), slog.New(slog.NewJSONHandler(io.Discard, nil)), rec, http.StatusOK, make(chan int))

	if rec.Result().StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Result().StatusCode)
	}

	rec2 := httptest.NewRecorder()
	writeJSON(context.Background(), nil, rec2, http.StatusCreated, map[string]string{"status": "ok"})
	if rec2.Result().StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", rec2.Result().StatusCode)
	}

	writer := &failingWriter{}
	writeJSON(context.Background(), slog.New(slog.NewJSONHandler(io.Discard, nil)), writer, http.StatusOK, map[string]string{"foo": "bar"})
	if writer.status != http.StatusOK {
		t.Fatalf("expected status written despite write failure, got %d", writer.status)
	}
}

func TestShouldTraceRequest(t *testing.T) {
	testCases := []struct {
		name        string
		method      string
		path        string
		accept      string
		contentType string
		body        string
		want        bool
	}{
		{name: "metricsExcluded", method: http.MethodGet, path: "/metrics", want: false},
		{name: "healthzExcluded", method: http.MethodGet, path: "/healthz", want: false},
		{name: "readyzExcluded", method: http.MethodGet, path: "/readyz", want: false},
		{name: "sseStreamExcluded", method: http.MethodGet, path: "/", accept: "text/event-stream", want: false},
		{name: "plainGetTraced", method: http.MethodGet, path: "/", want: true},

		// Handshake methods carry no application work.
		{name: "initialize", method: http.MethodPost, path: "/", contentType: "application/json", body: `{"method":"initialize"}`, want: false},
		{name: "notificationsInitialized", method: http.MethodPost, path: "/", contentType: "application/json", body: `{"method":"notifications/initialized"}`, want: false},
		{name: "serverDiscover", method: http.MethodPost, path: "/", contentType: "application/json", body: `{"method":"server/discover"}`, want: false},
		{name: "subscriptionsListen", method: http.MethodPost, path: "/", contentType: "application/json", body: `{"method":"subscriptions/listen"}`, want: false},
		{name: "toolsList", method: http.MethodPost, path: "/", contentType: "application/json", body: `{"method":"tools/list"}`, want: false},
		{name: "ping", method: http.MethodPost, path: "/", contentType: "application/json", body: `{"method":"ping"}`, want: false},

		// Case is normalised before the lookup.
		{name: "mixedCaseHandshake", method: http.MethodPost, path: "/", contentType: "application/json", body: `{"method":"Server/Discover"}`, want: false},

		// Real work, and anything unrecognised, stays traced.
		{name: "toolsCallTraced", method: http.MethodPost, path: "/", contentType: "application/json", body: `{"method":"tools/call"}`, want: true},
		{name: "emptyMethodTraced", method: http.MethodPost, path: "/", contentType: "application/json", body: `{"method":""}`, want: true},
		{name: "malformedBodyTraced", method: http.MethodPost, path: "/", contentType: "application/json", body: `{not json`, want: true},
		{name: "nonJSONPostTraced", method: http.MethodPost, path: "/", contentType: "text/plain", body: "hello", want: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}

			if got := shouldTraceRequest(req); got != tc.want {
				t.Fatalf("shouldTraceRequest() = %v, want %v", got, tc.want)
			}
		})
	}
}

// shouldTraceRequest consumes the request body to read the JSON-RPC method and
// must put it back intact, at any size. If this regresses, MCP POSTs reach the
// handler with a drained or truncated body and the server stops answering while
// every other test still passes.
func TestShouldTraceRequestRestoresBody(t *testing.T) {
	testCases := []struct {
		name string
		body string
	}{
		{
			name: "smallBody",
			body: `{"method":"tools/call","params":{"name":"get_deployments"}}`,
		},
		{
			// Larger than the filter's 1 KiB inspection window. Protocol
			// 2026-07-28 carries clientCapabilities in every request's _meta, so
			// real requests do reach this size.
			name: "bodyLargerThanInspectionWindow",
			body: `{"method":"tools/call","params":{"name":"get_deployments","padding":"` +
				strings.Repeat("x", 2048) + `"}}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")

			shouldTraceRequest(req)

			replayed, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body after filter: %v", err)
			}
			if string(replayed) != tc.body {
				t.Fatalf("body was not restored intact: got %d bytes, want %d",
					len(replayed), len(tc.body))
			}
		})
	}
}
