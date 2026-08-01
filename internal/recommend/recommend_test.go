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
