package rest

import (
	"net/http"
	"sort"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
)

// Preflight answers the question every team asks before a node-pool rotation
// and usually answers mid-upgrade, at 3am, via a wedged PDB: will every node
// actually drain, and if not, which pod is in the way and what would fix it?
//
// Nothing here is computed for the occasion. The constraint analyzer already
// answers "can this node drain" for the planner on every cycle; this endpoint
// prints that answer node by node instead of feeding it to the packer. It is
// the same engine asked a different question — which is why an upgrade
// preflight can be trusted to agree with the consolidation plan beside it.

type preflightNode struct {
	Node      string `json:"node"`
	Ready     bool   `json:"ready"`
	Cordoned  bool   `json:"cordoned"`
	Pods      int    `json:"pods"`
	Drainable bool   `json:"drainable"`
	// Blockers name the pod and quote the analyzer's canonical explanation.
	// Empty for drainable nodes rather than omitted, so clients can range
	// without nil checks.
	Blockers []preflightBlocker `json:"blockers"`
}

type preflightBlocker struct {
	Pod         string `json:"pod"`
	Kind        string `json:"kind"`
	Explanation string `json:"explanation"`
}

func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	rec, err := s.record(r.Context(), "latest")
	if err != nil {
		s.fail(w, err)
		return
	}
	snap, analysis := rec.Snapshot, rec.Analysis

	// One pass to group constraints by node. Analysis.ForNode scans all pods
	// per call, which is fine for the planner's occasional question and
	// quadratic for a report that asks it for every node.
	byNode := map[string][]constraints.PodConstraints{}
	for _, pc := range analysis.Pods {
		if pc.NodeName != "" {
			byNode[pc.NodeName] = append(byNode[pc.NodeName], pc)
		}
	}

	nodes := make([]preflightNode, 0, len(snap.Nodes))
	drainable := 0
	for _, n := range snap.Nodes {
		out := preflightNode{
			Node:     n.Name,
			Ready:    n.Ready,
			Cordoned: n.Unschedulable,
			Blockers: []preflightBlocker{},
		}
		for _, pc := range byNode[n.Name] {
			out.Pods++
			if !pc.Movable {
				for _, c := range pc.Blockers() {
					out.Blockers = append(out.Blockers, preflightBlocker{
						Pod: pc.Key(), Kind: string(c.Kind), Explanation: c.Explanation,
					})
				}
				continue
			}
			if len(pc.CandidateNodes) == 0 {
				out.Blockers = append(out.Blockers, preflightBlocker{
					Pod: pc.Key(), Kind: string(constraints.KindResources),
					Explanation: "No other node can currently accept this pod.",
				})
			}
		}
		out.Drainable = len(out.Blockers) == 0
		if out.Drainable {
			drainable++
		}
		nodes = append(nodes, out)
	}

	// Blocked nodes first — they are the reason anyone runs a preflight —
	// then by name for a stable report.
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Drainable != nodes[j].Drainable {
			return !nodes[i].Drainable
		}
		return nodes[i].Node < nodes[j].Node
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"takenAt":   snap.TakenAt,
		"planId":    rec.Plan.ID,
		"nodes":     nodes,
		"drainable": drainable,
		"total":     len(nodes),
	})
}
