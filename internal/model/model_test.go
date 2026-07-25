package model_test

import (
	"go/build"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// TestModelHasNoKubernetesImports guards the property the whole testing
// strategy rests on. If model ever imports k8s.io, snapshots stop being plain
// data, fixtures stop round-tripping, and the planner can no longer be tested
// without a cluster. That degradation would otherwise happen silently.
func TestModelHasNoKubernetesImports(t *testing.T) {
	pkg, err := build.Default.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("inspect package: %v", err)
	}
	for _, imp := range pkg.Imports {
		if strings.HasPrefix(imp, "k8s.io/") || strings.HasPrefix(imp, "sigs.k8s.io/") {
			t.Errorf("internal/model must not import Kubernetes packages, found %q", imp)
		}
	}
}

func TestSnapshotRoundTripsThroughYAML(t *testing.T) {
	original := &model.ClusterSnapshot{
		TakenAt: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
		Nodes: []model.Node{{
			Name:        "kwok-node-0",
			Labels:      map[string]string{model.LabelZone: "zone-a", "type": "kwok"},
			Taints:      []model.Taint{{Key: "kwok.x-k8s.io/node", Value: "fake", Effect: model.TaintEffectNoSchedule}},
			Capacity:    model.Resources{MilliCPU: 8000, MemoryBytes: 34359738368, Pods: 110},
			Allocatable: model.Resources{MilliCPU: 8000, MemoryBytes: 34359738368, Pods: 110},
			Ready:       true,
		}},
		Pods: []model.Pod{{
			Namespace: "dencer-demo",
			Name:      "filler-abc",
			NodeName:  "kwok-node-0",
			Labels:    map[string]string{"app": "dencer-filler"},
			Phase:     model.PodRunning,
			Requests:  model.Resources{MilliCPU: 1000, MemoryBytes: 2147483648},
			Owner:     &model.OwnerRef{Kind: "Deployment", Name: "filler"},
			TopologySpread: []model.TopologySpreadConstraint{{
				MaxSkew:           1,
				TopologyKey:       model.LabelZone,
				WhenUnsatisfiable: model.DoNotSchedule,
				LabelSelector:     &model.LabelSelector{MatchLabels: map[string]string{"app": "dencer-filler"}},
			}},
		}},
		PDBs: []model.PodDisruptionBudget{{
			Namespace:          "dencer-demo",
			Name:               "payments",
			Selector:           &model.LabelSelector{MatchLabels: map[string]string{"app": "payments"}},
			DisruptionsAllowed: 0,
			CurrentHealthy:     3,
			DesiredHealthy:     3,
			ExpectedPods:       3,
		}},
	}

	encoded, err := yaml.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded model.ClusterSnapshot
	if err := yaml.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	reencoded, err := yaml.Marshal(&decoded)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(encoded) != string(reencoded) {
		t.Errorf("snapshot did not round-trip\n--- first ---\n%s\n--- second ---\n%s", encoded, reencoded)
	}

	if !decoded.PDBs[0].Blocks() {
		t.Error("PDB with zero disruptions allowed must report Blocks()")
	}
	if got := decoded.Nodes[0].Zone(); got != "zone-a" {
		t.Errorf("Zone() = %q, want zone-a", got)
	}
}

func TestResourcesArithmetic(t *testing.T) {
	a := model.Resources{MilliCPU: 1500, MemoryBytes: 1024, Pods: 1}
	b := model.Resources{MilliCPU: 500, MemoryBytes: 512, Pods: 1}

	if got := a.Add(b); got != (model.Resources{MilliCPU: 2000, MemoryBytes: 1536, Pods: 2}) {
		t.Errorf("Add = %+v", got)
	}
	if got := a.Sub(b); got != (model.Resources{MilliCPU: 1000, MemoryBytes: 512, Pods: 0}) {
		t.Errorf("Sub = %+v", got)
	}

	// Subtraction clamps: a negative remainder would make an over-committed
	// node look like it had capacity.
	if got := b.Sub(a); !got.IsZero() {
		t.Errorf("Sub must clamp at zero, got %+v", got)
	}

	capacity := model.Resources{MilliCPU: 2000, MemoryBytes: 2048, Pods: 10}
	if !a.Fits(capacity) {
		t.Error("expected fit")
	}
	if (model.Resources{MilliCPU: 4000}).Fits(capacity) {
		t.Error("expected no fit on cpu")
	}

	// The dominant dimension is what limits packing: 75% cpu beats 50% memory.
	if got := a.DominantRatio(capacity); got != 0.75 {
		t.Errorf("DominantRatio = %v, want 0.75", got)
	}
}

func TestRatioHandlesZeroCapacity(t *testing.T) {
	r := model.Resources{MilliCPU: 100}
	cpu, mem, pods := r.Ratio(model.Resources{})
	if cpu != 0 || mem != 0 || pods != 0 {
		t.Errorf("zero capacity must yield zero ratios, got %v %v %v", cpu, mem, pods)
	}
}

func TestTolerationMatching(t *testing.T) {
	kwokTaint := model.Taint{Key: "kwok.x-k8s.io/node", Value: "fake", Effect: model.TaintEffectNoSchedule}
	dedicated := model.Taint{Key: "dedicated", Value: "batch", Effect: model.TaintEffectNoSchedule}

	cases := []struct {
		name string
		tol  model.Toleration
		want bool
	}{
		{"exists on key", model.Toleration{Key: "kwok.x-k8s.io/node", Operator: model.TolerationOpExists, Effect: model.TaintEffectNoSchedule}, true},
		{"equal matching value", model.Toleration{Key: "kwok.x-k8s.io/node", Operator: model.TolerationOpEqual, Value: "fake"}, true},
		{"equal wrong value", model.Toleration{Key: "kwok.x-k8s.io/node", Operator: model.TolerationOpEqual, Value: "real"}, false},
		{"empty operator defaults to equal", model.Toleration{Key: "kwok.x-k8s.io/node", Value: "fake"}, true},
		{"wrong key", model.Toleration{Key: "other", Operator: model.TolerationOpExists}, false},
		{"empty key with exists tolerates everything", model.Toleration{Operator: model.TolerationOpExists}, true},
		{"effect mismatch", model.Toleration{Key: "kwok.x-k8s.io/node", Operator: model.TolerationOpExists, Effect: model.TaintEffectNoExecute}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tol.ToleratesTaint(kwokTaint); got != tc.want {
				t.Errorf("ToleratesTaint = %v, want %v", got, tc.want)
			}
		})
	}

	// A pod tolerating only the kwok taint must not reach the dedicated pool —
	// this is scenario e's whole point.
	kwokOnly := model.Toleration{Key: "kwok.x-k8s.io/node", Operator: model.TolerationOpExists}
	if kwokOnly.ToleratesTaint(dedicated) {
		t.Error("kwok toleration must not tolerate the dedicated taint")
	}
}

func TestLabelSelectorMatching(t *testing.T) {
	labels := map[string]string{"app": "payments", "tier": "backend"}

	var nilSelector *model.LabelSelector
	if !nilSelector.Matches(labels) {
		t.Error("nil selector must match everything")
	}
	if !nilSelector.IsEmpty() {
		t.Error("nil selector must report empty")
	}

	cases := []struct {
		name string
		sel  model.LabelSelector
		want bool
	}{
		{"matchLabels hit", model.LabelSelector{MatchLabels: map[string]string{"app": "payments"}}, true},
		{"matchLabels miss", model.LabelSelector{MatchLabels: map[string]string{"app": "catalog"}}, false},
		{"In hit", model.LabelSelector{MatchExpressions: []model.SelectorRequirement{{Key: "tier", Operator: model.SelectorOpIn, Values: []string{"backend", "web"}}}}, true},
		{"NotIn hit", model.LabelSelector{MatchExpressions: []model.SelectorRequirement{{Key: "tier", Operator: model.SelectorOpNotIn, Values: []string{"web"}}}}, true},
		{"Exists", model.LabelSelector{MatchExpressions: []model.SelectorRequirement{{Key: "app", Operator: model.SelectorOpExists}}}, true},
		{"DoesNotExist", model.LabelSelector{MatchExpressions: []model.SelectorRequirement{{Key: "absent", Operator: model.SelectorOpDoesNotExist}}}, true},
		{"all requirements must hold", model.LabelSelector{
			MatchLabels:      map[string]string{"app": "payments"},
			MatchExpressions: []model.SelectorRequirement{{Key: "tier", Operator: model.SelectorOpIn, Values: []string{"web"}}},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sel.Matches(labels); got != tc.want {
				t.Errorf("Matches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodMovability(t *testing.T) {
	cases := []struct {
		name    string
		pod     model.Pod
		movable bool
	}{
		{"deployment pod", model.Pod{Phase: model.PodRunning, Owner: &model.OwnerRef{Kind: "Deployment"}}, true},
		{"daemonset pod is pinned", model.Pod{Phase: model.PodRunning, Owner: &model.OwnerRef{Kind: "DaemonSet"}}, false},
		{"terminating pod", model.Pod{Phase: model.PodRunning, Terminating: true}, false},
		{"succeeded pod", model.Pod{Phase: model.PodSucceeded}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.pod.IsMovable(); got != tc.movable {
				t.Errorf("IsMovable = %v, want %v", got, tc.movable)
			}
		})
	}

	if !(model.Pod{}).IsSingleton() {
		t.Error("bare pod must count as a singleton")
	}
	if !(model.Pod{Owner: &model.OwnerRef{Kind: "StatefulSet"}}).IsSingleton() {
		t.Error("statefulset pod must count as a singleton")
	}
	if (model.Pod{Owner: &model.OwnerRef{Kind: "Deployment"}}).IsSingleton() {
		t.Error("deployment pod must not count as a singleton")
	}
}

func TestSnapshotAggregation(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{
			{Name: "a", Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 8192, Pods: 110}},
			{Name: "b", Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 8192, Pods: 110}},
		},
		Pods: []model.Pod{
			{Name: "p1", NodeName: "a", Phase: model.PodRunning, Requests: model.Resources{MilliCPU: 1000, MemoryBytes: 2048}},
			{Name: "p2", NodeName: "a", Phase: model.PodRunning, Requests: model.Resources{MilliCPU: 1000, MemoryBytes: 2048}},
			// Terminated pods must not count against node load.
			{Name: "p3", NodeName: "a", Phase: model.PodSucceeded, Requests: model.Resources{MilliCPU: 9000}},
		},
	}

	onA := snap.RequestedOnNode("a")
	if onA.MilliCPU != 2000 {
		t.Errorf("RequestedOnNode cpu = %d, want 2000 (succeeded pod must be excluded)", onA.MilliCPU)
	}
	if onA.Pods != 2 {
		t.Errorf("RequestedOnNode pods = %d, want 2", onA.Pods)
	}
	if !snap.RequestedOnNode("b").IsZero() {
		t.Error("node b must be empty")
	}

	allocatable, requested := snap.Totals()
	if allocatable.MilliCPU != 8000 {
		t.Errorf("total allocatable cpu = %d, want 8000", allocatable.MilliCPU)
	}
	if requested.MilliCPU != 2000 {
		t.Errorf("total requested cpu = %d, want 2000", requested.MilliCPU)
	}

	if _, ok := snap.NodeByName("missing"); ok {
		t.Error("NodeByName must report absence")
	}
	if got := len(snap.PodsOnNode("a")); got != 3 {
		t.Errorf("PodsOnNode = %d, want 3 (unfiltered)", got)
	}
}
