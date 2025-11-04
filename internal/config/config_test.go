package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadSuccess(t *testing.T) {
	baseEnv := map[string]string{
		"ARGO_WATCHER_URL": "https://example.com",
		"APP_NAME":         "test-app",
		"APP_VERSION":      "1.2.3",
		"HTTP_LISTEN_ADDR": "127.0.0.1:9000",
		"REQUEST_TIMEOUT":  "30s",
		"TRANSPORT_MODE":   "HTTP",
	}

	testCases := []struct {
		name     string
		env      map[string]string
		expected Config
	}{
		{
			name: "defaults",
			env:  map[string]string{},
			expected: Config{
				Name:                     "test-app",
				Version:                  "1.2.3",
				HTTPListenAddr:           "127.0.0.1:9000",
				ArgoWatcherBaseURL:       "https://example.com",
				RequestTimeout:           30 * time.Second,
				TransportMode:            TransportModeHTTP,
				OtelEnabled:              true,
				OtelServiceName:          "",
				OtelExporterOtlpEndpoint: "",
			},
		},
		{
			name: "otel overrides",
			env: map[string]string{
				"OTEL_ENABLED":                "false",
				"OTEL_SERVICE_NAME":           "telemetry-test",
				"OTEL_EXPORTER_OTLP_ENDPOINT": "otel-collector:4317",
			},
			expected: Config{
				Name:                     "test-app",
				Version:                  "1.2.3",
				HTTPListenAddr:           "127.0.0.1:9000",
				ArgoWatcherBaseURL:       "https://example.com",
				RequestTimeout:           30 * time.Second,
				TransportMode:            TransportModeHTTP,
				OtelEnabled:              false,
				OtelServiceName:          "telemetry-test",
				OtelExporterOtlpEndpoint: "otel-collector:4317",
			},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			for key, value := range baseEnv {
				t.Setenv(key, value)
			}
			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}

			if cfg.Name != tc.expected.Name {
				t.Fatalf("expected Name %s, got %s", tc.expected.Name, cfg.Name)
			}
			if cfg.Version != tc.expected.Version {
				t.Fatalf("expected Version %s, got %s", tc.expected.Version, cfg.Version)
			}
			if cfg.HTTPListenAddr != tc.expected.HTTPListenAddr {
				t.Fatalf("unexpected HTTPListenAddr %s", cfg.HTTPListenAddr)
			}
			if cfg.ArgoWatcherBaseURL != tc.expected.ArgoWatcherBaseURL {
				t.Fatalf("unexpected ArgoWatcherBaseURL %s", cfg.ArgoWatcherBaseURL)
			}
			if cfg.RequestTimeout != tc.expected.RequestTimeout {
				t.Fatalf("expected RequestTimeout %s, got %s", tc.expected.RequestTimeout, cfg.RequestTimeout)
			}
			if cfg.TransportMode != tc.expected.TransportMode {
				t.Fatalf("expected TransportMode %s, got %s", tc.expected.TransportMode, cfg.TransportMode)
			}
			if cfg.OtelEnabled != tc.expected.OtelEnabled {
				t.Fatalf("expected OtelEnabled %t, got %t", tc.expected.OtelEnabled, cfg.OtelEnabled)
			}
			if cfg.OtelServiceName != tc.expected.OtelServiceName {
				t.Fatalf("expected OtelServiceName %q, got %q", tc.expected.OtelServiceName, cfg.OtelServiceName)
			}
			if cfg.OtelExporterOtlpEndpoint != tc.expected.OtelExporterOtlpEndpoint {
				t.Fatalf("expected OtelExporterOtlpEndpoint %q, got %q", tc.expected.OtelExporterOtlpEndpoint, cfg.OtelExporterOtlpEndpoint)
			}
		})
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
