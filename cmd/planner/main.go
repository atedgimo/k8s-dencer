// Command planner runs the k8s-dencer consolidation planning loop.
//
// M2: collects cluster state through informers and publishes an immutable
// snapshot on every tick. The constraint analyzer, bin-packer and impact
// classifier consume these snapshots from M3 onward.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/yaml"

	"github.com/atedgimo/k8s-dencer/internal/cluster"
	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/httpserver"
	"github.com/atedgimo/k8s-dencer/internal/model"
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

	collector, err := cluster.New(cfg, cluster.Options{
		ResyncPeriod: resync,
		Namespaces:   splitList(os.Getenv("WATCH_NAMESPACES")),
		Logger:       log,
	})
	if err != nil {
		return err
	}

	var latest atomic.Pointer[model.ClusterSnapshot]
	var latestAnalysis atomic.Pointer[constraints.Analysis]

	health := &httpserver.Health{}
	mux := http.NewServeMux()
	health.Register(mux)
	mux.HandleFunc("GET /debug/snapshot", yamlHandler(func() any {
		if v := latest.Load(); v != nil {
			return v
		}
		return nil
	}))
	mux.HandleFunc("GET /debug/constraints", yamlHandler(func() any {
		if v := latestAnalysis.Load(); v != nil {
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

	collect(ctx, log, collector, &latest, &latestAnalysis)

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-cacheErr:
			return err
		case err := <-serveErr:
			return err
		case <-ticker.C:
			collect(ctx, log, collector, &latest, &latestAnalysis)
		}
	}
}

func collect(
	ctx context.Context,
	log *slog.Logger,
	c *cluster.Collector,
	latest *atomic.Pointer[model.ClusterSnapshot],
	latestAnalysis *atomic.Pointer[constraints.Analysis],
) {
	snap, err := c.Snapshot(ctx)
	if err != nil {
		log.Error("snapshot failed", "error", err)
		return
	}
	latest.Store(snap)

	allocatable, requested := snap.Totals()
	cpu, mem, pods := requested.Ratio(allocatable)

	occupied := 0
	for _, n := range snap.Nodes {
		if !snap.RequestedOnNode(n.Name).IsZero() {
			occupied++
		}
	}

	blocking := 0
	for _, p := range snap.PDBs {
		if p.Blocks() {
			blocking++
		}
	}

	log.Info("snapshot",
		"nodes", len(snap.Nodes),
		"nodesOccupied", occupied,
		"pods", len(snap.Pods),
		"pdbs", len(snap.PDBs),
		"pdbsBlocking", blocking,
		"cpuRequestedPct", pct(cpu),
		"memRequestedPct", pct(mem),
		"podSlotsUsedPct", pct(pods),
		"usageData", snap.HasUsageData,
	)

	analysis := constraints.Analyze(snap)
	latestAnalysis.Store(analysis)

	cs := analysis.Summarize()
	undrainable := 0
	for _, n := range snap.Nodes {
		if drainable, _ := analysis.NodeDrainable(n.Name); !drainable {
			undrainable++
		}
	}

	log.Info("constraints",
		"movable", cs.Movable,
		"blocked", cs.Blocked,
		"stuck", cs.Stuck,
		"pdbBlocked", cs.PDBBlocked,
		"antiAffinity", cs.AntiAffinity,
		"spreadBound", cs.SpreadBound,
		"controllerPinned", cs.ControllerPin,
		"nodesUndrainable", undrainable,
	)
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

func pct(f float64) string {
	return fmt.Sprintf("%.1f%%", f*100)
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
