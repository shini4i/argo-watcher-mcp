package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

const (
	// TransportModeStdio runs the MCP server using the stdio transport.
	TransportModeStdio = "stdio"
	// TransportModeHTTP runs the MCP server using the HTTP/SSE transport.
	TransportModeHTTP = "http"
)

// defaultVersion is reported when APP_VERSION is not set.
//
// Release builds overwrite it at link time, so a released binary identifies
// itself correctly without the operator having to set anything:
//
//	-ldflags "-X github.com/shini4i/argo-watcher-mcp/internal/config.defaultVersion=1.2.3"
//
// It must stay a var rather than a const, and cannot move into an `envDefault`
// struct tag, because the linker can only patch variables.
var defaultVersion = "0.0.1-dev"

// Config aggregates environment-driven settings for the server.
type Config struct {
	// Name identifies the MCP server instance.
	Name string `env:"APP_NAME" envDefault:"argo-watcher-mcp"`

	// Version is reported via MCP metadata. Defaults to the version stamped in
	// at build time; see defaultVersion.
	Version string `env:"APP_VERSION"`

	// HTTPListenAddr is the address the HTTP server binds to.
	HTTPListenAddr string `env:"HTTP_LISTEN_ADDR" envDefault:":8000"`

	// ArgoWatcherBaseURL points to the upstream Argo Watcher service.
	ArgoWatcherBaseURL string `env:"ARGO_WATCHER_URL,required"`

	// RequestTimeout bounds outbound calls to Argo Watcher.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"15s"`

	// TransportMode selects which transport the MCP server exposes.
	// Supported values: "stdio" and "http".
	TransportMode string `env:"TRANSPORT_MODE" envDefault:"stdio"`

	// OtelEnabled controls whether telemetry instrumentation is initialized.
	OtelEnabled bool `env:"OTEL_ENABLED" envDefault:"true"`

	// OtelServiceName controls the OpenTelemetry service.name resource attribute.
	OtelServiceName string `env:"OTEL_SERVICE_NAME"`

	// OtelExporterOtlpEndpoint controls the OTLP gRPC endpoint used for traces and metrics.
	OtelExporterOtlpEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`

	// OtelExporterOtlpInsecure allows opting into plaintext OTLP connections. Defaults to secure transport.
	OtelExporterOtlpInsecure bool `env:"OTEL_EXPORTER_OTLP_INSECURE" envDefault:"false"`
}

// Load parses environment variables into Config while applying defaults.
func Load() (Config, error) {
	var cfg Config

	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}

	// An unset or empty APP_VERSION falls back to the build-time version rather
	// than reporting an empty string to MCP clients.
	if cfg.Version == "" {
		cfg.Version = defaultVersion
	}

	cfg.TransportMode = strings.ToLower(cfg.TransportMode)
	switch cfg.TransportMode {
	case TransportModeHTTP, TransportModeStdio:
	default:
		return Config{}, fmt.Errorf(
			"invalid transport mode %q: must be %q or %q",
			cfg.TransportMode,
			TransportModeStdio,
			TransportModeHTTP,
		)
	}

	return cfg, nil
}
