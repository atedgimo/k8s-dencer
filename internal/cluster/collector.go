// Package cluster observes live cluster state and produces immutable
// ClusterSnapshots for the planner.
//
// State comes from controller-runtime informers rather than polling: a
// cluster-wide pod list every few seconds is the classic way to melt an API
// server, and the watch-backed cache gives near-real-time state at a fraction
// of the cost. The trade-off is memory, which is why the watched namespaces
// are configurable.
package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/atedgimo/k8s-dencer/internal/cluster/metrics"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Options configures the collector.
type Options struct {
	// ResyncPeriod is how often informers do a full relist. Zero disables
	// periodic resync and relies on watches alone.
	ResyncPeriod time.Duration

	// Namespaces limits the pod and PDB caches. Empty watches all namespaces.
	// On a large cluster the pod informer dominates the planner's memory
	// footprint, so this is the first knob to reach for.
	Namespaces []string

	// Metrics supplies actual usage. Defaults to metrics.Noop.
	Metrics metrics.Source

	Logger *slog.Logger
}

// Collector maintains an informer-backed cache and builds snapshots from it.
type Collector struct {
	cache   cache.Cache
	metrics metrics.Source
	log     *slog.Logger
}

// New builds a collector against the given REST config. It does not start
// watching; call Start.
func New(cfg *rest.Config, opts Options) (*Collector, error) {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	cacheOpts := cache.Options{Scheme: scheme}
	if opts.ResyncPeriod > 0 {
		cacheOpts.SyncPeriod = &opts.ResyncPeriod
	}
	if len(opts.Namespaces) > 0 {
		byNS := make(map[string]cache.Config, len(opts.Namespaces))
		for _, ns := range opts.Namespaces {
			byNS[ns] = cache.Config{}
		}
		// Nodes are cluster-scoped and must stay unrestricted even when pod
		// caching is namespaced, or the planner would see workloads with no
		// nodes to place them on.
		cacheOpts.ByObject = map[client.Object]cache.ByObject{
			&corev1.Pod{}:                   {Namespaces: byNS},
			&policyv1.PodDisruptionBudget{}: {Namespaces: byNS},
			&appsv1.ReplicaSet{}:            {Namespaces: byNS},
		}
	}

	// Strip what is never read before anything is cached.
	//
	// On a pod-heavy cluster the informer cache is the planner's dominant
	// memory cost, and most of what it holds is managed fields, annotations
	// and container detail this product never looks at. Transforming on the
	// way in is the standard remedy, and it applies to every pod for the life
	// of the process rather than per read.
	applyTransform(&cacheOpts)

	c, err := cache.New(cfg, cacheOpts)
	if err != nil {
		return nil, fmt.Errorf("build informer cache: %w", err)
	}

	src := opts.Metrics
	if src == nil {
		src = metrics.Noop{}
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	return &Collector{cache: c, metrics: src, log: log}, nil
}

// Start runs the informers until ctx is cancelled. It blocks.
func (c *Collector) Start(ctx context.Context) error {
	return c.cache.Start(ctx)
}

// WaitForSync blocks until every informer has completed its initial list.
//
// The planner must not report ready before this returns: planning against a
// half-populated cache would propose draining nodes whose pods simply had not
// been observed yet.
func (c *Collector) WaitForSync(ctx context.Context) bool {
	return c.cache.WaitForCacheSync(ctx)
}

// Snapshot builds an immutable view of current cluster state.
func (c *Collector) Snapshot(ctx context.Context) (*model.ClusterSnapshot, error) {
	var nodeList corev1.NodeList
	if err := c.cache.List(ctx, &nodeList); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	var podList corev1.PodList
	if err := c.cache.List(ctx, &podList); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	var pdbList policyv1.PodDisruptionBudgetList
	if err := c.cache.List(ctx, &pdbList); err != nil {
		return nil, fmt.Errorf("list poddisruptionbudgets: %w", err)
	}

	resolver := c.ownerResolver(ctx)

	snap := &model.ClusterSnapshot{
		TakenAt: time.Now().UTC(),
		Nodes:   make([]model.Node, 0, len(nodeList.Items)),
		Pods:    make([]model.Pod, 0, len(podList.Items)),
		PDBs:    make([]model.PodDisruptionBudget, 0, len(pdbList.Items)),
	}

	for i := range nodeList.Items {
		snap.Nodes = append(snap.Nodes, convertNode(&nodeList.Items[i]))
	}
	for i := range podList.Items {
		p := &podList.Items[i]
		// Terminated pods hold no resources and would inflate every node's
		// apparent load.
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		snap.Pods = append(snap.Pods, convertPod(p, resolver))
	}
	for i := range pdbList.Items {
		snap.PDBs = append(snap.PDBs, convertPDB(&pdbList.Items[i]))
	}

	if err := c.attachUsage(ctx, snap); err != nil {
		// Usage is strictly supplementary; a metrics outage must never stop
		// the planner from producing a requests-based plan.
		c.log.Warn("usage data unavailable, continuing with requests only", "error", err)
	}

	return snap, nil
}

func (c *Collector) attachUsage(ctx context.Context, snap *model.ClusterSnapshot) error {
	if !c.metrics.Available() {
		return nil
	}
	nodeUsage, err := c.metrics.NodeUsage(ctx)
	if err != nil {
		return err
	}
	podUsage, err := c.metrics.PodUsage(ctx)
	if err != nil {
		return err
	}
	for i := range snap.Nodes {
		if u, ok := nodeUsage[snap.Nodes[i].Name]; ok {
			snap.Nodes[i].Usage = &u
		}
	}
	for i := range snap.Pods {
		if u, ok := podUsage[snap.Pods[i].Key()]; ok {
			snap.Pods[i].Usage = &u
		}
	}
	snap.HasUsageData = true
	return nil
}

// ownerResolver maps a pod to its top-level workload controller.
type ownerResolver func(*corev1.Pod) *model.OwnerRef

// ownerResolver walks one level up from a ReplicaSet to its Deployment, so the
// model reports the workload an operator recognises rather than a generated
// ReplicaSet name. Lookups hit the informer cache, not the API server.
func (c *Collector) ownerResolver(ctx context.Context) ownerResolver {
	return func(p *corev1.Pod) *model.OwnerRef {
		for _, ref := range p.OwnerReferences {
			if ref.Controller == nil || !*ref.Controller {
				continue
			}
			if ref.Kind != "ReplicaSet" {
				return &model.OwnerRef{Kind: ref.Kind, Name: ref.Name}
			}
			var rs appsv1.ReplicaSet
			key := client.ObjectKey{Namespace: p.Namespace, Name: ref.Name}
			if err := c.cache.Get(ctx, key, &rs); err != nil {
				// Fall back to the ReplicaSet itself rather than dropping
				// ownership: the classifier needs to know the pod is
				// replicated even if the Deployment name is unavailable.
				return &model.OwnerRef{Kind: ref.Kind, Name: ref.Name}
			}
			for _, rsRef := range rs.OwnerReferences {
				if rsRef.Controller != nil && *rsRef.Controller {
					return &model.OwnerRef{Kind: rsRef.Kind, Name: rsRef.Name}
				}
			}
			return &model.OwnerRef{Kind: ref.Kind, Name: ref.Name}
		}
		return nil
	}
}

// applyTransform installs cache transforms that discard unread fields.
//
// Deliberately conservative: anything convert.go reads must survive. That means
// keeping labels, the owner references, node assignment, the full container
// list (effective requests are computed from it), conditions, tolerations and
// affinity. What goes is managed fields — often the largest single part of a
// pod object — annotations, and the container statuses, which carry image
// digests and restart history that nothing here consults.
func applyTransform(opts *cache.Options) {
	if opts.ByObject == nil {
		opts.ByObject = map[client.Object]cache.ByObject{}
	}

	pod := opts.ByObject[&corev1.Pod{}]
	pod.Transform = func(obj any) (any, error) {
		p, ok := obj.(*corev1.Pod)
		if !ok {
			return obj, nil
		}
		p.ManagedFields = nil
		p.Annotations = nil
		p.Status.ContainerStatuses = nil
		p.Status.InitContainerStatuses = nil
		p.Status.EphemeralContainerStatuses = nil
		return p, nil
	}
	opts.ByObject[&corev1.Pod{}] = pod

	node := opts.ByObject[&corev1.Node{}]
	node.Transform = func(obj any) (any, error) {
		n, ok := obj.(*corev1.Node)
		if !ok {
			return obj, nil
		}
		n.ManagedFields = nil
		// Node labels and taints are load-bearing for placement, so only the
		// image inventory goes — which on a busy node is most of the object.
		n.Status.Images = nil
		return n, nil
	}
	opts.ByObject[&corev1.Node{}] = node

	rs := opts.ByObject[&appsv1.ReplicaSet{}]
	rs.Transform = func(obj any) (any, error) {
		r, ok := obj.(*appsv1.ReplicaSet)
		if !ok {
			return obj, nil
		}
		r.ManagedFields = nil
		r.Annotations = nil
		// Only the owner chain is read, to name a pod's real controller.
		r.Spec.Template = corev1.PodTemplateSpec{}
		return r, nil
	}
	opts.ByObject[&appsv1.ReplicaSet{}] = rs
}
