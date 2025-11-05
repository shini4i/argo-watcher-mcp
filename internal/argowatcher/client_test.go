package argowatcher

import (
	"context"
	"encoding/json"
	"errors"
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

func TestCheckSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	metrics := &trackingReachability{}
	client := New(srv.URL, srv.Client(), nil, WithReachabilityMetrics(metrics))
	if err := client.Check(context.Background()); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if metrics.reachable != 1 || metrics.unreachable != 0 {
		t.Fatalf("expected reachability metrics reachable=1 unreachable=0, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

func TestCheckFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	metrics := &trackingReachability{}
	client := New(srv.URL, srv.Client(), nil, WithReachabilityMetrics(metrics))
	if err := client.Check(context.Background()); err == nil {
		t.Fatalf("expected error, got nil")
	}
	if metrics.reachable != 0 || metrics.unreachable != 1 {
		t.Fatalf("expected reachability metrics reachable=0 unreachable=1, got %d/%d", metrics.reachable, metrics.unreachable)
	}
}

func TestListDeployments(t *testing.T) {
	requested := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tasks" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}

		values := r.URL.Query()
		if values.Get("app") != "api" {
			t.Fatalf("expected app query api, got %s", values.Get("app"))
		}
		if values.Get("from_timestamp") != "10" {
			t.Fatalf("expected from_timestamp 10, got %s", values.Get("from_timestamp"))
		}
		if values.Get("to_timestamp") != "20" {
			t.Fatalf("expected to_timestamp 20, got %s", values.Get("to_timestamp"))
		}

		response := map[string]any{
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
					"status":        "Success",
					"created":       time.Unix(10, 0).UTC(),
					"updated":       time.Unix(20, 0).UTC(),
					"status_reason": nil,
					"validated":     true,
				},
			},
		}

		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatalf("encode response: %v", err)
		}
		requested <- struct{}{}
	}))
	defer srv.Close()

	metrics := &trackingReachability{}
	client := New(srv.URL, srv.Client(), nil, WithReachabilityMetrics(metrics))
	app := "api"
	deployments, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{
		App:           &app,
		FromTimestamp: 10,
		ToTimestamp:   20,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(deployments) != 1 {
		t.Fatalf("expected one deployment, got %d", len(deployments))
	}
	if len(deployments[0].Images) != 1 {
		t.Fatalf("expected one image after filtering, got %d", len(deployments[0].Images))
	}
	if deployments[0].Images[0].Image != "repo" || deployments[0].Images[0].Tag != "v1" {
		t.Fatalf("unexpected image payload: %#v", deployments[0].Images[0])
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
			t.Fatalf("encode response: %v", err)
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
			name:         "milliseconds",
			createdValue: base.UnixMilli(),
			updatedValue: updated.UnixMilli(),
			wantCreated:  time.UnixMilli(base.UnixMilli()).UTC(),
			wantUpdated:  time.UnixMilli(updated.UnixMilli()).UTC(),
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
							"id":        "task-1",
							"app":       "api",
							"author":    "alice",
							"project":   "proj",
							"images":    []any{},
							"status":    "Success",
							"created":   tc.createdValue,
							"updated":   tc.updatedValue,
							"validated": true,
						},
					},
				}
				if err := json.NewEncoder(w).Encode(resp); err != nil {
					t.Fatalf("encode response: %v", err)
				}
			}))
			defer srv.Close()

			client := New(srv.URL, srv.Client(), nil)
			filter := domain.DeploymentFilter{FromTimestamp: base.Unix()}

			deployments, err := client.ListDeployments(context.Background(), filter)
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if len(deployments) != 1 {
				t.Fatalf("expected one deployment, got %d", len(deployments))
			}
			if !deployments[0].Created.Equal(tc.wantCreated) {
				t.Fatalf("unexpected created timestamp: got %s, want %s", deployments[0].Created, tc.wantCreated)
			}
			if !deployments[0].Updated.Equal(tc.wantUpdated) {
				t.Fatalf("unexpected updated timestamp: got %s, want %s", deployments[0].Updated, tc.wantUpdated)
			}
		})
	}
}

func TestListDeploymentsHandlesNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("upstream failed"))
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
		w.Write([]byte("not-json"))
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
		json.NewEncoder(w).Encode(map[string]any{
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

	err := client.Check(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "health request") {
		t.Fatalf("expected error to mention health request, got %v", err)
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

	if err := client.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "build health request") {
		t.Fatalf("expected build health request error, got %v", err)
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
