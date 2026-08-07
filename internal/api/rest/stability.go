package rest

import (
	"net/http"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/store"
)

// stabilityWindow is how far back "usually" reaches. Matched to the sampler's
// retention: asking for more would silently answer from less.
const stabilityWindow = 14 * 24 * time.Hour

// Node stability: what a node usually does, not just what it is doing now.
//
// Every other surface in this product describes a single instant and then asks
// for a decision about a system that varies. A node at 30% on a Tuesday
// afternoon and a node that peaks at 92% every night look identical on the
// Cluster page, and draining them are very different decisions.
//
// Aggregated in SQL and returned as one row per node: the browser needs a
// sentence, not a fortnight of points for every node in the fleet.
func (s *Server) handleStability(w http.ResponseWriter, r *http.Request) {
	ts, ok := s.store.(store.SampleStore)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	rows, err := ts.NodeStabilitySince(r.Context(), time.Now().Add(-stabilityWindow))
	if err != nil {
		s.fail(w, err)
		return
	}
	// Empty is a real answer — no usage source configured, or a cluster that
	// started ten minutes ago — and it is reported as empty rather than as
	// zeroes, on the same principle as the rest of the ledger.
	writeJSON(w, http.StatusOK, map[string]any{
		"available":  true,
		"windowDays": int(stabilityWindow.Hours() / 24),
		"nodes":      rows,
	})
}
