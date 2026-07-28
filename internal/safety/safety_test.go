package safety_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/safety"
)

func node(name string, cpu int64, opts ...func(*model.Node)) model.Node {
	n := model.Node{
		Name: name, Ready: true,
		Labels:      map[string]string{model.LabelZone: "z1"},
		Allocatable: model.Resources{MilliCPU: cpu, MemoryBytes: 1 << 34, Pods: 110},
	}
	for _, o := range opts {
		o(&n)
	}
	return n
}

func cordoned(n *model.Node) { n.Unschedulable = true }
func notReady(n *model.Node) { n.Ready = false }

func pod(ns, name, on string, cpu int64, opts ...func(*model.Pod)) model.Pod {
	p := model.Pod{
		Namespace: ns, Name: name, NodeName: on, Phase: model.PodRunning,
		Labels:   map[string]string{"app": name},
		Requests: model.Resources{MilliCPU: cpu, MemoryBytes: 1 << 28},
		Owner:    &model.OwnerRef{Kind: "ReplicaSet", Name: name},
	}
	for _, o := range opts {
		o(&p)
	}
	return p
}

func daemonSet(p *model.Pod) { p.Owner = &model.OwnerRef{Kind: "DaemonSet", Name: "agent"} }

func step(seq int, target string, impact model.ImpactRating) model.PlanStep {
	return model.PlanStep{
		ID: "s", SequenceNumber: seq, TargetNode: target, Impact: impact,
		Rationale: "test rationale",
	}
}

// permissive limits isolate the rule under test from the caps.
func permissive() safety.Limits {
	return safety.Limits{MaxNodesPerRun: 100, MinReadyNodes: 0, AllowRed: false}
}

func blockedBy(t *testing.T, err error, want safety.Rule) *safety.Blocked {
	t.Helper()
	var b *safety.Blocked
	if !errors.As(err, &b) {
		t.Fatalf("expected a %s refusal, got %v", want, err)
	}
	if b.Rule != want {
		t.Fatalf("blocked by %s, want %s: %s", b.Rule, want, b.Reason)
	}
	return b
}

// Red is refused outright. Phase 3 introduces maintenance windows; until then
// there is no window to be inside, and treating "window-only" as "allowed" is
// the failure this rule exists to prevent.
func TestRedStepsAreRefused(t *testing.T) {
	live := &model.ClusterSnapshot{Nodes: []model.Node{node("a", 4000), node("b", 4000)}}
	g := safety.New(permissive())

	err := g.CheckStep(step(7, "a", model.ImpactRed), live, safety.RunState{})
	b := blockedBy(t, err, safety.RuleRedRequiresWindow)

	// The refusal must quote the classifier's own rationale, so the operator
	// reads the same words the UI and the agent show them.
	if !strings.Contains(b.Reason, "test rationale") {
		t.Errorf("refusal does not quote the rationale: %s", b.Reason)
	}
	if !strings.Contains(b.Reason, "maintenance window") {
		t.Errorf("refusal should say why Red is special: %s", b.Reason)
	}
}

func TestGreenAndYellowStepsPass(t *testing.T) {
	live := &model.ClusterSnapshot{Nodes: []model.Node{node("a", 4000), node("b", 4000)}}
	g := safety.New(permissive())

	for _, impact := range []model.ImpactRating{model.ImpactGreen, model.ImpactYellow} {
		if err := g.CheckStep(step(1, "a", impact), live, safety.RunState{}); err != nil {
			t.Errorf("%s step refused: %v", impact, err)
		}
	}
}

func TestMaxNodesPerRunCapsTheWholeRequest(t *testing.T) {
	live := &model.ClusterSnapshot{Nodes: []model.Node{node("a", 4000), node("b", 4000)}}
	g := safety.New(safety.Limits{MaxNodesPerRun: 3})

	if err := g.CheckStep(step(1, "a", model.ImpactGreen), live, safety.RunState{NodesDrainedSoFar: 2}); err != nil {
		t.Fatalf("third node refused early: %v", err)
	}
	err := g.CheckStep(step(1, "a", model.ImpactGreen), live, safety.RunState{NodesDrainedSoFar: 3})
	blockedBy(t, err, safety.RuleMaxNodesPerRun)
}

// The floor counts schedulable capacity, not raw node count: a cordoned or
// NotReady node cannot absorb anything and must not prop up the total.
func TestMinReadyNodesCountsOnlySchedulableCapacity(t *testing.T) {
	g := safety.New(safety.Limits{MinReadyNodes: 2, MaxNodesPerRun: 100})

	t.Run("enough remain", func(t *testing.T) {
		live := &model.ClusterSnapshot{Nodes: []model.Node{
			node("a", 4000), node("b", 4000), node("c", 4000),
		}}
		if err := g.CheckStep(step(1, "a", model.ImpactGreen), live, safety.RunState{}); err != nil {
			t.Errorf("refused with 2 nodes left: %v", err)
		}
	})

	t.Run("cordoned and NotReady nodes do not count", func(t *testing.T) {
		live := &model.ClusterSnapshot{Nodes: []model.Node{
			node("a", 4000),
			node("b", 4000, cordoned),
			node("c", 4000, notReady),
			node("d", 4000),
		}}
		// Only a and d are schedulable; draining a leaves 1, below the floor.
		err := g.CheckStep(step(1, "a", model.ImpactGreen), live, safety.RunState{})
		b := blockedBy(t, err, safety.RuleMinReadyNodes)
		if !strings.Contains(b.Reason, "floor of 2") {
			t.Errorf("refusal should state the floor: %s", b.Reason)
		}
	})
}

func TestVanishedNodeIsRefused(t *testing.T) {
	live := &model.ClusterSnapshot{Nodes: []model.Node{node("b", 4000)}}
	err := safety.New(permissive()).CheckStep(step(1, "gone", model.ImpactGreen), live, safety.RunState{})
	blockedBy(t, err, safety.RuleNodeNotFound)
}

// The freshness rule is what catches a plan that was valid when computed and
// is not any more — the cluster filled up in between.
func TestStepIsRefusedWhenPodsHaveNowhereToGo(t *testing.T) {
	g := safety.New(permissive())

	t.Run("feasible", func(t *testing.T) {
		live := &model.ClusterSnapshot{
			Nodes: []model.Node{node("a", 4000), node("b", 4000)},
			Pods:  []model.Pod{pod("app", "x", "a", 1000)},
		}
		if err := g.CheckStep(step(1, "a", model.ImpactGreen), live, safety.RunState{}); err != nil {
			t.Errorf("refused a feasible step: %v", err)
		}
	})

	t.Run("destination too full", func(t *testing.T) {
		live := &model.ClusterSnapshot{
			Nodes: []model.Node{node("a", 4000), node("b", 4000)},
			Pods: []model.Pod{
				pod("app", "big", "a", 3000),
				pod("app", "filler", "b", 3800), // b has only 200m left
			},
		}
		err := g.CheckStep(step(1, "a", model.ImpactGreen), live, safety.RunState{})
		b := blockedBy(t, err, safety.RuleStepFreshness)
		if !strings.Contains(b.Reason, "app/big") {
			t.Errorf("refusal should name the homeless pod: %s", b.Reason)
		}
	})

	// Two pods that each fit alone but not together must be refused: the check
	// has to model them landing cumulatively, not independently.
	t.Run("pods fit individually but not collectively", func(t *testing.T) {
		live := &model.ClusterSnapshot{
			Nodes: []model.Node{node("a", 4000), node("b", 4000)},
			Pods: []model.Pod{
				pod("app", "p1", "a", 2500),
				pod("app", "p2", "a", 2500),
			},
		}
		err := g.CheckStep(step(1, "a", model.ImpactGreen), live, safety.RunState{})
		blockedBy(t, err, safety.RuleStepFreshness)
	})
}

// DaemonSet pods leave with the node. Counting them would make every node
// look undrainable and block the product from doing anything at all.
func TestDaemonSetPodsDoNotBlockADrain(t *testing.T) {
	live := &model.ClusterSnapshot{
		Nodes: []model.Node{node("a", 1000), node("b", 1000)},
		Pods: []model.Pod{
			pod("kube-system", "agent-a", "a", 900, daemonSet),
			pod("kube-system", "agent-b", "b", 900, daemonSet),
		},
	}
	if err := safety.New(permissive()).CheckStep(step(1, "a", model.ImpactGreen), live, safety.RunState{}); err != nil {
		t.Errorf("a node holding only DaemonSet pods should be drainable: %v", err)
	}
}

// The eviction gate reads live headroom. A PDB that exists but permits
// disruption must not block; one at zero must.
func TestEvictionRespectsLivePDBHeadroom(t *testing.T) {
	g := safety.New(permissive())
	target := pod("app", "web-1", "a", 100)

	covering := func(allowed int32) model.PodDisruptionBudget {
		return model.PodDisruptionBudget{
			Namespace: "app", Name: "web",
			Selector:           &model.LabelSelector{MatchLabels: map[string]string{"app": "web-1"}},
			DisruptionsAllowed: allowed, CurrentHealthy: 3, DesiredHealthy: 3,
		}
	}

	t.Run("headroom available", func(t *testing.T) {
		live := &model.ClusterSnapshot{PDBs: []model.PodDisruptionBudget{covering(1)}}
		if err := g.CheckEviction(target, live); err != nil {
			t.Errorf("blocked despite headroom: %v", err)
		}
	})

	t.Run("zero headroom", func(t *testing.T) {
		live := &model.ClusterSnapshot{PDBs: []model.PodDisruptionBudget{covering(0)}}
		b := blockedBy(t, g.CheckEviction(target, live), safety.RulePDBHeadroom)
		if !strings.Contains(b.Reason, "app/web") {
			t.Errorf("refusal should name the PDB: %s", b.Reason)
		}
	})

	t.Run("PDB in another namespace is irrelevant", func(t *testing.T) {
		other := covering(0)
		other.Namespace = "elsewhere"
		live := &model.ClusterSnapshot{PDBs: []model.PodDisruptionBudget{other}}
		if err := g.CheckEviction(target, live); err != nil {
			t.Errorf("a PDB in another namespace blocked the eviction: %v", err)
		}
	})

	t.Run("PDB whose selector does not match is irrelevant", func(t *testing.T) {
		other := covering(0)
		other.Selector = &model.LabelSelector{MatchLabels: map[string]string{"app": "something-else"}}
		live := &model.ClusterSnapshot{PDBs: []model.PodDisruptionBudget{other}}
		if err := g.CheckEviction(target, live); err != nil {
			t.Errorf("a non-matching PDB blocked the eviction: %v", err)
		}
	})
}

// AllowRed exists so the rule reads honestly, but nothing in Phase 2 sets it.
// If a future change flips the default, this fails.
func TestRedIsNotAllowedByDefault(t *testing.T) {
	if safety.DefaultLimits().AllowRed {
		t.Error("DefaultLimits permits Red steps; maintenance windows do not exist yet")
	}
	if safety.DefaultLimits().MinReadyNodes <= 0 {
		t.Error("DefaultLimits has no node floor")
	}
	if safety.DefaultLimits().MaxNodesPerRun <= 0 {
		t.Error("DefaultLimits has no cap on nodes per run")
	}
}

// stubWindows stands in for the cluster's maintenance windows.
type stubWindows struct {
	allow bool
	why   string
}

func (s stubWindows) AllowsRedOn(model.Node) (bool, string) { return s.allow, s.why }

// The whole point of Phase 3: an open window that permits Red unlocks a step
// that was previously refused outright.
func TestRedIsPermittedByAnOpenWindow(t *testing.T) {
	live := &model.ClusterSnapshot{Nodes: []model.Node{node("a", 4000), node("b", 4000)}}
	red := step(7, "a", model.ImpactRed)

	t.Run("no windows configured", func(t *testing.T) {
		err := safety.New(permissive()).CheckStep(red, live, safety.RunState{})
		b := blockedBy(t, err, safety.RuleRedRequiresWindow)
		if !strings.Contains(b.Reason, "none is configured") {
			t.Errorf("refusal should say no window exists: %s", b.Reason)
		}
	})

	t.Run("window open but Red not permitted", func(t *testing.T) {
		g := safety.New(permissive()).WithWindows(stubWindows{
			allow: false, why: "maintenance window nightly is open but does not permit Red steps",
		})
		b := blockedBy(t, g.CheckStep(red, live, safety.RunState{}), safety.RuleRedRequiresWindow)
		// The refusal must carry the window's own explanation, so an operator
		// can tell "nothing is open" from "open, but not for this".
		if !strings.Contains(b.Reason, "does not permit Red steps") {
			t.Errorf("refusal lost the window's reason: %s", b.Reason)
		}
		// And still quote the classifier.
		if !strings.Contains(b.Reason, "test rationale") {
			t.Errorf("refusal lost the rationale: %s", b.Reason)
		}
	})

	t.Run("window open and permits Red", func(t *testing.T) {
		g := safety.New(permissive()).WithWindows(stubWindows{allow: true, why: "open until 06:00"})
		if err := g.CheckStep(red, live, safety.RunState{}); err != nil {
			t.Errorf("an open permissive window should admit a Red step: %v", err)
		}
	})
}

// A window must not turn off the other rails. It unlocks Red; it does not
// license draining the cluster to nothing.
func TestAnOpenWindowDoesNotSuspendTheOtherRails(t *testing.T) {
	g := safety.New(safety.Limits{MaxNodesPerRun: 2, MinReadyNodes: 2}).
		WithWindows(stubWindows{allow: true, why: "open"})

	live := &model.ClusterSnapshot{Nodes: []model.Node{node("a", 4000), node("b", 4000)}}

	// Node floor still applies.
	blockedBy(t, g.CheckStep(step(1, "a", model.ImpactRed), live, safety.RunState{}),
		safety.RuleMinReadyNodes)

	// Run cap still applies.
	big := &model.ClusterSnapshot{Nodes: []model.Node{
		node("a", 4000), node("b", 4000), node("c", 4000), node("d", 4000),
	}}
	blockedBy(t, g.CheckStep(step(1, "a", model.ImpactRed), big, safety.RunState{NodesDrainedSoFar: 2}),
		safety.RuleMaxNodesPerRun)
}

// Windows are evaluated per node, so a step on an uncovered node is still
// refused while one inside the window's scope is not.
func TestRedIsCheckedAgainstTheStepsOwnNode(t *testing.T) {
	live := &model.ClusterSnapshot{Nodes: []model.Node{
		{Name: "batch-1", Ready: true, Labels: map[string]string{"pool": "batch"},
			Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 34, Pods: 110}},
		{Name: "web-1", Ready: true, Labels: map[string]string{"pool": "web"},
			Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 34, Pods: 110}},
	}}

	// A stub that only covers the batch pool, the way a scoped window would.
	scoped := scopedWindows{pool: "batch"}
	g := safety.New(permissive()).WithWindows(scoped)

	if err := g.CheckStep(step(1, "batch-1", model.ImpactRed), live, safety.RunState{}); err != nil {
		t.Errorf("a node inside the window's scope should be permitted: %v", err)
	}
	blockedBy(t, g.CheckStep(step(2, "web-1", model.ImpactRed), live, safety.RunState{}),
		safety.RuleRedRequiresWindow)
}

type scopedWindows struct{ pool string }

func (s scopedWindows) AllowsRedOn(n model.Node) (bool, string) {
	if n.Labels["pool"] == s.pool {
		return true, "covered"
	}
	return false, "no open maintenance window covers node " + n.Name
}
