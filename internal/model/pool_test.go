package model

import "testing"

// A pool is not a machine type. The UI conflated them and reported "1 pool"
// while counting distinct instance types, so two pools of one shape read as
// one and a single pool of mixed shapes read as two.
func TestPoolReadsWhicheverProviderLabelIsPresent(t *testing.T) {
	cases := []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"GKE", map[string]string{"cloud.google.com/gke-nodepool": "default-pool"}, "default-pool"},
		{"EKS", map[string]string{"eks.amazonaws.com/nodegroup": "workers"}, "workers"},
		{"Karpenter v1", map[string]string{"karpenter.sh/nodepool": "general"}, "general"},
		{"Karpenter v1beta1", map[string]string{"karpenter.sh/provisioner-name": "spot-pool"}, "spot-pool"},
		{"AKS", map[string]string{"kubernetes.azure.com/agentpool": "agentpool1"}, "agentpool1"},
		{"AKS older", map[string]string{"agentpool": "nodepool2"}, "nodepool2"},
		{"unlabelled", map[string]string{"node.kubernetes.io/instance-type": "e2-medium"}, ""},
		{"no labels", nil, ""},
	}
	for _, c := range cases {
		if got := (Node{Labels: c.labels}).Pool(); got != c.want {
			t.Errorf("%s: pool = %q, want %q", c.name, got, c.want)
		}
	}
}

// The machine shape must never be reported as the pool: that was the bug.
func TestPoolIsNotTheInstanceType(t *testing.T) {
	n := Node{Labels: map[string]string{
		"node.kubernetes.io/instance-type": "e2-medium",
		"cloud.google.com/gke-nodepool":    "batch",
	}}
	if got := n.Pool(); got != "batch" {
		t.Errorf("pool = %q, want batch", got)
	}
	if got := n.InstanceType(); got != "e2-medium" {
		t.Errorf("instance type = %q, want e2-medium", got)
	}
}

// Two pools of one shape are two pools; one pool of mixed shapes is one.
// Counting instance types got both of these wrong, in opposite directions.
func TestPoolCountIndependentOfShape(t *testing.T) {
	sameShape := []Node{
		{Labels: map[string]string{"cloud.google.com/gke-nodepool": "a", "node.kubernetes.io/instance-type": "e2-medium"}},
		{Labels: map[string]string{"cloud.google.com/gke-nodepool": "b", "node.kubernetes.io/instance-type": "e2-medium"}},
	}
	if got := distinctPools(sameShape); got != 2 {
		t.Errorf("two pools sharing a machine type counted as %d, want 2", got)
	}

	mixedShape := []Node{
		{Labels: map[string]string{"cloud.google.com/gke-nodepool": "a", "node.kubernetes.io/instance-type": "e2-medium"}},
		{Labels: map[string]string{"cloud.google.com/gke-nodepool": "a", "node.kubernetes.io/instance-type": "n2-standard-4"}},
	}
	if got := distinctPools(mixedShape); got != 1 {
		t.Errorf("one pool of mixed machine types counted as %d, want 1", got)
	}
}

func distinctPools(nodes []Node) int {
	seen := map[string]bool{}
	for _, n := range nodes {
		if p := n.Pool(); p != "" {
			seen[p] = true
		}
	}
	return len(seen)
}
