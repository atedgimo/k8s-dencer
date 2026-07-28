package executor

import (
	"context"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Cluster is the complete set of mutations the executor is capable of.
//
// Deliberately tiny, and deliberately an interface. It is the enumeration of
// everything this product can do to a cluster: read state, cordon, uncordon,
// evict. There is no delete, no patch, no scale — and the executor's RBAC in
// the chart grants exactly these and nothing more, so the interface and the
// Role can be read against each other.
//
// It is also what makes the drain loop testable without a cluster: the fake in
// executor_test.go can fail an eviction, stall a reschedule, or flip a PDB
// mid-drain, none of which is convenient to arrange for real.
type Cluster interface {
	// Snapshot reads current cluster state. Called repeatedly during a drain —
	// the whole safety model depends on decisions being made against fresh
	// state rather than the plan's original snapshot.
	Snapshot(ctx context.Context) (*model.ClusterSnapshot, error)

	// Cordon marks a node unschedulable.
	Cordon(ctx context.Context, node string) error

	// Uncordon restores schedulability. This is the abort path: an aborted
	// drain must leave the node usable, since evicted pods cannot be un-evicted.
	Uncordon(ctx context.Context, node string) error

	// PodPresent reports whether one pod still exists and is not terminating.
	//
	// Narrow on purpose. The drain loop asks this every couple of seconds per
	// evicted pod, and answering it from a full cluster snapshot meant listing
	// every pod in the cluster to learn about one — which on a large cluster
	// cost more than the drain itself.
	PodPresent(ctx context.Context, namespace, name string) (bool, error)

	// Evict requests voluntary eviction through the policy/v1 eviction API,
	// which is what makes the API server enforce PodDisruptionBudgets. A plain
	// pod delete would bypass them entirely — the single most important
	// distinction in this package.
	Evict(ctx context.Context, namespace, name string) error
}

// PodKey is the namespace/name identifier used in logs and audit events.
func PodKey(namespace, name string) string { return namespace + "/" + name }

// ownerKey identifies the workload a pod belongs to, so recovery can be
// verified against the controller rather than the pod.
//
// Eviction replaces a pod with a differently-named one, so "did it come back?"
// can only be asked of the owner. A pod with no owner has no answer, which is
// exactly why the impact classifier rates those Red.
func ownerKey(pod model.Pod) string {
	if pod.Owner == nil {
		return ""
	}
	return pod.Namespace + "/" + pod.Owner.Kind + "/" + pod.Owner.Name
}

// Readiness is how the executor decides a replacement pod counts.
type Readiness string

const (
	// ReadinessReady waits for the pod's Ready condition. The correct answer
	// on a real cluster: a pod can be Running and failing its probes, and
	// treating that as recovered drains the next node while the service is
	// still down.
	ReadinessReady Readiness = "Ready"

	// ReadinessRunning waits only for the phase.
	//
	// Exists for KWOK. stage-fast's pod-ready Stage selector matches only
	// `phase In [Pending]`, so a fake pod that reaches Running never becomes
	// Ready and a Ready-based wait hangs forever. Correct on the fabric,
	// wrong on a real cluster — which is why the default is Ready and the
	// KWOK overlay is the only thing that changes it.
	ReadinessRunning Readiness = "Running"
)

// healthy reports whether a pod counts towards a workload having recovered.
func healthy(pod model.Pod, criterion Readiness) bool {
	if pod.Terminating || pod.Phase != model.PodRunning {
		return false
	}
	if criterion == ReadinessRunning {
		return true
	}
	return pod.Ready
}
