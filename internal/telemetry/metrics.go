package telemetry

import (
	"context"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MCPRequestMetrics records request outcomes for the MCP tool surface.
type MCPRequestMetrics interface {
	// RecordSuccess tracks a successfully processed request.
	RecordSuccess(ctx context.Context)
	// RecordInvalid tracks a request rejected due to validation errors.
	RecordInvalid(ctx context.Context)
	// RecordFailure tracks a request that failed while interacting with downstream services.
	RecordFailure(ctx context.Context)
}

// NewMCPRequestMetrics creates counters for MCP request tracking.
func NewMCPRequestMetrics() (MCPRequestMetrics, error) {
	meter := otel.GetMeterProvider().Meter("github.com/shini4i/argo-watcher-mcp/mcpserver")

	total, err := meter.Int64Counter(
		"argo_watcher_mcp_requests_total",
		metric.WithDescription("Total MCP tool requests processed by argo-watcher-mcp, partitioned by result."),
	)
	if err != nil {
		return nil, err
	}

	return &mcpRequestMetrics{
		total:       total,
		successAttr: attribute.String("result", "success"),
		invalidAttr: attribute.String("result", "invalid"),
		failedAttr:  attribute.String("result", "failed"),
	}, nil
}

// NoopMCPRequestMetrics returns a recorder that discards all measurements.
func NoopMCPRequestMetrics() MCPRequestMetrics {
	return noopMCPRequestMetrics{}
}

type mcpRequestMetrics struct {
	total       metric.Int64Counter
	successAttr attribute.KeyValue
	invalidAttr attribute.KeyValue
	failedAttr  attribute.KeyValue
}

func (m *mcpRequestMetrics) RecordSuccess(ctx context.Context) {
	m.total.Add(ctx, 1, metric.WithAttributes(m.successAttr))
}

func (m *mcpRequestMetrics) RecordInvalid(ctx context.Context) {
	m.total.Add(ctx, 1, metric.WithAttributes(m.invalidAttr))
}

func (m *mcpRequestMetrics) RecordFailure(ctx context.Context) {
	m.total.Add(ctx, 1, metric.WithAttributes(m.failedAttr))
}

type noopMCPRequestMetrics struct{}

func (noopMCPRequestMetrics) RecordSuccess(context.Context) {}
func (noopMCPRequestMetrics) RecordInvalid(context.Context) {}
func (noopMCPRequestMetrics) RecordFailure(context.Context) {}

// ArgoWatcherReachability reports whether the downstream Argo Watcher instance is reachable.
type ArgoWatcherReachability interface {
	// ReportReachable marks the downstream service as reachable.
	ReportReachable()
	// ReportUnreachable marks the downstream service as unreachable.
	ReportUnreachable()
}

// NewArgoWatcherReachability establishes an observable gauge that reports downstream reachability.
func NewArgoWatcherReachability() (ArgoWatcherReachability, error) {
	meter := otel.GetMeterProvider().Meter("github.com/shini4i/argo-watcher-mcp/argowatcher")

	gauge, err := meter.Int64ObservableGauge(
		"argo_watcher_reachable",
		metric.WithDescription("Reachability of the downstream Argo Watcher instance (1 reachable, 0 unreachable)."),
	)
	if err != nil {
		return nil, err
	}

	recorder := &argoWatcherReachability{
		gauge: gauge,
	}

	registration, err := meter.RegisterCallback(recorder.observe, gauge)
	if err != nil {
		return nil, err
	}
	recorder.registration = registration

	return recorder, nil
}

// NoopArgoWatcherReachability returns a reachability recorder that ignores all updates.
func NoopArgoWatcherReachability() ArgoWatcherReachability {
	return noopArgoWatcherReachability{}
}

type argoWatcherReachability struct {
	gauge        metric.Int64ObservableGauge
	state        atomic.Int64
	registration metric.Registration
}

func (r *argoWatcherReachability) observe(ctx context.Context, observer metric.Observer) error {
	observer.ObserveInt64(r.gauge, r.state.Load())
	return nil
}

func (r *argoWatcherReachability) ReportReachable() {
	r.state.Store(1)
}

func (r *argoWatcherReachability) ReportUnreachable() {
	r.state.Store(0)
}

type noopArgoWatcherReachability struct{}

func (noopArgoWatcherReachability) ReportReachable()   {}
func (noopArgoWatcherReachability) ReportUnreachable() {}
