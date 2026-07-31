package rest

import (
	"net/http"
	"sort"
)

// The resilience audit: the analyzer's never-evictable list, re-sorted into
// the question it also answers — can this cluster survive losing a node?
//
// A pod that cannot be evicted *voluntarily* is also a pod that handles an
// involuntary node loss badly: the zero-headroom PDB that blocks a drain is
// the same PDB that will be violated when the node dies, and the pod with no
// controller that eviction would delete permanently is deleted just as
// permanently by hardware. Consolidation and resilience are the same analysis
// read in opposite moods, which is why this endpoint is a projection rather
// than a feature.

type resilienceFinding struct {
	// Kind is the analyzer's constraint kind, e.g. PDB, Controller, Resources.
	Kind string `json:"kind"`
	Pod  string `json:"pod"`
	Node string `json:"node,omitempty"`
	// Explanation is the analyzer's canonical text, quoted not paraphrased.
	Explanation string `json:"explanation"`
}

func (s *Server) handleResilience(w http.ResponseWriter, r *http.Request) {
	rec, err := s.record(r.Context(), "latest")
	if err != nil {
		s.fail(w, err)
		return
	}

	findings := []resilienceFinding{}
	for _, pc := range rec.Analysis.Pods {
		if pc.Movable {
			continue
		}
		for _, c := range pc.Blockers() {
			findings = append(findings, resilienceFinding{
				Kind: string(c.Kind), Pod: pc.Key(), Node: pc.NodeName,
				Explanation: c.Explanation,
			})
		}
	}

	// Zero-headroom PDBs are a finding even when their pods are movable on
	// paper: zero headroom means any single disruption — voluntary or a node
	// simply dying — violates the budget.
	for _, pdb := range rec.Snapshot.PDBs {
		if pdb.Blocks() {
			findings = append(findings, resilienceFinding{
				Kind: "PDBZeroHeadroom", Pod: pdb.Key(),
				Explanation: "This PodDisruptionBudget allows zero disruptions right now. " +
					"A node loss — voluntary or not — violates it immediately.",
			})
		}
	}

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Kind != findings[j].Kind {
			return findings[i].Kind < findings[j].Kind
		}
		return findings[i].Pod < findings[j].Pod
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"takenAt":  rec.Snapshot.TakenAt,
		"planId":   rec.Plan.ID,
		"findings": findings,
		"pods":     len(rec.Analysis.Pods),
	})
}
