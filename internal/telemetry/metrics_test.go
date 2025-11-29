package telemetry

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestMCPRequestMetricsRecordsCounters(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	origProvider := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(origProvider) })
	otel.SetMeterProvider(provider)

	recorder, err := NewMCPRequestMetrics()
	require.NoError(t, err)

	recorder.RecordSuccess(ctx)
	recorder.RecordInvalid(ctx)
	recorder.RecordFailure(ctx)

	rm := metricdata.ResourceMetrics{}
	require.NoError(t, reader.Collect(ctx, &rm))

	got := map[string]int64{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "argo_watcher_mcp_requests_total" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			require.Len(t, sum.DataPoints, 3)
			for _, dp := range sum.DataPoints {
				val, ok := dp.Attributes.Value(attribute.Key("result"))
				require.True(t, ok)
				got[val.AsString()] = dp.Value
			}
		}
	}

	want := map[string]int64{"success": 1, "invalid": 1, "failed": 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("unexpected counter results (-want +got):\n%s", diff)
	}
}

func TestNoopMCPRequestMetrics(t *testing.T) {
	require.NotPanics(t, func() {
		recorder := NoopMCPRequestMetrics()
		recorder.RecordSuccess(context.Background())
		recorder.RecordInvalid(context.Background())
		recorder.RecordFailure(context.Background())
	})
}

func TestArgoWatcherReachabilityGauge(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	origProvider := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(origProvider) })
	otel.SetMeterProvider(provider)

	tracker, err := NewArgoWatcherReachability()
	require.NoError(t, err)

	assertGaugeValue := func(expected int64) {
		rm := metricdata.ResourceMetrics{}
		require.NoError(t, reader.Collect(ctx, &rm))
		var found bool
		for _, scope := range rm.ScopeMetrics {
			for _, m := range scope.Metrics {
				if m.Name != "argo_watcher_reachable" {
					continue
				}
				gauge, ok := m.Data.(metricdata.Gauge[int64])
				require.True(t, ok)
				require.Len(t, gauge.DataPoints, 1)
				require.Equal(t, expected, gauge.DataPoints[0].Value)
				found = true
			}
		}
		require.True(t, found, "expected gauge metric to be present")
	}

	assertGaugeValue(0)

	tracker.ReportReachable()
	assertGaugeValue(1)

	tracker.ReportUnreachable()
	assertGaugeValue(0)
}

func TestNoopArgoWatcherReachability(t *testing.T) {
	require.NotPanics(t, func() {
		tracker := NoopArgoWatcherReachability()
		tracker.ReportReachable()
		tracker.ReportUnreachable()
	})
}

func TestNewMCPRequestMetricsCounterFailure(t *testing.T) {
	origProvider := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(origProvider) })
	otel.SetMeterProvider(failingMeterProvider{
		MeterProvider: noop.NewMeterProvider(),
		meter:         failingCounterMeter{},
	})

	recorder, err := NewMCPRequestMetrics()
	require.Error(t, err)
	require.Nil(t, recorder)
}

func TestNewArgoWatcherReachabilityGaugeFailure(t *testing.T) {
	origProvider := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(origProvider) })
	otel.SetMeterProvider(failingMeterProvider{
		MeterProvider: noop.NewMeterProvider(),
		meter:         failingGaugeMeter{},
	})

	tracker, err := NewArgoWatcherReachability()
	require.Error(t, err)
	require.Nil(t, tracker)
}

func TestNewArgoWatcherReachabilityRegistrationFailure(t *testing.T) {
	origProvider := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(origProvider) })
	otel.SetMeterProvider(failingMeterProvider{
		MeterProvider: noop.NewMeterProvider(),
		meter:         failingRegistrationMeter{},
	})

	tracker, err := NewArgoWatcherReachability()
	require.Error(t, err)
	require.Nil(t, tracker)
}

type failingMeterProvider struct {
	noop.MeterProvider
	meter metric.Meter
}

func (f failingMeterProvider) Meter(string, ...metric.MeterOption) metric.Meter {
	return f.meter
}

type failingCounterMeter struct {
	noop.Meter
}

func (f failingCounterMeter) Int64Counter(string, ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	return noop.Int64Counter{}, errors.New("counter failure")
}

type failingGaugeMeter struct {
	noop.Meter
}

func (f failingGaugeMeter) Int64ObservableGauge(string, ...metric.Int64ObservableGaugeOption) (metric.Int64ObservableGauge, error) {
	return noop.Int64ObservableGauge{}, errors.New("gauge failure")
}

type failingRegistrationMeter struct {
	noop.Meter
}

func (f failingRegistrationMeter) RegisterCallback(metric.Callback, ...metric.Observable) (metric.Registration, error) {
	return noop.Registration{}, errors.New("registration failure")
}
