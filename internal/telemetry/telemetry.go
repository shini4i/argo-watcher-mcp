package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	clientprom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/shini4i/argo-watcher-mcp/internal/config"
)

// Provider owns the telemetry exporters and their lifecycle hooks.
type Provider struct {
	// PrometheusHandler exposes the aggregated metrics in Prometheus format.
	PrometheusHandler http.Handler
	// Shutdown terminates the telemetry providers and exporters gracefully.
	Shutdown func(context.Context) error
}

// NewProvider wires OpenTelemetry instrumentation based on the supplied config.
func NewProvider(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Provider, error) {
	if !cfg.OtelEnabled {
		return &Provider{
			PrometheusHandler: http.HandlerFunc(http.NotFound),
			Shutdown: func(context.Context) error {
				return nil
			},
		}, nil
	}

	if logger == nil {
		logger = slog.Default()
	}

	serviceName := strings.TrimSpace(cfg.OtelServiceName)
	if serviceName == "" {
		serviceName = cfg.Name
	}

	res, err := resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(cfg.Version),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	registry := clientprom.NewRegistry()
	promExporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("create prometheus exporter: %w", err)
	}
	promHandler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})

	var shutdowns []func(context.Context) error
	runShutdowns := func(ctx context.Context) error {
		var errs []error
		for i := len(shutdowns) - 1; i >= 0; i-- {
			if err := shutdowns[i](ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}

	meterProviderOpts := []sdkmetric.Option{
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(promExporter),
	}

	tracerProviderOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
	}

	endpoint := strings.TrimSpace(cfg.OtelExporterOtlpEndpoint)
	if endpoint != "" {
		conn, err := grpc.DialContext(ctx, endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			return nil, fmt.Errorf("dial otlp endpoint %q: %w", endpoint, err)
		}
		shutdowns = append(shutdowns, func(context.Context) error {
			return conn.Close()
		})

		metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithGRPCConn(conn))
		if err != nil {
			if shutdownErr := runShutdowns(context.Background()); shutdownErr != nil {
				logger.Error("failed to clean up after OTLP metric exporter error", "err", shutdownErr)
			}
			return nil, fmt.Errorf("create otlp metric exporter: %w", err)
		}
		shutdowns = append(shutdowns, metricExporter.Shutdown)
		meterProviderOpts = append(meterProviderOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)))

		traceExporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
		if err != nil {
			if shutdownErr := runShutdowns(context.Background()); shutdownErr != nil {
				logger.Error("failed to clean up after OTLP trace exporter error", "err", shutdownErr)
			}
			return nil, fmt.Errorf("create otlp trace exporter: %w", err)
		}
		shutdowns = append(shutdowns, traceExporter.Shutdown)
		tracerProviderOpts = append(tracerProviderOpts, sdktrace.WithBatcher(traceExporter))
	} else {
		logger.Debug("OTLP endpoint not configured; prometheus exporter only")
	}

	meterProvider := sdkmetric.NewMeterProvider(meterProviderOpts...)
	shutdowns = append(shutdowns, meterProvider.Shutdown)

	tracerProvider := sdktrace.NewTracerProvider(tracerProviderOpts...)
	shutdowns = append(shutdowns, tracerProvider.Shutdown)

	otel.SetMeterProvider(meterProvider)
	otel.SetTracerProvider(tracerProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &Provider{
		PrometheusHandler: promHandler,
		Shutdown: func(ctx context.Context) error {
			return runShutdowns(ctx)
		},
	}, nil
}
