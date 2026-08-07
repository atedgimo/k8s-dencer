package constraints_test

import (
	"strings"
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
)

// The gke-managed fixture reproduces a real managed cluster: four e2-medium
// nodes with 940m allocatable, the system DaemonSets every GKE node carries,
// and — the reason it exists — kube-proxy as a static pod mirrored into the
// API with the Node as its controller.
//
// Nothing in the kwok fixtures has a system pod at all, which is why the bugs
// these tests pin down survived a full CI suite and were found by paying for a
// cluster.

const kubeProxy = "kube-system/kube-proxy-gke-dencer-play-default-pool-4ddb9382-0xk4"

// A mirror pod cannot be rescheduled: delete it and the kubelet recreates it
// on the same node. Nothing else can place it anywhere.
func TestGKEManagedMirrorPodIsPinned(t *testing.T) {
	snap := loadFixture(t, "gke-managed")
	a := constraints.Analyze(snap)

	pc, ok := a.ForPod(kubeProxy)
	if !ok {
		t.Fatalf("fixture does not contain %s", kubeProxy)
	}
	if pc.Movable {
		t.Errorf("kube-proxy is a Node-owned mirror pod and must not be movable")
	}
	if len(pc.Blockers()) == 0 {
		t.Errorf("a pinned pod should say why it cannot move")
	}
}

// The cascade the mirror pod caused on the live cluster: treated as movable,
// it was handed to the placement search, found no room under the ceiling, and
// took its whole node down with it — on every node, because every node runs
// one.
func TestGKEManagedNoNodeIsBlockedByAMirrorPod(t *testing.T) {
	snap := loadFixture(t, "gke-managed")
	a := constraints.Analyze(snap)

	for _, n := range snap.Nodes {
		_, blockers := a.NodeDrainable(n.Name)
		for _, b := range blockers {
			if strings.Contains(b.Subject, "kube-proxy") {
				t.Errorf("node %s is blocked by a mirror pod (%s: %s)",
					n.Name, b.Kind, b.Explanation)
			}
		}
	}
}

// DaemonSet pods are pinned, but pinning is not blocking: `kubectl drain
// --ignore-daemonsets` is universal and the executor already drains through
// them. A managed cluster runs five or more per node, so counting them is the
// difference between a useful answer and "0 of N nodes will drain cleanly" on
// every real cluster in existence.
func TestGKEManagedDaemonSetsDoNotBlockDrains(t *testing.T) {
	snap := loadFixture(t, "gke-managed")
	a := constraints.Analyze(snap)

	for _, n := range snap.Nodes {
		_, blockers := a.NodeDrainable(n.Name)
		for _, b := range blockers {
			if b.Kind == constraints.KindControllerPinned && strings.Contains(b.Explanation, "DaemonSet") {
				t.Errorf("node %s counts a DaemonSet as a drain blocker: %s", n.Name, b.Subject)
			}
		}
	}
}

// The point of the two fixes together: a managed cluster becomes answerable.
// Before them this fleet reported 0 of 4 nodes drainable behind roughly thirty
// DaemonSet "blockers"; now one node drains and every remaining blocker is a
// real capacity constraint an operator can act on.
func TestGKEManagedGivesAUsefulAnswer(t *testing.T) {
	snap := loadFixture(t, "gke-managed")
	a := constraints.Analyze(snap)

	drainable := 0
	for _, n := range snap.Nodes {
		ok, blockers := a.NodeDrainable(n.Name)
		if ok {
			drainable++
		}
		for _, b := range blockers {
			if b.Kind != constraints.KindResources {
				t.Errorf("node %s blocked by %s (%s); on this fixture every real blocker is capacity",
					n.Name, b.Kind, b.Subject)
			}
		}
	}
	if drainable == 0 {
		t.Errorf("no node drains: the fleet has slack, so this is the managed-cluster bug returning")
	}
}

// The fixture is only useful while it keeps reproducing the cluster it was
// taken from. These are the measurements from 2026-08-07.
func TestGKEManagedShape(t *testing.T) {
	snap := loadFixture(t, "gke-managed")

	if got := len(snap.Nodes); got != 4 {
		t.Fatalf("nodes = %d, want 4", got)
	}
	for _, n := range snap.Nodes {
		if n.Allocatable.MilliCPU != 940 {
			t.Errorf("%s allocatable = %dm, want 940m (e2-medium after GKE's reservation)",
				n.Name, n.Allocatable.MilliCPU)
		}
		if n.InstanceType() != "e2-medium" {
			t.Errorf("%s instance type = %q, want e2-medium", n.Name, n.InstanceType())
		}
	}

	// System overhead per node, the number that cannot be reproduced on kwok.
	system := map[string]int64{}
	for _, p := range snap.Pods {
		switch p.Namespace {
		case "dencer-demo", "k8s-dencer":
		default:
			system[p.NodeName] += p.Requests.MilliCPU
		}
	}
	var min, max int64 = 1 << 30, 0
	for _, v := range system {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	if min != 276 {
		t.Errorf("minimum system overhead = %dm, want 276m", min)
	}
	if max < 600 {
		t.Errorf("maximum system overhead = %dm, want the uneven fleet (>600m)", max)
	}
}
