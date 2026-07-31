package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// What-if: the planner against a cluster that has lost something.
//
// "Can I lose zone B?" is normally answered with a spreadsheet or a shrug.
// Here it is answered by the same analyzer and packer that plan real
// consolidations, run against the latest snapshot minus the removed nodes:
// every pod that lived on them is re-homed by the constraint engine, and the
// ones with nowhere legal to go are named, with the analyzer's explanations.
//
// Read-only by construction — the simulation mutates a copy of a stored
// snapshot and touches nothing else. It answers with the same honesty rules
// as everything here: "fits" means the constraint engine found every
// displaced pod a legal home in the simulated cluster, not a promise about
// what the scheduler will do on the day.

// whatifRequest is the body of POST /api/v1/whatif.
type whatifRequest struct {
	// RemoveNodes simulates losing these nodes.
	RemoveNodes []string `json:"removeNodes,omitempty"`
	// RemoveZone simulates losing every node in a topology zone.
	RemoveZone string `json:"removeZone,omitempty"`
}

type whatifHomeless struct {
	Pod string `json:"pod"`
	// Why quotes the analyzer's blocking constraints for this pod in the
	// simulated cluster.
	Why []string `json:"why"`
}

func (s *Server) handleWhatif(w http.ResponseWriter, r *http.Request) {
	rec, err := s.record(r.Context(), "latest")
	if err != nil {
		s.fail(w, err)
		return
	}

	var req whatifRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}
	if len(req.RemoveNodes) == 0 && req.RemoveZone == "" {
		writeError(w, http.StatusBadRequest, "remove something: removeNodes or removeZone")
		return
	}

	remove := map[string]bool{}
	for _, n := range req.RemoveNodes {
		remove[n] = true
	}
	for _, n := range rec.Snapshot.Nodes {
		if req.RemoveZone != "" && n.Zone() == req.RemoveZone {
			remove[n.Name] = true
		}
	}

	// Validate before simulating, so a typo answers "no such node" rather
	// than a confident report about a cluster that was never asked about.
	known := map[string]bool{}
	for _, n := range rec.Snapshot.Nodes {
		known[n.Name] = true
	}
	for name := range remove {
		if !known[name] {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("node %q is not in the latest snapshot", name))
			return
		}
	}
	if len(remove) == len(rec.Snapshot.Nodes) {
		writeError(w, http.StatusBadRequest, "that removes every node; nothing is left to simulate")
		return
	}

	// The simulated cluster: surviving nodes, with the dead nodes' pods made
	// homeless (NodeName cleared) so the analyzer treats them as needing a
	// placement rather than as gone. DaemonSet pods on removed nodes are
	// dropped — their controller would not reschedule them elsewhere.
	sim := &model.ClusterSnapshot{
		TakenAt: time.Now().UTC(),
		PDBs:    rec.Snapshot.PDBs,
	}
	for _, n := range rec.Snapshot.Nodes {
		if !remove[n.Name] {
			sim.Nodes = append(sim.Nodes, n)
		}
	}
	displaced := []model.Pod{}
	for _, p := range rec.Snapshot.Pods {
		switch {
		case !remove[p.NodeName]:
			sim.Pods = append(sim.Pods, p)
		case p.IsMovable():
			cp := p
			cp.NodeName = ""
			sim.Pods = append(sim.Pods, cp)
			displaced = append(displaced, p)
		}
		// Immovable pods on removed nodes fall away with their node.
	}

	analysis := constraints.Analyze(sim)

	homeless := []whatifHomeless{}
	for _, p := range displaced {
		pc, ok := analysis.ForPod(p.Key())
		if !ok {
			continue
		}
		if len(pc.CandidateNodes) == 0 {
			why := []string{}
			for _, c := range pc.Blockers() {
				why = append(why, c.Explanation)
			}
			if len(why) == 0 {
				why = append(why, "No surviving node has room for this pod's requests.")
			}
			homeless = append(homeless, whatifHomeless{Pod: p.Key(), Why: why})
		}
	}
	sort.Slice(homeless, func(i, j int) bool { return homeless[i].Pod < homeless[j].Pod })

	removed := make([]string, 0, len(remove))
	for name := range remove {
		removed = append(removed, name)
	}
	sort.Strings(removed)

	writeJSON(w, http.StatusOK, map[string]any{
		"removed":   removed,
		"displaced": len(displaced),
		"fits":      len(homeless) == 0,
		"homeless":  homeless,
		"basedOn":   rec.Plan.ID,
		"takenAt":   rec.Snapshot.TakenAt,
	})
}
