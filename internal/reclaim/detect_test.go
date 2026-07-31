package reclaim_test

import (
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/reclaim"
)

func TestDetectReclaimerSeesInClusterAutoscalers(t *testing.T) {
	cases := []struct {
		name string
		pod  model.Pod
		want string
	}{
		{"karpenter by label", model.Pod{Name: "x", Labels: map[string]string{"app.kubernetes.io/name": "karpenter"}}, "Karpenter"},
		{"karpenter by name", model.Pod{Name: "karpenter-7d9f-abc"}, "Karpenter"},
		{"CA by label", model.Pod{Name: "y", Labels: map[string]string{"app": "cluster-autoscaler"}}, "cluster-autoscaler"},
		{"CA by name", model.Pod{Name: "cluster-autoscaler-6c-xyz"}, "cluster-autoscaler"},
	}
	for _, c := range cases {
		got, ok := reclaim.DetectReclaimer(&model.ClusterSnapshot{Pods: []model.Pod{c.pod}})
		if !ok || got != c.want {
			t.Errorf("%s: got (%q,%v), want (%q,true)", c.name, got, ok, c.want)
		}
	}
}

// Absence must read as "none visible", never "none exists": managed control
// planes run their autoscalers where no pod scan can see them.
func TestDetectReclaimerAbsenceIsNotEvidence(t *testing.T) {
	snap := &model.ClusterSnapshot{Pods: []model.Pod{{Name: "web-1"}}}
	if name, ok := reclaim.DetectReclaimer(snap); ok {
		t.Errorf("detected %q in a cluster with no autoscaler pods", name)
	}
}
