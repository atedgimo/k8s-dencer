// Command planner runs the k8s-dencer consolidation planning loop.
//
// M2: collects cluster state through informers and publishes an immutable
// snapshot on every tick. The constraint analyzer, bin-packer and impact
// classifier consume these snapshots from M3 onward.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/yaml"

	"github.com/atedgimo/k8s-dencer/internal/cluster"
	"github.com/atedgimo/k8s-dencer/internal/cluster/metrics"
	"github.com/atedgimo/k8s-dencer/internal/httpserver"
	"github.com/atedgimo/k8s-dencer/internal/impact"
	"github.com/atedgimo/k8s-dencer/internal/planner"
	"github.com/atedgimo/k8s-dencer/internal/publish"
	sqlitestore "github.com/atedgimo/k8s-dencer/internal/store/sqlite"
	"github.com/atedgimo/k8s-dencer/internal/telemetry"
)

var version = "dev"

func main() {
	log := telemetry.NewLogger("planner", env("LOG_LEVEL", "info"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
	log.Info("planner stopped")
}

func run(ctx context.Context, log *slog.Logger) error {
	resync := duration(log, "RESYNC_PERIOD", 30*time.Second)

	// Resolves in-cluster config, falling back to KUBECONFIG, so the same
	// binary runs in a pod and against a kubeconfig for debugging.
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}

	// Usage collection is opt-in and self-degrading: with the source enabled
	// but metrics-server absent or broken, Available() answers false and the
	// snapshot honestly reports no usage data — never a guess, never an error
	// loop.
	var usage metrics.Source
	if env("USAGE_SOURCE", "") == "metrics-server" {
		src, err := metrics.NewMetricsServer(cfg)
		if err != nil {
			return err
		}
		usage = src
		log.Info("usage source enabled", "source", "metrics-server")
	}

	collector, err := cluster.New(cfg, cluster.Options{
		ResyncPeriod: resync,
		Namespaces:   splitList(os.Getenv("WATCH_NAMESPACES")),
		Logger:       log,
		Metrics:      usage,
	})
	if err != nil {
		return err
	}

	// The planner writes plans; the ui-backend owns the schema and runs
	// migrations. Opening read-write here without migrating keeps the
	// ownership boundary explicit.
	db, err := sqlitestore.Open(env("DATABASE_PATH", "/data/dencer.db"))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// The planner may start before the ui-backend has ever run, in which case
	// there is no schema to write into. Migrating here too is idempotent and
	// removes a startup ordering dependency between the two.
	if err := db.Migrate(ctx); err != nil {
		return err
	}
	planOpts := planner.DefaultOptions()
	planOpts.MinNodeAge = duration(log, "MIN_NODE_AGE", planOpts.MinNodeAge)
	planOpts.MaxSteps = intEnv(log, "MAX_STEPS", 0)
	planOpts.ExcludeNamespaces = splitList(os.Getenv("EXCLUDE_NAMESPACES"))
	planOpts.PackCeiling = floatEnv(log, "PACK_CEILING", planOpts.PackCeiling)

	metrics := telemetry.NewMetrics(telemetry.ComponentPlanner)

	// The cycle itself lives in internal/publish, where its semantics are
	// tested. This file is only wiring: env, informers, HTTP, the ticker.
	pub := &publish.Publisher{
		Log:      log,
		Source:   collector,
		Strategy: planner.Greedy{},
		Options:  planOpts,
		Classifier: impact.New(impact.Thresholds{
			YellowPodsMoved:  intEnv(log, "YELLOW_PODS_MOVED", 0),
			RedPodsMoved:     intEnv(log, "RED_PODS_MOVED", 0),
			TightPDBHeadroom: int32(intEnv(log, "TIGHT_PDB_HEADROOM", 0)),
		}),
		DB:      db,
		Retain:  intEnv(log, "RETAIN_PLANS", 200),
		Metrics: metrics,
	}

	health := &httpserver.Health{}
	mux := http.NewServeMux()
	health.Register(mux)
	metrics.Register(mux)
	mux.HandleFunc("GET /debug/snapshot", yamlHandler(func() any {
		if v := pub.LatestSnapshot(); v != nil {
			return v
		}
		return nil
	}))
	mux.HandleFunc("GET /debug/constraints", yamlHandler(func() any {
		if v := pub.LatestAnalysis(); v != nil {
			return v
		}
		return nil
	}))
	mux.HandleFunc("GET /debug/plan", yamlHandler(func() any {
		if v := pub.LatestPlan(); v != nil {
			return v
		}
		return nil
	}))

	// Informers run for the process lifetime; a cache failure is fatal because
	// planning against a dead cache would silently use stale state.
	cacheErr := make(chan error, 1)
	go func() { cacheErr <- collector.Start(ctx) }()

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpserver.Run(ctx, log, env("HEALTH_ADDR", ":8081"), mux) }()

	log.Info("waiting for informer cache sync")
	if !collector.WaitForSync(ctx) {
		return context.Cause(ctx)
	}
	log.Info("cache synced")

	// Only now is the planner safe to call ready: a snapshot taken from a
	// half-populated cache would show nodes as empty and invite a plan to
	// drain them.
	health.SetReady(true)

	ticker := time.NewTicker(resync)
	defer ticker.Stop()

	pub.Cycle(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-cacheErr:
			return err
		case err := <-serveErr:
			return err
		case <-ticker.C:
			pub.Cycle(ctx)
		}
	}
}

// floatEnv reads a fraction like PACK_CEILING=0.85. Out-of-range values
// (below 0 or above 1) fall back rather than silently disabling the ceiling
// with a typo.
func floatEnv(log *slog.Logger, key string, fallback float64) float64 {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 || v > 1 {
		log.Warn("invalid fraction, using default", "key", key, "value", raw, "default", fallback)
		return fallback
	}
	return v
}

func intEnv(log *slog.Logger, key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		log.Warn("invalid integer, using default", "key", key, "value", raw, "default", fallback)
		return fallback
	}
	return v
}

// yamlHandler serves whatever load returns, as YAML.
//
// This is how test/fixtures are captured: the golden planner tests replay real
// cluster state, so the fixtures must come from a real cluster rather than
// being written by hand.
func yamlHandler(load func() any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		v := load()
		if v == nil {
			http.Error(w, "nothing collected yet", http.StatusServiceUnavailable)
			return
		}
		out, err := yaml.Marshal(v)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(out)
	}
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
