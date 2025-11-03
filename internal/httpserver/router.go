package httpserver

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/shini4i/argo-watcher-mcp/internal/domain"
)

// NewRouter wires health endpoints and optionally the MCP handler.
func NewRouter(checker domain.HealthChecker, mcpHandler http.Handler, enableMCP bool) http.Handler {
	r := chi.NewRouter()

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "up"})
	})

	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if checker == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "unknown",
				"error":  "no health checker configured",
			})
			return
		}

		if err := checker.Check(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "not_ready",
				"error":  err.Error(),
			})
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	if enableMCP && mcpHandler != nil {
		r.Mount("/", mcpHandler)
	} else {
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		})
	}

	return r
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(payload); err != nil {
		http.Error(w, `{"error":"failed to encode response"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		log.Printf("httpserver: writeJSON write failure: %v", err)
	}
}
