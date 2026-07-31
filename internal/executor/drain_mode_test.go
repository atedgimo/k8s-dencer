package executor_test

import (
	"context"
	"strings"
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/impact"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

func (h *harness) requestDrain(t *testing.T, node string, dryRun bool) store.Run {
	t.Helper()
	ctx := context.Background()
	id, err := h.db.Enqueue(ctx, store.Run{
		PlanID: h.planID, Mode: store.RunModeDrain, Node: node,
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

// The guarded drain is kubectl drain with the rails: cordon, per-eviction
// guard checks, recovery verification — the full performStep chain, on a node
// the operator named rather than a plan proposed.
func TestGuardedDrainDrainsTheNamedNode(t *testing.T) {
	snap := model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods: []model.Pod{
			testPod("shop", "web-1", "a", "web"),
			testPod("shop", "web-2", "b", "web"),
		},
	}
	h := newHarness(t, snap, nil, permissive())

	run := h.requestDrain(t, "a", false)

	if run.Status != store.RunSucceeded {
		t.Fatalf("status = %s (%s), want Succeeded", run.Status, run.Summary)
	}
	calls := strings.Join(h.cluster.transcript(), " ")
	if !strings.Contains(calls, "cordon:a") || !strings.Contains(calls, "evict:shop/web-1") {
		t.Errorf("drain did not cordon and evict through the cluster interface: %v", h.cluster.transcript())
	}
}

// A named drain must not be a side-channel around the impact ratings: a drain
// the classifier rates Red needs a maintenance window like any planned step.
func TestGuardedDrainIsRatedNotWavedThrough(t *testing.T) {
	snap := model.ClusterSnapshot{
		Nodes: []model.Node{testNode("a"), testNode("b")},
		Pods: []model.Pod{
			testPod("shop", "web-1", "a", "web"),
			testPod("shop", "web-2", "b", "web"),
		},
	}
	h := newHarness(t, snap, nil, permissive())
	// Everything is Red at this threshold; no window is configured.
	h.exec = h.rebuildWithClassifier(t, impact.New(impact.Thresholds{RedPodsMoved: 1}))

	run := h.requestDrain(t, "a", false)

	if run.Status != store.RunBlocked {
		t.Fatalf("status = %s (%s), want Blocked — a named drain rated Red must need a window", run.Status, run.Summary)
	}
	for _, call := range h.cluster.transcript() {
		if strings.HasPrefix(call, "evict:") {
			t.Fatalf("a Red-rated drain evicted %q before being blocked", call)
		}
	}
}

func TestGuardedDrainRefusesAnUnknownNode(t *testing.T) {
	h := newHarness(t, convergeSnapshot(), nil, permissive())
	run := h.requestDrain(t, "no-such-node", false)
	if run.Status != store.RunFailed {
		t.Fatalf("status = %s, want Failed for an unknown node", run.Status)
	}
}
