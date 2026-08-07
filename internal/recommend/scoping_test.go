package recommend_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/recommend"
)

func loadGKE(t *testing.T) *model.ClusterSnapshot {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "test", "fixtures", "gke-managed.yaml"))
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	var snap model.ClusterSnapshot
	if err := yaml.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return &snap
}

// A live GKE cluster produced 15 recommendations, 11 of them HIGH, of which
// exactly one named a workload the operator owned. The rest were Google's —
// kube-dns-autoscaler, l7-default-backend, metrics-server — where the platform
// controller reverts any change, or k8s-dencer's own Deployments.
func TestBuildIgnoresWorkloadsTheOperatorCannotChange(t *testing.T) {
	snap := loadGKE(t)

	for _, r := range recommend.Build(snap) {
		ns, _, _ := strings.Cut(r.Workload, "/")
		switch {
		case ns == "kube-system", ns == "gmp-system", strings.HasPrefix(ns, "gke-managed-"):
			t.Errorf("advises a platform workload nobody can change: %s (%s)", r.Workload, r.Kind)
		case ns == "k8s-dencer":
			t.Errorf("advises changing the product's own deployment: %s (%s)", r.Workload, r.Kind)
		}
	}
}

// The chart pins ui-backend to one replica because SQLite has one writer, and
// the chart's contract test rejects a second. Recommending one would be
// advising a change the product refuses to install.
func TestBuildNeverAdvisesWhatTheChartForbids(t *testing.T) {
	snap := loadGKE(t)

	for _, r := range recommend.Build(snap) {
		if r.Kind == "SingleReplica" && strings.Contains(r.Workload, "ui-backend") {
			t.Fatalf("recommends a second ui-backend replica, which the chart rejects at install time")
		}
	}
}

// Guessing `app:` and finding nothing produced `app: ` with no value — a PDB
// matching no pods, which is worse than none because it looks like coverage.
func TestMissingPDBFixNeverHasAnEmptySelector(t *testing.T) {
	snap := loadGKE(t)
	// A workload the operator owns, with replicas, no PDB, and no `app` label.
	snap.Pods = append(snap.Pods,
		model.Pod{
			Namespace: "shop", Name: "unlabelled-a", NodeName: snap.Nodes[0].Name,
			Phase: model.PodRunning, Ready: true,
			Owner:  &model.OwnerRef{Kind: "Deployment", Name: "unlabelled"},
			Labels: map[string]string{"pod-template-hash": "abc123"},
		},
		model.Pod{
			Namespace: "shop", Name: "unlabelled-b", NodeName: snap.Nodes[0].Name,
			Phase: model.PodRunning, Ready: true,
			Owner:  &model.OwnerRef{Kind: "Deployment", Name: "unlabelled"},
			Labels: map[string]string{"pod-template-hash": "abc123"},
		},
	)

	var seen bool
	for _, r := range recommend.Build(snap) {
		if r.Kind != "MissingPDB" {
			continue
		}
		for _, line := range strings.Split(r.Fix, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasSuffix(trimmed, ":") && !strings.HasPrefix(trimmed, "#") {
				continue // a mapping key, e.g. "spec:"
			}
			if k, v, ok := strings.Cut(trimmed, ": "); ok && strings.TrimSpace(v) == "" {
				t.Errorf("%s: generated PDB has an empty value for %q", r.Workload, k)
			}
		}
		if strings.Contains(r.Workload, "unlabelled") {
			seen = true
			if r.Fix != "" {
				t.Errorf("emitted a PDB manifest for a workload with no identifying label:\n%s", r.Fix)
			}
			if !strings.Contains(r.Why, "by hand") {
				t.Errorf("should say the selector needs writing by hand, got: %s", r.Why)
			}
		}
	}
	if !seen {
		t.Error("expected a MissingPDB finding for the unlabelled workload")
	}
}

// The operator's own workloads must still be advised on — the point is to
// remove noise, not to go quiet.
func TestBuildStillAdvisesTheOperatorsOwnWorkloads(t *testing.T) {
	snap := loadGKE(t)

	var own int
	for _, r := range recommend.Build(snap) {
		if strings.HasPrefix(r.Workload, "dencer-demo/") {
			own++
		}
	}
	if own == 0 {
		t.Error("no recommendations for the operator's own workloads; scoping has gone too far")
	}
}
