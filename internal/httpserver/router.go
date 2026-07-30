package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
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
			otelhttp.WithFilter(shouldTraceRequest),
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

func shouldTraceRequest(req *http.Request) bool {
	trace := true
	reason := "allow"
	methodName := ""

	if _, skip := excludedPaths[req.URL.Path]; skip {
		trace = false
		reason = "excluded_path"
	} else if req.Method == http.MethodGet && strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		trace = false
		reason = "sse_stream"
	} else if req.Method == http.MethodPost && strings.Contains(req.Header.Get("Content-Type"), "application/json") {
		// Read only enough to find the JSON-RPC method, then put those bytes back
		// in front of the untouched remainder. Replacing the body with just the
		// prefix would hand the handler truncated JSON for any request over the
		// limit, and this filter runs on every JSON-RPC POST — so a large request
		// would fail to parse with the cause buried in a tracing filter. Protocol
		// 2026-07-28 puts clientCapabilities in each request's _meta, which pushes
		// bodies closer to this limit than earlier revisions did.
		//
		// The original body is deliberately not closed: net/http closes the
		// request body itself once the handler returns.
		const maxBodySize = 1024
		body, err := io.ReadAll(io.LimitReader(req.Body, maxBodySize))
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), req.Body))
		if err != nil {
			reason = "body_read_error"
		} else {
			var envelope struct {
				Method string `json:"method"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				reason = "rpc_parse_error"
			} else {
				methodName = strings.ToLower(strings.TrimSpace(envelope.Method))
				switch {
				case methodName == "":
					reason = "rpc_no_method"
				case handshakeMethods[methodName]:
					trace = false
					reason = "handshake_method"
				default:
					reason = "rpc_method"
				}
			}
		}
	}

	slog.Debug("otelhttp filter decision",
		slog.Bool("trace", trace),
		slog.String("reason", reason),
		slog.String("path", req.URL.Path),
		slog.String("http_method", req.Method),
		slog.String("accept", req.Header.Get("Accept")),
		slog.String("rpc_method", methodName),
	)

	return trace
}

var excludedPaths = map[string]struct{}{
	"/metrics": {},
	"/healthz": {},
	"/readyz":  {},
}

// handshakeMethods lists JSON-RPC methods that carry no application work, so
// tracing them would bury the actual tool calls in connection noise.
//
// Every entry is a method the MCP Go SDK actually emits. It covers both protocol
// revisions the SDK speaks: "initialize" plus its "notifications/initialized"
// follow-up for 2025-11-25 and earlier, and "server/discover" which replaces
// that handshake in 2026-07-28. The long-lived "subscriptions/listen" stream is
// excluded for the same reason SSE GETs are — its span would stay open for the
// whole session. Discovery calls ("*/list") are excluded as client bookkeeping;
// "tools/call" and the read methods that do real work are deliberately absent
// so they keep being traced.
var handshakeMethods = map[string]bool{
	"initialize":                true,
	"notifications/initialized": true,
	"server/discover":           true,
	"subscriptions/listen":      true,
	"resources/list":            true,
	"prompts/list":              true,
	"tools/list":                true,
	"ping":                      true,
}
