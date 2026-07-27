package cluster

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Direct reads cluster state straight from the API server, bypassing any cache.
//
// The planner uses the informer-backed Collector, which is right for it: it
// re-plans on a timer and a few seconds of lag costs nothing. The executor
// cannot use it. Its whole safety model rests on deciding against state that is
// true *now* — a cached PodDisruptionBudget would let it authorise an eviction
// the budget stopped permitting seconds ago.
//
// The cost is a full List per read, which is why the executor is idle except
// during a run and polls on the order of seconds rather than continuously.
// Conversion is shared with the Collector deliberately: the effective-request
// rules (init containers, sidecars, overhead) are subtle enough that a second
// implementation would drift.
type Direct struct {
	client kubernetes.Interface
}

// NewDirect builds an uncached reader.
func NewDirect(cfg *rest.Config) (*Direct, error) {
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Direct{client: client}, nil
}

// NewDirectWithClient builds a reader over an existing client, for tests.
func NewDirectWithClient(client kubernetes.Interface) *Direct {
	return &Direct{client: client}
}

// Client exposes the underlying clientset so a caller can perform the
// mutations the executor needs without opening a second connection.
func (d *Direct) Client() kubernetes.Interface { return d.client }

// Snapshot builds an immutable view of current cluster state.
func (d *Direct) Snapshot(ctx context.Context) (*model.ClusterSnapshot, error) {
	nodes, err := d.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	pods, err := d.client.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	pdbs, err := d.client.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list poddisruptionbudgets: %w", err)
	}

	resolver := d.ownerResolver(ctx)
	snap := &model.ClusterSnapshot{
		TakenAt: time.Now().UTC(),
		Nodes:   make([]model.Node, 0, len(nodes.Items)),
		Pods:    make([]model.Pod, 0, len(pods.Items)),
		PDBs:    make([]model.PodDisruptionBudget, 0, len(pdbs.Items)),
	}

	for i := range nodes.Items {
		snap.Nodes = append(snap.Nodes, convertNode(&nodes.Items[i]))
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		// Terminated pods hold no resources and would inflate every node.
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		snap.Pods = append(snap.Pods, convertPod(p, resolver))
	}
	for i := range pdbs.Items {
		snap.PDBs = append(snap.PDBs, convertPDB(&pdbs.Items[i]))
	}
	return snap, nil
}

// ownerResolver mirrors the Collector's, walking ReplicaSet -> Deployment so a
// pod's real controller is named.
func (d *Direct) ownerResolver(ctx context.Context) ownerResolver {
	// Cached for the life of one snapshot: a node's worth of pods usually
	// shares a handful of ReplicaSets, and re-fetching each would turn one
	// read into hundreds.
	seen := map[string]*model.OwnerRef{}

	return func(p *corev1.Pod) *model.OwnerRef {
		for _, ref := range p.OwnerReferences {
			if ref.Controller == nil || !*ref.Controller {
				continue
			}
			if ref.Kind != "ReplicaSet" {
				return &model.OwnerRef{Kind: ref.Kind, Name: ref.Name}
			}
			key := p.Namespace + "/" + ref.Name
			if cached, ok := seen[key]; ok {
				return cached
			}

			owner := &model.OwnerRef{Kind: ref.Kind, Name: ref.Name}
			var rs *appsv1.ReplicaSet
			rs, err := d.client.AppsV1().ReplicaSets(p.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
			if err == nil {
				for _, rsRef := range rs.OwnerReferences {
					if rsRef.Controller != nil && *rsRef.Controller {
						owner = &model.OwnerRef{Kind: rsRef.Kind, Name: rsRef.Name}
						break
					}
				}
			}
			// On error the ReplicaSet itself stands in, rather than dropping
			// ownership: the classifier still needs to know the pod is
			// replicated even when the Deployment name is unavailable.
			seen[key] = owner
			return owner
		}
		return nil
	}
}
