package model

import "time"

// ImpactRating is the risk classification of a single plan step.
//
// The ratings are policy-enforced, not advisory: Red steps may only execute
// inside an approved maintenance window, and that is checked by the Safety
// Guard rather than by UI input validation. Phase 1 has no executor, so today
// the rating is purely explanatory.
type ImpactRating string

const (
	// ImpactGreen is safe to run at any time on request.
	ImpactGreen ImpactRating = "Green"
	// ImpactYellow is executable on request but requires confirmation.
	ImpactYellow ImpactRating = "Yellow"
	// ImpactRed may only run inside an approved maintenance window.
	ImpactRed ImpactRating = "Red"
)

// RequiresMaintenanceWindow reports whether policy confines this rating to a
// maintenance window.
func (r ImpactRating) RequiresMaintenanceWindow() bool { return r == ImpactRed }

// PlanStatus is the validity of a plan against live cluster state.
type PlanStatus string

const (
	PlanValid   PlanStatus = "Valid"
	PlanStale   PlanStatus = "Stale"
	PlanInvalid PlanStatus = "Invalid"
)

// Plan is an ordered sequence of independently executable consolidation steps.
type Plan struct {
	ID          string     `json:"id"`
	GeneratedAt time.Time  `json:"generatedAt"`
	Status      PlanStatus `json:"status"`

	// SnapshotTakenAt ties the plan to the cluster state it was computed
	// against, so staleness is detectable without re-planning.
	SnapshotTakenAt time.Time `json:"snapshotTakenAt"`

	Steps []PlanStep `json:"steps"`

	// NodesBefore and NodesAfter describe the packing this plan achieves.
	//
	// NodesBefore is the fleet: every node carrying anything at all. It used
	// to count only nodes the packer had work for, which meant a node holding
	// nothing but DaemonSets was missing from the number an operator reads as
	// "how big is my cluster".
	NodesBefore int `json:"nodesBefore"`
	NodesAfter  int `json:"nodesAfter"`

	// AlreadyReclaimable is how many nodes hold nothing but pods that cannot move
	// and do not need to — DaemonSets, and static pods owned by the node.
	//
	// They need no step, because a step is a list of pods to relocate and there is
	// nothing to relocate. But they are the cheapest capacity in the cluster:
	// `kubectl drain --ignore-daemonsets` empties them instantly, and most
	// autoscalers remove them unprompted.
	//
	// Counted separately because the planner used to leave them out of everything.
	// They were absent from the steps, which was right, and absent from
	// NodesBefore, which was not: on GKE, 2026-08-15, three of seven nodes held
	// only DaemonSets and the product reported "4 nodes now, 4 after, nothing to
	// do" while three machines sat there costing money.
	AlreadyReclaimable int `json:"alreadyReclaimable,omitempty"`

	// PackCeiling is the utilisation fraction this plan refused to pack
	// destinations above (0.85 = plan to 85% of allocatable). Recorded on
	// the plan, not read from config, so the UI's ceiling line describes the
	// plan on screen even after the setting changes. Zero on plans that
	// predate the ceiling.
	PackCeiling float64 `json:"packCeiling,omitempty"`
}

// ReclaimedNodes is how many nodes the full plan frees.
func (p *Plan) ReclaimedNodes() int {
	if p.NodesBefore <= p.NodesAfter {
		return 0
	}
	return p.NodesBefore - p.NodesAfter
}

// CountByRating tallies steps per impact rating.
func (p *Plan) CountByRating() map[ImpactRating]int {
	out := map[ImpactRating]int{ImpactGreen: 0, ImpactYellow: 0, ImpactRed: 0}
	for _, s := range p.Steps {
		out[s.Impact]++
	}
	return out
}

// PlanStep is one atomic unit of work, typically draining a single node.
//
// Steps are independently addressable and independently executable: an
// operator may request any subset or range rather than all-or-nothing.
type PlanStep struct {
	ID             string `json:"id"`
	SequenceNumber int    `json:"sequenceNumber"`

	// TargetNode is the node this step drains, if any.
	TargetNode string `json:"targetNode,omitempty"`
	Moves      []Move `json:"moves"`

	Impact ImpactRating `json:"impact"`
	// Rationale is human-readable text naming the constraint that drove the
	// rating. The UI and the Kagent agent both surface this exact string
	// rather than deriving their own explanation, so the two can never
	// disagree about why a step is rated as it is.
	Rationale string `json:"rationale"`

	// Reasons are the machine-readable factors behind the rating, for
	// filtering and for the agent to reason over.
	Reasons []ImpactReason `json:"reasons,omitempty"`

	// Audit fields, populated once an executor exists.
	ExecutedAt *time.Time `json:"executedAt,omitempty"`
	ExecutedBy string     `json:"executedBy,omitempty"`
	Result     string     `json:"result,omitempty"`
}

// RequiresMaintenanceWindow reports whether this step is window-confined.
func (s PlanStep) RequiresMaintenanceWindow() bool {
	return s.Impact.RequiresMaintenanceWindow()
}

// ImpactReason is one machine-readable risk factor.
type ImpactReason struct {
	// Kind is a stable identifier such as "PDBZeroHeadroom" or
	// "RequiredAntiAffinity".
	Kind string `json:"kind"`
	// Subject is the object responsible, e.g. "payments/dencer-demo".
	Subject string `json:"subject,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

// Move relocates one pod between nodes.
type Move struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	FromNode  string `json:"fromNode"`
	ToNode    string `json:"toNode"`

	// What the pod takes with it.
	//
	// Carried on the move rather than looked up, because above the graph
	// endpoint's detail limit the UI has no per-pod data to look it up in.
	// Without these the density view could empty a drained node but not show
	// the receiving nodes filling — understating exactly the number an
	// operator is judging the plan on.
	CPUMilli    int64 `json:"cpuMilli,omitempty"`
	MemoryBytes int64 `json:"memoryBytes,omitempty"`
}

// ReclaimableCapacity sums the allocatable of every node the plan drains —
// what the plan's node count is actually worth. Nodes are not fungible:
// "reclaim 15 nodes" may be a rack of 96-core machines or a drawer of 2-core
// ones, and the count alone cannot say whether the plan matters.
//
// Computed from the snapshot rather than stored on the plan, because the plan
// persists in columns the schema would have to grow, while every consumer that
// wants this figure already holds the snapshot the plan was made from.
func ReclaimableCapacity(plan *Plan, snap *ClusterSnapshot) (cpuMilli, memBytes int64) {
	byName := make(map[string]*Node, len(snap.Nodes))
	for i := range snap.Nodes {
		byName[snap.Nodes[i].Name] = &snap.Nodes[i]
	}
	for _, step := range plan.Steps {
		if n, ok := byName[step.TargetNode]; ok {
			cpuMilli += n.Allocatable.MilliCPU
			memBytes += n.Allocatable.MemoryBytes
		}
	}
	return cpuMilli, memBytes
}
