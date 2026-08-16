package recommend_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/recommend"
)

func pod(ns, name, owner, kind string, cpu int64, labels map[string]string) model.Pod {
	return model.Pod{
		Namespace: ns, Name: name, NodeName: "a", Phase: model.PodRunning, Ready: true,
		Labels:   labels,
		Requests: model.Resources{MilliCPU: cpu, MemoryBytes: cpu << 20},
		Owner:    &model.OwnerRef{Kind: kind, Name: owner},
	}
}

func TestRecommendationsNameWhatIsMissing(t *testing.T) {
	web := map[string]string{"app": "web"}
	lonely := map[string]string{"app": "lonely"}
	naked := map[string]string{"app": "naked"}
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{{Name: "a", Ready: true}},
		Pods: []model.Pod{
			// multi-replica, no PDB -> MissingPDB
			pod("shop", "web-1", "web", "ReplicaSet", 500, web),
			pod("shop", "web-2", "web", "ReplicaSet", 500, web),
			// single replica -> SingleReplica
			pod("shop", "lonely-1", "lonely", "Deployment", 500, lonely),
			// zero requests -> MissingRequests
			pod("shop", "naked-1", "naked", "ReplicaSet", 0, naked),
			pod("shop", "naked-2", "naked", "ReplicaSet", 0, naked),
			// DaemonSet: no advice, ever
			pod("kube-system", "ds-1", "ds", "DaemonSet", 100, map[string]string{"app": "ds"}),
		},
		PDBs: []model.PodDisruptionBudget{
			// covers naked -> no MissingPDB for it; zero headroom -> its own rec
			{Namespace: "shop", Name: "naked", Selector: &model.LabelSelector{MatchLabels: naked},
				CurrentHealthy: 2, DesiredHealthy: 2, DisruptionsAllowed: 0},
		},
	}

	recs := recommend.Build(snap)
	byKind := map[string][]recommend.Recommendation{}
	for _, r := range recs {
		byKind[r.Kind] = append(byKind[r.Kind], r)
	}

	if len(byKind["MissingPDB"]) != 1 || byKind["MissingPDB"][0].Workload != "shop/ReplicaSet/web" {
		t.Errorf("MissingPDB = %+v, want exactly shop/ReplicaSet/web", byKind["MissingPDB"])
	}
	if got := byKind["MissingPDB"]; len(got) == 1 && !strings.Contains(got[0].Fix, "maxUnavailable: 1") {
		t.Error("the MissingPDB fix is not paste-ready YAML")
	}
	if len(byKind["SingleReplica"]) != 1 || byKind["SingleReplica"][0].Workload != "shop/Deployment/lonely" {
		t.Errorf("SingleReplica = %+v", byKind["SingleReplica"])
	}
	if len(byKind["MissingRequests"]) != 1 {
		t.Errorf("MissingRequests = %+v", byKind["MissingRequests"])
	}
	if len(byKind["ZeroHeadroomPDB"]) != 1 {
		t.Errorf("ZeroHeadroomPDB = %+v", byKind["ZeroHeadroomPDB"])
	}
	for _, r := range recs {
		if strings.Contains(r.Workload, "DaemonSet") {
			t.Errorf("advice for a DaemonSet is noise: %+v", r)
		}
		if r.Why == "" {
			t.Errorf("%s has no why; a fix without a reason is a decree", r.Kind)
		}
	}
	// severity ordering: high before medium before info
	last := 0
	rank := map[recommend.Severity]int{"high": 0, "medium": 1, "info": 2}
	for _, r := range recs {
		if rank[r.Severity] < last {
			t.Fatalf("recommendations not sorted by severity: %+v", recs)
		}
		last = rank[r.Severity]
	}
}

// The queue leads with the plan's own blocking rules — the step reasons the
// impact assessor wrote, grouped by rule and responsible workload, carrying
// exactly the steps they appear on. Advice (Build's findings) ranks below
// every blocker, and a blocker's severity follows its worst attribution.
func TestQueueLeadsWithThePlansBlockingRules(t *testing.T) {
	web := map[string]string{"app": "web"}
	lonely := map[string]string{"app": "lonely"}

	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{
			{Name: "a", Ready: true, Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
			{Name: "b", Ready: true, Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
		},
		Pods: []model.Pod{
			pod("shop", "web-1", "web", "Deployment", 500, web),
			pod("shop", "web-2", "web", "Deployment", 500, web),
			pod("shop", "lonely-1", "lonely", "Deployment", 500, lonely),
		},
	}

	plan := &model.Plan{
		NodesBefore: 2,
		Steps: []model.PlanStep{
			{SequenceNumber: 1, TargetNode: "a", Impact: model.ImpactGreen},
			// The same rule holds two steps back, once at Red — the grouped
			// finding must carry both steps and the worst severity.
			{SequenceNumber: 2, TargetNode: "b", Impact: model.ImpactYellow,
				Reasons: []model.ImpactReason{{
					Kind: "HardTopologySpread", Subject: "shop/web-1",
					Detail: "any move must keep the per-zone counts within 1",
				}}},
			{SequenceNumber: 3, TargetNode: "a", Impact: model.ImpactRed,
				Reasons: []model.ImpactReason{{
					Kind: "HardTopologySpread", Subject: "shop/web-2",
					Detail: "any move must keep the per-zone counts within 1",
				}}},
		},
	}

	queue := recommend.Queue(plan, snap)
	if len(queue) == 0 {
		t.Fatal("empty queue")
	}

	head := queue[0]
	if head.Kind != "HardTopologySpread" || head.Workload != "shop/Deployment/web" {
		t.Fatalf("queue head = %s/%s, want the blocking rule grouped by workload", head.Kind, head.Workload)
	}
	if fmt.Sprint(head.UnblocksSteps) != "[2 3]" {
		t.Errorf("unblocksSteps = %v, want [2 3]", head.UnblocksSteps)
	}
	if head.Severity != recommend.SeverityHigh {
		t.Errorf("severity = %s, want high — one attribution was Red", head.Severity)
	}
	if head.Why == "" {
		t.Error("a blocking rule must carry the assessor's own explanation")
	}

	// Advice never outranks a blocker.
	for i, r := range queue {
		if len(r.UnblocksSteps) == 0 {
			for _, later := range queue[i:] {
				if len(later.UnblocksSteps) > 0 {
					t.Fatalf("advice %s ranks above blocker %s", r.Kind, later.Kind)
				}
			}
			break
		}
	}
}

// A finding must say WHERE it costs capacity, not only how much.
//
// "This PDB blocks three steps" is useful. "This PDB blocks three steps on
// your spot pool" is actionable: spot is the capacity an operator is most
// willing to move work off, and least willing to hold open for a budget
// written without thinking about it. Both facts were already on the node —
// the well-known pool and capacity-type labels — and nothing joined them to
// a finding until now.
func TestAFindingNamesThePoolsItHoldsOpen(t *testing.T) {
	web := map[string]string{"app": "web"}
	spot := map[string]string{
		"cloud.google.com/gke-nodepool": "burst",
		"cloud.google.com/gke-spot":     "true",
	}
	// The instance-type label is what makes on-demand a fact rather than an
	// assumption: CapacityType reports nothing at all for a node no cloud has
	// labelled, and "not marked spot" is not evidence of on-demand.
	steady := map[string]string{
		"cloud.google.com/gke-nodepool":    "steady",
		"node.kubernetes.io/instance-type": "e2-medium",
	}

	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{
			{Name: "spot-1", Ready: true, Labels: spot,
				Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
			{Name: "spot-2", Ready: true, Labels: spot,
				Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
			{Name: "steady-1", Ready: true, Labels: steady,
				Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
		},
		Pods: []model.Pod{
			pod("shop", "web-1", "web", "Deployment", 500, web),
			pod("shop", "web-2", "web", "Deployment", 500, web),
			pod("shop", "web-3", "web", "Deployment", 500, web),
		},
	}

	reason := func(subject string) []model.ImpactReason {
		return []model.ImpactReason{{
			Kind: "HardTopologySpread", Subject: subject,
			Detail: "any move must keep the per-zone counts within 1",
		}}
	}
	plan := &model.Plan{
		NodesBefore: 3,
		Steps: []model.PlanStep{
			{SequenceNumber: 1, TargetNode: "spot-1", Impact: model.ImpactYellow, Reasons: reason("shop/web-1")},
			{SequenceNumber: 2, TargetNode: "spot-2", Impact: model.ImpactYellow, Reasons: reason("shop/web-2")},
			{SequenceNumber: 3, TargetNode: "steady-1", Impact: model.ImpactYellow, Reasons: reason("shop/web-3")},
		},
	}

	head := recommend.Queue(plan, snap)[0]
	if len(head.Pools) != 2 {
		t.Fatalf("pools = %+v, want two", head.Pools)
	}

	// Most nodes first: the pool it costs most reads first.
	if head.Pools[0].Name != "burst" || head.Pools[0].Nodes != 2 {
		t.Errorf("pools[0] = %+v, want burst with 2 nodes first", head.Pools[0])
	}
	if head.Pools[0].CapacityType != "spot" {
		t.Errorf("burst capacityType = %q, want spot — the labels say so and "+
			"spot is the whole reason this distinction is worth drawing",
			head.Pools[0].CapacityType)
	}
	if head.Pools[1].Name != "steady" || head.Pools[1].Nodes != 1 {
		t.Errorf("pools[1] = %+v, want steady with 1 node", head.Pools[1])
	}
	if head.Pools[1].CapacityType != "on-demand" {
		t.Errorf("steady capacityType = %q, want on-demand", head.Pools[1].CapacityType)
	}
}

// An unlabelled node belongs to no pool, and must not be filed under one.
//
// The same doctrine as pricing: a node with no known price is unpriced, never
// free. A node with no pool label is not in a pool called "unknown" — naming
// one would put a finding against a group that does not exist, and send
// somebody looking for it.
func TestAnUnlabelledNodeIsNotAPool(t *testing.T) {
	web := map[string]string{"app": "web"}
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{
			{Name: "plain", Ready: true,
				Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
		},
		Pods: []model.Pod{pod("shop", "web-1", "web", "Deployment", 500, web)},
	}
	plan := &model.Plan{
		NodesBefore: 1,
		Steps: []model.PlanStep{{
			SequenceNumber: 1, TargetNode: "plain", Impact: model.ImpactYellow,
			Reasons: []model.ImpactReason{{
				Kind: "HardTopologySpread", Subject: "shop/web-1", Detail: "spread",
			}},
		}},
	}

	head := recommend.Queue(plan, snap)[0]
	if len(head.Pools) != 0 {
		t.Errorf("pools = %+v on an unlabelled cluster, want none — an invented "+
			"pool name sends somebody looking for a group that does not exist",
			head.Pools)
	}
	// The finding itself must survive; only the pool attribution is absent.
	if len(head.UnblocksSteps) != 1 {
		t.Errorf("the finding lost its steps: %+v", head.UnblocksSteps)
	}
}

// A pool whose nodes are bought differently is reported as the split.
//
// The first design collapsed such a pool into one row with no capacity type,
// on the grounds that picking one would be a coin-flip. True, but it threw
// away a fact the cluster had already stated: one of these nodes is spot.
// Grouping by (pool, capacity) instead says so — "mixed (spot) 1, mixed
// (on-demand) 1" — without guessing anything.
func TestAMixedPoolIsReportedAsItsSplit(t *testing.T) {
	web := map[string]string{"app": "web"}
	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{
			{Name: "n1", Ready: true, Labels: map[string]string{
				"cloud.google.com/gke-nodepool": "mixed",
				"cloud.google.com/gke-spot":     "true",
			}, Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
			{Name: "n2", Ready: true, Labels: map[string]string{
				"cloud.google.com/gke-nodepool":    "mixed",
				"node.kubernetes.io/instance-type": "e2-medium",
			}, Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}},
		},
		Pods: []model.Pod{
			pod("shop", "web-1", "web", "Deployment", 500, web),
			pod("shop", "web-2", "web", "Deployment", 500, web),
		},
	}
	reason := func(s string) []model.ImpactReason {
		return []model.ImpactReason{{Kind: "HardTopologySpread", Subject: s, Detail: "spread"}}
	}
	plan := &model.Plan{
		NodesBefore: 2,
		Steps: []model.PlanStep{
			{SequenceNumber: 1, TargetNode: "n1", Impact: model.ImpactYellow, Reasons: reason("shop/web-1")},
			{SequenceNumber: 2, TargetNode: "n2", Impact: model.ImpactYellow, Reasons: reason("shop/web-2")},
		},
	}

	head := recommend.Queue(plan, snap)[0]
	if len(head.Pools) != 2 {
		t.Fatalf("pools = %+v, want one row per capacity type", head.Pools)
	}
	got := map[string]int{}
	for _, p := range head.Pools {
		if p.Name != "mixed" {
			t.Errorf("pool name = %q, want mixed", p.Name)
		}
		got[p.CapacityType] = p.Nodes
	}
	if got["spot"] != 1 || got["on-demand"] != 1 {
		t.Errorf("split = %v, want one spot and one on-demand", got)
	}
}

// A cluster with no node-pool labels still says spot versus on-demand.
//
// Grouping only by pool name would report nothing at all on the KWOK fabric
// and on any self-managed cluster — clusters that plainly know half their
// nodes are spot, because the capacity-type label says so. The pool name is a
// nicety; the capacity type is the thing an operator acts on.
func TestCapacityTypeIsReportedWithoutAPoolLabel(t *testing.T) {
	web := map[string]string{"app": "web"}
	spot := map[string]string{"karpenter.sh/capacity-type": "spot"}
	onDemand := map[string]string{"karpenter.sh/capacity-type": "on-demand"}
	alloc := model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}

	snap := &model.ClusterSnapshot{
		Nodes: []model.Node{
			{Name: "s1", Ready: true, Labels: spot, Allocatable: alloc},
			{Name: "s2", Ready: true, Labels: spot, Allocatable: alloc},
			{Name: "d1", Ready: true, Labels: onDemand, Allocatable: alloc},
		},
		Pods: []model.Pod{
			pod("shop", "web-1", "web", "Deployment", 500, web),
			pod("shop", "web-2", "web", "Deployment", 500, web),
			pod("shop", "web-3", "web", "Deployment", 500, web),
		},
	}
	reason := func(s string) []model.ImpactReason {
		return []model.ImpactReason{{Kind: "HardTopologySpread", Subject: s, Detail: "spread"}}
	}
	plan := &model.Plan{
		NodesBefore: 3,
		Steps: []model.PlanStep{
			{SequenceNumber: 1, TargetNode: "s1", Impact: model.ImpactYellow, Reasons: reason("shop/web-1")},
			{SequenceNumber: 2, TargetNode: "s2", Impact: model.ImpactYellow, Reasons: reason("shop/web-2")},
			{SequenceNumber: 3, TargetNode: "d1", Impact: model.ImpactYellow, Reasons: reason("shop/web-3")},
		},
	}

	head := recommend.Queue(plan, snap)[0]
	if len(head.Pools) != 2 {
		t.Fatalf("pools = %+v, want spot and on-demand", head.Pools)
	}
	if head.Pools[0].CapacityType != "spot" || head.Pools[0].Nodes != 2 {
		t.Errorf("pools[0] = %+v, want 2 spot nodes first", head.Pools[0])
	}
	if head.Pools[0].Name != "" {
		t.Errorf("pool name = %q on a cluster with no pool labels, want empty",
			head.Pools[0].Name)
	}
}
