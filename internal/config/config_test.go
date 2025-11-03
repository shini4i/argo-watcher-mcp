package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadSuccess(t *testing.T) {
	t.Setenv("ARGO_WATCHER_URL", "https://example.com")
	t.Setenv("APP_NAME", "test-app")
	t.Setenv("APP_VERSION", "1.2.3")
	t.Setenv("HTTP_LISTEN_ADDR", "127.0.0.1:9000")
	t.Setenv("REQUEST_TIMEOUT", "30s")
	t.Setenv("ENABLE_HTTP_TRANSPORT", "false")

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
	if cfg.EnableHTTPTransport {
		t.Fatalf("expected EnableHTTPTransport false, got true")
	}
}

func TestLoadMissingRequired(t *testing.T) {
	keys := []string{"ENABLE_HTTP_TRANSPORT", "APP_NAME", "APP_VERSION", "HTTP_LISTEN_ADDR", "REQUEST_TIMEOUT"}
	for _, key := range keys {
		t.Setenv(key, "")
	}

	prev, has := os.LookupEnv("ARGO_WATCHER_URL")
	os.Unsetenv("ARGO_WATCHER_URL")
	if has {
		t.Cleanup(func() { os.Setenv("ARGO_WATCHER_URL", prev) })
	}

	if _, err := Load(); err == nil {
		t.Fatal("expected error when required ARGO_WATCHER_URL is missing")
	}
}
