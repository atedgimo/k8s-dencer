// Command executor performs consolidation steps an operator has approved.
//
// This is the only k8s-dencer component that changes a cluster, and the only
// one whose ServiceAccount holds pods/eviction. It exposes no API: work is
// claimed from the shared plan store, so the workload that can drain nodes
// cannot be reached over the network at all. The health endpoints are the only
// listener, and they serve probes.
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

	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/atedgimo/k8s-dencer/internal/cluster"
	"github.com/atedgimo/k8s-dencer/internal/executor"
	"github.com/atedgimo/k8s-dencer/internal/httpserver"
	"github.com/atedgimo/k8s-dencer/internal/safety"
	"github.com/atedgimo/k8s-dencer/internal/store"
	sqlitestore "github.com/atedgimo/k8s-dencer/internal/store/sqlite"
	"github.com/atedgimo/k8s-dencer/internal/telemetry"
)

// version is stamped at build time via -ldflags.
var version = "dev"

func main() {
	log := telemetry.NewLogger("executor", env("LOG_LEVEL", "info"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
	log.Info("executor stopped")
}

func run(ctx context.Context, log *slog.Logger) error {
	path := env("DATABASE_PATH", "/data/dencer.db")
	db, err := sqlitestore.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// The ui-backend owns the schema and runs migrations. The executor waits
	// for the tables rather than creating them, so two writers can never race
	// to define them differently.
	if err := waitForSchema(ctx, db, log); err != nil {
		return err
	}

	cfg, err := ctrlconfig.GetConfig()
	if err != nil {
		return err
	}
	reader, err := cluster.NewDirect(cfg)
	if err != nil {
		return err
	}

	limits := safety.Limits{
		MaxNodesPerRun: intEnv("SAFETY_MAX_NODES_PER_RUN", safety.DefaultLimits().MaxNodesPerRun),
		MinReadyNodes:  intEnv("SAFETY_MIN_READY_NODES", safety.DefaultLimits().MinReadyNodes),
		// Never read from configuration. Red steps are confined to approved
		// maintenance windows, which do not exist until Phase 3, and making
		// that a values key would turn a safety rail into a footgun.
		AllowRed: false,
	}

	exec := executor.New(executor.NewK8sCluster(reader), db, db, log, executor.Options{
		Worker:        env("POD_NAME", "executor"),
		Limits:        limits,
		StepTimeout:   duration(log, "STEP_TIMEOUT", 10*time.Minute),
		SettleTimeout: duration(log, "SETTLE_TIMEOUT", 5*time.Minute),
		PollInterval:  duration(log, "CLUSTER_POLL_INTERVAL", 2*time.Second),
	})

	health := &httpserver.Health{}
	mux := http.NewServeMux()
	health.Register(mux)
	health.SetReady(true)

	log.Info("starting", "version", version, "worker", env("POD_NAME", "executor"),
		"maxNodesPerRun", limits.MaxNodesPerRun, "minReadyNodes", limits.MinReadyNodes)

	go exec.Run(ctx, duration(log, "QUEUE_POLL_INTERVAL", 3*time.Second))

	return httpserver.Run(ctx, log, env("HTTP_ADDR", ":8081"), mux)
}

// waitForSchema blocks until ui-backend has migrated the database.
//
// On a fresh install all three pods start together and the executor may well
// win the race. Crashlooping until the tables appear would work but reads as a
// broken deploy; waiting quietly is the same outcome without the alarm.
func waitForSchema(ctx context.Context, db *sqlitestore.Store, log *slog.Logger) error {
	for attempt := 0; ; attempt++ {
		// ErrNotFound means the query ran and matched nothing, which is
		// exactly what a migrated but idle database looks like.
		if _, err := db.ActiveRun(ctx); err == nil || errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if attempt == 0 {
			log.Info("waiting for ui-backend to migrate the plan store", "path", env("DATABASE_PATH", "/data/dencer.db"))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
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
