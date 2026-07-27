package executor

import (
	"context"
	"encoding/json"
	"fmt"

	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/atedgimo/k8s-dencer/internal/cluster"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/safety"
)

// K8sCluster performs the executor's mutations against a real API server.
type K8sCluster struct {
	client kubernetes.Interface
	reader *cluster.Direct
}

// NewK8sCluster builds the adapter over an uncached reader.
func NewK8sCluster(reader *cluster.Direct) *K8sCluster {
	return &K8sCluster{client: reader.Client(), reader: reader}
}

// Snapshot reads fresh cluster state.
func (k *K8sCluster) Snapshot(ctx context.Context) (*model.ClusterSnapshot, error) {
	return k.reader.Snapshot(ctx)
}

// Cordon marks a node unschedulable.
func (k *K8sCluster) Cordon(ctx context.Context, node string) error {
	return k.setUnschedulable(ctx, node, true)
}

// Uncordon restores schedulability.
func (k *K8sCluster) Uncordon(ctx context.Context, node string) error {
	return k.setUnschedulable(ctx, node, false)
}

// setUnschedulable patches the cordon flag.
//
// A merge patch of the single field, not a read-modify-write Update: an Update
// would carry the whole object and could clobber a concurrent change made by
// the cluster autoscaler or an operator with kubectl. It also means the
// executor's RBAC needs only `patch` on nodes, never `update`.
func (k *K8sCluster) setUnschedulable(ctx context.Context, node string, value bool) error {
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{"unschedulable": value},
	})
	if err != nil {
		return err
	}
	_, err = k.client.CoreV1().Nodes().Patch(ctx, node, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("patch node %s: %w", node, err)
	}
	return nil
}

// Evict requests voluntary eviction through the policy/v1 eviction API.
//
// This is the single most important line in the package. The eviction
// subresource is what makes the API server enforce PodDisruptionBudgets; a
// plain Delete on the pod would succeed regardless and quietly take a service
// below its minimum. The executor's RBAC grants create on pods/eviction and
// deliberately does not grant delete on pods.
//
// A 429 is the API server refusing on PDB grounds. That is not a failure —
// it is the same rail the Safety Guard checks, enforced by the authority that
// actually owns it — so it is translated back into a guard refusal and the run
// stops as Blocked rather than Failed. The pre-flight check exists to catch
// this early with a better message; this is the backstop that cannot be raced.
func (k *K8sCluster) Evict(ctx context.Context, namespace, name string) error {
	err := k.client.PolicyV1().Evictions(namespace).Evict(ctx, &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
	})
	switch {
	case err == nil:
		return nil
	case apierrors.IsNotFound(err):
		// Already gone — the drain's goal for this pod is met.
		return nil
	case apierrors.IsTooManyRequests(err):
		return &safety.Blocked{
			Rule: safety.RulePDBHeadroom,
			Reason: fmt.Sprintf(
				"the API server refused to evict %s: a PodDisruptionBudget has no headroom (%v)",
				PodKey(namespace, name), err),
		}
	default:
		return fmt.Errorf("evict %s: %w", PodKey(namespace, name), err)
	}
}
