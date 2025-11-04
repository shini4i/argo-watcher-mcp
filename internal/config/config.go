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

// Config aggregates environment-driven settings for the server.
type Config struct {
	// Name identifies the MCP server instance.
	Name string `env:"APP_NAME" envDefault:"argo-watcher-mcp"`

	// Version is reported via MCP metadata.
	Version string `env:"APP_VERSION" envDefault:"0.0.1-dev"`

	// HTTPListenAddr is the address the HTTP server binds to.
	HTTPListenAddr string `env:"HTTP_LISTEN_ADDR" envDefault:":8000"`

	// ArgoWatcherBaseURL points to the upstream Argo Watcher service.
	ArgoWatcherBaseURL string `env:"ARGO_WATCHER_URL,required"`

	// RequestTimeout bounds outbound calls to Argo Watcher.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"15s"`

	// TransportMode selects which transport the MCP server exposes.
	// Supported values: "stdio" and "http".
	TransportMode string `env:"TRANSPORT_MODE" envDefault:"stdio"`
}

// Load parses environment variables into Config while applying defaults.
func Load() (Config, error) {
	var cfg Config

	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
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
