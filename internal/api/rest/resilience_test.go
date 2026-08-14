package rest_test

import (
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// A DaemonSet pod cannot move, and that is not a resilience finding.
//
// This audit reports what will not survive losing a node. A DaemonSet pod
// survives it in the only sense that matters: the DaemonSet is meant to have
// one pod per node, the node is gone, so one fewer pod is the correct outcome.
// A static pod owned by the node goes away with the thing that defined it.
//
// Before this, a healthy k3s cluster reported three pods "at risk" and all
// three were svclb DaemonSet pods. A screen that cries wolf on a correct
// cluster is one people stop reading — and this is the same pinned-versus-
// blocking distinction whose absence made every GKE node read as undrainable.
func TestResilienceIgnoresPodsPinnedToTheirNode(t *testing.T) {
	snap := &model.ClusterSnapshot{
		TakenAt: time.Now().UTC(),
		Nodes: []model.Node{{
			Name: "n1", Ready: true,
			Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110},
		}},
		Pods: []model.Pod{
			{
				Namespace: "kube-system", Name: "kube-proxy-xyz", NodeName: "n1",
				Phase: model.PodRunning,
				Owner: &model.OwnerRef{Kind: "DaemonSet", Name: "kube-proxy"},
			},
			{
				// GKE runs kube-proxy this way: owned by the node itself.
				Namespace: "kube-system", Name: "static-thing", NodeName: "n1",
				Phase: model.PodRunning,
				Owner: &model.OwnerRef{Kind: "Node", Name: "n1"},
			},
		},
	}

	rec := store.Record{
		Plan: &model.Plan{
			ID: "pinned01", GeneratedAt: time.Now().UTC(), SnapshotTakenAt: snap.TakenAt,
			Status: model.PlanValid, NodesBefore: 1, NodesAfter: 1,
		},
		Snapshot: snap,
		Analysis: &constraints.Analysis{},
		Strategy: "greedy-first-fit-decreasing",
	}

	srv := testServer(t, rec)
	code, body := get(t, srv, "/api/v1/resilience")
	if code != 200 {
		t.Fatalf("resilience = %d, want 200", code)
	}

	findings, _ := body["findings"].([]any)
	for _, f := range findings {
		m, _ := f.(map[string]any)
		t.Errorf("pinned pod reported as a resilience risk: %v (%v)", m["pod"], m["kind"])
	}
}
