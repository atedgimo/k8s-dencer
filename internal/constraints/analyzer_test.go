package constraints_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// loadFixture replays a snapshot captured from a live cluster. This works only
// because internal/model imports nothing from k8s.io — the whole point of that
// constraint.
func loadFixture(t *testing.T, name string) *model.ClusterSnapshot {
	t.Helper()
	path := filepath.Join("..", "..", "test", "fixtures", name+".yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixture %s not available: %v", name, err)
	}
	var snap model.ClusterSnapshot
	if err := yaml.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return &snap
}

func TestAnalyzeFragmentedFixture(t *testing.T) {
	snap := loadFixture(t, "a-fragmented")
	analysis := constraints.Analyze(snap)

	if len(analysis.Pods) != len(snap.Pods) {
		t.Fatalf("analyzed %d pods, snapshot has %d", len(analysis.Pods), len(snap.Pods))
	}

	summary := analysis.Summarize()
	if summary.Pods == 0 {
		t.Fatal("summary counted no pods")
	}
	if summary.Movable == 0 {
		t.Error("no pod was considered movable in an unconstrained scenario")
	}

	// The filler workload is unconstrained apart from the KWOK node affinity,
	// so every one of its pods must have somewhere else to go.
	filler := 0
	for _, pc := range analysis.Pods {
		key := pc.Key()
		if !strings.Contains(key, "filler") {
			continue
		}
		filler++
		if !pc.Movable {
			t.Errorf("%s should be movable, blockers: %v", key, pc.Blockers())
		}
		if len(pc.CandidateNodes) == 0 {
			t.Errorf("%s has no candidate nodes", key)
		}
	}
	if filler == 0 {
		t.Skip("fixture contains no filler pods")
	}
	t.Logf("fragmented: %d pods, %d movable, %d blocked, %d stuck (%d filler)",
		summary.Pods, summary.Movable, summary.Blocked, summary.Stuck, filler)
}

// The whole product hinges on telling a PDB with headroom apart from one
// without, and on saying so in words a human can act on.
func TestAnalyzeDistinguishesBlockingPDBFromHealthyOne(t *testing.T) {
	snap := loadFixture(t, "b-pdb-blocked")
	analysis := constraints.Analyze(snap)

	var payments, catalog []constraints.Constraint
	for _, pc := range analysis.Pods {
		key := pc.Key()
		switch {
		case strings.Contains(key, "payments"):
			payments = append(payments, pc.Of(constraints.KindPDB)...)
			if pc.Movable {
				t.Errorf("%s must not be movable: its PDB allows no disruptions", key)
			}
		case strings.Contains(key, "catalog"):
			catalog = append(catalog, pc.Of(constraints.KindPDB)...)
			if !pc.Movable {
				t.Errorf("%s should be movable: its PDB has headroom. blockers=%v", key, pc.Blockers())
			}
		}
	}

	if len(payments) == 0 || len(catalog) == 0 {
		t.Skip("fixture lacks the pdb-blocked workloads")
	}

	for _, c := range payments {
		if !c.Blocking {
			t.Error("payments PDB constraint must be marked blocking")
		}
		if !strings.Contains(c.Explanation, "allows 0 disruptions") {
			t.Errorf("explanation must state the headroom, got: %q", c.Explanation)
		}
		if !strings.Contains(c.Explanation, "payments") {
			t.Errorf("explanation must name the responsible PDB, got: %q", c.Explanation)
		}
	}
	for _, c := range catalog {
		if c.Blocking {
			t.Errorf("catalog PDB has headroom and must not block: %q", c.Explanation)
		}
	}
	t.Logf("payments: %s", payments[0].Explanation)
	t.Logf("catalog:  %s", catalog[0].Explanation)
}

func TestNodeDrainableReportsBlockers(t *testing.T) {
	snap := loadFixture(t, "b-pdb-blocked")
	analysis := constraints.Analyze(snap)

	blockedNodes := 0
	for _, n := range snap.Nodes {
		drainable, blockers := analysis.NodeDrainable(n.Name)
		if drainable {
			continue
		}
		blockedNodes++
		if len(blockers) == 0 {
			t.Errorf("node %s is not drainable but reported no blockers", n.Name)
		}
		for _, b := range blockers {
			if b.Explanation == "" {
				t.Errorf("node %s has a blocker with no explanation: %+v", n.Name, b)
			}
		}
	}
	if blockedNodes == 0 {
		t.Error("expected at least the node hosting the zero-headroom PDB to be undrainable")
	}
}

// Every constraint must carry text a human can act on. A blank or generic
// explanation would surface in the UI and in the agent's answers.
func TestEveryConstraintHasAUsefulExplanation(t *testing.T) {
	for _, fixture := range []string{"a-fragmented", "b-pdb-blocked"} {
		snap := loadFixture(t, fixture)
		analysis := constraints.Analyze(snap)

		for _, pc := range analysis.Pods {
			key := pc.Key()
			for _, c := range pc.Constraints {
				if strings.TrimSpace(c.Explanation) == "" {
					t.Errorf("%s/%s: empty explanation", fixture, key)
				}
				if len(c.Explanation) < 20 {
					t.Errorf("%s/%s: explanation too terse to be useful: %q", fixture, key, c.Explanation)
				}
				if c.Kind == "" {
					t.Errorf("%s/%s: constraint has no kind: %+v", fixture, key, c)
				}
			}
		}
	}
}

// A pod is blocked or it is not; an analysis that says a pod is movable while
// also listing a blocker would make the planner and the UI disagree.
func TestMovableAndBlockersAreConsistent(t *testing.T) {
	for _, fixture := range []string{"a-fragmented", "b-pdb-blocked"} {
		snap := loadFixture(t, fixture)
		analysis := constraints.Analyze(snap)
		for _, pc := range analysis.Pods {
			key := pc.Key()
			if pc.Movable && len(pc.Blockers()) > 0 {
				t.Errorf("%s/%s: movable but has blockers %v", fixture, key, pc.Blockers())
			}
		}
	}
}

func TestAnalysisIsDeterministic(t *testing.T) {
	snap := loadFixture(t, "a-fragmented")

	first, err := yaml.Marshal(constraints.Analyze(snap))
	if err != nil {
		t.Fatal(err)
	}
	second, err := yaml.Marshal(constraints.Analyze(snap))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("analysis is not deterministic across runs on identical input")
	}
}

// The ecosystem's hands-off annotations must make a pod immovable, with the
// convention named in the explanation. Until this landed, the informer
// transform stripped ALL annotations, so the analyzer was structurally blind
// to the one signal Karpenter and the cluster autoscaler both honour.
func TestHandsOffAnnotationsPinThePod(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{
			{Name: "a", Ready: true, Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 32 << 30, Pods: 110}},
			{Name: "b", Ready: true, Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 32 << 30, Pods: 110}},
		},
		Pods: []model.Pod{{
			Namespace: "shop", Name: "pinned-1", NodeName: "a",
			Phase: model.PodRunning, Ready: true, DoNotDisrupt: true,
			Requests: model.Resources{MilliCPU: 500, MemoryBytes: 1 << 28},
			Owner:    &model.OwnerRef{Kind: "ReplicaSet", Name: "shop"},
		}},
	}

	analysis := constraints.Analyze(snap)
	pc, ok := analysis.ForPod("shop/pinned-1")
	if !ok {
		t.Fatal("pod not analysed")
	}
	if pc.Movable {
		t.Error("a do-not-disrupt pod is Movable; the owner's explicit opt-out is being ignored")
	}
	found := false
	for _, c := range pc.Of(constraints.KindDoNotDisrupt) {
		found = true
		if !c.Blocking || !c.Hard {
			t.Error("the hands-off constraint must be hard and blocking")
		}
	}
	if !found {
		t.Error("no DoNotDisrupt constraint; the pod is pinned without its reason being explainable")
	}
	if drainable, _ := analysis.NodeDrainable("a"); drainable {
		t.Error("a node holding a hands-off pod reports drainable")
	}
}
