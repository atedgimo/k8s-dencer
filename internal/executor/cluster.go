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

// healthy reports whether a pod counts towards a workload having recovered.
//
// The domain model carries no readiness condition — only phase — so recovery
// is verified at Running rather than Ready. That is weaker than it sounds in
// one direction and stronger in another: it will not catch a pod that starts
// and then fails its probes, but it also sidesteps the KWOK trap where
// stage-fast's pod-ready Stage matches only `phase In [Pending]`, so a fake pod
// reaching Running never becomes Ready and a Ready-based wait hangs forever.
func healthy(pod model.Pod) bool {
	return pod.Phase == model.PodRunning && !pod.Terminating
}
