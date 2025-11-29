package telemetry

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shini4i/argo-watcher-mcp/internal/config"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func TestNewProviderDisabled(t *testing.T) {
	cfg := config.Config{OtelEnabled: false}

	provider, err := NewProvider(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	provider.PrometheusHandler.ServeHTTP(rec, req)

	if rec.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when telemetry disabled, got %d", rec.Result().StatusCode)
	}

	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("expected no shutdown error, got %v", err)
	}
}

func TestNewProviderPrometheusOnly(t *testing.T) {
	origTracer := otel.GetTracerProvider()
	origMeter := otel.GetMeterProvider()
	t.Cleanup(func() {
		otel.SetTracerProvider(origTracer)
		otel.SetMeterProvider(origMeter)
	})

	cfg := config.Config{
		OtelEnabled: true,
		Name:        "test",
		Version:     "v0.1.0",
	}

	provider, err := NewProvider(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	provider.PrometheusHandler.ServeHTTP(rec, req)

	if rec.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from prometheus handler, got %d", rec.Result().StatusCode)
	}

	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("failed reading response body: %v", err)
	}
	if !strings.Contains(string(body), "# HELP") {
		t.Fatalf("expected prometheus exposition format, got: %s", string(body))
	}
}

func TestNewProviderWithOTLPEndpointInsecure(t *testing.T) {
	origTracer := otel.GetTracerProvider()
	origMeter := otel.GetMeterProvider()
	t.Cleanup(func() {
		otel.SetTracerProvider(origTracer)
		otel.SetMeterProvider(origMeter)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lis := bufconn.Listen(1024 * 1024)
	t.Cleanup(func() { _ = lis.Close() })

	server := grpc.NewServer()
	t.Cleanup(server.Stop)

	go func() {
		_ = server.Serve(lis)
	}()

	originalDial := dialOTLP
	t.Cleanup(func() { dialOTLP = originalDial })

	var dialed bool
	dialOTLP = func(ctx context.Context, target string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
		dialed = true
		opts = append(opts, grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}))
		return grpc.DialContext(ctx, target, opts...)
	}

	cfg := config.Config{
		OtelEnabled:              true,
		Name:                     "test",
		Version:                  "dev",
		OtelExporterOtlpEndpoint: "bufconn",
		OtelExporterOtlpInsecure: true,
	}

	provider, err := NewProvider(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("NewProvider returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	if !dialed {
		t.Fatal("expected OTLP endpoint to be dialed")
	}
}

func TestOTLPTransportCredentials(t *testing.T) {
	t.Run("secure by default", func(t *testing.T) {
		creds := otlpTransportCredentials(config.Config{})

		info := creds.Info()
		if !strings.EqualFold(info.SecurityProtocol, "tls") {
			t.Fatalf("expected TLS security protocol, got %q", info.SecurityProtocol)
		}
	})

	t.Run("insecure opt-in", func(t *testing.T) {
		creds := otlpTransportCredentials(config.Config{OtelExporterOtlpInsecure: true})
		if !strings.EqualFold(creds.Info().SecurityProtocol, "insecure") {
			t.Fatalf("expected insecure security protocol, got %q", creds.Info().SecurityProtocol)
		}
	})
}
