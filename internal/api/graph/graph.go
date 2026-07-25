// Package graph renders a plan and its cluster snapshot as a Cytoscape.js
// element list.
//
// Shaped for Cytoscape's compound-node model: a cluster node is a parent and
// its pods are children. That maps directly onto "this node contains these
// pods" without any layout trickery, which is why Cytoscape was chosen over a
// force-directed library.
//
// Each pod carries both where it is now and where the plan would put it, so
// the frontend can render the before/after view and animate the step scrubber
// from one payload rather than diffing two.
package graph

import (
	"fmt"
	"sort"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// Payload is the whole graph.
type Payload struct {
	PlanID   string    `json:"planId"`
	Elements []Element `json:"elements"`
	Stats    Stats     `json:"stats"`
}

// Element is one Cytoscape element.
type Element struct {
	Group string `json:"group"` // "nodes" or "edges"
	Data  Data   `json:"data"`
}

// Data carries the element's fields. Cytoscape requires id; parent creates the
// compound nesting; source/target define edges.
type Data struct {
	ID     string `json:"id"`
	Parent string `json:"parent,omitempty"`
	Source string `json:"source,omitempty"`
	Target string `json:"target,omitempty"`

	Kind  string `json:"kind"` // node | pod | edge
	Label string `json:"label"`

	// Node fields.
	Zone           string  `json:"zone,omitempty"`
	Ready          bool    `json:"ready,omitempty"`
	Cordoned       bool    `json:"cordoned,omitempty"`
	CPUAllocatable int64   `json:"cpuAllocatable,omitempty"`
	CPURequested   int64   `json:"cpuRequested,omitempty"`
	MemAllocatable int64   `json:"memAllocatable,omitempty"`
	MemRequested   int64   `json:"memRequested,omitempty"`
	Utilization    float64 `json:"utilization,omitempty"`
	Drained        bool    `json:"drained,omitempty"`
	DrainStep      int     `json:"drainStep,omitempty"`

	// Pod fields.
	Namespace  string `json:"namespace,omitempty"`
	CPURequest int64  `json:"cpuRequest,omitempty"`
	MemRequest int64  `json:"memRequest,omitempty"`
	OwnerKind  string `json:"ownerKind,omitempty"`
	OwnerName  string `json:"ownerName,omitempty"`
	Movable    bool   `json:"movable"`
	// TargetNode is where the plan relocates this pod, empty if it stays.
	TargetNode string `json:"targetNode,omitempty"`
	MoveStep   int    `json:"moveStep,omitempty"`
	Blocked    bool   `json:"blocked,omitempty"`

	// Edge fields. Relation is "anti-affinity", "pdb" or "move".
	Relation string `json:"relation,omitempty"`
	Impact   string `json:"impact,omitempty"`
}

// Stats are the headline figures for the UI's tile row.
type Stats struct {
	NodesBefore     int            `json:"nodesBefore"`
	NodesAfter      int            `json:"nodesAfter"`
	Reclaimed       int            `json:"reclaimed"`
	Steps           int            `json:"steps"`
	Ratings         map[string]int `json:"ratings"`
	PodsMoved       int            `json:"podsMoved"`
	CPUReclaimed    int64          `json:"cpuReclaimedMilli"`
	MemoryReclaimed int64          `json:"memoryReclaimedBytes"`
}

// Build renders the payload.
func Build(plan *model.Plan, snap *model.ClusterSnapshot, analysis *constraints.Analysis) Payload {
	p := Payload{PlanID: plan.ID}

	// Where the plan sends each pod, and which step does it.
	targets := make(map[string]model.Move, 64)
	moveStep := make(map[string]int, 64)
	drainStep := make(map[string]int, len(plan.Steps))
	stepImpact := make(map[string]string, len(plan.Steps))

	for _, step := range plan.Steps {
		if step.TargetNode != "" {
			drainStep[step.TargetNode] = step.SequenceNumber
			stepImpact[step.TargetNode] = string(step.Impact)
		}
		for _, m := range step.Moves {
			key := m.Namespace + "/" + m.Pod
			targets[key] = m
			moveStep[key] = step.SequenceNumber
		}
	}

	var reclaimedCPU, reclaimedMem int64

	for _, n := range snap.Nodes {
		requested := snap.RequestedOnNode(n.Name)
		_, isDrained := drainStep[n.Name]
		if isDrained {
			reclaimedCPU += n.Allocatable.MilliCPU
			reclaimedMem += n.Allocatable.MemoryBytes
		}
		p.Elements = append(p.Elements, Element{
			Group: "nodes",
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
				Utilization:    requested.DominantRatio(n.Allocatable),
				Drained:        isDrained,
				DrainStep:      drainStep[n.Name],
				Impact:         stepImpact[n.Name],
			},
		})
	}

	for _, pod := range snap.Pods {
		if pod.NodeName == "" {
			// Unscheduled pods have no parent to nest under; Cytoscape would
			// render them as orphans floating outside the graph.
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
		if m, moved := targets[key]; moved {
			data.TargetNode = m.ToNode
			data.MoveStep = moveStep[key]
		}
		if analysis != nil {
			if pc, ok := analysis.ForPod(key); ok {
				data.Blocked = !pc.Movable
			}
		}
		p.Elements = append(p.Elements, Element{Group: "nodes", Data: data})
	}

	p.Elements = append(p.Elements, moveEdges(targets, moveStep, stepImpact)...)
	if analysis != nil {
		p.Elements = append(p.Elements, constraintEdges(snap, analysis)...)
	}

	ratings := plan.CountByRating()
	p.Stats = Stats{
		NodesBefore: plan.NodesBefore,
		NodesAfter:  plan.NodesAfter,
		Reclaimed:   plan.ReclaimedNodes(),
		Steps:       len(plan.Steps),
		Ratings: map[string]int{
			"Green":  ratings[model.ImpactGreen],
			"Yellow": ratings[model.ImpactYellow],
			"Red":    ratings[model.ImpactRed],
		},
		PodsMoved:       len(targets),
		CPUReclaimed:    reclaimedCPU,
		MemoryReclaimed: reclaimedMem,
	}
	return p
}

// moveEdges draw each proposed relocation, so the frontend can animate a pod
// travelling to its destination when the scrubber reaches that step.
func moveEdges(targets map[string]model.Move, moveStep map[string]int, stepImpact map[string]string) []Element {
	out := make([]Element, 0, len(targets))
	for key, m := range targets {
		out = append(out, Element{
			Group: "edges",
			Data: Data{
				ID:       fmt.Sprintf("move:%s", key),
				Source:   podID(key),
				Target:   nodeID(m.ToNode),
				Kind:     "edge",
				Relation: "move",
				MoveStep: moveStep[key],
				Impact:   stepImpact[m.FromNode],
				Label:    fmt.Sprintf("step %d", moveStep[key]),
			},
		})
	}
	sortElements(out)
	return out
}

// constraintEdges expose the relationships that shape the plan. Anti-affinity
// as a visible repulsion between pods is the clearest way to show why a node
// cannot be emptied; a PDB grouping shows which pods share a disruption
// budget.
func constraintEdges(snap *model.ClusterSnapshot, analysis *constraints.Analysis) []Element {
	var out []Element

	for _, pdb := range snap.PDBs {
		if pdb.Selector.IsEmpty() {
			continue
		}
		var members []string
		for _, pod := range snap.Pods {
			if pod.Namespace == pdb.Namespace && pod.NodeName != "" && pdb.Selector.Matches(pod.Labels) {
				members = append(members, pod.Key())
			}
		}
		// A chain rather than a clique: n-1 edges instead of n²/2, which keeps
		// a 90-pod deployment from swamping the canvas.
		for i := 1; i < len(members); i++ {
			out = append(out, Element{
				Group: "edges",
				Data: Data{
					ID:       fmt.Sprintf("pdb:%s:%d", pdb.Key(), i),
					Source:   podID(members[i-1]),
					Target:   podID(members[i]),
					Kind:     "edge",
					Relation: "pdb",
					Label:    pdb.Name,
					Blocked:  pdb.Blocks(),
				},
			})
		}
	}

	for _, pc := range analysis.Pods {
		for _, c := range pc.Of(constraints.KindPodAntiAffinity) {
			out = append(out, Element{
				Group: "edges",
				Data: Data{
					ID:       fmt.Sprintf("anti:%s:%s", pc.Key(), c.Subject),
					Source:   podID(pc.Key()),
					Target:   nodeID(pc.NodeName),
					Kind:     "edge",
					Relation: "anti-affinity",
					Label:    c.Subject,
				},
			})
		}
	}
	sortElements(out)
	return out
}

func nodeID(name string) string { return "node:" + name }
func podID(key string) string   { return "pod:" + key }

// sortElements keeps element order stable so the payload does not reshuffle
// between identical requests, which would make the UI relayout for no reason.
func sortElements(e []Element) {
	sort.SliceStable(e, func(i, j int) bool { return e[i].Data.ID < e[j].Data.ID })
}
