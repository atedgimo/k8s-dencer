package rest

import (
	"net/http"

	"github.com/atedgimo/k8s-dencer/internal/recommend"
)

// Recommendations: what is missing, with fixes. audit reports what cannot
// survive; this reports what to change — the PDB nobody wrote, the second
// replica, the requests the scheduler is blind without. Same freshness rule
// as the other reports: derived from the stored snapshot per request.
func (s *Server) handleRecommendations(w http.ResponseWriter, r *http.Request) {
	rec, err := s.record(r.Context(), "latest")
	if err != nil {
		s.fail(w, err)
		return
	}
	// The queue, not just the advice: the plan's own blocking rules lead,
	// each carrying the steps it holds back, computed against the same
	// record so the numbers cannot describe a different moment than the
	// findings.
	recs := recommend.Queue(rec.Plan, rec.Snapshot)
	writeJSON(w, http.StatusOK, map[string]any{
		"takenAt":         rec.Snapshot.TakenAt,
		"recommendations": recs,
	})
}
