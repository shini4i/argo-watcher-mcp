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

// stubArgoWatcher implements domain.ArgoWatcher, recording the filter it was
// called with so tests can assert on request translation.
type stubArgoWatcher struct {
	capturedFilter domain.DeploymentFilter
	result         []domain.Deployment
	total          int64
	truncated      bool
	err            error

	lock         domain.DeployLockState
	lockErr      error
	reachability domain.Reachability
	reachErr     error
	info         domain.ServerInfo
	infoErr      error
}

// ListDeployments returns exactly what the test configured. It deliberately does
// not synthesise Total from len(result): the handler's job on the response side
// is verbatim pass-through, and a stub that fills in a plausible total would hide
// both a handler that recomputed it and the Total=0-with-rows case.
func (s *stubArgoWatcher) ListDeployments(_ context.Context, filter domain.DeploymentFilter) (domain.DeploymentPage, error) {
	s.capturedFilter = filter
	if s.err != nil {
		return domain.DeploymentPage{}, s.err
	}
	return domain.DeploymentPage{
		Deployments: s.result,
		Total:       s.total,
		Limit:       filter.Limit,
		Offset:      filter.Offset,
		Truncated:   s.truncated,
	}, nil
}

func (s *stubArgoWatcher) GetDeployLock(context.Context) (domain.DeployLockState, error) {
	return s.lock, s.lockErr
}

func (s *stubArgoWatcher) GetReachability(context.Context) (domain.Reachability, error) {
	return s.reachability, s.reachErr
}

func (s *stubArgoWatcher) GetServerInfo(context.Context) (domain.ServerInfo, error) {
	return s.info, s.infoErr
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
	fakeService := &stubArgoWatcher{
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

	page, ok := out.(domain.DeploymentPage)
	if !ok {
		t.Fatalf("expected domain.DeploymentPage, got %T", out)
	}
	if len(page.Deployments) != 1 || page.Deployments[0].ID != "task-1" {
		t.Fatalf("unexpected deployments result: %#v", page.Deployments)
	}

	wantTo := now.Unix()
	wantFrom := wantTo - 30*24*60*60

	if fakeService.capturedFilter.FromTimestamp != wantFrom {
		t.Fatalf("expected from_timestamp %d, got %d", wantFrom, fakeService.capturedFilter.FromTimestamp)
	}
	if fakeService.capturedFilter.ToTimestamp != wantTo {
		t.Fatalf("expected to_timestamp %d, got %d", wantTo, fakeService.capturedFilter.ToTimestamp)
	}
	if fakeService.capturedFilter.Limit != defaultDeploymentLimit {
		t.Fatalf("expected default limit %d, got %d", defaultDeploymentLimit, fakeService.capturedFilter.Limit)
	}
	if fakeService.capturedFilter.Offset != 0 {
		t.Fatalf("expected default offset 0, got %d", fakeService.capturedFilter.Offset)
	}
	if metrics.success != 1 {
		t.Fatalf("expected success metric to record 1, got %d", metrics.success)
	}
}

func TestGetDeploymentsHandlerCustomTimestamps(t *testing.T) {
	fakeService := &stubArgoWatcher{}
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
	fakeService := &stubArgoWatcher{
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
	fakeService := &stubArgoWatcher{}
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

	fakeService := &stubArgoWatcher{
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

	fakeService := &stubArgoWatcher{
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
		Service: &stubArgoWatcher{},
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
		Service: &stubArgoWatcher{
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

	fakeService := &stubArgoWatcher{
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

func TestGetDeploymentsHandlerPassesFiltersThrough(t *testing.T) {
	fakeService := &stubArgoWatcher{}
	handler := &getDeploymentsHandler{
		clock:   clock.FixedClock{At: time.Unix(1_000_000, 0)},
		svc:     fakeService,
		metrics: &trackingMetrics{},
	}

	limit := 200
	offset := 400
	if _, _, err := handler.Handle(context.Background(), nil, getDeploymentsInput{
		Status: ptrOf("failed"),
		Limit:  &limit,
		Offset: &offset,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := fakeService.capturedFilter
	if got.Status == nil || *got.Status != "failed" {
		t.Fatalf("expected status filter 'failed', got %#v", got.Status)
	}
	if got.Limit != limit {
		t.Fatalf("expected limit %d, got %d", limit, got.Limit)
	}
	if got.Offset != offset {
		t.Fatalf("expected offset %d, got %d", offset, got.Offset)
	}
}

// A rejected limit must name the real ceiling rather than being silently
// clamped, which is the failure mode this tool exists to remove.
func TestGetDeploymentsHandlerPaginationValidation(t *testing.T) {
	testCases := []struct {
		name  string
		input getDeploymentsInput
		want  string
	}{
		{name: "zeroLimit", input: getDeploymentsInput{Limit: ptrOf(0)}, want: "limit must be positive"},
		{name: "negativeLimit", input: getDeploymentsInput{Limit: ptrOf(-5)}, want: "limit must be positive"},
		{name: "limitAboveCap", input: getDeploymentsInput{Limit: ptrOf(maxDeploymentLimit + 1)}, want: "must not exceed 1000"},
		{name: "negativeOffset", input: getDeploymentsInput{Offset: ptrOf(-1)}, want: "offset must be non-negative"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeService := &stubArgoWatcher{}
			metrics := &trackingMetrics{}
			handler := &getDeploymentsHandler{
				clock:   clock.FixedClock{At: time.Unix(1_000_000, 0)},
				svc:     fakeService,
				metrics: metrics,
			}

			_, _, err := handler.Handle(context.Background(), nil, tc.input)
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error mentioning %q, got %v", tc.want, err)
			}
			if metrics.invalid != 1 {
				t.Fatalf("expected invalid metric recorded once, got %d", metrics.invalid)
			}
			if fakeService.capturedFilter.Limit != 0 {
				t.Fatal("expected the upstream call to be skipped on invalid input")
			}
		})
	}
}

func TestGetDeploymentsHandlerLimitAtCapIsAccepted(t *testing.T) {
	fakeService := &stubArgoWatcher{}
	handler := &getDeploymentsHandler{
		clock:   clock.FixedClock{At: time.Unix(1_000_000, 0)},
		svc:     fakeService,
		metrics: &trackingMetrics{},
	}

	if _, _, err := handler.Handle(context.Background(), nil, getDeploymentsInput{
		Limit: ptrOf(maxDeploymentLimit),
	}); err != nil {
		t.Fatalf("expected the cap itself to be accepted, got %v", err)
	}
	if fakeService.capturedFilter.Limit != maxDeploymentLimit {
		t.Fatalf("expected limit %d, got %d", maxDeploymentLimit, fakeService.capturedFilter.Limit)
	}
}

func TestGetDeployLockHandler(t *testing.T) {
	fakeService := &stubArgoWatcher{lock: domain.DeployLockState{Locked: true}}
	metrics := &trackingMetrics{}
	handler := &getDeployLockHandler{svc: fakeService, metrics: metrics}

	_, out, err := handler.Handle(context.Background(), nil, noInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state, ok := out.(domain.DeployLockState)
	if !ok {
		t.Fatalf("expected domain.DeployLockState, got %T", out)
	}
	if !state.Locked {
		t.Fatal("expected locked=true")
	}
	if metrics.success != 1 {
		t.Fatalf("expected success metric recorded once, got %d", metrics.success)
	}
}

func TestGetReachabilityHandler(t *testing.T) {
	fakeService := &stubArgoWatcher{
		reachability: domain.Reachability{Available: false, Reason: "state backend"},
	}
	metrics := &trackingMetrics{}
	handler := &getReachabilityHandler{svc: fakeService, metrics: metrics}

	_, out, err := handler.Handle(context.Background(), nil, noInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := out.(domain.Reachability)
	if !ok {
		t.Fatalf("expected domain.Reachability, got %T", out)
	}
	if got.Available || got.Reason != "state backend" {
		t.Fatalf("unexpected reachability: %#v", got)
	}
	if metrics.success != 1 {
		t.Fatalf("expected success metric recorded once, got %d", metrics.success)
	}
}

func TestGetServerInfoHandler(t *testing.T) {
	fakeService := &stubArgoWatcher{
		info: domain.ServerInfo{
			Version: "2.0.0",
			Config:  map[string]any{"state_type": "postgres"},
		},
	}
	metrics := &trackingMetrics{}
	handler := &getServerInfoHandler{svc: fakeService, metrics: metrics}

	_, out, err := handler.Handle(context.Background(), nil, noInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	info, ok := out.(domain.ServerInfo)
	if !ok {
		t.Fatalf("expected domain.ServerInfo, got %T", out)
	}
	if info.Version != "2.0.0" || info.Config["state_type"] != "postgres" {
		t.Fatalf("unexpected server info: %#v", info)
	}
	if metrics.success != 1 {
		t.Fatalf("expected success metric recorded once, got %d", metrics.success)
	}
}

func TestNoArgumentHandlersRecordFailures(t *testing.T) {
	wantErr := errors.New("upstream down")
	metrics := &trackingMetrics{}
	service := &stubArgoWatcher{lockErr: wantErr, reachErr: wantErr, infoErr: wantErr}

	handlers := map[string]func() error{
		"get_deploy_lock": func() error {
			_, _, err := (&getDeployLockHandler{svc: service, metrics: metrics}).Handle(context.Background(), nil, noInput{})
			return err
		},
		"get_reachability": func() error {
			_, _, err := (&getReachabilityHandler{svc: service, metrics: metrics}).Handle(context.Background(), nil, noInput{})
			return err
		},
		"get_server_info": func() error {
			_, _, err := (&getServerInfoHandler{svc: service, metrics: metrics}).Handle(context.Background(), nil, noInput{})
			return err
		},
	}

	for name, call := range handlers {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, wantErr) {
				t.Fatalf("expected %v, got %v", wantErr, err)
			}
		})
	}

	if metrics.failure != len(handlers) {
		t.Fatalf("expected %d failures recorded, got %d", len(handlers), metrics.failure)
	}
}

// Every tool must be advertised over tools/list, or a client can never call it.
func TestAllToolsAreRegistered(t *testing.T) {
	srv, err := New(Options{Service: &stubArgoWatcher{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.MCP().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Wait() })

	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "c"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = clientSession.Wait()
	})

	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}

	found := make(map[string]string, len(listed.Tools))
	for _, tool := range listed.Tools {
		found[tool.Name] = tool.Description
	}

	for _, want := range []string{"get_deployments", "get_deploy_lock", "get_reachability", "get_server_info"} {
		description, ok := found[want]
		if !ok {
			t.Fatalf("tool %q is not advertised, got %v", want, found)
		}
		if description == "" {
			t.Fatalf("tool %q has no description", want)
		}
	}

	// This server wraps only GET endpoints, so every tool must declare itself
	// read-only. A tool missing the hint is the signal that a write crept in.
	for _, tool := range listed.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %q does not advertise ReadOnlyHint", tool.Name)
		}
	}
}

// The handler must not recompute or repair the page it gets back. A Total of 0
// alongside a returned deployment is exactly the shape an unsupported upstream
// produces, and the handler's job is to report it, not to paper over it.
func TestGetDeploymentsHandlerReturnsPageVerbatim(t *testing.T) {
	fakeService := &stubArgoWatcher{
		result:    []domain.Deployment{{ID: "task-1"}},
		total:     0,
		truncated: false,
	}
	handler := &getDeploymentsHandler{
		clock:   clock.FixedClock{At: time.Unix(1_000_000, 0)},
		svc:     fakeService,
		metrics: &trackingMetrics{},
	}

	_, out, err := handler.Handle(context.Background(), nil, getDeploymentsInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	page := out.(domain.DeploymentPage)
	if page.Total != 0 {
		t.Fatalf("expected Total passed through as 0, got %d", page.Total)
	}
	if len(page.Deployments) != 1 {
		t.Fatalf("expected the single deployment passed through, got %d", len(page.Deployments))
	}
	if page.Truncated {
		t.Fatal("expected Truncated passed through as false")
	}
	if page.Limit != defaultDeploymentLimit {
		t.Fatalf("expected Limit %d echoed, got %d", defaultDeploymentLimit, page.Limit)
	}
}

// When the caller already has a recording span, the handler must attach to it
// and leave ending it to the caller. Ending it here would truncate the parent
// trace; failing to reuse it would detach the tool call from its request.
func TestGetDeploymentsHandlerReusesCallerSpan(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	orig := otel.GetTracerProvider()
	tp := tracesdk.NewTracerProvider()
	tp.RegisterSpanProcessor(spanRecorder)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(orig)
	})

	ctx, parent := otel.Tracer("test").Start(context.Background(), "caller")

	handler := &getDeploymentsHandler{
		clock:   clock.FixedClock{At: time.Unix(1, 0)},
		svc:     &stubArgoWatcher{},
		metrics: &trackingMetrics{},
	}
	if _, _, err := handler.Handle(ctx, nil, getDeploymentsInput{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := len(spanRecorder.Ended()); got != 0 {
		t.Fatalf("expected the handler to end no span while the caller's is recording, got %d", got)
	}
	if !parent.IsRecording() {
		t.Fatal("expected the caller's span to still be recording after Handle returned")
	}

	parent.End()
	if got := len(spanRecorder.Ended()); got != 1 {
		t.Fatalf("expected exactly the caller's span to end, got %d", got)
	}
}
