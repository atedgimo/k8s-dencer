package planner_test

import (
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/planner"
)

// How much of a plan survives executing its own first step?
//
// Not a pass/fail test — a measurement, printed for a design question: is a
// multi-step plan a script worth keeping, or a forecast that decays the moment
// anything moves?
func TestPlanStabilityAfterOneStep(t *testing.T) {
	for _, nodes := range []int{34, 100} {
		snap := model.Synthesize(model.DefaultSynthetic(nodes))
		analysis := constraints.Analyze(snap)
		planA, err := (planner.Greedy{}).Plan(snap, analysis, planner.DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		if len(planA.Steps) < 3 {
			continue
		}

		// Apply step 1 exactly as planned — the optimistic case, where the
		// real scheduler agrees with us completely.
		next := applyStep(snap, planA.Steps[0], false)
		planB, err := (planner.Greedy{}).Plan(next, constraints.Analyze(next), planner.DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}

		// Which of A's remaining target nodes does B still propose draining?
		inB := map[string]int{}
		for _, s := range planB.Steps {
			inB[s.TargetNode] = s.SequenceNumber
		}
		survived, moved := 0, 0
		for _, s := range planA.Steps[1:] {
			if newSeq, ok := inB[s.TargetNode]; ok {
				survived++
				if newSeq != s.SequenceNumber-1 {
					moved++
				}
			}
		}
		remaining := len(planA.Steps) - 1

		t.Logf("%d nodes, %d pods", len(snap.Nodes), len(snap.Pods))
		t.Logf("  plan A: %d steps, reclaims %d nodes", len(planA.Steps), planA.ReclaimedNodes())
		t.Logf("  plan B after executing A's step 1: %d steps", len(planB.Steps))
		t.Logf("  of A's %d remaining steps, %d still proposed (%.0f%%), %d at a different position",
			remaining, survived, 100*float64(survived)/float64(remaining), moved)
		t.Logf("  A total reclaim %d  vs  1 + B total reclaim %d",
			planA.ReclaimedNodes(), 1+planB.ReclaimedNodes())

		// The realistic case. kube-scheduler's default scoring favours the
		// LEAST allocated node — it spreads. We pack. So an evicted pod tends
		// to land on the emptiest node available, which is precisely the node
		// a later step wanted to drain.
		spread := applyStep(snap, planA.Steps[0], true)
		planC, err := (planner.Greedy{}).Plan(spread, constraints.Analyze(spread), planner.DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		inC := map[string]bool{}
		for _, st := range planC.Steps {
			inC[st.TargetNode] = true
		}
		survivedC := 0
		for _, st := range planA.Steps[1:] {
			if inC[st.TargetNode] {
				survivedC++
			}
		}
		t.Logf("  IF THE SCHEDULER SPREADS INSTEAD: %d steps, %d/%d of A survive (%.0f%%), reclaim 1+%d",
			len(planC.Steps), survivedC, remaining,
			100*float64(survivedC)/float64(remaining), planC.ReclaimedNodes())
	}
}

// applyStep produces the snapshot that would result from a step running.
func applyStep(snap *model.ClusterSnapshot, step model.PlanStep, spread bool) *model.ClusterSnapshot {
	out := &model.ClusterSnapshot{
		TakenAt: snap.TakenAt, PDBs: snap.PDBs,
		Nodes: make([]model.Node, 0, len(snap.Nodes)),
		Pods:  make([]model.Pod, 0, len(snap.Pods)),
	}
	dest := map[string]string{}
	for _, m := range step.Moves {
		dest[m.Namespace+"/"+m.Pod] = m.ToNode
	}
	if spread {
		// Approximate LeastAllocated: send everything to the emptiest node
		// that is not the one being drained.
		load := map[string]int64{}
		for _, p := range snap.Pods {
			load[p.NodeName] += p.Requests.MilliCPU
		}
		emptiest, best := "", int64(1<<62)
		for _, n := range snap.Nodes {
			if n.Name == step.TargetNode || n.Unschedulable {
				continue
			}
			if load[n.Name] < best {
				best, emptiest = load[n.Name], n.Name
			}
		}
		for k := range dest {
			dest[k] = emptiest
		}
	}
	for _, n := range snap.Nodes {
		// The drained node is cordoned, exactly as the executor leaves it.
		if n.Name == step.TargetNode {
			n.Unschedulable = true
		}
		out.Nodes = append(out.Nodes, n)
	}
	for _, p := range snap.Pods {
		if to, ok := dest[p.Namespace+"/"+p.Name]; ok {
			p.NodeName = to
		} else if p.NodeName == step.TargetNode && p.Owner != nil && p.Owner.Kind == "DaemonSet" {
			continue // leaves with the node
		}
		out.Pods = append(out.Pods, p)
	}
	return out
}
