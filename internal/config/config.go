package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
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
	ArgoWatcherBaseURL string `env:"ARGO_WATCHER_URL" envRequired:"true"`

	// RequestTimeout bounds outbound calls to Argo Watcher.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"15s"`

	// EnableHTTPTransport toggles the HTTP/SSE transport. Stdio remains enabled by default.
	EnableHTTPTransport bool `env:"ENABLE_HTTP_TRANSPORT" envDefault:"true"`
}

// Load parses environment variables into Config while applying defaults.
func Load() (Config, error) {
	var cfg Config

	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}

	if cfg.ArgoWatcherBaseURL == "" {
		return Config{}, fmt.Errorf("parse environment: ARGO_WATCHER_URL is required")
	}

	return cfg, nil
}
