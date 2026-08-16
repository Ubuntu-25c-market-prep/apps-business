// Command storefront is the first business application on the u25c platform.
//
// It exists to be deployed, not to be clever: a handful of JSON endpoints, the
// two probes Kubernetes needs, and metrics in the format Prometheus scrapes.
// Everything about it that matters is operational - it starts fast, stops
// cleanly, reports its own readiness honestly, and carries no state.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

// Set at build time: -ldflags "-X main.version=<tag>".
//
// This is the image tag the pod is running, surfaced at /healthz and as a
// metric label. During an incident the first question is "which build is
// actually running", and answering it from the cluster rather than from a
// deployment log is worth the two lines it costs.
var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(getenv("LOG_LEVEL", "info")),
	}))
	slog.SetDefault(logger)

	port := getenv("PORT", "8080")

	app := newApp(version, logger)

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: app.routes(),

		// Timeouts are not optional on a public-facing server. Without them a
		// stalled client holds a connection until the process restarts, and the
		// symptom is a slow leak rather than an error anyone notices.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Shut down on SIGTERM, which is what kubelet sends first.
	//
	// The ordering here is the part that matters. On signal, readiness flips to
	// false immediately so the endpoints controller starts removing this pod
	// from Service backends, and only then does the server stop accepting. The
	// sleep covers the gap between "we are unready" and "kube-proxy on every
	// node has caught up" - without it, requests are still being routed here
	// while connections are being closed, which is a 502 the client sees and
	// nobody can reproduce.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	go func() {
		logger.Info("listening", "port", port, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received, draining")

	app.setReady(false)
	time.Sleep(drainDelay())

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("stopped cleanly")
}

// drainDelay is how long to keep serving after going unready.
//
// It must be shorter than terminationGracePeriodSeconds in the Deployment, or
// kubelet SIGKILLs the process mid-drain and the drain accomplishes nothing.
func drainDelay() time.Duration {
	if s := os.Getenv("DRAIN_DELAY_SECONDS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 5 * time.Second
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
