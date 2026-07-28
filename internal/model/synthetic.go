package model

import (
	"fmt"
	"math/rand"
	"time"
)

// SyntheticOptions shapes a generated cluster.
//
// The defaults aim at a plausible mid-size cluster rather than a convenient
// one. Benchmarks over a uniform cluster — every pod the same size, no PDBs, no
// affinity — would make the constraint work look far cheaper than it is, since
// most of the cost lives in the checks that uniform data never triggers.
type SyntheticOptions struct {
	Nodes int
	// PodsPerNode is the mean; actual counts vary around it.
	PodsPerNode int
	// Zones spreads nodes across topology domains.
	Zones int
	// PDBFraction is the share of workloads carrying a PodDisruptionBudget.
	PDBFraction float64
	// AntiAffinityFraction is the share of workloads with required pod
	// anti-affinity, which is the most expensive predicate to evaluate.
	AntiAffinityFraction float64
	// SpreadFraction is the share carrying a topology spread constraint.
	SpreadFraction float64
	// DaemonSetsPerNode are pinned pods that can never move.
	DaemonSetsPerNode int
	// Utilization is the mean fraction of each node's CPU that is requested.
	Utilization float64
	// Seed makes generation deterministic, so a benchmark measures a change in
	// the code rather than a change in the data.
	Seed int64
}

// DefaultSynthetic returns options for a cluster of the given node count.
func DefaultSynthetic(nodes int) SyntheticOptions {
	return SyntheticOptions{
		Nodes:                nodes,
		PodsPerNode:          30,
		Zones:                3,
		PDBFraction:          0.30,
		AntiAffinityFraction: 0.10,
		SpreadFraction:       0.15,
		DaemonSetsPerNode:    2,
		Utilization:          0.45,
		Seed:                 1,
	}
}

// Synthesize builds a deterministic cluster snapshot of arbitrary size.
//
// Test-only in practice, but it lives in the production package because it
// depends on every field of the domain model and would drift instantly if it
// were kept beside the tests. It has no Kubernetes imports, like the rest of
// this package — which is precisely what makes it possible to benchmark the
// planner at fifty thousand pods without a cluster.
func Synthesize(o SyntheticOptions) *ClusterSnapshot {
	if o.Nodes <= 0 {
		o.Nodes = 1
	}
	if o.PodsPerNode <= 0 {
		o.PodsPerNode = 10
	}
	if o.Zones <= 0 {
		o.Zones = 1
	}
	rng := rand.New(rand.NewSource(o.Seed))

	// A node's capacity, fixed so utilisation is easy to reason about.
	const nodeCPU int64 = 8000
	const nodeMem int64 = 32 << 30

	snap := &ClusterSnapshot{
		TakenAt: time.Unix(1_750_000_000, 0).UTC(),
		Nodes:   make([]Node, 0, o.Nodes),
		Pods:    make([]Pod, 0, o.Nodes*o.PodsPerNode),
	}

	for i := range o.Nodes {
		snap.Nodes = append(snap.Nodes, Node{
			Name:  fmt.Sprintf("node-%05d", i),
			Ready: true,
			Labels: map[string]string{
				LabelZone:     fmt.Sprintf("zone-%d", i%o.Zones),
				LabelHostname: fmt.Sprintf("node-%05d", i),
				"pool":        pool(i),
			},
			Allocatable: Resources{MilliCPU: nodeCPU, MemoryBytes: nodeMem, Pods: 110},
			Capacity:    Resources{MilliCPU: nodeCPU, MemoryBytes: nodeMem, Pods: 110},
			CreatedAt:   snap.TakenAt.Add(-24 * time.Hour),
		})
	}

	// Pod sizes are scaled to the requested density rather than fixed, so
	// PodsPerNode is actually honoured. With a fixed ladder the CPU budget
	// filled long before the pod count did, and asking for 50 pods per node
	// quietly produced 9 — which would have made a "50k pod" benchmark
	// measure something else entirely.
	perNodeBudget := int64(float64(nodeCPU) * o.Utilization)
	base := max64(10, perNodeBudget/int64(o.PodsPerNode))
	// A long tail: many small pods, a few large. That spread is what makes
	// first-fit-decreasing do real work.
	ladder := []int64{base / 2, base / 2, base, base, base, base * 2, base * 3}

	// Workloads are shared across nodes, which is what makes anti-affinity and
	// spread constraints meaningful — a per-node workload would never conflict
	// with anything.
	workloads := max(1, (o.Nodes*o.PodsPerNode)/12)
	type workload struct {
		name         string
		namespace    string
		hasPDB       bool
		antiAffinity bool
		spread       bool
		cpu          int64
	}
	kinds := make([]workload, workloads)
	for w := range workloads {
		kinds[w] = workload{
			name:         fmt.Sprintf("app-%04d", w),
			namespace:    fmt.Sprintf("ns-%02d", w%20),
			hasPDB:       rng.Float64() < o.PDBFraction,
			cpu:          ladder[rng.Intn(len(ladder))],
			antiAffinity: rng.Float64() < o.AntiAffinityFraction,
			spread:       rng.Float64() < o.SpreadFraction,
		}
	}

	for i := range o.Nodes {
		node := snap.Nodes[i].Name

		for d := range o.DaemonSetsPerNode {
			snap.Pods = append(snap.Pods, Pod{
				Namespace: "kube-system",
				Name:      fmt.Sprintf("agent-%d-%s", d, node),
				NodeName:  node, Phase: PodRunning, Ready: true,
				Labels:   map[string]string{"app": fmt.Sprintf("agent-%d", d)},
				Requests: Resources{MilliCPU: 50, MemoryBytes: 64 << 20},
				Owner:    &OwnerRef{Kind: "DaemonSet", Name: fmt.Sprintf("agent-%d", d)},
			})
		}

		var used int64
		for n := 0; n < o.PodsPerNode; n++ {
			w := kinds[rng.Intn(len(kinds))]
			// Overshooting the budget slightly is realistic; refusing to place
			// the pod at all would make density depend on the size draw.
			if used+w.cpu > perNodeBudget && n > 0 {
				break
			}
			used += w.cpu

			pod := Pod{
				Namespace: w.namespace,
				Name:      fmt.Sprintf("%s-%s-%d", w.name, node, n),
				NodeName:  node, Phase: PodRunning, Ready: true,
				Labels:   map[string]string{"app": w.name},
				Requests: Resources{MilliCPU: w.cpu, MemoryBytes: w.cpu * (1 << 20) * 2},
				Owner:    &OwnerRef{Kind: "ReplicaSet", Name: w.name},
			}
			if w.antiAffinity {
				pod.PodAffinity = &PodAffinity{
					RequiredAntiAffinity: []PodAffinityTerm{{
						TopologyKey:   LabelHostname,
						LabelSelector: &LabelSelector{MatchLabels: map[string]string{"app": w.name}},
					}},
				}
			}
			if w.spread {
				pod.TopologySpread = []TopologySpreadConstraint{{
					MaxSkew:           1,
					TopologyKey:       LabelZone,
					WhenUnsatisfiable: DoNotSchedule,
					LabelSelector:     &LabelSelector{MatchLabels: map[string]string{"app": w.name}},
				}}
			}
			snap.Pods = append(snap.Pods, pod)
		}
	}

	for _, w := range kinds {
		if !w.hasPDB {
			continue
		}
		snap.PDBs = append(snap.PDBs, PodDisruptionBudget{
			Namespace: w.namespace, Name: w.name,
			Selector:           &LabelSelector{MatchLabels: map[string]string{"app": w.name}},
			DisruptionsAllowed: 1,
			CurrentHealthy:     3, DesiredHealthy: 2, ExpectedPods: 3,
		})
	}

	return snap
}

func pool(i int) string {
	switch i % 4 {
	case 0:
		return "batch"
	case 1:
		return "web"
	default:
		return "general"
	}
}
