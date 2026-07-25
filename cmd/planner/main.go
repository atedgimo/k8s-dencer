// Command planner runs the k8s-dencer consolidation planning loop.
//
// M0 scaffold: serves health probes and logs a heartbeat. The cluster state
// collector, constraint analyzer, planner and impact classifier land in M2-M5.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/httpserver"
	"github.com/atedgimo/k8s-dencer/internal/telemetry"
)

func main() {
	log := telemetry.NewLogger("planner", env("LOG_LEVEL", "info"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	health := &httpserver.Health{}
	mux := http.NewServeMux()
	health.Register(mux)

	// Nothing to warm up yet, so the planner is ready as soon as it is serving.
	// From M2 this flips only once the informer cache has synced.
	health.SetReady(true)

	go heartbeat(ctx, log, duration(log, "RESYNC_PERIOD", 30*time.Second))

	if err := httpserver.Run(ctx, log, env("HEALTH_ADDR", ":8081"), mux); err != nil {
		log.Error("http server failed", "error", err)
		os.Exit(1)
	}
	log.Info("planner stopped")
}

// heartbeat stands in for the planning loop until M4, so that a deployed
// planner visibly does something on the interval the chart configures.
func heartbeat(ctx context.Context, log *slog.Logger, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			log.Info("planner loop tick (no-op: planner lands in M4)")
		}
	}
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func duration(log *slog.Logger, key string, fallback time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Warn("invalid duration, using default", "key", key, "value", raw, "default", fallback)
		return fallback
	}
	return d
}
