package planner_test

// The lookahead question, answered by measurement before any implementation:
// how many nodes does greedy leave on the table? A one-step lookahead (or any
// smarter search) can recover AT MOST the gap between greedy's result and the
// capacity lower bound — the fewest nodes that could possibly hold the
// cluster's requests if packing were perfect and constraints did not exist.
// If that gap is near zero, lookahead is complexity with no purchase.
//
// The bound is generous to the challenger: it ignores every constraint
// (anti-affinity, spread, PDBs, daemonsets pinning nodes open), so the true
// attainable minimum is higher than the bound and the true recoverable gap
// smaller than measured here.

import (
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/planner"
)

func TestGreedyIsNearTheCapacityBound(t *testing.T) {
	totalGap := 0
	runs := 0
	for _, nodes := range []int{30, 60, 100} {
		for seed := int64(1); seed <= 5; seed++ {
			opts := model.DefaultSynthetic(nodes)
			opts.Seed = seed
			snap := model.Synthesize(opts)

			analysis := constraints.Analyze(snap)
			popts := planner.DefaultOptions()
			popts.MinNodeAge = 0 // synthetic nodes have no age
			plan, err := planner.Greedy{}.Plan(snap, analysis, popts)
			if err != nil {
				t.Fatalf("nodes=%d seed=%d: %v", nodes, seed, err)
			}

			// The constraint-free lower bound on nodes, per dimension.
			alloc, req := snap.Totals()
			bound := 0
			if alloc.MilliCPU > 0 {
				perNodeCPU := alloc.MilliCPU / int64(len(snap.Nodes))
				bound = max(bound, int((req.MilliCPU+perNodeCPU-1)/perNodeCPU))
			}
			if alloc.MemoryBytes > 0 {
				perNodeMem := alloc.MemoryBytes / int64(len(snap.Nodes))
				bound = max(bound, int((req.MemoryBytes+perNodeMem-1)/perNodeMem))
			}

			gap := plan.NodesAfter - bound
			totalGap += gap
			runs++
			t.Logf("nodes=%3d seed=%d  after=%3d  bound=%3d  gap=%d",
				nodes, seed, plan.NodesAfter, bound, gap)
		}
	}
	avg := float64(totalGap) / float64(runs)
	t.Logf("average gap over %d runs: %.2f nodes", runs, avg)

	// Measured 2026-08: average gap -0.20 over 15 runs (30/60/100 nodes,
	// seeds 1-5). Negative is possible because the bound rounds up from
	// cluster-average capacity; at or below it means greedy is at the packing
	// ceiling within the bound's own rounding. Worst observed: +2 at 30
	// nodes. Conclusion: a lookahead could recover at most a node or two in
	// small-cluster edge cases and usually nothing — not worth its complexity
	// or the plan-stability risk. This test stays so the conclusion is
	// re-verified on every change to the planner, and the assertion below
	// reopens the question automatically if greedy ever drifts.

	// The experiment's conclusion, pinned: if greedy ever drifts far from the
	// bound, this fails and the lookahead question reopens with data.
	if avg > 3.0 {
		t.Errorf(
			"greedy averages %.2f nodes above the capacity bound; a lookahead could be worth building — reopen the experiment",
			avg)
	}
}
