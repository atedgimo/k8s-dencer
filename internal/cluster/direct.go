package cluster

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// pageSize bounds a single List response.
//
// Without it the API server assembles every pod in the cluster into one reply —
// tens of megabytes at fifty thousand pods, allocated in full on both sides.
// Paging trades a few more round trips for a bounded footprint, which is the
// right trade for a component that reads repeatedly during a drain.
const pageSize = 500

// Snapshot builds an immutable view of current cluster state.
func (d *Direct) Snapshot(ctx context.Context) (*model.ClusterSnapshot, error) {
	resolver := d.ownerResolver(ctx)
	snap := &model.ClusterSnapshot{TakenAt: time.Now().UTC()}

	if err := d.eachNodePage(ctx, func(items []corev1.Node) {
		for i := range items {
			snap.Nodes = append(snap.Nodes, convertNode(&items[i]))
		}
	}); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	if err := d.eachPodPage(ctx, "", func(items []corev1.Pod) {
		for i := range items {
			p := &items[i]
			// Terminated pods hold no resources and would inflate every node.
			if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
				continue
			}
			snap.Pods = append(snap.Pods, convertPod(p, resolver))
		}
	}); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	if err := d.eachPDBPage(ctx, func(items []policyv1.PodDisruptionBudget) {
		for i := range items {
			snap.PDBs = append(snap.PDBs, convertPDB(&items[i]))
		}
	}); err != nil {
		return nil, fmt.Errorf("list poddisruptionbudgets: %w", err)
	}
	return snap, nil
}

// PodPresent reports whether a pod still exists and is not terminating.
//
// A single Get, because the alternative was listing every pod in the cluster
// every two seconds to answer a question about one of them. On a large cluster
// that dominated the entire drain.
func (d *Direct) PodPresent(ctx context.Context, namespace, name string) (bool, error) {
	pod, err := d.client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get pod %s/%s: %w", namespace, name, err)
	}
	return pod.DeletionTimestamp == nil, nil
}

func (d *Direct) eachNodePage(ctx context.Context, fn func([]corev1.Node)) error {
	opts := metav1.ListOptions{Limit: pageSize}
	for {
		page, err := d.client.CoreV1().Nodes().List(ctx, opts)
		if err != nil {
			return err
		}
		fn(page.Items)
		if page.Continue == "" {
			return nil
		}
		opts.Continue = page.Continue
	}
}

func (d *Direct) eachPodPage(ctx context.Context, namespace string, fn func([]corev1.Pod)) error {
	opts := metav1.ListOptions{Limit: pageSize}
	for {
		page, err := d.client.CoreV1().Pods(namespace).List(ctx, opts)
		if err != nil {
			return err
		}
		fn(page.Items)
		if page.Continue == "" {
			return nil
		}
		opts.Continue = page.Continue
	}
}

func (d *Direct) eachPDBPage(ctx context.Context, fn func([]policyv1.PodDisruptionBudget)) error {
	opts := metav1.ListOptions{Limit: pageSize}
	for {
		page, err := d.client.PolicyV1().PodDisruptionBudgets("").List(ctx, opts)
		if err != nil {
			return err
		}
		fn(page.Items)
		if page.Continue == "" {
			return nil
		}
		opts.Continue = page.Continue
	}
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
