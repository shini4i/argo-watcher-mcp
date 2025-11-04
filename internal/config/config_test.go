package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadSuccess(t *testing.T) {
	t.Setenv("ARGO_WATCHER_URL", "https://example.com")
	t.Setenv("APP_NAME", "test-app")
	t.Setenv("APP_VERSION", "1.2.3")
	t.Setenv("HTTP_LISTEN_ADDR", "127.0.0.1:9000")
	t.Setenv("REQUEST_TIMEOUT", "30s")
	t.Setenv("TRANSPORT_MODE", "HTTP")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Name != "test-app" {
		t.Fatalf("expected Name test-app, got %s", cfg.Name)
	}
	if cfg.Version != "1.2.3" {
		t.Fatalf("expected Version 1.2.3, got %s", cfg.Version)
	}
	if cfg.HTTPListenAddr != "127.0.0.1:9000" {
		t.Fatalf("unexpected HTTPListenAddr %s", cfg.HTTPListenAddr)
	}
	if cfg.ArgoWatcherBaseURL != "https://example.com" {
		t.Fatalf("unexpected ArgoWatcherBaseURL %s", cfg.ArgoWatcherBaseURL)
	}
	if cfg.RequestTimeout != 30*time.Second {
		t.Fatalf("expected RequestTimeout 30s, got %s", cfg.RequestTimeout)
	}
	if cfg.TransportMode != TransportModeHTTP {
		t.Fatalf("expected TransportMode http, got %s", cfg.TransportMode)
	}
}

func TestLoadMissingArgoWatcherURL(t *testing.T) {
	t.Setenv("APP_NAME", "test-app")
	t.Setenv("APP_VERSION", "1.2.3")
	t.Setenv("HTTP_LISTEN_ADDR", "127.0.0.1:9000")
	t.Setenv("REQUEST_TIMEOUT", "30s")
	t.Setenv("TRANSPORT_MODE", "stdio")

	prev, has := os.LookupEnv("ARGO_WATCHER_URL")
	os.Unsetenv("ARGO_WATCHER_URL")
	if has {
		t.Cleanup(func() { os.Setenv("ARGO_WATCHER_URL", prev) })
	}

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when ARGO_WATCHER_URL is missing")
	}
	if !strings.Contains(err.Error(), "ARGO_WATCHER_URL") {
		t.Fatalf("expected error to mention missing ARGO_WATCHER_URL, got %v", err)
	}
}

func TestLoadInvalidTransportMode(t *testing.T) {
	t.Setenv("ARGO_WATCHER_URL", "https://example.com")
	t.Setenv("TRANSPORT_MODE", "gRPC")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid TRANSPORT_MODE")
	}
	if !strings.Contains(err.Error(), "invalid transport mode") {
		t.Fatalf("expected invalid transport mode error, got %v", err)
	}
}
