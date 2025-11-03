package httpserver

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/shini4i/argo-watcher-mcp/internal/domain"
)

// NewRouter wires health endpoints and optionally the MCP handler.
func NewRouter(logger *slog.Logger, checker domain.HealthChecker, mcpHandler http.Handler, enableMCP bool) http.Handler {
	r := chi.NewRouter()

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(logger, w, http.StatusOK, map[string]string{"status": "up"})
	})

	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if checker == nil {
			writeJSON(logger, w, http.StatusServiceUnavailable, map[string]string{
				"status": "unknown",
				"error":  "no health checker configured",
			})
			return
		}

		if err := checker.Check(r.Context()); err != nil {
			writeJSON(logger, w, http.StatusServiceUnavailable, map[string]string{
				"status": "not_ready",
				"error":  err.Error(),
			})
			return
		}

		writeJSON(logger, w, http.StatusOK, map[string]string{"status": "ready"})
	})

	if enableMCP && mcpHandler != nil {
		r.Mount("/", mcpHandler)
	} else {
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(logger, w, http.StatusNotFound, map[string]string{"error": "not found"})
		})
	}

	return r
}

func writeJSON(logger *slog.Logger, w http.ResponseWriter, status int, payload any) {
	if logger == nil {
		logger = slog.Default()
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		http.Error(w, `{"error":"failed to encode response"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		logger.Error("httpserver: writeJSON write failure", slog.Any("error", err))
	}
}
