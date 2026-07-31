package rest_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

func whatifRecord(snap *model.ClusterSnapshot) store.Record {
	return store.Record{
		Plan: &model.Plan{
			ID: "wf-test-plan", Status: model.PlanValid,
			GeneratedAt: time.Now().UTC(), SnapshotTakenAt: time.Now().UTC(),
			NodesBefore: len(snap.Nodes),
		},
		Snapshot: snap,
		Analysis: &constraints.Analysis{},
		Strategy: "test",
	}
}

// The simulation's two verdicts, both grounded: a cluster with room answers
// "fits", and one without names the pods and quotes why.
func TestWhatifVerdicts(t *testing.T) {
	node := func(name string, cpu int64) model.Node {
		return model.Node{
			Name: name, Ready: true,
			Allocatable: model.Resources{MilliCPU: cpu, MemoryBytes: 32 << 30, Pods: 110},
		}
	}
	pod := func(name, on string, cpu int64) model.Pod {
		return model.Pod{
			Namespace: "shop", Name: name, NodeName: on, Phase: model.PodRunning, Ready: true,
			Requests: model.Resources{MilliCPU: cpu, MemoryBytes: 1 << 28},
			Owner:    &model.OwnerRef{Kind: "ReplicaSet", Name: "web"},
		}
	}

	roomy := &model.ClusterSnapshot{
		Nodes: []model.Node{node("a", 8000), node("b", 8000)},
		Pods:  []model.Pod{pod("web-1", "a", 500), pod("web-2", "b", 500)},
	}
	tight := &model.ClusterSnapshot{
		Nodes: []model.Node{node("a", 8000), node("b", 1000)},
		Pods:  []model.Pod{pod("big-1", "a", 4000), pod("web-2", "b", 500)},
	}

	for name, tc := range map[string]struct {
		snap *model.ClusterSnapshot
		fits bool
	}{
		"roomy cluster fits":     {roomy, true},
		"tight cluster does not": {tight, false},
	} {
		srv := testServer(t, whatifRecord(tc.snap))
		body, _ := json.Marshal(map[string]any{"removeNodes": []string{"a"}})
		resp, err := http.Post(srv.URL+"/api/v1/whatif", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Fits     bool `json:"fits"`
			Homeless []struct {
				Pod string   `json:"pod"`
				Why []string `json:"why"`
			} `json:"homeless"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: status %d", name, resp.StatusCode)
		}
		if out.Fits != tc.fits {
			t.Errorf("%s: fits = %v, want %v", name, out.Fits, tc.fits)
		}
		if !tc.fits {
			if len(out.Homeless) != 1 || out.Homeless[0].Pod != "shop/big-1" {
				t.Errorf("%s: homeless = %+v, want shop/big-1 named", name, out.Homeless)
			}
			if len(out.Homeless) == 1 && len(out.Homeless[0].Why) == 0 {
				t.Errorf("%s: a homeless pod with no why is a verdict without a reason", name)
			}
		}
	}

	// A typo answers 400, not a confident report about the wrong question.
	srv := testServer(t, whatifRecord(roomy))
	body, _ := json.Marshal(map[string]any{"removeNodes": []string{"no-such"}})
	resp, err := http.Post(srv.URL+"/api/v1/whatif", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("unknown node: status %d, want 400", resp.StatusCode)
	}
}
