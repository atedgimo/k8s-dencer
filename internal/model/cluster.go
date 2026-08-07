// Package model holds the k8s-dencer domain types.
//
// This package imports nothing from k8s.io, by design and enforced by a test.
// A ClusterSnapshot is therefore plain data that round-trips through YAML,
// which is what allows the constraint analyzer, planner and impact classifier
// to be tested against a captured fixture with no API server, no envtest and
// no cluster. Translation from Kubernetes objects lives in internal/cluster.
package model

import "time"

// ClusterSnapshot is an immutable point-in-time view of everything the planner
// reasons about. Treat it as read-only once built: the planner may evaluate
// many candidate packings against the same snapshot concurrently.
type ClusterSnapshot struct {
	TakenAt time.Time `json:"takenAt"`

	Nodes []Node                `json:"nodes"`
	Pods  []Pod                 `json:"pods"`
	PDBs  []PodDisruptionBudget `json:"pdbs"`

	// Set when the snapshot came from a metrics-capable source. Requests-only
	// snapshots are the norm; see internal/cluster/metrics.
	HasUsageData bool `json:"hasUsageData"`
}

// Node is a schedulable machine.
type Node struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Taints      []Taint           `json:"taints,omitempty"`

	Capacity    Resources `json:"capacity"`
	Allocatable Resources `json:"allocatable"`

	// Unschedulable is the cordon flag.
	Unschedulable bool      `json:"unschedulable"`
	Ready         bool      `json:"ready"`
	CreatedAt     time.Time `json:"createdAt"`

	// Actual usage, populated only when HasUsageData is set on the snapshot.
	Usage *Resources `json:"usage,omitempty"`
}

// Zone returns the node's failure domain, or "" if unlabelled.
func (n Node) Zone() string { return n.Labels[LabelZone] }

// InstanceType returns the machine shape, from the standard well-known label.
// Empty when the platform does not set it (KWOK fixtures, bare metal).
func (n Node) InstanceType() string { return n.Labels["node.kubernetes.io/instance-type"] }

// CapacityType reports how the node is bought: "spot" when any of the
// platforms' spot/preemptible markers is present, "on-demand" when the
// platform is recognisably present without one, and "" when nothing can be
// said. Three-valued for the usual reason — a KWOK node is not "on-demand",
// it is unpriced, and pretending otherwise would put a made-up word in a
// cost report.

// Pool returns the node group this machine belongs to, from whichever
// provider label is present. Empty when nothing says.
//
// This is not the machine type, though the UI conflated the two for a while:
// it reported "1 pool" while counting distinct instance types, so two pools of
// one shape read as one and a single pool of mixed shapes read as two.
//
// A pool is the unit that scales, the unit that gets rotated, and the unit
// that has a price — which makes it the unit an operator's question is
// usually about ("which pool should shrink"), and worth reading properly.
func (n Node) Pool() string {
	for _, k := range []string{
		"cloud.google.com/gke-nodepool",    // GKE
		"eks.amazonaws.com/nodegroup",      // EKS managed node groups
		"karpenter.sh/nodepool",            // Karpenter v1
		"karpenter.sh/provisioner-name",    // Karpenter v1beta1 and earlier
		"kubernetes.azure.com/agentpool",   // AKS
		"agentpool",                        // AKS, older clusters
		"node.kubernetes.io/instancegroup", // various self-managed setups
	} {
		if v := n.Labels[k]; v != "" {
			return v
		}
	}
	return ""
}

// DoNotDisrupt reports the node-level hands-off marker
// (karpenter.sh/do-not-disrupt: "true"). Such a node is not a drain
// candidate, whatever its utilisation says.
func (n Node) DoNotDisrupt() bool {
	return n.Annotations["karpenter.sh/do-not-disrupt"] == "true"
}

func (n Node) CapacityType() string {
	switch {
	case n.Labels["karpenter.sh/capacity-type"] == "spot",
		n.Labels["cloud.google.com/gke-spot"] == "true",
		n.Labels["cloud.google.com/gke-preemptible"] == "true",
		n.Labels["eks.amazonaws.com/capacityType"] == "SPOT",
		n.Labels["kubernetes.azure.com/scalesetpriority"] == "spot":
		return "spot"
	case n.Labels["karpenter.sh/capacity-type"] != "",
		n.Labels["eks.amazonaws.com/capacityType"] != "":
		return "on-demand"
	case n.InstanceType() != "":
		// A cloud set the instance type; absent any spot marker, on-demand is
		// what remains.
		return "on-demand"
	}
	return ""
}

// Well-known label keys the planner reads directly.
const (
	LabelZone     = "topology.kubernetes.io/zone"
	LabelRegion   = "topology.kubernetes.io/region"
	LabelHostname = "kubernetes.io/hostname"
)

// PodPhase is the coarse lifecycle state.
type PodPhase string

const (
	PodPending   PodPhase = "Pending"
	PodRunning   PodPhase = "Running"
	PodSucceeded PodPhase = "Succeeded"
	PodFailed    PodPhase = "Failed"
)

// Pod is a scheduled or pending workload unit.
type Pod struct {
	Namespace string            `json:"namespace"`
	Name      string            `json:"name"`
	NodeName  string            `json:"nodeName,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Phase     PodPhase          `json:"phase"`
	CreatedAt time.Time         `json:"createdAt"`

	// Requests is the sum across containers, and is the planner's entire input
	// for sizing: scheduling is request-based, and this cluster has no
	// metrics-server. Init containers are folded in per Kubernetes' own
	// effective-request rules.
	Requests Resources `json:"requests"`

	NodeSelector   map[string]string          `json:"nodeSelector,omitempty"`
	Tolerations    []Toleration               `json:"tolerations,omitempty"`
	NodeAffinity   *NodeAffinity              `json:"nodeAffinity,omitempty"`
	PodAffinity    *PodAffinity               `json:"podAffinity,omitempty"`
	TopologySpread []TopologySpreadConstraint `json:"topologySpread,omitempty"`

	// Ready is the pod's Ready condition, not its phase.
	//
	// The distinction is load-bearing for the executor. A pod can be Running
	// and failing its probes, and treating that as recovered means draining the
	// next node while the service is still down. Phase alone cannot see it.
	Ready bool `json:"ready"`

	Owner            *OwnerRef `json:"owner,omitempty"`
	PriorityClass    string    `json:"priorityClass,omitempty"`
	Priority         int32     `json:"priority,omitempty"`
	Terminating      bool      `json:"terminating,omitempty"`
	HasPersistentVol bool      `json:"hasPersistentVolume,omitempty"`

	// DoNotDisrupt is the ecosystem's own "hands off" signal, captured from
	// either karpenter.sh/do-not-disrupt: "true" or
	// cluster-autoscaler.kubernetes.io/safe-to-evict: "false". The owner has
	// explicitly opted out of voluntary disruption, and a consolidation tool
	// that ignores the conventions its neighbours honour is not safe to run
	// beside them.
	DoNotDisrupt bool `json:"doNotDisrupt,omitempty"`

	Usage *Resources `json:"usage,omitempty"`
}

// Key is the namespace/name identifier.
func (p Pod) Key() string { return p.Namespace + "/" + p.Name }

// IsMirrorPod reports whether the kubelet, not a controller, owns this pod.
//
// A static pod is defined by a file on the node and mirrored into the API with
// the Node itself as its controller. GKE ships kube-proxy this way, and every
// managed platform ships something. Deleting the mirror object changes
// nothing: the kubelet recreates it on the same node, immediately.
func (p Pod) IsMirrorPod() bool { return p.Owner != nil && p.Owner.Kind == "Node" }

// PinnedToNode reports whether the pod is bound to the node it is on by
// design, so that no scheduler could place it anywhere else.
//
// This is deliberately not a list of owner kinds. The question is whether
// anything is capable of moving the pod to a different node: a DaemonSet
// recreates it here, and for a static pod there is no controller at all.
// Treating a pinned pod as relocatable sends it to the placement search, which
// then reports — correctly, and uselessly — that nowhere will take it.
func (p Pod) PinnedToNode() bool {
	if p.Owner == nil {
		return false
	}
	return p.Owner.Kind == "DaemonSet" || p.Owner.Kind == "Node"
}

// IsMovable reports whether consolidation may relocate this pod at all.
//
// DaemonSet pods are excluded because they are pinned to their node by design:
// draining one does not free capacity, it just gets recreated. Mirror and
// static pods likewise. Terminated pods hold no resources.
func (p Pod) IsMovable() bool {
	if p.Terminating || p.Phase == PodSucceeded || p.Phase == PodFailed {
		return false
	}
	if p.PinnedToNode() {
		return false
	}
	if p.DoNotDisrupt {
		// The owner said hands off, in the ecosystem's own vocabulary.
		return false
	}
	return true
}

// IsSingleton reports whether the pod has no replicated controller above it,
// making its eviction inherently higher risk. Bare pods have no controller to
// recreate them at all.
func (p Pod) IsSingleton() bool {
	if p.Owner == nil {
		return true
	}
	return p.Owner.Kind == "StatefulSet"
}

// OwnerRef identifies the controller that manages a pod. Kind is the top-level
// workload kind, so a pod owned by a ReplicaSet reports Deployment.
type OwnerRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// PodDisruptionBudget limits voluntary eviction of a pod set.
type PodDisruptionBudget struct {
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Selector  *LabelSelector `json:"selector,omitempty"`

	// DisruptionsAllowed is the live headroom published by the PDB controller.
	// Zero means the API server will refuse every voluntary eviction, which is
	// the difference between a PDB that merely exists and one that blocks.
	DisruptionsAllowed int32 `json:"disruptionsAllowed"`
	CurrentHealthy     int32 `json:"currentHealthy"`
	DesiredHealthy     int32 `json:"desiredHealthy"`
	ExpectedPods       int32 `json:"expectedPods"`
}

// Key is the namespace/name identifier.
func (p PodDisruptionBudget) Key() string { return p.Namespace + "/" + p.Name }

// Blocks reports whether the PDB currently forbids evicting any pod it covers.
func (p PodDisruptionBudget) Blocks() bool { return p.DisruptionsAllowed <= 0 }

// NodeByName returns the named node, or false if absent.
func (s *ClusterSnapshot) NodeByName(name string) (Node, bool) {
	for _, n := range s.Nodes {
		if n.Name == name {
			return n, true
		}
	}
	return Node{}, false
}

// PodsOnNode returns the pods assigned to a node.
func (s *ClusterSnapshot) PodsOnNode(nodeName string) []Pod {
	var out []Pod
	for _, p := range s.Pods {
		if p.NodeName == nodeName {
			out = append(out, p)
		}
	}
	return out
}

// RequestedOnNode sums the requests of pods assigned to a node. Pods that hold
// no resources (terminated) are skipped.
func (s *ClusterSnapshot) RequestedOnNode(nodeName string) Resources {
	var total Resources
	for _, p := range s.Pods {
		if p.NodeName != nodeName {
			continue
		}
		if p.Phase == PodSucceeded || p.Phase == PodFailed {
			continue
		}
		total = total.Add(p.Requests).Add(Resources{Pods: 1})
	}
	return total
}

// Totals returns cluster-wide allocatable capacity and requested resources.
func (s *ClusterSnapshot) Totals() (allocatable, requested Resources) {
	for _, n := range s.Nodes {
		allocatable = allocatable.Add(n.Allocatable)
	}
	for _, n := range s.Nodes {
		requested = requested.Add(s.RequestedOnNode(n.Name))
	}
	return allocatable, requested
}
