package constraints_test

import (
	"strings"
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// These exercise the feasibility rules directly with hand-built topologies.
// The planner in M4 trusts CanPlace completely: a false positive here becomes
// a plan the scheduler then refuses to execute.

func node(name, zone string, milliCPU int64, taints ...model.Taint) model.Node {
	return model.Node{
		Name:        name,
		Ready:       true,
		Labels:      map[string]string{model.LabelZone: zone, model.LabelHostname: name},
		Taints:      taints,
		Capacity:    model.Resources{MilliCPU: milliCPU, MemoryBytes: 1 << 34, Pods: 110},
		Allocatable: model.Resources{MilliCPU: milliCPU, MemoryBytes: 1 << 34, Pods: 110},
	}
}

func pod(name, nodeName string, milliCPU int64, labels map[string]string) model.Pod {
	return model.Pod{
		Namespace: "default",
		Name:      name,
		NodeName:  nodeName,
		Labels:    labels,
		Phase:     model.PodRunning,
		Requests:  model.Resources{MilliCPU: milliCPU, MemoryBytes: 1 << 30},
	}
}

func TestCanPlaceResources(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{node("a", "z1", 4000), node("b", "z1", 4000)},
		Pods:  []model.Pod{pod("big", "b", 3500, nil)},
	}
	p := constraints.NewPlacement(snap)

	candidate := pod("new", "", 1000, nil)
	if ok, why := p.CanPlace(candidate, "a"); !ok {
		t.Errorf("should fit on empty node a: %s", why)
	}
	if ok, _ := p.CanPlace(candidate, "b"); ok {
		t.Error("must not fit on node b: only 500m free")
	}

	// The reason must be specific enough to show a user.
	_, why := p.CanPlace(candidate, "b")
	if !strings.Contains(why, "free") {
		t.Errorf("resource rejection should describe free capacity, got: %q", why)
	}
}

func TestCanPlaceRespectsTaints(t *testing.T) {
	dedicated := model.Taint{Key: "dedicated", Value: "batch", Effect: model.TaintEffectNoSchedule}
	prefer := model.Taint{Key: "soft", Effect: model.TaintEffectPreferNoSchedule}

	snap := &model.ClusterSnapshot{Nodes: []model.Node{
		node("plain", "z1", 4000),
		node("tainted", "z1", 4000, dedicated),
		node("soft", "z1", 4000, prefer),
	}}
	p := constraints.NewPlacement(snap)

	plain := pod("plain-pod", "", 100, nil)
	if ok, _ := p.CanPlace(plain, "tainted"); ok {
		t.Error("untolerating pod must not be placed on a tainted node")
	}
	// PreferNoSchedule is a preference; the scheduler will still use the node,
	// so it must not block a move.
	if ok, why := p.CanPlace(plain, "soft"); !ok {
		t.Errorf("PreferNoSchedule must not block placement: %s", why)
	}

	tolerating := plain
	tolerating.Tolerations = []model.Toleration{
		{Key: "dedicated", Operator: model.TolerationOpEqual, Value: "batch", Effect: model.TaintEffectNoSchedule},
	}
	if ok, why := p.CanPlace(tolerating, "tainted"); !ok {
		t.Errorf("tolerating pod should be placeable: %s", why)
	}
}

func TestCanPlaceRespectsNodeSelectorAndAffinity(t *testing.T) {
	nodes := []model.Node{node("kwok", "z1", 4000), node("real", "z1", 4000)}
	nodes[0].Labels["type"] = "kwok"
	snap := &model.ClusterSnapshot{Nodes: nodes}
	p := constraints.NewPlacement(snap)

	selector := pod("sel", "", 100, nil)
	selector.NodeSelector = map[string]string{"type": "kwok"}
	if ok, _ := p.CanPlace(selector, "real"); ok {
		t.Error("nodeSelector must exclude the unlabelled node")
	}
	if ok, why := p.CanPlace(selector, "kwok"); !ok {
		t.Errorf("nodeSelector should admit the labelled node: %s", why)
	}

	affinity := pod("aff", "", 100, nil)
	affinity.NodeAffinity = &model.NodeAffinity{RequiredTerms: []model.NodeSelectorTerm{{
		MatchExpressions: []model.SelectorRequirement{
			{Key: "type", Operator: model.SelectorOpIn, Values: []string{"kwok"}},
		},
	}}}
	if ok, _ := p.CanPlace(affinity, "real"); ok {
		t.Error("required node affinity must exclude the unlabelled node")
	}
	if ok, why := p.CanPlace(affinity, "kwok"); !ok {
		t.Errorf("required node affinity should admit the labelled node: %s", why)
	}
}

// Scenario d: cache replicas cannot share a node, so each one pins a node open
// regardless of how much capacity is free elsewhere.
func TestCanPlaceHostnameAntiAffinity(t *testing.T) {
	labels := map[string]string{"app": "cache"}
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{node("a", "z1", 8000), node("b", "z1", 8000)},
		Pods:  []model.Pod{pod("cache-0", "a", 100, labels)},
	}
	p := constraints.NewPlacement(snap)

	candidate := pod("cache-1", "", 100, labels)
	candidate.PodAffinity = &model.PodAffinity{RequiredAntiAffinity: []model.PodAffinityTerm{{
		TopologyKey:   model.LabelHostname,
		LabelSelector: &model.LabelSelector{MatchLabels: labels},
	}}}

	if ok, _ := p.CanPlace(candidate, "a"); ok {
		t.Error("anti-affinity must forbid sharing a node with a matching pod")
	}
	if ok, why := p.CanPlace(candidate, "b"); !ok {
		t.Errorf("empty node b should accept it: %s", why)
	}
	_, why := p.CanPlace(candidate, "a")
	if !strings.Contains(why, "anti-affinity") {
		t.Errorf("reason should name anti-affinity, got %q", why)
	}
}

// A zone-scoped anti-affinity rule must consider sibling nodes in the same
// zone, not just the target node. Checking only the target node is the easy
// bug here, and it produces plans the scheduler rejects.
func TestCanPlaceZoneAntiAffinitySpansNodes(t *testing.T) {
	labels := map[string]string{"app": "zonal"}
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{
			node("a1", "z1", 8000), node("a2", "z1", 8000), node("b1", "z2", 8000),
		},
		Pods: []model.Pod{pod("zonal-0", "a1", 100, labels)},
	}
	p := constraints.NewPlacement(snap)

	candidate := pod("zonal-1", "", 100, labels)
	candidate.PodAffinity = &model.PodAffinity{RequiredAntiAffinity: []model.PodAffinityTerm{{
		TopologyKey:   model.LabelZone,
		LabelSelector: &model.LabelSelector{MatchLabels: labels},
	}}}

	if ok, _ := p.CanPlace(candidate, "a2"); ok {
		t.Error("a2 is empty but shares zone z1 with zonal-0; zone anti-affinity must reject it")
	}
	if ok, why := p.CanPlace(candidate, "b1"); !ok {
		t.Errorf("b1 is in a different zone and should be accepted: %s", why)
	}
}

// Scenario c: maxSkew 1 across three zones. The check must simulate the
// placement, since the skew is evaluated after the pod lands.
func TestCanPlaceTopologySpread(t *testing.T) {
	labels := map[string]string{"app": "frontend"}
	spread := []model.TopologySpreadConstraint{{
		MaxSkew:           1,
		TopologyKey:       model.LabelZone,
		WhenUnsatisfiable: model.DoNotSchedule,
		LabelSelector:     &model.LabelSelector{MatchLabels: labels},
	}}

	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{node("a", "z1", 8000), node("b", "z2", 8000), node("c", "z3", 8000)},
		Pods: []model.Pod{
			pod("fe-0", "a", 100, labels),
			pod("fe-1", "b", 100, labels),
		},
	}
	p := constraints.NewPlacement(snap)

	candidate := pod("fe-2", "", 100, labels)
	candidate.TopologySpread = spread

	// z3 is empty; landing there gives 1/1/1 — skew 0.
	if ok, why := p.CanPlace(candidate, "c"); !ok {
		t.Errorf("empty zone z3 must be allowed: %s", why)
	}
	// z1 already has one; landing there gives 2/1/0 against empty z3 — skew 2.
	if ok, _ := p.CanPlace(candidate, "a"); ok {
		t.Error("placing into z1 would exceed maxSkew 1 while z3 is empty")
	}
	_, why := p.CanPlace(candidate, "a")
	if !strings.Contains(why, "maxSkew") {
		t.Errorf("reason should mention maxSkew, got %q", why)
	}
}

// ScheduleAnyway is a preference. Reporting it as a blocker would make the
// planner refuse moves the scheduler would happily make.
func TestSoftTopologySpreadDoesNotBlock(t *testing.T) {
	labels := map[string]string{"app": "frontend"}
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{node("a", "z1", 8000), node("b", "z2", 8000)},
		Pods:  []model.Pod{pod("fe-0", "a", 100, labels), pod("fe-1", "a", 100, labels)},
	}
	p := constraints.NewPlacement(snap)

	candidate := pod("fe-2", "", 100, labels)
	candidate.TopologySpread = []model.TopologySpreadConstraint{{
		MaxSkew:           1,
		TopologyKey:       model.LabelZone,
		WhenUnsatisfiable: model.ScheduleAnyway,
		LabelSelector:     &model.LabelSelector{MatchLabels: labels},
	}}

	if ok, why := p.CanPlace(candidate, "a"); !ok {
		t.Errorf("ScheduleAnyway must never block placement: %s", why)
	}
}

func TestCanPlaceRejectsCordonedAndNotReady(t *testing.T) {
	cordoned := node("cordoned", "z1", 8000)
	cordoned.Unschedulable = true
	notReady := node("down", "z1", 8000)
	notReady.Ready = false

	snap := &model.ClusterSnapshot{Nodes: []model.Node{cordoned, notReady, node("ok", "z1", 8000)}}
	p := constraints.NewPlacement(snap)

	candidate := pod("x", "", 100, nil)
	if ok, _ := p.CanPlace(candidate, "cordoned"); ok {
		t.Error("cordoned node must be rejected")
	}
	if ok, _ := p.CanPlace(candidate, "down"); ok {
		t.Error("NotReady node must be rejected")
	}
	if ok, why := p.CanPlace(candidate, "ok"); !ok {
		t.Errorf("healthy node should be accepted: %s", why)
	}
	if ok, _ := p.CanPlace(candidate, "nonexistent"); ok {
		t.Error("unknown node must be rejected")
	}
}

func TestPlaceRemoveAndIsEmpty(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{node("a", "z1", 4000)},
		Pods:  []model.Pod{pod("p1", "a", 1000, nil)},
	}
	p := constraints.NewPlacement(snap)

	if p.IsEmpty("a") {
		t.Error("node a holds a movable pod")
	}
	free := p.Free("a")
	if free.MilliCPU != 3000 {
		t.Errorf("free cpu = %d, want 3000", free.MilliCPU)
	}

	p.Remove(pod("p1", "a", 1000, nil))
	if !p.IsEmpty("a") {
		t.Error("node a should be empty after removal")
	}
	if got := p.Free("a").MilliCPU; got != 4000 {
		t.Errorf("free cpu after removal = %d, want 4000", got)
	}

	p.Place(pod("p2", "", 500, nil), "a")
	if got := p.Free("a").MilliCPU; got != 3500 {
		t.Errorf("free cpu after place = %d, want 3500", got)
	}
}

// A node holding only DaemonSet pods is still reclaimable: those pods are
// recreated on every node regardless, so they do not keep a node alive.
func TestIsEmptyIgnoresDaemonSetPods(t *testing.T) {
	ds := pod("node-exporter", "a", 100, nil)
	ds.Owner = &model.OwnerRef{Kind: "DaemonSet", Name: "node-exporter"}

	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{node("a", "z1", 4000)},
		Pods:  []model.Pod{ds},
	}
	p := constraints.NewPlacement(snap)

	if !p.IsEmpty("a") {
		t.Error("a node holding only DaemonSet pods must count as reclaimable")
	}
}

// The packer explores many candidate packings; each must be isolated.
func TestCloneIsolatesMutations(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{node("a", "z1", 4000), node("b", "z1", 4000)},
		Pods:  []model.Pod{pod("p1", "a", 1000, nil)},
	}
	original := constraints.NewPlacement(snap)
	clone := original.Clone()

	clone.Place(pod("p2", "", 1000, nil), "b")

	if original.Free("b").MilliCPU != 4000 {
		t.Error("mutating the clone changed the original")
	}
	if clone.Free("b").MilliCPU != 3000 {
		t.Error("clone did not record its own placement")
	}
}

func TestCandidateNodesExcludesCurrentNode(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{node("a", "z1", 4000), node("b", "z1", 4000), node("c", "z1", 4000)},
		Pods:  []model.Pod{pod("p1", "a", 1000, nil)},
	}
	p := constraints.NewPlacement(snap)

	got := p.CandidateNodes(pod("p1", "a", 1000, nil))
	for _, n := range got {
		if n == "a" {
			t.Error("candidate list must exclude the pod's current node")
		}
	}
	if len(got) != 2 {
		t.Errorf("expected 2 candidates, got %v", got)
	}
}
