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
