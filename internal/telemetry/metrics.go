package telemetry

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the set of series k8s-dencer publishes.
//
// The selection rule is "would an operator page on this", not "can we measure
// it". Four things can go wrong in a way nobody would otherwise notice:
//
//   - the planner stops planning, and the UI keeps serving a stale plan that
//     looks fine (PlanAge);
//   - runs start failing, and nobody looks at the ledger (RunsTotal);
//   - the guard starts refusing everything, which means the plan and the
//     cluster have diverged (GuardRefusalsTotal);
//   - evictions hang on a PDB, turning a drain into an indefinite wait
//     (EvictionDuration).
//
// Everything here is wired to a real call site. A registered metric that
// nothing ever updates is worse than no metric: it reads as zero, which is
// indistinguishable from healthy.
type Metrics struct {
	registry *prometheus.Registry

	// Planner.
	//
	// planStamp holds when the current plan was produced. Plan age is derived
	// from it at scrape time rather than written into a gauge by the planning
	// loop — a gauge the loop sets would freeze at its last value if the loop
	// died, reporting a fresh plan precisely when there is none. The whole
	// point of this series is to notice that the loop stopped.
	planStamp       atomic.Int64
	PlanSteps       *prometheus.GaugeVec
	NodesReclaimed  prometheus.Gauge
	SnapshotNodes   prometheus.Gauge
	SnapshotPods    prometheus.Gauge
	PlanCycleTime   prometheus.Histogram
	SnapshotFailure prometheus.Counter

	// Executor.
	RunsTotal           *prometheus.CounterVec
	GuardRefusalsTotal  *prometheus.CounterVec
	EvictionDuration    prometheus.Histogram
	EvictionsTotal      *prometheus.CounterVec
	NodesDrainedTotal   prometheus.Counter
	RecoveryWaitSeconds prometheus.Histogram
}

// Component names the process publishing metrics.
//
// Each one registers only the series it actually writes. Registering the whole
// set everywhere would have the planner publish dencer_eviction_duration_seconds
// as a permanent zero — the planner cannot evict — and an operator reading that
// would see a component reporting healthy evictions it never performs. A series
// that is absent is a question; a series pinned at zero is a wrong answer.
type Component string

const (
	ComponentPlanner   Component = "planner"
	ComponentExecutor  Component = "executor"
	ComponentUIBackend Component = "ui-backend"
)

// NewMetrics builds the metric set against a private registry, registering the
// series that belong to the given component.
//
// A private registry rather than prometheus.DefaultRegisterer: controller-runtime
// registers its own workqueue and client-go series into the default one, and
// exporting those by accident would make the scrape output depend on which
// libraries happen to be linked in.
func NewMetrics(component Component) *Metrics {
	reg := prometheus.NewRegistry()

	// Go runtime and process series are worth the bytes here: Phase 4 is about
	// memory, and the informer cache is the largest consumer in the planner.
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		registry: reg,

		PlanSteps: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dencer_plan_steps",
			Help: "Steps in the current plan, by impact classification.",
		}, []string{"impact"}),
		NodesReclaimed: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dencer_plan_nodes_reclaimable",
			Help: "Nodes the current plan would free if executed in full.",
		}),
		SnapshotNodes: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dencer_snapshot_nodes",
			Help: "Nodes in the most recent cluster snapshot.",
		}),
		SnapshotPods: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dencer_snapshot_pods",
			Help: "Pods in the most recent cluster snapshot.",
		}),
		PlanCycleTime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "dencer_plan_cycle_seconds",
			Help: "Time to snapshot, analyse and plan. Approaching the resync period means the planner is falling behind.",
			// Spans the measured range in docs/benchmarks.md, from a small
			// cluster to well past the point where a cycle outruns its resync.
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}),
		SnapshotFailure: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "dencer_snapshot_failures_total",
			Help: "Snapshots that could not be taken.",
		}),

		RunsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dencer_runs_total",
			Help: "Execution runs by terminal status.",
		}, []string{"status"}),
		GuardRefusalsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dencer_guard_refusals_total",
			Help: "Steps or evictions refused by the safety guard, by rule.",
		}, []string{"rule"}),
		EvictionDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "dencer_eviction_duration_seconds",
			Help: "Time from eviction call to the pod being gone. A long tail means PDBs are holding drains open.",
			// Terminating a pod is bounded by its grace period, which defaults
			// to 30s and is routinely raised; the top bucket has to sit well
			// above it or a stuck drain looks the same as a slow one.
			Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}),
		EvictionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dencer_evictions_total",
			Help: "Eviction attempts by outcome.",
		}, []string{"outcome"}),
		NodesDrainedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "dencer_nodes_drained_total",
			Help: "Nodes fully drained and cordoned.",
		}),
		RecoveryWaitSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "dencer_recovery_wait_seconds",
			Help:    "Time spent waiting for evicted workloads to become Ready again.",
			Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600},
		}),
	}

	switch component {
	case ComponentPlanner:
		reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "dencer_plan_age_seconds",
			Help: "Age of the most recent plan. Rises without bound if the planner stops planning. Negative one before the first plan.",
		}, func() float64 {
			ns := m.planStamp.Load()
			if ns == 0 {
				// Distinguishable from a genuinely fresh plan: an alert on
				// plan age must not fire during startup, and must not be
				// silenced by a zero that means "never planned".
				return -1
			}
			return time.Since(time.Unix(0, ns)).Seconds()
		}))
		reg.MustRegister(
			m.PlanSteps, m.NodesReclaimed, m.SnapshotNodes, m.SnapshotPods,
			m.PlanCycleTime, m.SnapshotFailure,
		)
	case ComponentExecutor:
		reg.MustRegister(
			m.RunsTotal, m.GuardRefusalsTotal, m.EvictionDuration, m.EvictionsTotal,
			m.NodesDrainedTotal, m.RecoveryWaitSeconds,
		)
	case ComponentUIBackend:
		// Nothing of its own yet. The Go and process collectors above are the
		// point: ui-backend is the component that holds the SQLite writer and
		// serves the API, so heap and file descriptors are what an operator
		// would look at. Adding request counters here is M20 work that has not
		// been justified by an actual question anyone asked.
	}
	return m
}

// Register installs the scrape endpoint on mux.
//
// The path is fixed rather than configurable: the chart's serviceMonitor has to
// name it, and a mismatch between the two is exactly the failure this milestone
// exists to remove.
func (m *Metrics) Register(mux *http.ServeMux) {
	mux.Handle("GET "+MetricsPath, promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		// A scrape must never take the process down.
		ErrorHandling: promhttp.ContinueOnError,
	}))
}

// MetricsPath is where every component serves its scrape endpoint. The chart
// asserts against this value; see hack/lint-chart.sh.
const MetricsPath = "/metrics"

// PlanProduced records that a plan was just produced, which resets plan age.
func (m *Metrics) PlanProduced(at time.Time) { m.planStamp.Store(at.UnixNano()) }

// Gatherer exposes the registry for tests that want to read series back.
func (m *Metrics) Gatherer() prometheus.Gatherer { return m.registry }
