package argowatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/shini4i/argo-watcher-mcp/internal/domain"
)

// Assertions inside httptest handlers use t.Errorf, never t.Fatalf: the handler
// runs on the server's goroutine, and t.Fatalf calls runtime.Goexit there, which
// aborts the handler instead of the test. The client then sees a truncated
// response and fails for an unrelated-looking reason. Errorf records the real
// failure and lets the exchange finish.

type mockHTTPClient struct {
	resp    *http.Response
	respErr error
}

func (m *mockHTTPClient) Do(*http.Request) (*http.Response, error) {
	if m.resp != nil {
		return m.resp, nil
	}
	return nil, m.respErr
}

// sequencedHTTPClient serves responses in order, then fails every later call.
type sequencedHTTPClient struct {
	responses []*http.Response
	err       error
	calls     int
}

func (s *sequencedHTTPClient) Do(*http.Request) (*http.Response, error) {
	defer func() { s.calls++ }()

	if s.calls < len(s.responses) {
		return s.responses[s.calls], nil
	}

	return nil, s.err
}

type trackingReachability struct {
	reachable   int
	unreachable int
}

func (t *trackingReachability) ReportReachable() {
	t.reachable++
}

func (t *trackingReachability) ReportUnreachable() {
	t.unreachable++
}

// probeServer answers Argo Watcher's probe endpoints from a table and records
// the paths asked for. An absent path answers 404.
type probeServer struct {
	responses map[string]probeResponse
	requested []string
}

type probeResponse struct {
	status int
	body   string
}

func newProbeServer(t *testing.T, responses map[string]probeResponse) (*probeServer, *Client, *trackingReachability) {
	t.Helper()

	probes := &probeServer{responses: responses}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.requested = append(probes.requested, r.URL.Path)

		response, known := probes.responses[r.URL.Path]
		if !known {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.WriteHeader(response.status)
		if response.body != "" {
			_, _ = w.Write([]byte(response.body))
		}
	}))
	t.Cleanup(srv.Close)

	metrics := &trackingReachability{}
	return probes, New(srv.URL, srv.Client(), nil, WithReachabilityMetrics(metrics)), metrics
}

func TestCheckSuccess(t *testing.T) {
	probes, client, metrics := newProbeServer(t, map[string]probeResponse{
		"/livez":  {status: http.StatusOK, body: `{"status":"up"}`},
		"/readyz": {status: http.StatusOK, body: `{"status":"up"}`},
	})

	health, err := client.Check(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !health.Ready {
		t.Fatalf("expected upstream reported ready, got %+v", health)
	}
	if health.Reason != "" {
		t.Fatalf("expected no reason on a ready upstream, got %q", health.Reason)
	}
	if got := strings.Join(probes.requested, ","); got != "/livez,/readyz" {
		t.Fatalf("expected /livez then /readyz, got %s", got)
	}
	if metrics.reachable != 1 || metrics.unreachable != 0 {
		t.Fatalf("expected reachability metrics reachable=1 unreachable=0, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

func TestCheckUnreadyUpstreamStillPasses(t *testing.T) {
	_, client, metrics := newProbeServer(t, map[string]probeResponse{
		"/livez":  {status: http.StatusOK, body: `{"status":"up"}`},
		"/readyz": {status: http.StatusServiceUnavailable, body: `{"status":"down","reason":"state backend unreachable"}`},
	})

	health, err := client.Check(context.Background())
	if err != nil {
		t.Fatalf("an unready upstream must not fail the check, got %v", err)
	}
	if health.Ready {
		t.Fatal("expected upstream reported unready")
	}
	if health.Reason != "state backend unreachable" {
		t.Fatalf("expected Argo Watcher's own reason verbatim, got %q", health.Reason)
	}
	// The gauge tracks reachability, not readiness: both probes answered.
	if metrics.reachable != 1 || metrics.unreachable != 0 {
		t.Fatalf("expected reachability metrics reachable=1 unreachable=0, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

func TestCheckUnreadyWithoutReasonFallsBackToStatus(t *testing.T) {
	_, client, _ := newProbeServer(t, map[string]probeResponse{
		"/livez":  {status: http.StatusOK, body: `{"status":"up"}`},
		"/readyz": {status: http.StatusServiceUnavailable, body: `{"status":"down"}`},
	})

	health, err := client.Check(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if health.Ready || health.Reason != "status 503" {
		t.Fatalf("expected unready with a status fallback reason, got %+v", health)
	}
}

func TestCheckLivenessReportsDown(t *testing.T) {
	_, client, metrics := newProbeServer(t, map[string]probeResponse{
		"/livez": {status: http.StatusServiceUnavailable, body: `{"status":"down","reason":"shutting down"}`},
	})

	_, err := client.Check(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("expected the error to carry Argo Watcher's reason, got %v", err)
	}
	if metrics.reachable != 0 || metrics.unreachable != 1 {
		t.Fatalf("expected reachability metrics reachable=0 unreachable=1, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

// A pre-split Argo Watcher answers /livez from the catch-all serving its Web UI.
func TestCheckRejectsTheWebUIShell(t *testing.T) {
	probes, client, metrics := newProbeServer(t, map[string]probeResponse{
		"/livez": {status: http.StatusOK, body: "<!doctype html><title>Argo Watcher</title>"},
	})

	_, err := client.Check(context.Background())
	if err == nil {
		t.Fatal("expected an error when /livez answers with something other than a probe payload")
	}
	if !strings.Contains(err.Error(), "no probe payload") {
		t.Fatalf("expected the error to name the missing payload, got %v", err)
	}
	if got := strings.Join(probes.requested, ","); got != "/livez" {
		t.Fatalf("expected /readyz not to be consulted, got %s", got)
	}
	if metrics.reachable != 0 || metrics.unreachable != 1 {
		t.Fatalf("expected reachability metrics reachable=0 unreachable=1, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

// Serving no probe endpoint means ARGO_WATCHER_URL points somewhere else, which
// must be diagnosed by its status rather than blamed on the upstream version.
func TestCheckNoProbeEndpoint(t *testing.T) {
	_, client, metrics := newProbeServer(t, nil)

	_, err := client.Check(context.Background())
	if err == nil {
		t.Fatal("expected an error when the probe endpoint does not exist")
	}
	if !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("expected the error to report the observed status, got %v", err)
	}
	if strings.Contains(err.Error(), "probe payload") {
		t.Fatalf("a 404 is an unreachable upstream, not a pre-split one, got %v", err)
	}
	if metrics.reachable != 0 || metrics.unreachable != 1 {
		t.Fatalf("expected reachability metrics reachable=0 unreachable=1, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

// An unobserved verdict must not be reported as ready, whatever status carried it.
func TestCheckReadinessWithoutAProbePayloadIsNotReady(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "server error", status: http.StatusInternalServerError, body: "boom"},
		{name: "2xx web UI shell", status: http.StatusOK, body: "<!doctype html><title>Argo Watcher</title>"},
		{name: "2xx empty body", status: http.StatusOK, body: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, client, _ := newProbeServer(t, map[string]probeResponse{
				"/livez":  {status: http.StatusOK, body: `{"status":"up"}`},
				"/readyz": {status: tc.status, body: tc.body},
			})

			health, err := client.Check(context.Background())
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if health.Ready {
				t.Fatalf("expected unready when no verdict was obtained, got %+v", health)
			}
			if health.Reason != fmt.Sprintf("status %d carried no probe payload", tc.status) {
				t.Fatalf("expected the reason to name the missing payload, got %q", health.Reason)
			}
		})
	}
}

// The reason on a transport failure must stay bounded: this server serves it on
// an unauthenticated endpoint, and the Go error names Argo Watcher's host.
func TestCheckReadinessTransportFailureReasonIsBounded(t *testing.T) {
	live := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"status":"up"}`)),
	}
	metrics := &trackingReachability{}
	client := New("http://argo-watcher.internal:8080", &sequencedHTTPClient{
		responses: []*http.Response{live},
		err:       errors.New("dial tcp 10.1.2.3:8080: connect: connection refused"),
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)), WithReachabilityMetrics(metrics))

	health, err := client.Check(context.Background())
	if err != nil {
		t.Fatalf("a failed readiness probe must not fail the check, got %v", err)
	}
	if health.Ready {
		t.Fatal("expected unready when the readiness verdict could not be obtained")
	}
	if health.Reason != "readiness probe unreachable" {
		t.Fatalf("expected a bounded reason, got %q", health.Reason)
	}
	// /livez answered, so the reachability gauge must survive the readiness failure.
	if metrics.reachable != 1 || metrics.unreachable != 0 {
		t.Fatalf("expected reachability metrics reachable=1 unreachable=0, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

func TestListDeployments(t *testing.T) {
	requested := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tasks" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}

		values := r.URL.Query()
		if values.Get("app") != "api" {
			t.Errorf("expected app query api, got %s", values.Get("app"))
		}
		if values.Get("from_timestamp") != "10" {
			t.Errorf("expected from_timestamp 10, got %s", values.Get("from_timestamp"))
		}
		if values.Get("to_timestamp") != "20" {
			t.Errorf("expected to_timestamp 20, got %s", values.Get("to_timestamp"))
		}
		if values.Get("status") != "deployed" {
			t.Errorf("expected status deployed, got %s", values.Get("status"))
		}
		if values.Get("limit") != "25" {
			t.Errorf("expected limit 25, got %s", values.Get("limit"))
		}
		if values.Get("offset") != "50" {
			t.Errorf("expected offset 50, got %s", values.Get("offset"))
		}

		response := map[string]any{
			"total": 200,
			"tasks": []any{
				map[string]any{
					"id":      "task-1",
					"app":     "api",
					"author":  "alice",
					"project": "proj",
					"images": []any{
						map[string]any{"image": "repo", "tag": "v1"},
						map[string]any{"image": "", "tag": ""},
					},
					"status":             "deployed",
					"created":            time.Unix(10, 0).UTC(),
					"updated":            time.Unix(20, 0).UTC(),
					"status_reason":      nil,
					"is_rollback":        true,
					"rollback_target_id": "task-0",
				},
			},
		}

		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
		requested <- struct{}{}
	}))
	defer srv.Close()

	metrics := &trackingReachability{}
	client := New(srv.URL, srv.Client(), nil, WithReachabilityMetrics(metrics))
	app := "api"
	status := "deployed"
	page, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{
		App:           &app,
		Status:        &status,
		FromTimestamp: 10,
		ToTimestamp:   20,
		Limit:         25,
		Offset:        50,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(page.Deployments) != 1 {
		t.Fatalf("expected one deployment, got %d", len(page.Deployments))
	}
	if len(page.Deployments[0].Images) != 1 {
		t.Fatalf("expected one image after filtering, got %d", len(page.Deployments[0].Images))
	}
	if page.Deployments[0].Images[0].Image != "repo" || page.Deployments[0].Images[0].Tag != "v1" {
		t.Fatalf("unexpected image payload: %#v", page.Deployments[0].Images[0])
	}
	if !page.Deployments[0].IsRollback {
		t.Fatal("expected is_rollback to be mapped as true")
	}
	if page.Deployments[0].RollbackTargetID != "task-0" {
		t.Fatalf("expected rollback target task-0, got %q", page.Deployments[0].RollbackTargetID)
	}
	if page.Total != 200 {
		t.Fatalf("expected total 200, got %d", page.Total)
	}
	if !page.Truncated {
		t.Fatal("expected truncated to be true when total exceeds the page")
	}
	if page.Limit != 25 || page.Offset != 50 {
		t.Fatalf("expected limit/offset echoed as 25/50, got %d/%d", page.Limit, page.Offset)
	}

	select {
	case <-requested:
	default:
		t.Fatalf("request was not received")
	}
	if metrics.reachable != 1 || metrics.unreachable != 0 {
		t.Fatalf("expected reachability metrics reachable=1 unreachable=0, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

func TestListDeploymentsEmitsSpan(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	orig := otel.GetTracerProvider()
	tp := tracesdk.NewTracerProvider()
	tp.RegisterSpanProcessor(spanRecorder)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(orig)
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response := map[string]any{
			"tasks": []any{
				map[string]any{
					"id":      "task-1",
					"app":     "api",
					"author":  "alice",
					"project": "proj",
					"images":  []any{},
					"status":  "Success",
					"created": time.Unix(10, 0).UTC(),
					"updated": time.Unix(20, 0).UTC(),
				},
			},
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	_, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{
		FromTimestamp: 10,
		ToTimestamp:   20,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	spans := spanRecorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	span := spans[0]
	if span.Name() != "client.list_deployments" {
		t.Fatalf("expected span name client.list_deployments, got %s", span.Name())
	}
	if span.SpanKind() != trace.SpanKindInternal {
		t.Fatalf("expected internal span, got %v", span.SpanKind())
	}

	foundURL := false
	foundResult := false
	for _, attr := range span.Attributes() {
		switch attr.Key {
		case attribute.Key("argo_watcher.request_url"):
			foundURL = attr.Value.AsString() == srv.URL+"/api/v1/tasks?from_timestamp=10&to_timestamp=20"
		case attribute.Key("argo_watcher.result_count"):
			foundResult = attr.Value.AsInt64() == 1
		}
	}
	if !foundURL {
		t.Fatalf("expected span to include argo_watcher.request_url attribute, got %+v", span.Attributes())
	}
	if !foundResult {
		t.Fatalf("expected span to include result count attribute, got %+v", span.Attributes())
	}
}

func TestListDeploymentsNumericTimestamps(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	updated := base.Add(5 * time.Minute)

	testCases := []struct {
		name         string
		createdValue any
		updatedValue any
		wantCreated  time.Time
		wantUpdated  time.Time
	}{
		{
			name:         "seconds",
			createdValue: base.Unix(),
			updatedValue: updated.Unix(),
			wantCreated:  time.Unix(base.Unix(), 0).UTC(),
			wantUpdated:  time.Unix(updated.Unix(), 0).UTC(),
		},
		{
			name:         "rfc3339Strings",
			createdValue: base.Format(time.RFC3339),
			updatedValue: updated.Format(time.RFC3339),
			wantCreated:  base,
			wantUpdated:  updated,
		},
		{
			name:         "fractionalSeconds",
			createdValue: float64(base.Unix()) + 0.75,
			updatedValue: float64(updated.Unix()) + 0.5,
			wantCreated:  time.Unix(base.Unix(), int64(0.75*float64(time.Second))).UTC(),
			wantUpdated:  time.Unix(updated.Unix(), int64(0.5*float64(time.Second))).UTC(),
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				resp := map[string]any{
					"tasks": []any{
						map[string]any{
							"id":      "task-1",
							"app":     "api",
							"author":  "alice",
							"project": "proj",
							"images":  []any{},
							"status":  "Success",
							"created": tc.createdValue,
							"updated": tc.updatedValue,
						},
					},
				}
				if err := json.NewEncoder(w).Encode(resp); err != nil {
					t.Errorf("encode response: %v", err)
				}
			}))
			defer srv.Close()

			client := New(srv.URL, srv.Client(), nil)
			filter := domain.DeploymentFilter{FromTimestamp: base.Unix()}

			page, err := client.ListDeployments(context.Background(), filter)
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if len(page.Deployments) != 1 {
				t.Fatalf("expected one deployment, got %d", len(page.Deployments))
			}
			if !page.Deployments[0].Created.Equal(tc.wantCreated) {
				t.Fatalf("unexpected created timestamp: got %s, want %s", page.Deployments[0].Created, tc.wantCreated)
			}
			if !page.Deployments[0].Updated.Equal(tc.wantUpdated) {
				t.Fatalf("unexpected updated timestamp: got %s, want %s", page.Deployments[0].Updated, tc.wantUpdated)
			}
		})
	}
}

func TestListDeploymentsHandlesNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream failed"))
	}))
	defer srv.Close()

	metrics := &trackingReachability{}
	client := New(srv.URL, srv.Client(), nil, WithReachabilityMetrics(metrics))
	if _, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{}); err == nil {
		t.Fatal("expected error for non-success status")
	}
	if metrics.reachable != 0 || metrics.unreachable != 1 {
		t.Fatalf("expected reachability metrics reachable=0 unreachable=1, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

func TestListDeploymentsDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	metrics := &trackingReachability{}
	client := New(srv.URL, srv.Client(), nil, WithReachabilityMetrics(metrics))
	if _, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{}); err == nil {
		t.Fatal("expected error decoding payload")
	}
	if metrics.reachable != 1 || metrics.unreachable != 0 {
		t.Fatalf("expected reachability metrics reachable=1 unreachable=0, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

func TestListDeploymentsToDomainError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tasks": []any{
				map[string]any{
					"id":      "task-1",
					"app":     "app",
					"author":  "author",
					"project": "proj",
				},
			},
		})
	}))
	defer srv.Close()

	metrics := &trackingReachability{}
	client := New(srv.URL, srv.Client(), nil, WithReachabilityMetrics(metrics))
	if _, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{}); err == nil {
		t.Fatal("expected error when payload missing timestamps")
	}
	if metrics.reachable != 1 || metrics.unreachable != 0 {
		t.Fatalf("expected reachability metrics reachable=1 unreachable=0, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

func TestCheckNetworkError(t *testing.T) {
	metrics := &trackingReachability{}
	client := New("http://example.com", &mockHTTPClient{respErr: errors.New("connection refused")}, nil, WithReachabilityMetrics(metrics))

	_, err := client.Check(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "probe /livez") {
		t.Fatalf("expected error to name the probe, got %v", err)
	}
	if metrics.reachable != 0 || metrics.unreachable != 1 {
		t.Fatalf("expected reachability metrics reachable=0 unreachable=1, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

func TestListDeploymentsNetworkError(t *testing.T) {
	metrics := &trackingReachability{}
	client := New("http://example.com", &mockHTTPClient{respErr: errors.New("connection refused")}, nil, WithReachabilityMetrics(metrics))

	_, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{FromTimestamp: 10})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "fetch tasks") {
		t.Fatalf("expected error to mention fetch tasks, got %v", err)
	}
	if metrics.reachable != 0 || metrics.unreachable != 1 {
		t.Fatalf("expected reachability metrics reachable=0 unreachable=1, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

func TestRequestBuildFailures(t *testing.T) {
	invalidURL := "http://\x7f.invalid"
	metrics := &trackingReachability{}
	client := New(invalidURL, &mockHTTPClient{}, nil, WithReachabilityMetrics(metrics))

	if _, err := client.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "build probe request") {
		t.Fatalf("expected build probe request error, got %v", err)
	}

	_, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{FromTimestamp: time.Now().Unix()})
	if err == nil {
		t.Fatal("expected error for malformed base URL")
	}
	if !strings.Contains(err.Error(), "parse tasks endpoint") {
		t.Fatalf("expected parse tasks endpoint error, got %v", err)
	}
	if metrics.reachable != 0 || metrics.unreachable != 2 {
		t.Fatalf("expected reachability metrics reachable=0 unreachable=2, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

func TestListDeploymentsNoTruncationWhenPageCoversTotal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("limit") {
			t.Errorf("expected no limit query when filter limit is zero, got %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total": 2,
			"tasks": []any{
				map[string]any{"id": "a", "created": 10, "updated": 20},
				map[string]any{"id": "b", "created": 10, "updated": 20},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	page, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{FromTimestamp: 10})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if page.Truncated {
		t.Fatal("expected truncated to be false when the page covers the total")
	}
	if page.Total != 2 {
		t.Fatalf("expected total 2, got %d", page.Total)
	}
}

// An offset past the end of the result set returns no rows while Argo Watcher
// still reports the real total. The client must pass that total through
// untouched: inflating it to match the offset would invent deployments that do
// not exist, which is exactly the miscount this page contract prevents.
func TestListDeploymentsOffsetPastEndKeepsUpstreamTotal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total": 30,
			"tasks": []any{},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	page, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{
		FromTimestamp: 10,
		Limit:         50,
		Offset:        50,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if page.Total != 30 {
		t.Fatalf("expected the upstream total 30 to be preserved, got %d", page.Total)
	}
	if len(page.Deployments) != 0 {
		t.Fatalf("expected no deployments, got %d", len(page.Deployments))
	}
	if page.Truncated {
		t.Fatal("expected truncated to be false when the offset is past the end")
	}
}

// Argo Watcher tags `total` omitempty, so zero matches arrive as an absent
// field. That must read as a genuine zero, not as missing data.
func TestListDeploymentsZeroMatches(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"tasks": []any{}})
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	page, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{FromTimestamp: 10, Limit: 50})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("expected total 0, got %d", page.Total)
	}
	if page.Truncated {
		t.Fatal("expected truncated to be false when nothing matched")
	}
}

// The config passthrough is an allowlist: fields Argo Watcher may add later must
// not reach an MCP client without a deliberate code change.
func TestGetServerInfoProjectsConfigToAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/version" {
			_ = json.NewEncoder(w).Encode("1.2.3")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"argo_cd_url":         "https://argocd.example.com",
			"state_type":          "postgres",
			"lockdown_schedule":   "0 2 * * *",
			"webhook":             map[string]any{"enabled": true, "url": "https://hooks.example.com/secret-path"},
			"mattermost":          map[string]any{"enabled": false, "channel_id": "abc123"},
			"oidc":                map[string]any{"enabled": true, "issuer_url": "https://sso.example.com"},
			"future_secret_token": "must-not-be-forwarded",
		})
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	info, err := client.GetServerInfo(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if _, leaked := info.Config["future_secret_token"]; leaked {
		t.Fatal("an unrecognised config field was forwarded to the client")
	}
	if info.Config["argo_cd_url"] != "https://argocd.example.com" {
		t.Fatalf("expected argo_cd_url to be forwarded, got %#v", info.Config["argo_cd_url"])
	}
	if info.Config["lockdown_schedule"] != "0 2 * * *" {
		t.Fatalf("expected lockdown_schedule to be forwarded, got %#v", info.Config["lockdown_schedule"])
	}

	// Integrations are reduced to their enabled flag; their endpoints are not.
	webhook, ok := info.Config["webhook"].(map[string]any)
	if !ok {
		t.Fatalf("expected webhook to be present as an object, got %#v", info.Config["webhook"])
	}
	if webhook["enabled"] != true {
		t.Fatalf("expected webhook.enabled true, got %#v", webhook["enabled"])
	}
	if _, leaked := webhook["url"]; leaked {
		t.Fatal("webhook URL was forwarded to the client")
	}
	if mattermost := info.Config["mattermost"].(map[string]any); len(mattermost) != 1 {
		t.Fatalf("expected mattermost reduced to enabled only, got %#v", mattermost)
	}
	if oidc := info.Config["oidc"].(map[string]any); len(oidc) != 1 {
		t.Fatalf("expected oidc reduced to enabled only, got %#v", oidc)
	}
}

func TestGetDeployLock(t *testing.T) {
	for _, locked := range []bool{true, false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/deploy-lock" {
				t.Errorf("unexpected path %s", r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(locked)
		}))

		metrics := &trackingReachability{}
		client := New(srv.URL, srv.Client(), nil, WithReachabilityMetrics(metrics))
		state, err := client.GetDeployLock(context.Background())
		srv.Close()

		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if state.Locked != locked {
			t.Fatalf("expected locked=%v, got %v", locked, state.Locked)
		}
		if metrics.reachable != 1 {
			t.Fatalf("expected reachable metric recorded once, got %d", metrics.reachable)
		}
	}
}

func TestGetDeployLockError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	metrics := &trackingReachability{}
	client := New(srv.URL, srv.Client(), nil, WithReachabilityMetrics(metrics))
	if _, err := client.GetDeployLock(context.Background()); err == nil {
		t.Fatal("expected error for non-success status")
	} else if !strings.Contains(err.Error(), "deploy lock") {
		t.Fatalf("expected error to mention deploy lock, got %v", err)
	}
	if metrics.unreachable != 1 {
		t.Fatalf("expected unreachable metric recorded once, got %d", metrics.unreachable)
	}
}

func TestGetReachability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/reachability" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"available": false, "reason": "argocd"})
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	got, err := client.GetReachability(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got.Available {
		t.Fatal("expected available=false")
	}
	if got.Reason != "argocd" {
		t.Fatalf("expected reason argocd, got %q", got.Reason)
	}
}

func TestGetReachabilityError(t *testing.T) {
	client := New("http://example.com", &mockHTTPClient{respErr: errors.New("connection refused")}, nil)
	if _, err := client.GetReachability(context.Background()); err == nil {
		t.Fatal("expected error, got nil")
	} else if !strings.Contains(err.Error(), "reachability") {
		t.Fatalf("expected error to mention reachability, got %v", err)
	}
}

func TestGetServerInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/version":
			_ = json.NewEncoder(w).Encode("1.2.3")
		case "/api/v1/config":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"argo_cd_url":        "https://argocd.example.com",
				"deployment_timeout": 900,
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	info, err := client.GetServerInfo(context.Background())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if info.Version != "1.2.3" {
		t.Fatalf("expected version 1.2.3, got %q", info.Version)
	}
	if info.Config["argo_cd_url"] != "https://argocd.example.com" {
		t.Fatalf("unexpected config passthrough: %#v", info.Config)
	}
}

// GetServerInfo makes two calls; a failure in either must surface rather than
// yielding a half-populated result.
func TestGetServerInfoFailsWhenConfigUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/version" {
			_ = json.NewEncoder(w).Encode("1.2.3")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	info, err := client.GetServerInfo(context.Background())
	if err == nil {
		t.Fatal("expected error when the config endpoint fails")
	}
	if !strings.Contains(err.Error(), "config") {
		t.Fatalf("expected error to mention config, got %v", err)
	}
	if info.Version != "" {
		t.Fatalf("expected zero-value info on error, got %#v", info)
	}
}

// Argo Watcher has reported `total` since v0.10.0, so this shape only arises
// against an upstream below the documented floor. Pin what happens anyway, since
// two other tests exercise the path incidentally: the count is not invented from
// the offset, so it under-reports rather than over-reports.
func TestListDeploymentsTotalAbsentWithRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tasks": []any{
				map[string]any{"id": "a", "created": 10, "updated": 20},
				map[string]any{"id": "b", "created": 10, "updated": 20},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	page, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{FromTimestamp: 10, Limit: 50})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(page.Deployments) != 2 {
		t.Fatalf("expected 2 deployments, got %d", len(page.Deployments))
	}
	if page.Total != 0 {
		t.Fatalf("expected total to stay at the upstream value 0, got %d", page.Total)
	}
	if page.Truncated {
		t.Fatal("expected truncated false: no total means no basis to claim more remain")
	}
}

// Truncated hinges on a single comparison, so pin both sides of the boundary
// including at a nonzero offset.
func TestListDeploymentsTruncationBoundary(t *testing.T) {
	testCases := []struct {
		name          string
		total         int64
		limit         int
		offset        int
		rows          int
		wantTruncated bool
	}{
		{name: "lastPageExactlyCoversTotal", total: 75, limit: 25, offset: 50, rows: 25, wantTruncated: false},
		{name: "oneRowRemainsAfterLastPage", total: 76, limit: 25, offset: 50, rows: 25, wantTruncated: true},
		{name: "singleFullPageCoversTotal", total: 50, limit: 50, offset: 0, rows: 50, wantTruncated: false},
		{name: "singleFullPageLeavesOne", total: 51, limit: 50, offset: 0, rows: 50, wantTruncated: true},
		{name: "noMatchesAtAll", total: 0, limit: 50, offset: 0, rows: 0, wantTruncated: false},
		{name: "offsetPastEnd", total: 30, limit: 50, offset: 50, rows: 0, wantTruncated: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tasks := make([]any, 0, tc.rows)
			for i := 0; i < tc.rows; i++ {
				tasks = append(tasks, map[string]any{"id": "t", "created": 10, "updated": 20})
			}

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"total": tc.total, "tasks": tasks})
			}))
			defer srv.Close()

			client := New(srv.URL, srv.Client(), nil)
			page, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{
				FromTimestamp: 10,
				Limit:         tc.limit,
				Offset:        tc.offset,
			})
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if page.Truncated != tc.wantTruncated {
				t.Fatalf("truncated = %v, want %v (total=%d offset=%d rows=%d)",
					page.Truncated, tc.wantTruncated, tc.total, tc.offset, tc.rows)
			}
			if page.Total != tc.total {
				t.Fatalf("expected total %d passed through, got %d", tc.total, page.Total)
			}
			if page.Limit != tc.limit || page.Offset != tc.offset {
				t.Fatalf("expected limit/offset %d/%d echoed, got %d/%d", tc.limit, tc.offset, page.Limit, page.Offset)
			}
		})
	}
}

func TestProjectConfigEdgeCases(t *testing.T) {
	client := New("http://example.com", &mockHTTPClient{}, nil)

	if got := client.projectConfig(nil); got != nil {
		t.Fatalf("expected nil config to stay nil so the field is omitted, got %#v", got)
	}

	if got := client.projectConfig(map[string]any{}); got == nil || len(got) != 0 {
		t.Fatalf("expected an empty non-nil map, got %#v", got)
	}

	testCases := []struct {
		name string
		cfg  map[string]any
		want map[string]any
	}{
		{
			name: "nullIntegrationSkipped",
			cfg:  map[string]any{"webhook": nil},
			want: map[string]any{},
		},
		{
			name: "nonObjectIntegrationSkipped",
			cfg:  map[string]any{"webhook": "on"},
			want: map[string]any{},
		},
		{
			name: "integrationWithoutEnabledDropped",
			cfg:  map[string]any{"oidc": map[string]any{"issuer_url": "https://sso.example.com"}},
			want: map[string]any{},
		},
		{
			name: "disabledIntegrationKeepsOnlyFlag",
			cfg:  map[string]any{"oidc": map[string]any{"enabled": false, "issuer_url": "https://sso.example.com"}},
			want: map[string]any{"oidc": map[string]any{"enabled": false}},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := client.projectConfig(tc.cfg)
			if len(got) != len(tc.want) {
				t.Fatalf("expected %d keys, got %#v", len(tc.want), got)
			}
			for key, wantValue := range tc.want {
				nested, ok := got[key].(map[string]any)
				if !ok {
					t.Fatalf("expected %q to be an object, got %#v", key, got[key])
				}
				wantNested := wantValue.(map[string]any)
				if len(nested) != len(wantNested) {
					t.Fatalf("expected %q reduced to %#v, got %#v", key, wantNested, nested)
				}
				for field, value := range wantNested {
					if nested[field] != value {
						t.Fatalf("expected %s.%s = %#v, got %#v", key, field, value, nested[field])
					}
				}
			}
		})
	}
}

// Every allowlisted key must survive, and the extras Argo Watcher really emits
// must not. The length assertion is the point: a newly forwarded key fails here
// rather than reaching an MCP client unnoticed.
func TestProjectConfigForwardsEveryAllowlistedKey(t *testing.T) {
	client := New("http://example.com", &mockHTTPClient{}, nil)

	cfg := map[string]any{
		// The verbatim shape encoding/json produces for a net/url.URL, field for
		// field, rather than a hand-trimmed subset.
		"argo_cd_url": map[string]any{
			"Scheme":      "https",
			"Opaque":      "",
			"User":        nil,
			"Host":        "argocd.example.com",
			"Path":        "/argo",
			"RawPath":     "",
			"OmitHost":    false,
			"ForceQuery":  false,
			"RawQuery":    "",
			"Fragment":    "",
			"RawFragment": "",
		},
		"argo_cd_url_alias":    "https://argocd.public.example.com",
		"argo_api_timeout":     60,
		"argo_api_retries":     3,
		"argo_refresh_app":     true,
		"accept_suspended_app": false,
		"deployment_timeout":   900,
		"registry_proxy_url":   "https://proxy.example.com",
		"state_type":           "postgres",
		"skip_tls_verify":      false,
		"log_level":            "info",
		"lockdown_schedule":    "0 2 * * *",

		// Reduced to their enabled flag.
		"oidc":       map[string]any{"enabled": true, "issuer_url": "https://sso.example.com"},
		"webhook":    map[string]any{"enabled": true, "url": "https://hooks.example.com/secret"},
		"mattermost": map[string]any{"enabled": false, "channel_id": "abc123"},

		// Dropped: the legacy OIDC mirror upstream emits alongside `oidc`, an
		// internal flag, and a stand-in for whatever upstream adds next.
		"keycloak":            map[string]any{"enabled": true, "issuer_url": "https://sso.example.com", "client_id": "watcher"},
		"devEnvironment":      false,
		"future_secret_token": "must-not-be-forwarded",
	}

	got := client.projectConfig(cfg)

	for _, key := range []string{"keycloak", "devEnvironment", "future_secret_token"} {
		if _, leaked := got[key]; leaked {
			t.Fatalf("%q must not be forwarded, got %#v", key, got[key])
		}
	}

	for key := range exposedConfigKeys {
		if _, ok := got[key]; !ok {
			t.Fatalf("allowlisted key %q was dropped — does it still match an upstream JSON tag?", key)
		}
	}

	// 12 allowlisted scalars + 3 integrations reduced to their flag.
	if want := len(exposedConfigKeys) + len(integrationKeys); len(got) != want {
		t.Fatalf("expected exactly %d forwarded keys, got %d: %#v", want, len(got), got)
	}

	// argo_cd_url is a net/url.URL upstream and must arrive as a usable string.
	if got["argo_cd_url"] != "https://argocd.example.com/argo" {
		t.Fatalf("expected argo_cd_url flattened to a URL string, got %#v", got["argo_cd_url"])
	}
}

// url.Userinfo has only unexported fields, so credentials embedded in ARGO_URL
// marshal as an empty object. flattenURL ignores User entirely; pin that, since
// reconstructing it would put a credential into an MCP response.
func TestProjectConfigDropsURLUserInfo(t *testing.T) {
	client := New("http://example.com", &mockHTTPClient{}, nil)

	got := client.projectConfig(map[string]any{
		"argo_cd_url": map[string]any{
			"Scheme": "https",
			"User":   map[string]any{},
			"Host":   "argocd.example.com",
			"Path":   "",
		},
	})

	flattened, ok := got["argo_cd_url"].(string)
	if !ok {
		t.Fatalf("expected a flattened string, got %#v", got["argo_cd_url"])
	}
	if flattened != "https://argocd.example.com" {
		t.Fatalf("expected no user info in the flattened URL, got %q", flattened)
	}
	if strings.Contains(flattened, "@") {
		t.Fatalf("flattened URL carries user info: %q", flattened)
	}
}

func TestProjectConfigLeavesPlainStringURLAlone(t *testing.T) {
	client := New("http://example.com", &mockHTTPClient{}, nil)

	got := client.projectConfig(map[string]any{"argo_cd_url": "https://argocd.example.com"})
	if got["argo_cd_url"] != "https://argocd.example.com" {
		t.Fatalf("expected a plain string URL to pass through, got %#v", got["argo_cd_url"])
	}
}

// A malformed upstream answer must surface as a labelled error, not as a
// plausible zero value: "Locked=false" would read as "deploys are not frozen".
func TestNewEndpointDecodeAndTransportErrors(t *testing.T) {
	t.Run("buildRequestFailure", func(t *testing.T) {
		metrics := &trackingReachability{}
		client := New("http://\x7f.invalid", &mockHTTPClient{}, nil, WithReachabilityMetrics(metrics))

		_, err := client.GetDeployLock(context.Background())
		if err == nil || !strings.Contains(err.Error(), "build deploy lock request") {
			t.Fatalf("expected build deploy lock request error, got %v", err)
		}
		if metrics.unreachable != 1 {
			t.Fatalf("expected unreachable recorded once, got %d", metrics.unreachable)
		}
	})

	t.Run("deployLockNotABoolean", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}))
		defer srv.Close()

		metrics := &trackingReachability{}
		client := New(srv.URL, srv.Client(), nil, WithReachabilityMetrics(metrics))
		state, err := client.GetDeployLock(context.Background())
		if err == nil || !strings.Contains(err.Error(), "decode deploy lock response") {
			t.Fatalf("expected decode deploy lock response error, got %v", err)
		}
		if state.Locked {
			t.Fatal("expected the zero value, not a claim that deploys are unlocked")
		}
		if metrics.reachable != 1 {
			t.Fatalf("the server answered, so it is reachable: got reachable=%d", metrics.reachable)
		}
	})

	t.Run("reachabilityNonSuccess", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer srv.Close()

		client := New(srv.URL, srv.Client(), nil)
		_, err := client.GetReachability(context.Background())
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "reachability") || !strings.Contains(err.Error(), "503") {
			t.Fatalf("expected the error to name the endpoint and status, got %v", err)
		}
	})

	t.Run("versionFailureSkipsConfig", func(t *testing.T) {
		configCalls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v1/config" {
				configCalls++
			}
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		client := New(srv.URL, srv.Client(), nil)
		info, err := client.GetServerInfo(context.Background())
		if err == nil || !strings.Contains(err.Error(), "version") {
			t.Fatalf("expected a version error, got %v", err)
		}
		if configCalls != 0 {
			t.Fatalf("expected config not to be fetched after version failed, got %d calls", configCalls)
		}
		if info.Version != "" || info.Config != nil {
			t.Fatalf("expected zero-value info on error, got %#v", info)
		}
	})

	t.Run("versionNotAString", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"v": 1})
		}))
		defer srv.Close()

		client := New(srv.URL, srv.Client(), nil)
		if _, err := client.GetServerInfo(context.Background()); err == nil ||
			!strings.Contains(err.Error(), "decode version response") {
			t.Fatalf("expected decode version response error, got %v", err)
		}
	})
}

// The upstream status and body are the only signal that lets a caller correct a
// bad status filter, so assert they survive into the error.
func TestListDeploymentsSurfacesUpstreamRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"unsupported status filter"}`))
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	bogus := "Deployed"
	_, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{
		FromTimestamp: 10,
		Status:        &bogus,
	})
	if err == nil {
		t.Fatal("expected an error for a rejected status filter")
	}
	for _, want := range []string{"fetch tasks", "400", "unsupported status filter"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error to contain %q, got %v", want, err)
		}
	}
}

// One unparseable row fails the whole page, so make sure the message identifies
// which row and what was wrong with it.
func TestListDeploymentsMissingUpdatedTimestamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tasks": []any{
				map[string]any{"id": "task-broken", "app": "api", "created": 10},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	_, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{FromTimestamp: 10})
	if err == nil {
		t.Fatal("expected an error for a task with no updated timestamp")
	}
	if !strings.Contains(err.Error(), "missing updated timestamp") {
		t.Fatalf("expected the error to name the missing field, got %v", err)
	}
	if !strings.Contains(err.Error(), "task-broken") {
		t.Fatalf("expected the error to identify the offending task, got %v", err)
	}
}

// The decoder handles only the shapes Argo Watcher's read paths emit: Unix
// seconds (whole or fractional) and RFC3339. Large magnitudes are read as
// seconds, not guessed at as milliseconds — the guess is what was removed.
func TestJSONTimestampUnmarshal(t *testing.T) {
	testCases := []struct {
		name    string
		input   string
		want    time.Time
		zero    bool
		wantErr string
	}{
		{name: "null", input: `null`, zero: true},
		{name: "emptyString", input: `""`, zero: true},
		{name: "unixSeconds", input: `1700000000`, want: time.Unix(1_700_000_000, 0).UTC()},
		{name: "negativeSeconds", input: `-100`, want: time.Unix(-100, 0).UTC()},
		{name: "zeroSeconds", input: `0`, want: time.Unix(0, 0).UTC()},
		{
			name:  "fractionalSeconds",
			input: `1700000000.5`,
			want:  time.Unix(1_700_000_000, int64(0.5*float64(time.Second))).UTC(),
		},
		{
			name:  "rfc3339",
			input: `"2024-03-10T15:04:05Z"`,
			want:  time.Date(2024, time.March, 10, 15, 4, 5, 0, time.UTC),
		},
		{
			// Previously this magnitude was guessed as milliseconds. It is now read
			// as seconds, so no value is silently misdated by a factor of 1000.
			name:  "largeMagnitudeIsStillSeconds",
			input: `1700000000000`,
			want:  time.Unix(1_700_000_000_000, 0).UTC(),
		},
		{name: "invalidRFC3339", input: `"not-a-date"`, wantErr: "parse RFC3339 timestamp"},
		{name: "notANumber", input: `{}`, wantErr: "unmarshal numeric timestamp"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var ts jsonTimestamp
			err := json.Unmarshal([]byte(tc.input), &ts)

			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.zero {
				if !ts.IsZero() {
					t.Fatalf("expected the zero time, got %s", ts.Time)
				}
				return
			}
			if !ts.Equal(tc.want) {
				t.Fatalf("got %s, want %s", ts.Time, tc.want)
			}
		})
	}
}

// Argo Watcher takes ARGO_URL_ALIAS and DOCKER_IMAGES_PROXY verbatim from the
// environment and does not redact them, so an operator using a registry proxy
// behind basic auth has the credential sitting in /api/v1/config. It must not
// reach an MCP client.
func TestProjectConfigStripsCredentialsFromURLValuedFields(t *testing.T) {
	client := New("http://example.com", &mockHTTPClient{}, nil)

	got := client.projectConfig(map[string]any{
		// Schemeless is the conventional form for a registry reference, so cover
		// it here rather than only the scheme-bearing shape.
		"registry_proxy_url": "robot$puller:s3cr3t-token@registry.example.com/v2",
		"argo_cd_url_alias":  "https://admin:hunter2@argocd.public.example.com",
	})

	for key, want := range map[string]string{
		"registry_proxy_url": "registry.example.com/v2",
		"argo_cd_url_alias":  "https://argocd.public.example.com",
	} {
		value, ok := got[key].(string)
		if !ok {
			t.Fatalf("expected %q to be forwarded as a string, got %#v", key, got[key])
		}
		if value != want {
			t.Fatalf("expected %q redacted to %q, got %q", key, want, value)
		}
		if strings.Contains(value, "@") {
			t.Fatalf("%q still carries userinfo: %q", key, value)
		}
	}

	// The secrets must not survive anywhere in the projection.
	for _, secret := range []string{"s3cr3t-token", "hunter2", "robot$puller", "admin"} {
		if strings.Contains(fmt.Sprintf("%v", got), secret) {
			t.Fatalf("secret %q leaked into the projected config: %#v", secret, got)
		}
	}
}

func TestRedactURLUserinfo(t *testing.T) {
	testCases := []struct {
		name   string
		raw    string
		want   string
		wantOK bool
	}{
		{name: "empty", raw: "", want: "", wantOK: true},
		{name: "noUserinfo", raw: "https://registry.example.com/v2", want: "https://registry.example.com/v2", wantOK: true},
		{name: "userAndPassword", raw: "https://u:p@host.example.com", want: "https://host.example.com", wantOK: true},
		{name: "userOnly", raw: "https://u@host.example.com", want: "https://host.example.com", wantOK: true},
		{
			name:   "preservesPathQueryAndPort",
			raw:    "https://u:p@host.example.com:8443/v2/base?ns=lib",
			want:   "https://host.example.com:8443/v2/base?ns=lib",
			wantOK: true,
		},
		// A bare host with no scheme still parses, as a path — nothing to strip.
		{name: "schemelessHost", raw: "registry.example.com", want: "registry.example.com", wantOK: true},

		// Schemeless values are the conventional way to write a registry
		// reference, and url.Parse reads "user:pass@host" as scheme "user" with an
		// opaque remainder, so userinfo here is invisible without normalising the
		// value first. These are the cases a naive url.Parse lets through intact.
		{
			name:   "schemelessUserAndPassword",
			raw:    "user:pass@registry.example.com/v2",
			want:   "registry.example.com/v2",
			wantOK: true,
		},
		{
			name:   "schemelessUserOnly",
			raw:    "admin@registry.example.com",
			want:   "registry.example.com",
			wantOK: true,
		},
		{
			name:   "schemelessPreservesPortAndPath",
			raw:    "robot:tok@registry.example.com:5000/v2/library",
			want:   "registry.example.com:5000/v2/library",
			wantOK: true,
		},
		// Already scheme-relative: must not be double-prefixed, and must still be
		// redacted.
		{
			name:   "schemeRelativeWithUserinfo",
			raw:    "//user:pass@registry.example.com/v2",
			want:   "//registry.example.com/v2",
			wantOK: true,
		},

		// Unparseable values are dropped rather than guessed at.
		{name: "controlCharacter", raw: "https://u:p@host\x7f.example.com", wantOK: false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := redactURLUserinfo(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tc.wantOK, got)
			}
			if !tc.wantOK {
				return
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Upstream changing a URL field's type is a shape this code cannot sanitise, so
// the field is dropped rather than forwarded.
func TestProjectConfigDropsNonStringURLField(t *testing.T) {
	client := New("http://example.com", &mockHTTPClient{}, nil)

	got := client.projectConfig(map[string]any{
		"registry_proxy_url": map[string]any{"Host": "registry.example.com"},
	})

	if _, present := got["registry_proxy_url"]; present {
		t.Fatalf("expected a non-string URL field to be dropped, got %#v", got["registry_proxy_url"])
	}
}
