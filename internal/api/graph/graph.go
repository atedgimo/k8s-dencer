// Package graph renders a plan and its cluster snapshot for the UI.
//
// The shape is a flat element list where a pod names its node as Parent. That
// began as Cytoscape's compound-node model; M11 replaced the node-link graph
// with the packing field, and the shape survived because "this node contains
// these pods" is exactly what the packing field needs too.
//
// What did not survive is everything Cytoscape needed and the packing field
// does not. The payload used to carry an edge per proposed move, per
// anti-affinity relation and per PDB membership — 45% of all elements at 2,500
// pods — and no code has read an edge since M11. Building the PDB edges also
// walked every pod for every PDB. They are gone, along with the per-element
// fields nothing read. Emitting data no consumer reads is not free: it is
// serialised, transferred, parsed and held in memory by a browser.
package graph

import (
	"sort"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Payload is the whole graph.
type Payload struct {
	PlanID   string    `json:"planId"`
	Elements []Element `json:"elements"`
	Stats    Stats     `json:"stats"`

	// Aggregated reports that pods were summarised onto their nodes rather
	// than sent individually. The UI switches to density rendering when it is
	// set — it cannot infer this from an empty pod list, which is also what a
	// genuinely empty cluster looks like.
	Aggregated bool `json:"aggregated"`
}

// Options controls how much detail the payload carries.
type Options struct {
	// PodDetailLimit is the largest pod count for which the payload carries an
	// element per pod. Above it, pods are summarised onto their node.
	//
	// The limit exists because the cost is per pod on both sides: ~256 bytes
	// serialised, and one absolutely-positioned DOM element in the packing
	// field. 50,000 pods is 12.8 MB and 50,000 elements, which no browser
	// renders usefully — and at that density an individual 6px block conveys
	// nothing anyway. Aggregating is not a degraded view so much as the right
	// zoom level.
	PodDetailLimit int
}

// DefaultOptions keeps per-pod detail through the range where it is still
// legible and the DOM is still manageable, which measurement puts at a few
// thousand: 2,526 pods is 2,626 elements and 0.65 MB, and renders fine.
func DefaultOptions() Options { return Options{PodDetailLimit: 4000} }

// Element is one element of the payload.
type Element struct {
	Data Data `json:"data"`
}

// Data carries the element's fields.
//
// Every field here is read by the frontend. graph_contract_test.go asserts
// that, by parsing this struct and searching the UI sources for each json tag,
// so a field cannot be added speculatively or outlive its last reader. Where a
// pod moves and which step moves it are deliberately absent: the UI derives
// both from the plan's steps, which it already has.
type Data struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`

	Kind  string `json:"kind"` // node | pod
	Label string `json:"label"`

	// Node fields.
	Zone           string `json:"zone,omitempty"`
	Ready          bool   `json:"ready,omitempty"`
	Cordoned       bool   `json:"cordoned,omitempty"`
	CPUAllocatable int64  `json:"cpuAllocatable,omitempty"`
	CPURequested   int64  `json:"cpuRequested,omitempty"`
	MemAllocatable int64  `json:"memAllocatable,omitempty"`
	MemRequested   int64  `json:"memRequested,omitempty"`
	DrainStep      int    `json:"drainStep,omitempty"`

	// Occupancy, always sent. In detail mode the UI could count the pod
	// elements itself; in aggregated mode there are none to count, and having
	// one source for the number in both modes means the density view and the
	// detail view cannot disagree about how full a node is.
	PodCount     int `json:"podCount,omitempty"`
	BlockedCount int `json:"blockedCount,omitempty"`
	PinnedCount  int `json:"pinnedCount,omitempty"`

	// Pod fields.
	Namespace  string `json:"namespace,omitempty"`
	CPURequest int64  `json:"cpuRequest,omitempty"`
	MemRequest int64  `json:"memRequest,omitempty"`
	OwnerKind  string `json:"ownerKind,omitempty"`
	OwnerName  string `json:"ownerName,omitempty"`
	Movable    bool   `json:"movable"`
	Blocked    bool   `json:"blocked,omitempty"`
}

// Stats are the headline figures for the UI's tile row.
type Stats struct {
	NodesBefore int `json:"nodesBefore"`

	// Reclaimable, not "reclaimed": what the plan *would* free if executed in
	// full. Whether anything actually removed a drained node is a separate,
	// observed fact — see /api/v1/reclamations. This field was called
	// "reclaimed" until the reclamation loop landed, which is precisely how
	// the product came to report a prediction in the language of an outcome.
	Reclaimable int            `json:"reclaimable"`
	Steps       int            `json:"steps"`
	Ratings     map[string]int `json:"ratings"`
	PodsMoved   int            `json:"podsMoved"`

	// nodesAfter, cpuReclaimedMilli and memoryReclaimedBytes used to live
	// here. Nothing read any of them, and the freed-capacity pair were
	// plan-time predictions dressed in the past tense besides. Removed when a
	// guard over this struct was finally written; they can come back the day
	// something reads them.
}

// Build renders the payload at default detail.
func Build(plan *model.Plan, snap *model.ClusterSnapshot, analysis *constraints.Analysis) Payload {
	return BuildWith(plan, snap, analysis, DefaultOptions())
}

// BuildWith renders the payload, summarising pods when there are too many to
// send individually.
func BuildWith(plan *model.Plan, snap *model.ClusterSnapshot, analysis *constraints.Analysis, opts Options) Payload {
	p := Payload{PlanID: plan.ID}
	p.Aggregated = opts.PodDetailLimit > 0 && len(snap.Pods) > opts.PodDetailLimit

	// Where the plan sends each pod, and which step does it.
	// targets is still needed for the PodsMoved stat; drainStep tells a node
	// element which step empties it, which is what the field animates against.
	targets := make(map[string]model.Move, 64)
	drainStep := make(map[string]int, len(plan.Steps))

	for _, step := range plan.Steps {
		if step.TargetNode != "" {
			drainStep[step.TargetNode] = step.SequenceNumber
		}
		for _, m := range step.Moves {
			targets[m.Namespace+"/"+m.Pod] = m
		}
	}

	// One pass for occupancy, so the node loop below does not walk every pod
	// per node — that shape is how the PDB edges came to be quadratic.
	type tally struct{ pods, blocked, pinned int }
	occupancy := make(map[string]*tally, len(snap.Nodes))
	for i := range snap.Pods {
		pod := &snap.Pods[i]
		if pod.NodeName == "" {
			continue
		}
		t := occupancy[pod.NodeName]
		if t == nil {
			t = &tally{}
			occupancy[pod.NodeName] = t
		}
		t.pods++
		if !pod.IsMovable() {
			t.pinned++
		}
		if analysis != nil {
			if pc, ok := analysis.ForPod(pod.Key()); ok && !pc.Movable {
				t.blocked++
			}
		}
	}

	for _, n := range snap.Nodes {
		requested := snap.RequestedOnNode(n.Name)
		p.Elements = append(p.Elements, Element{
			Data: Data{
				ID:             nodeID(n.Name),
				Kind:           "node",
				Label:          n.Name,
				Zone:           n.Zone(),
				Ready:          n.Ready,
				Cordoned:       n.Unschedulable,
				CPUAllocatable: n.Allocatable.MilliCPU,
				CPURequested:   requested.MilliCPU,
				MemAllocatable: n.Allocatable.MemoryBytes,
				MemRequested:   requested.MemoryBytes,
				DrainStep:      drainStep[n.Name],
				PodCount:       occ(occupancy, n.Name).pods,
				BlockedCount:   occ(occupancy, n.Name).blocked,
				PinnedCount:    occ(occupancy, n.Name).pinned,
			},
		})
	}

	if p.Aggregated {
		// The node elements above already carry everything the density view
		// draws. Skipping the pod loop is the entire saving.
		p.Stats = buildStats(plan, targets)
		return p
	}

	for _, pod := range snap.Pods {
		if pod.NodeName == "" {
			// Unscheduled pods have no node to sit inside, and the packing
			// field has nowhere to draw them.
			continue
		}
		key := pod.Key()
		data := Data{
			ID:         podID(key),
			Parent:     nodeID(pod.NodeName),
			Kind:       "pod",
			Label:      pod.Name,
			Namespace:  pod.Namespace,
			CPURequest: pod.Requests.MilliCPU,
			MemRequest: pod.Requests.MemoryBytes,
			Movable:    pod.IsMovable(),
		}
		if pod.Owner != nil {
			data.OwnerKind = pod.Owner.Kind
			data.OwnerName = pod.Owner.Name
		}
		if analysis != nil {
			if pc, ok := analysis.ForPod(key); ok {
				data.Blocked = !pc.Movable
			}
		}
		p.Elements = append(p.Elements, Element{Data: data})
	}

	p.Stats = buildStats(plan, targets)
	return p
}

func buildStats(plan *model.Plan, targets map[string]model.Move) Stats {
	ratings := plan.CountByRating()
	return Stats{
		NodesBefore: plan.NodesBefore,
		Reclaimable: plan.ReclaimedNodes(),
		Steps:       len(plan.Steps),
		Ratings: map[string]int{
			"Green":  ratings[model.ImpactGreen],
			"Yellow": ratings[model.ImpactYellow],
			"Red":    ratings[model.ImpactRed],
		},
		PodsMoved: len(targets),
	}
}

// occ returns a node's tally, or an empty one for a node holding no pods.
func occ[T any](m map[string]*T, name string) *T {
	if t := m[name]; t != nil {
		return t
	}
	var zero T
	return &zero
}

func nodeID(name string) string { return "node:" + name }
func podID(key string) string   { return "pod:" + key }

// sortElements keeps element order stable so the payload does not reshuffle
// between identical requests, which would make the UI relayout for no reason.
func sortElements(e []Element) {
	sort.SliceStable(e, func(i, j int) bool { return e[i].Data.ID < e[j].Data.ID })
}
