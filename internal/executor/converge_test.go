package executor_test

// M25's tests, in the order the roadmap said they matter:
//
//   1. the loop TERMINATES — a plausible-looking implementation that never
//      halts is the failure mode of this whole design
//   2. the envelope is never exceeded, including when a re-plan wants to
//   3. the ceiling stops execution BEFORE the step runs, not after
//
// The harness is the same fake cluster the steps tests use — including its
// controller-like rescheduling, which is exactly what makes closed-loop
// behaviour testable: where pods land is the fake's decision, not the plan's.

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/executor"

	"github.com/atedgimo/k8s-dencer/internal/impact"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// rebuildWithClassifier swaps in an executor whose classifier rates steps
// differently; everything else matches newHarness.
func (h *harness) rebuildWithClassifier(t *testing.T, c impact.Classifier) *executor.Executor {
	t.Helper()
	return executor.New(h.cluster, h.db, h.db, slog.New(slog.DiscardHandler), executor.Options{
		Worker: "test-worker", Limits: permissive(),
		StepTimeout: 5 * time.Second, SettleTimeout: 2 * time.Second,
		PollInterval: time.Millisecond, Classifier: c,
	})
}

// requestConverge enqueues a converge run and lets the executor claim it.
func (h *harness) requestConverge(t *testing.T, env *store.Envelope, dryRun bool) store.Run {
	t.Helper()
	ctx := context.Background()
	id, err := h.db.Enqueue(ctx, store.Run{
		PlanID: h.planID, Mode: store.RunModeConverge, Envelope: env,
		DryRun: dryRun, Actor: "alice@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.exec.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	run, err := h.db.RunByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// A cluster three lightly-loaded nodes wide plus one receiver. Every drain
// frees a node, so the loop's natural stop is the budget or the optimum.
func convergeSnapshot() model.ClusterSnapshot {
	return model.ClusterSnapshot{
		Nodes: []model.Node{
			testNode("a"), testNode("b"), testNode("c"), testNode("big"),
		},
		Pods: []model.Pod{
			testPod("shop", "web-1", "a", "web"),
			testPod("shop", "web-2", "b", "web"),
			testPod("shop", "web-3", "c", "web"),
			testPod("shop", "api-1", "big", "api"),
		},
	}
}

func TestConvergeHonoursTheNodeBudget(t *testing.T) {
	h := newHarness(t, convergeSnapshot(), nil, permissive())

	run := h.requestConverge(t, &store.Envelope{MaxNodes: 2, MaxImpact: model.ImpactYellow}, false)

	if run.Status != store.RunSucceeded {
		t.Fatalf("status = %s (%s), want Succeeded", run.Status, run.Summary)
	}
	if !strings.Contains(run.Summary, "2 node(s)") || !strings.Contains(run.Summary, "limit") {
		t.Errorf("summary does not state the envelope stopped it: %q", run.Summary)
	}

	// The budget in the transcript, not just the summary: exactly two cordons,
	// on two different nodes — which also covers the never-twice rail.
	cordoned := map[string]int{}
	for _, call := range h.cluster.transcript() {
		if name, ok := strings.CutPrefix(call, "cordon:"); ok {
			cordoned[name]++
		}
	}
	if len(cordoned) != 2 {
		t.Errorf("cordoned %d distinct nodes, want 2: %v", len(cordoned), cordoned)
	}
	for name, n := range cordoned {
		if n != 1 {
			t.Errorf("node %s cordoned %d times; a converge run must never revisit a node", name, n)
		}
	}
}

func TestConvergeStopsWhenARoundFreesNoNode(t *testing.T) {
	// Scheduler divergence, the exact case this rail exists for. The planner
	// proposes moving a's pod onto c — the consolidating choice; it refuses
	// to target the empty node, which is why the naive version of this
	// fixture could not fail. But the plan does not bind the scheduler, and
	// the fake (like a real scheduler with its own preferences) puts the
	// replacement on "spare", first schedulable in list order. A node was
	// emptied AND a node was filled: nothing was freed, and without the
	// monotonic rail the next round would happily drain "spare" back the
	// other way, forever.
	snap := model.ClusterSnapshot{
		Nodes: []model.Node{testNode("spare"), testNode("a"), testNode("c")},
		Pods: []model.Pod{
			testPod("shop", "web-1", "a", "web"),
			testPod("shop", "web-2", "c", "web"),
		},
	}
	h := newHarness(t, snap, nil, permissive())

	run := h.requestConverge(t, &store.Envelope{MaxNodes: 10, MaxImpact: model.ImpactYellow}, false)

	if run.Status != store.RunSucceeded {
		t.Fatalf("status = %s (%s), want Succeeded", run.Status, run.Summary)
	}
	if !strings.Contains(run.Summary, "freed no node") {
		t.Errorf("summary does not name the monotonic rail: %q", run.Summary)
	}
	// Termination, concretely: one round, not ten.
	cordons := 0
	for _, call := range h.cluster.transcript() {
		if strings.HasPrefix(call, "cordon:") {
			cordons++
		}
	}
	if cordons != 1 {
		t.Errorf("loop ran %d rounds against a cluster that cannot consolidate; the monotonic rail is not terminating it", cordons)
	}
}

func TestConvergeStopsAtTheImpactCeilingBeforeExecuting(t *testing.T) {
	h := newHarness(t, convergeSnapshot(), nil, permissive())
	// Every step moves at least one pod, so this threshold rates every
	// possible step Yellow — the ceiling must stop the run before anything
	// is touched.
	h.exec = h.rebuildWithClassifier(t, impact.New(impact.Thresholds{YellowPodsMoved: 1}))

	run := h.requestConverge(t, &store.Envelope{MaxNodes: 5, MaxImpact: model.ImpactGreen}, false)

	if run.Status != store.RunSucceeded {
		t.Fatalf("status = %s (%s), want Succeeded", run.Status, run.Summary)
	}
	if !strings.Contains(run.Summary, "Yellow") || !strings.Contains(run.Summary, "ceiling") {
		t.Errorf("summary does not explain the ceiling: %q", run.Summary)
	}
	for _, call := range h.cluster.transcript() {
		if strings.HasPrefix(call, "cordon:") || strings.HasPrefix(call, "evict:") {
			t.Fatalf("the ceiling stopped nothing: cluster call %q", call)
		}
	}
}

func TestConvergeStopsAtTheOptimum(t *testing.T) {
	// One node whose only pod is immovable: nothing is worth draining, and
	// the loop must say so rather than fail.
	pinned := testPod("kube-system", "ds-1", "a", "ds")
	pinned.Owner = &model.OwnerRef{Kind: "DaemonSet", Name: "ds"}
	snap := model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods:  []model.Pod{pinned},
	}
	h := newHarness(t, snap, nil, permissive())

	run := h.requestConverge(t, &store.Envelope{MaxNodes: 5, MaxImpact: model.ImpactYellow}, false)

	if run.Status != store.RunSucceeded {
		t.Fatalf("status = %s (%s), want Succeeded", run.Status, run.Summary)
	}
	if !strings.Contains(run.Summary, "converged after 0 node(s)") {
		t.Errorf("summary = %q, want the optimum stated with zero drains", run.Summary)
	}
}

func TestConvergeDryRunRehearsesExactlyOneRound(t *testing.T) {
	h := newHarness(t, convergeSnapshot(), nil, permissive())

	run := h.requestConverge(t, &store.Envelope{MaxNodes: 5, MaxImpact: model.ImpactYellow}, true)

	if run.Status != store.RunSucceeded {
		t.Fatalf("status = %s (%s), want Succeeded", run.Status, run.Summary)
	}
	if !strings.Contains(run.Summary, "round 1") {
		t.Errorf("summary does not limit the rehearsal's claim: %q", run.Summary)
	}
	// A rehearsal touches nothing, however many rounds it might have run.
	for _, call := range h.cluster.transcript() {
		if strings.HasPrefix(call, "cordon:") || strings.HasPrefix(call, "evict:") {
			t.Fatalf("dry run touched the cluster: %q", call)
		}
	}
}

func TestConvergeRefusesAMissingOrInvalidEnvelope(t *testing.T) {
	h := newHarness(t, convergeSnapshot(), nil, permissive())

	for name, env := range map[string]*store.Envelope{
		"nil envelope":  nil,
		"zero nodes":    {MaxNodes: 0, MaxImpact: model.ImpactGreen},
		"red ceiling":   {MaxNodes: 3, MaxImpact: model.ImpactRed},
		"empty ceiling": {MaxNodes: 3},
	} {
		run := h.requestConverge(t, env, false)
		if run.Status != store.RunFailed {
			t.Errorf("%s: status = %s, want Failed — an unbounded or malformed consent must not run", name, run.Status)
		}
	}
}

func TestConvergeEnvelopeSurvivesTheStore(t *testing.T) {
	h := newHarness(t, convergeSnapshot(), nil, permissive())
	ctx := context.Background()

	id, err := h.db.Enqueue(ctx, store.Run{
		PlanID: h.planID, Mode: store.RunModeConverge,
		Envelope: &store.Envelope{MaxNodes: 7, MaxImpact: model.ImpactYellow},
		Actor:    "alice@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.db.RunByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != store.RunModeConverge {
		t.Errorf("mode = %q, want converge", got.Mode)
	}
	if got.Envelope == nil || got.Envelope.MaxNodes != 7 || got.Envelope.MaxImpact != model.ImpactYellow {
		t.Errorf("envelope did not survive the store: %+v — the audit row no longer records what was consented to", got.Envelope)
	}
}
