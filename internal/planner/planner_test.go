package planner_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/planner"
)

func loadFixture(t *testing.T, name string) *model.ClusterSnapshot {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", name+".yaml"))
	if err != nil {
		t.Skipf("fixture %s not available: %v", name, err)
	}
	var snap model.ClusterSnapshot
	if err := yaml.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return &snap
}

func planFor(t *testing.T, snap *model.ClusterSnapshot, opts planner.Options) *model.Plan {
	t.Helper()
	analysis := constraints.Analyze(snap)
	p, err := planner.Greedy{}.Plan(snap, analysis, opts)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	return p
}

// testOptions disables the minimum node age. Fixtures are captured minutes
// after the fabric is created, so the production default would exclude every
// node and make these tests silently vacuous.
func testOptions() planner.Options {
	o := planner.DefaultOptions()
	o.MinNodeAge = 0
	return o
}

// A plan that changes between runs on identical input cannot be audited, and
// makes every golden test worthless.
func TestPlanIsDeterministic(t *testing.T) {
	snap := loadFixture(t, "a-fragmented")

	first := planFor(t, snap, testOptions())
	second := planFor(t, snap, testOptions())

	if first.ID != second.ID {
		t.Errorf("plan ID differs across runs: %s vs %s", first.ID, second.ID)
	}
	if len(first.Steps) != len(second.Steps) {
		t.Fatalf("step count differs: %d vs %d", len(first.Steps), len(second.Steps))
	}
	for i := range first.Steps {
		a, b := first.Steps[i], second.Steps[i]
		if a.TargetNode != b.TargetNode {
			t.Errorf("step %d targets %s then %s", i+1, a.TargetNode, b.TargetNode)
		}
		if len(a.Moves) != len(b.Moves) {
			t.Errorf("step %d move count differs", i+1)
			continue
		}
		for j := range a.Moves {
			if a.Moves[j] != b.Moves[j] {
				t.Errorf("step %d move %d differs: %+v vs %+v", i+1, j, a.Moves[j], b.Moves[j])
			}
		}
	}
}

func TestPlanConsolidatesFragmentedCluster(t *testing.T) {
	snap := loadFixture(t, "a-fragmented")
	plan := planFor(t, snap, testOptions())

	if len(plan.Steps) == 0 {
		t.Fatal("a cluster at ~37% requested should be consolidatable")
	}
	if plan.NodesAfter >= plan.NodesBefore {
		t.Errorf("plan frees nothing: before=%d after=%d", plan.NodesBefore, plan.NodesAfter)
	}

	total, requested := snap.Totals()
	_ = total
	// Nothing can pack tighter than the total requested CPU allows.
	var maxNodeCPU int64
	for _, n := range snap.Nodes {
		if n.Allocatable.MilliCPU > maxNodeCPU {
			maxNodeCPU = n.Allocatable.MilliCPU
		}
	}
	floor := int(requested.MilliCPU / maxNodeCPU)
	if plan.NodesAfter < floor {
		t.Errorf("plan claims %d nodes but %dm CPU needs at least %d", plan.NodesAfter, requested.MilliCPU, floor)
	}

	t.Logf("nodes %d -> %d across %d steps (reclaims %d), floor %d, plan %s",
		plan.NodesBefore, plan.NodesAfter, len(plan.Steps), plan.ReclaimedNodes(), floor, plan.ID)
}

// The critical correctness property: applying the plan must yield a cluster
// the scheduler would actually accept. A plan that looks good but cannot be
// executed is worse than no plan.
func TestEveryProposedMoveIsFeasibleWhenApplied(t *testing.T) {
	for _, fixture := range []string{"a-fragmented", "b-pdb-blocked"} {
		t.Run(fixture, func(t *testing.T) {
			snap := loadFixture(t, fixture)
			plan := planFor(t, snap, testOptions())

			work := constraints.NewPlacement(snap)
			for _, step := range plan.Steps {
				for _, m := range step.Moves {
					pod, ok := findPod(work, m.FromNode, m.Namespace, m.Pod)
					if !ok {
						t.Fatalf("step %d moves %s/%s from %s, but it is not there",
							step.SequenceNumber, m.Namespace, m.Pod, m.FromNode)
					}
					work.Remove(pod)
					if placeable, why := work.CanPlace(pod, m.ToNode); !placeable {
						t.Errorf("step %d proposes %s/%s -> %s, which is infeasible: %s",
							step.SequenceNumber, m.Namespace, m.Pod, m.ToNode, why)
					}
					work.Place(pod, m.ToNode)
				}
			}
		})
	}
}

// A step exists to free a node. If the node still holds movable pods after the
// step's moves are applied, the step accomplished nothing.
func TestEveryStepFullyEmptiesItsTargetNode(t *testing.T) {
	snap := loadFixture(t, "a-fragmented")
	plan := planFor(t, snap, testOptions())

	work := constraints.NewPlacement(snap)
	for _, step := range plan.Steps {
		for _, m := range step.Moves {
			pod, ok := findPod(work, m.FromNode, m.Namespace, m.Pod)
			if !ok {
				continue
			}
			work.Remove(pod)
			work.Place(pod, m.ToNode)
		}
		if !work.IsEmpty(step.TargetNode) {
			remaining := 0
			for _, p := range work.Occupants(step.TargetNode) {
				if p.IsMovable() {
					remaining++
				}
			}
			t.Errorf("step %d targets %s but leaves %d movable pod(s) on it",
				step.SequenceNumber, step.TargetNode, remaining)
		}
	}
}

// Steps must never target the same node twice, and sequence numbers must run
// 1..N: the UI addresses steps by number and lets an operator run a subrange.
func TestStepsAreOrderedAndUnique(t *testing.T) {
	snap := loadFixture(t, "a-fragmented")
	plan := planFor(t, snap, testOptions())

	seen := map[string]int{}
	for i, step := range plan.Steps {
		if step.SequenceNumber != i+1 {
			t.Errorf("step at index %d has sequence number %d", i, step.SequenceNumber)
		}
		if step.ID == "" {
			t.Errorf("step %d has no ID", step.SequenceNumber)
		}
		if prev, dup := seen[step.TargetNode]; dup {
			t.Errorf("node %s drained by both step %d and step %d", step.TargetNode, prev, step.SequenceNumber)
		}
		seen[step.TargetNode] = step.SequenceNumber

		if len(step.Moves) == 0 {
			t.Errorf("step %d has no moves", step.SequenceNumber)
		}
		for _, m := range step.Moves {
			if m.FromNode != step.TargetNode {
				t.Errorf("step %d targets %s but moves a pod off %s", step.SequenceNumber, step.TargetNode, m.FromNode)
			}
			if m.ToNode == m.FromNode {
				t.Errorf("step %d moves %s/%s to its own node", step.SequenceNumber, m.Namespace, m.Pod)
			}
		}
	}
}

// A pod behind a zero-headroom PDB cannot be evicted, so its node cannot be
// drained. Proposing it would produce a step guaranteed to fail.
func TestPlanNeverDrainsANodeHoldingABlockedPod(t *testing.T) {
	snap := loadFixture(t, "b-pdb-blocked")
	analysis := constraints.Analyze(snap)
	plan := planFor(t, snap, testOptions())

	blockedNodes := map[string]string{}
	for _, pc := range analysis.Pods {
		if !pc.Movable && pc.NodeName != "" {
			blockedNodes[pc.NodeName] = pc.Key()
		}
	}
	if len(blockedNodes) == 0 {
		t.Skip("fixture has no blocked pods")
	}

	for _, step := range plan.Steps {
		if pod, blocked := blockedNodes[step.TargetNode]; blocked {
			t.Errorf("step %d drains %s, which holds unevictable pod %s",
				step.SequenceNumber, step.TargetNode, pod)
		}
	}
	t.Logf("%d node(s) correctly excluded from draining", len(blockedNodes))
}

// Control-plane nodes are excluded by default; draining the API server out
// from under the cluster is not consolidation.
func TestControlPlaneNodesAreNeverDrained(t *testing.T) {
	snap := &model.ClusterSnapshot{
		TakenAt: time.Now(),
		Nodes: []model.Node{
			{
				Name:        "cp",
				Ready:       true,
				Labels:      map[string]string{"node-role.kubernetes.io/control-plane": ""},
				Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 1 << 34, Pods: 110},
			},
			{
				Name:        "worker",
				Ready:       true,
				Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 1 << 34, Pods: 110},
			},
		},
		Pods: []model.Pod{
			{Namespace: "kube-system", Name: "api", NodeName: "cp", Phase: model.PodRunning,
				Requests: model.Resources{MilliCPU: 500, MemoryBytes: 1 << 28}},
		},
	}

	plan := planFor(t, snap, testOptions())
	for _, step := range plan.Steps {
		if step.TargetNode == "cp" {
			t.Error("control-plane node must never be a drain target")
		}
	}
}

func TestMinNodeAgeSkipsFreshNodes(t *testing.T) {
	now := time.Now()
	snap := &model.ClusterSnapshot{
		TakenAt: now,
		Nodes: []model.Node{
			{Name: "fresh", Ready: true, CreatedAt: now.Add(-1 * time.Minute),
				Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 1 << 34, Pods: 110}},
			{Name: "old", Ready: true, CreatedAt: now.Add(-2 * time.Hour),
				Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 1 << 34, Pods: 110}},
		},
		Pods: []model.Pod{
			{Namespace: "d", Name: "p", NodeName: "fresh", Phase: model.PodRunning,
				Requests: model.Resources{MilliCPU: 100, MemoryBytes: 1 << 26}},
		},
	}

	opts := planner.DefaultOptions()
	opts.MinNodeAge = 10 * time.Minute
	opts.Now = now

	plan := planFor(t, snap, opts)
	for _, step := range plan.Steps {
		if step.TargetNode == "fresh" {
			t.Error("a node younger than MinNodeAge must not be drained; it is probably a deliberate scale-up")
		}
	}
}

func TestMaxStepsCapsPlanLength(t *testing.T) {
	snap := loadFixture(t, "a-fragmented")

	full := planFor(t, snap, testOptions())
	if len(full.Steps) < 3 {
		t.Skip("fixture does not produce enough steps to test the cap")
	}

	opts := testOptions()
	opts.MaxSteps = 2
	capped := planFor(t, snap, opts)

	if len(capped.Steps) != 2 {
		t.Errorf("MaxSteps=2 produced %d steps", len(capped.Steps))
	}
	// The capped plan must be a prefix of the full one, or "run steps 1-2"
	// would mean something different depending on the cap.
	for i := range capped.Steps {
		if capped.Steps[i].TargetNode != full.Steps[i].TargetNode {
			t.Errorf("capped plan diverges at step %d: %s vs %s",
				i+1, capped.Steps[i].TargetNode, full.Steps[i].TargetNode)
		}
	}
}

func TestEmptyClusterProducesEmptyPlan(t *testing.T) {
	snap := &model.ClusterSnapshot{TakenAt: time.Now()}
	plan := planFor(t, snap, testOptions())

	if len(plan.Steps) != 0 {
		t.Errorf("empty cluster produced %d steps", len(plan.Steps))
	}
	if plan.ReclaimedNodes() != 0 {
		t.Error("empty cluster cannot reclaim nodes")
	}
	if plan.ID == "" {
		t.Error("even an empty plan needs an ID")
	}
}

func findPod(p *constraints.Placement, nodeName, namespace, name string) (model.Pod, bool) {
	for _, occupant := range p.Occupants(nodeName) {
		if occupant.Namespace == namespace && occupant.Name == name {
			return occupant, true
		}
	}
	return model.Pod{}, false
}

// A node carrying karpenter.sh/do-not-disrupt must never be a drain
// candidate — Karpenter will not consolidate it, and neither may we.
func TestHandsOffNodeIsNeverACandidate(t *testing.T) {
	mk := func(name string, annotations map[string]string) model.Node {
		return model.Node{
			Name: name, Ready: true, Annotations: annotations,
			Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 32 << 30, Pods: 110},
		}
	}
	pod := func(name, on string) model.Pod {
		return model.Pod{
			Namespace: "shop", Name: name, NodeName: on, Phase: model.PodRunning, Ready: true,
			Requests: model.Resources{MilliCPU: 500, MemoryBytes: 1 << 28},
			Owner:    &model.OwnerRef{Kind: "ReplicaSet", Name: "web"},
		}
	}
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{
			mk("protected", map[string]string{"karpenter.sh/do-not-disrupt": "true"}),
			mk("ordinary", nil),
			mk("receiver", nil),
		},
		Pods: []model.Pod{pod("w1", "protected"), pod("w2", "ordinary"), pod("w3", "receiver")},
	}

	opts := planner.DefaultOptions()
	opts.MinNodeAge = 0
	plan, err := planner.Greedy{}.Plan(snap, constraints.Analyze(snap), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range plan.Steps {
		if s.TargetNode == "protected" {
			t.Fatal("the plan drains a node annotated do-not-disrupt")
		}
	}
}
