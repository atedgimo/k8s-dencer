package telemetry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A metric that nothing writes is worse than no metric at all: it scrapes as
// zero, and zero is indistinguishable from healthy. "No runs have failed" and
// "nobody wired up the failure counter" produce the same graph.
//
// So the declaration is checked against the call sites. Every exported field on
// Metrics must be referenced by non-test code somewhere outside this file.
func TestEveryMetricHasAWriter(t *testing.T) {
	fields := exportedMetricFields(t)
	if len(fields) == 0 {
		t.Fatal("found no fields on Metrics; the parser is broken, not the code")
	}

	root := repoRoot(t)
	used := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "ui", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// metrics.go declares them; it does not count as a writer.
		if filepath.Base(path) == "metrics.go" && strings.Contains(path, "telemetry") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, f := range fields {
			if strings.Contains(string(src), "."+f) {
				used[f] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	for _, f := range fields {
		if !used[f] {
			t.Errorf("Metrics.%s is declared but nothing outside metrics.go ever writes it; "+
				"it would scrape as zero forever, which reads as healthy", f)
		}
	}
}

// Plan age is the one series whose whole purpose is to notice that the planner
// stopped. Writing it from the planning loop would defeat that — the value
// would freeze at its last reading, so a dead planner would report a fresh
// plan. It has to be computed when scraped.
func TestPlanAgeIsDerivedAtScrapeTime(t *testing.T) {
	m := NewMetrics(ComponentPlanner)

	body := scrape(t, m)
	if !strings.Contains(body, "dencer_plan_age_seconds -1") {
		t.Errorf("before any plan, age should be -1 to distinguish 'never planned' from 'just planned'; got:\n%s",
			grepLines(body, "dencer_plan_age_seconds"))
	}

	m.PlanProduced(nowMinus(90))
	body = scrape(t, m)
	line := grepLines(body, "dencer_plan_age_seconds")
	if strings.Contains(line, "-1") {
		t.Fatalf("age still -1 after a plan was recorded: %s", line)
	}
	// Two scrapes of a static gauge would be identical; a derived one moves.
	if !strings.Contains(line, "89") && !strings.Contains(line, "90") {
		t.Errorf("expected an age near 90s, got: %s", line)
	}
}

// Every rating must be present even at zero, or "no Red steps" disappears from
// the scrape and reads as a gap in the graph rather than as reassurance.
func TestPlanStepsReportsZeroRatingsExplicitly(t *testing.T) {
	m := NewMetrics(ComponentPlanner)
	for _, r := range []string{"Green", "Yellow", "Red"} {
		m.PlanSteps.WithLabelValues(r).Set(0)
	}
	body := scrape(t, m)
	for _, r := range []string{"Green", "Yellow", "Red"} {
		if !strings.Contains(body, `dencer_plan_steps{impact="`+r+`"}`) {
			t.Errorf("rating %s missing from the scrape at zero", r)
		}
	}
}

func TestScrapeEndpointServes(t *testing.T) {
	m := NewMetrics(ComponentPlanner)
	mux := http.NewServeMux()
	m.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, MetricsPath, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", MetricsPath, rec.Code)
	}
	body := rec.Body.String()
	// Go runtime series come along deliberately: Phase 4 is about memory.
	if !strings.Contains(body, "go_memstats_") {
		t.Error("Go runtime collector is not registered")
	}
	// controller-runtime registers into the default registry; exporting those
	// by accident would make the output depend on linked libraries.
	if strings.Contains(body, "workqueue_") || strings.Contains(body, "rest_client_") {
		t.Error("default-registry series leaked into the scrape; use the private registry")
	}
}

func exportedMetricFields(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "metrics.go", nil, 0)
	if err != nil {
		t.Fatalf("parse metrics.go: %v", err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Metrics" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, fld := range st.Fields.List {
			for _, name := range fld.Names {
				if name.IsExported() {
					out = append(out, name.Name)
				}
			}
		}
		return false
	})
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = parent
	}
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	mux := http.NewServeMux()
	m.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, MetricsPath, nil))
	return rec.Body.String()
}

func grepLines(body, substr string) string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.Contains(l, substr) && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func nowMinus(seconds int) time.Time { return time.Now().Add(-time.Duration(seconds) * time.Second) }

// Each component must publish only what it can actually write. The planner
// cannot evict; an operator who sees dencer_eviction_duration_seconds on the
// planner reads a permanent zero as "evictions are fast", when the truth is
// that this process has never performed one.
func TestComponentsPublishOnlyTheirOwnSeries(t *testing.T) {
	// Write to every metric on both components before scraping. Labelled
	// metrics emit nothing until a child exists, so without this the check
	// would pass on two empty scrapes and prove nothing. Writing to a metric
	// the component does not register is also the realistic mistake: it is
	// silently dropped, and this asserts that it stays dropped.
	touch := func(m *Metrics) *Metrics {
		m.PlanProduced(time.Now())
		m.PlanSteps.WithLabelValues("Green").Set(1)
		m.NodesReclaimed.Set(1)
		m.SnapshotNodes.Set(1)
		m.SnapshotPods.Set(1)
		m.PlanCycleTime.Observe(1)
		m.SnapshotFailure.Inc()
		m.NodesAwaitingReclamation.Set(1)
		m.ReclamationSeconds.Observe(1)
		m.NodesReturnedTotal.Inc()
		m.RunsTotal.WithLabelValues("Succeeded").Inc()
		m.GuardRefusalsTotal.WithLabelValues("PDBHeadroom").Inc()
		m.EvictionDuration.Observe(1)
		m.EvictionsTotal.WithLabelValues("evicted").Inc()
		m.NodesDrainedTotal.Inc()
		m.RecoveryWaitSeconds.Observe(1)
		return m
	}

	planner := scrape(t, touch(NewMetrics(ComponentPlanner)))
	executor := scrape(t, touch(NewMetrics(ComponentExecutor)))

	plannerOnly := []string{
		"dencer_plan_age_seconds", "dencer_plan_steps", "dencer_snapshot_pods",
		"dencer_snapshot_nodes", "dencer_plan_cycle_seconds", "dencer_snapshot_failures_total",
		"dencer_plan_nodes_reclaimable",
		// The planner observes reclamation because it is the only component
		// watching nodes continuously; the executor drains and moves on.
		"dencer_nodes_awaiting_reclamation", "dencer_reclamation_seconds",
		"dencer_nodes_returned_total",
	}
	executorOnly := []string{
		"dencer_runs_total", "dencer_eviction_duration_seconds", "dencer_nodes_drained_total",
		"dencer_guard_refusals_total", "dencer_evictions_total", "dencer_recovery_wait_seconds",
	}

	for _, name := range plannerOnly {
		if !strings.Contains(planner, name) {
			t.Errorf("planner is missing its own series %s", name)
		}
		if strings.Contains(executor, name) {
			t.Errorf("executor publishes %s, which only the planner writes", name)
		}
	}
	for _, name := range executorOnly {
		if !strings.Contains(executor, name) {
			t.Errorf("executor is missing its own series %s", name)
		}
		if strings.Contains(planner, name) {
			t.Errorf("planner publishes %s, which only the executor writes; "+
				"a permanent zero reads as healthy evictions it never performed", name)
		}
	}

	// ui-backend has no series of its own yet, but must still scrape cleanly
	// with the runtime collectors, or the chart's ServiceMonitor is pointless.
	ui := scrape(t, touch(NewMetrics(ComponentUIBackend)))
	if !strings.Contains(ui, "go_memstats_") {
		t.Error("ui-backend scrape carries no runtime series at all")
	}
	if strings.Contains(ui, "dencer_") {
		t.Error("ui-backend publishes dencer_ series it does not write")
	}
}
