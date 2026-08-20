package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

type app struct {
	version string
	logger  *slog.Logger
	metrics *metrics

	// ready is flipped to false on SIGTERM so the readiness probe fails before
	// the server stops accepting. Atomic because the probe handler and the
	// signal handler touch it from different goroutines.
	ready atomic.Bool
}

func newApp(version string, logger *slog.Logger) *app {
	a := &app{
		version: version,
		logger:  logger,
		metrics: newMetrics(version),
	}
	a.ready.Store(true)
	return a
}

func (a *app) setReady(v bool) { a.ready.Store(v) }

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()

	// Business endpoints.
	mux.HandleFunc("GET /api/products", a.handleProducts)
	mux.HandleFunc("GET /api/products/{id}", a.handleProduct)

	// Operational endpoints. Deliberately not instrumented - probe traffic is
	// constant and would swamp the request metrics it shares a histogram with.
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /readyz", a.handleReady)
	mux.HandleFunc("GET /metrics", a.metrics.handler)

	return a.withObservability(mux)
}

// withObservability records every request and logs it once, at the end.
//
// One log line per request, after the fact, with the status and duration on it.
// Logging on the way in as well doubles the volume and tells you nothing the
// completed line does not.
func (a *app) withObservability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		elapsed := time.Since(start)

		if !isProbe(r.URL.Path) {
			a.metrics.observe(r.Method, routeLabel(r), rec.status, elapsed)
			a.logger.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", elapsed.Milliseconds(),
			)
		}
	})
}

func isProbe(path string) bool {
	return path == "/healthz" || path == "/readyz" || path == "/metrics"
}

// routeLabel returns the matched pattern rather than the raw path.
//
// Using r.URL.Path directly would make /api/products/1 and /api/products/2
// separate time series. That is unbounded cardinality, and it is the standard
// way to take down a Prometheus.
func routeLabel(r *http.Request) string {
	if p := r.Pattern; p != "" {
		if _, path, found := cut(p, " "); found {
			return path
		}
		return p
	}
	return "unmatched"
}

func cut(s, sep string) (before, after string, found bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
