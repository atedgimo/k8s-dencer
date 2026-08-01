package recommend_test

import (
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
