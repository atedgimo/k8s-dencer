// Command ui-backend serves the k8s-dencer REST/WebSocket API, the graph
// payload for the frontend, and the read-only MCP tool surface consumed by the
// Kagent agent.
//
// M0 scaffold: health probes plus a version endpoint. The API lands in M6 and
// the MCP tools in M8.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/atedgimo/k8s-dencer/internal/httpserver"
	"github.com/atedgimo/k8s-dencer/internal/telemetry"
)

// version is stamped at build time via -ldflags.
var version = "dev"

func main() {
	log := telemetry.NewLogger("ui-backend", env("LOG_LEVEL", "info"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	health := &httpserver.Health{}
	mux := http.NewServeMux()
	health.Register(mux)

	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version":  version,
			"database": env("DATABASE_TYPE", "sqlite"),
		})
	})

	// From M6 this flips only after the store has opened and migrated.
	health.SetReady(true)

	log.Info("starting", "version", version, "database", env("DATABASE_TYPE", "sqlite"))

	if err := httpserver.Run(ctx, log, env("HTTP_ADDR", ":8080"), mux); err != nil {
		log.Error("http server failed", "error", err)
		os.Exit(1)
	}
	log.Info("ui-backend stopped")
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
