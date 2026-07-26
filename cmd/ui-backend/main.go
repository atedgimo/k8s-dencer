// Command ui-backend serves the k8s-dencer read API, the graph payload for the
// frontend, and (from M8) the MCP tool surface consumed by the Kagent agent.
//
// It owns the plan-store schema and runs migrations at startup. The planner
// writes plans into the same database over a shared volume; the two never talk
// to each other directly, so a ui-backend outage cannot stop planning.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/api/agenttools"
	"github.com/atedgimo/k8s-dencer/internal/api/rest"
	"github.com/atedgimo/k8s-dencer/internal/httpserver"
	sqlitestore "github.com/atedgimo/k8s-dencer/internal/store/sqlite"
	"github.com/atedgimo/k8s-dencer/internal/telemetry"
)

// version is stamped at build time via -ldflags.
var version = "dev"

func main() {
	log := telemetry.NewLogger("ui-backend", env("LOG_LEVEL", "info"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
	log.Info("ui-backend stopped")
}

func run(ctx context.Context, log *slog.Logger) error {
	dbType := env("DATABASE_TYPE", "sqlite")
	if dbType != "sqlite" {
		// values.schema.json rejects this too, but a binary launched outside
		// the chart should fail loudly rather than silently using SQLite.
		return errUnsupportedDatabase(dbType)
	}

	path := env("DATABASE_PATH", "/data/dencer.db")
	db, err := sqlitestore.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := db.Migrate(ctx); err != nil {
		return err
	}
	log.Info("plan store ready", "path", path)

	api := rest.New(db, log, version)

	health := &httpserver.Health{}
	mux := http.NewServeMux()
	health.Register(mux)
	api.Routes(mux)

	// The Kagent agent reaches the same plan store over MCP. It is mounted on
	// the ui-backend rather than shipped as its own image because the agent
	// itself runs inside Kagent — architecture doc §5 — so all we owe it is a
	// tool endpoint.
	mux.Handle("/mcp", agenttools.New(db, log, version).Handler())
	mux.Handle("/mcp/", agenttools.New(db, log, version).Handler())

	// Change detection is a poll of the latest plan ID rather than an event
	// feed: the planner is a separate process reached only through the shared
	// volume. The ID is a content hash, so the check is a string comparison.
	go api.PollStore(ctx, duration(log, "POLL_INTERVAL", 5*time.Second))

	// The schema is migrated and the store is open, so the API can serve.
	health.SetReady(true)
	log.Info("starting", "version", version, "database", dbType)

	return httpserver.Run(ctx, log, env("HTTP_ADDR", ":8080"), mux)
}

type errUnsupportedDatabase string

func (e errUnsupportedDatabase) Error() string {
	return "unsupported database type " + strconv.Quote(string(e)) + "; only sqlite is implemented"
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
