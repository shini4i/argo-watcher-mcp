package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/shini4i/argo-watcher-mcp/internal/domain"
)

type contextKey string

const loggerKey contextKey = "logger"

// NewRouter wires health endpoints, applies basic request logging, and optionally mounts the MCP handler.
func NewRouter(logger *slog.Logger, checker domain.HealthChecker, mcpHandler http.Handler, enableMCP bool, promHandler http.Handler) http.Handler {
	r := chi.NewRouter()

	requestLogger := logger
	if requestLogger == nil {
		requestLogger = slog.Default()
	}
	requestLogger = requestLogger.With(
		slog.String("component", "httpserver"),
		slog.String("transport", "http"),
	)

	r.Use(func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(
			next,
			"http.server",
			otelhttp.WithFilter(func(req *http.Request) bool {
				return req.URL.Path != "/metrics"
			}),
		)
	})

	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)

			reqLogger := requestLogger
			spanCtx := trace.SpanFromContext(req.Context()).SpanContext()
			if spanCtx.IsValid() {
				reqLogger = reqLogger.With(
					slog.String("trace_id", spanCtx.TraceID().String()),
					slog.String("span_id", spanCtx.SpanID().String()),
				)
			}

			req = req.WithContext(context.WithValue(req.Context(), loggerKey, reqLogger))

			next.ServeHTTP(ww, req)

			duration := time.Since(start)
			reqLogger.Info("http request completed",
				slog.String("method", req.Method),
				slog.String("path", req.URL.Path),
				slog.Int("status", ww.Status()),
				slog.String("duration", duration.String()),
				slog.Float64("duration_ms", float64(duration)/float64(time.Millisecond)),
				slog.String("remote_addr", req.RemoteAddr),
			)
		})
	})

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(r.Context(), requestLogger, w, http.StatusOK, map[string]string{"status": "up"})
	})

	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if checker == nil {
			writeJSON(r.Context(), requestLogger, w, http.StatusServiceUnavailable, map[string]string{
				"status": "unknown",
				"error":  "no health checker configured",
			})
			return
		}

		if err := checker.Check(r.Context()); err != nil {
			writeJSON(r.Context(), requestLogger, w, http.StatusServiceUnavailable, map[string]string{
				"status": "not_ready",
				"error":  err.Error(),
			})
			return
		}

		writeJSON(r.Context(), requestLogger, w, http.StatusOK, map[string]string{"status": "ready"})
	})

	if promHandler != nil {
		r.Handle("/metrics", promHandler)
	}

	if enableMCP && mcpHandler != nil {
		r.Mount("/", mcpHandler)
	} else {
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(r.Context(), requestLogger, w, http.StatusNotFound, map[string]string{"error": "not found"})
		})
	}

	return r
}

func writeJSON(ctx context.Context, fallback *slog.Logger, w http.ResponseWriter, status int, payload any) {
	logger := LoggerFromContext(ctx, fallback)

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

// LoggerFromContext returns a request-scoped logger stored in ctx or falls back to the provided logger.
func LoggerFromContext(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if ctx != nil {
		if logger, ok := ctx.Value(loggerKey).(*slog.Logger); ok && logger != nil {
			return logger
		}
	}

	if fallback != nil {
		return fallback
	}

	return slog.Default()
}
