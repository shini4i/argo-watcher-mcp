package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shini4i/argo-watcher-mcp/internal/clock"
	"github.com/shini4i/argo-watcher-mcp/internal/domain"
)

type stubDeploymentService struct {
	capturedFilter domain.DeploymentFilter
	result         []domain.Deployment
	err            error
}

func (s *stubDeploymentService) ListDeployments(_ context.Context, filter domain.DeploymentFilter) ([]domain.Deployment, error) {
	s.capturedFilter = filter
	return s.result, s.err
}

type trackingMetrics struct {
	success int
	invalid int
	failure int
}

func (m *trackingMetrics) RecordSuccess(context.Context) {
	m.success++
}

func (m *trackingMetrics) RecordInvalid(context.Context) {
	m.invalid++
}

func (m *trackingMetrics) RecordFailure(context.Context) {
	m.failure++
}

func TestGetDeploymentsHandlerDefaults(t *testing.T) {
	now := time.Date(2024, time.January, 31, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.FixedClock{At: now}
	fakeService := &stubDeploymentService{
		result: []domain.Deployment{
			{ID: "task-1"},
		},
	}
	metrics := &trackingMetrics{}

	handler := &getDeploymentsHandler{
		clock:   fakeClock,
		svc:     fakeService,
		metrics: metrics,
	}

	_, out, err := handler.Handle(context.Background(), nil, getDeploymentsInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	deployments, ok := out.([]domain.Deployment)
	if !ok {
		t.Fatalf("expected []domain.Deployment, got %T", out)
	}
	if len(deployments) != 1 || deployments[0].ID != "task-1" {
		t.Fatalf("unexpected deployments result: %#v", deployments)
	}

	wantTo := now.Unix()
	wantFrom := wantTo - 30*24*60*60

	if fakeService.capturedFilter.FromTimestamp != wantFrom {
		t.Fatalf("expected from_timestamp %d, got %d", wantFrom, fakeService.capturedFilter.FromTimestamp)
	}
	if fakeService.capturedFilter.ToTimestamp != wantTo {
		t.Fatalf("expected to_timestamp %d, got %d", wantTo, fakeService.capturedFilter.ToTimestamp)
	}
	if metrics.success != 1 {
		t.Fatalf("expected success metric to record 1, got %d", metrics.success)
	}
}

func TestGetDeploymentsHandlerCustomTimestamps(t *testing.T) {
	fakeService := &stubDeploymentService{}
	metrics := &trackingMetrics{}
	handler := &getDeploymentsHandler{
		svc:     fakeService,
		metrics: metrics,
	}

	from := int64(1700000000)
	to := int64(1700100000)
	input := getDeploymentsInput{
		App:      ptrOf("api"),
		FromUnix: &from,
		ToUnix:   &to,
	}

	_, _, err := handler.Handle(context.Background(), nil, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fakeService.capturedFilter.App == nil || *fakeService.capturedFilter.App != "api" {
		t.Fatalf("expected app filter 'api', got %#v", fakeService.capturedFilter.App)
	}
	if fakeService.capturedFilter.FromTimestamp != from {
		t.Fatalf("expected from timestamp %d, got %d", from, fakeService.capturedFilter.FromTimestamp)
	}
	if fakeService.capturedFilter.ToTimestamp != to {
		t.Fatalf("expected to timestamp %d, got %d", to, fakeService.capturedFilter.ToTimestamp)
	}
}

func TestGetDeploymentsHandlerValidations(t *testing.T) {
	metrics := &trackingMetrics{}
	handler := &getDeploymentsHandler{
		metrics: metrics,
	}

	negativeDays := -1
	if _, _, err := handler.Handle(context.Background(), nil, getDeploymentsInput{DaysHistory: &negativeDays}); err == nil {
		t.Fatalf("expected error for negative day history")
	}

	from := int64(20)
	to := int64(10)
	if _, _, err := handler.Handle(context.Background(), nil, getDeploymentsInput{FromUnix: &from, ToUnix: &to}); err == nil {
		t.Fatalf("expected error when from > to")
	}
	if metrics.invalid != 2 {
		t.Fatalf("expected invalid metric recorded twice, got %d", metrics.invalid)
	}
}

func TestGetDeploymentsHandlerServiceError(t *testing.T) {
	wantErr := fmt.Errorf("boom")
	fakeService := &stubDeploymentService{
		err: wantErr,
	}
	metrics := &trackingMetrics{}

	handler := &getDeploymentsHandler{
		clock:   clock.FixedClock{At: time.Unix(100, 0)},
		svc:     fakeService,
		metrics: metrics,
	}

	_, _, err := handler.Handle(context.Background(), nil, getDeploymentsInput{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v, got %v", wantErr, err)
	}
	if metrics.failure != 1 {
		t.Fatalf("expected failure metric incremented once, got %d", metrics.failure)
	}
}

func TestGetDeploymentsHandlerDaysHistoryOverride(t *testing.T) {
	now := time.Unix(200, 0)
	fakeClock := clock.FixedClock{At: now}
	fakeService := &stubDeploymentService{}
	metrics := &trackingMetrics{}

	handler := &getDeploymentsHandler{
		clock:   fakeClock,
		svc:     fakeService,
		metrics: metrics,
	}

	days := 5
	if _, _, err := handler.Handle(context.Background(), nil, getDeploymentsInput{DaysHistory: &days}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantTo := now.Unix()
	wantFrom := wantTo - int64(days)*24*60*60

	if fakeService.capturedFilter.FromTimestamp != wantFrom {
		t.Fatalf("expected from timestamp %d, got %d", wantFrom, fakeService.capturedFilter.FromTimestamp)
	}
	if fakeService.capturedFilter.ToTimestamp != wantTo {
		t.Fatalf("expected to timestamp %d, got %d", wantTo, fakeService.capturedFilter.ToTimestamp)
	}
}

func TestGetDeploymentsHandlerEmitsSpan(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	orig := otel.GetTracerProvider()
	origProp := otel.GetTextMapPropagator()
	tp := tracesdk.NewTracerProvider()
	tp.RegisterSpanProcessor(spanRecorder)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(orig)
		otel.SetTextMapPropagator(origProp)
	})

	fakeService := &stubDeploymentService{
		result: []domain.Deployment{{ID: "task-1"}},
	}

	handler := &getDeploymentsHandler{
		clock:   clock.FixedClock{At: time.Unix(1, 0)},
		svc:     fakeService,
		metrics: &trackingMetrics{},
	}

	if _, _, err := handler.Handle(context.Background(), nil, getDeploymentsInput{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	spans := spanRecorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	span := spans[0]
	if got := span.Name(); got != "tool.get_deployments" {
		t.Fatalf("expected span name tool.get_deployments, got %s", got)
	}
	if got := span.SpanKind(); got != trace.SpanKindServer {
		t.Fatalf("expected server span, got %v", got)
	}

	foundResultAttr := false
	for _, attr := range span.Attributes() {
		if attr.Key == attribute.Key("argo_watcher.result_count") && attr.Value.AsInt64() == 1 {
			foundResultAttr = true
			break
		}
	}
	if !foundResultAttr {
		t.Fatalf("expected span to include result count attribute, got %+v", span.Attributes())
	}
}

func TestGetDeploymentsHandlerPropagatesTraceContext(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("4d2e3f4a5b6c7d8e9f00112233445566")
	if err != nil {
		t.Fatalf("parse trace ID: %v", err)
	}
	parentSpanID, err := trace.SpanIDFromHex("0011223344556677")
	if err != nil {
		t.Fatalf("parse span ID: %v", err)
	}
	parentCtx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     parentSpanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	}))

	headers := http.Header{}
	prop := propagation.TraceContext{}
	prop.Inject(parentCtx, propagation.HeaderCarrier(headers))

	spanRecorder := tracetest.NewSpanRecorder()
	orig := otel.GetTracerProvider()
	tp := tracesdk.NewTracerProvider()
	tp.RegisterSpanProcessor(spanRecorder)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(orig)
	})

	fakeService := &stubDeploymentService{
		result: []domain.Deployment{{ID: "task-1"}},
	}

	handler := &getDeploymentsHandler{
		clock:   clock.FixedClock{At: time.Unix(1, 0)},
		svc:     fakeService,
		metrics: &trackingMetrics{},
	}

	req := &mcp.CallToolRequest{
		Extra: &mcp.RequestExtra{
			Header: headers,
		},
	}

	if _, _, err := handler.Handle(context.Background(), req, getDeploymentsInput{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	spans := spanRecorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	span := spans[0]
	if span.SpanContext().TraceID() != traceID {
		t.Fatalf("expected span trace ID %s, got %s", traceID, span.SpanContext().TraceID())
	}
	if span.Parent().SpanID() != parentSpanID {
		t.Fatalf("expected parent span ID %s, got %s", parentSpanID, span.Parent().SpanID())
	}
}

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

func TestGetDeploymentsHandlerLogsProcessing(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	fakeService := &stubDeploymentService{
		result: []domain.Deployment{{ID: "task-1"}},
	}
	app := "api"
	handler := &getDeploymentsHandler{
		svc: fakeService,
		logger: logger.With(
			slog.String("component", "mcpserver"),
			slog.String("tool", "get_deployments"),
		),
		metrics: &trackingMetrics{},
	}

	ctx := context.Background()
	if _, _, err := handler.Handle(ctx, nil, getDeploymentsInput{
		App: &app,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	logs := buf.String()
	if !strings.Contains(logs, "get_deployments request") {
		t.Fatalf("expected request log entry, got %q", logs)
	}
	if !strings.Contains(logs, "get_deployments completed") {
		t.Fatalf("expected completion log entry, got %q", logs)
	}
	if !strings.Contains(logs, "tool=get_deployments") {
		t.Fatalf("expected tool attribute in logs, got %q", logs)
	}
}
