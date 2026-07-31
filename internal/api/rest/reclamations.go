package rest

import (
	"net/http"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/reclaim"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// reclamationWindow bounds the summary. Long enough that a weekly consolidation
// habit still shows a median, short enough that a number from six months ago
// does not describe a cluster that has since changed shape.
const reclamationWindow = 30 * 24 * time.Hour

// handleReclamations reports what actually became of drained nodes.
//
// The endpoint exists because every other number the product publishes about
// reclamation is a plan-time prediction. This is the only one derived from
// observation, and the "awaiting" list is the part with teeth: those are nodes
// this product told someone to drain, which nothing has removed.
func (s *Server) handleReclamations(w http.ResponseWriter, r *http.Request) {
	tracker, ok := s.store.(store.ReclamationStore)
	if !ok {
		// A store without tracking is a configuration, not an error. Report
		// the absence explicitly rather than an empty list, which would read
		// as "nothing drained yet".
		writeJSON(w, http.StatusOK, map[string]any{"tracking": false})
		return
	}

	ctx := r.Context()
	pending, err := tracker.PendingReclamations(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	recent, err := tracker.Reclamations(ctx, 50)
	if err != nil {
		s.fail(w, err)
		return
	}
	stats, err := tracker.ReclamationSummary(ctx, time.Now().Add(-reclamationWindow))
	if err != nil {
		s.fail(w, err)
		return
	}

	// Whether anything here removes drained nodes — three-valued, honestly.
	// A recorded removal is proof (measured, the strongest evidence there
	// is). A visible autoscaler pod is a promise. Neither is NOT "no
	// reclaimer": managed control planes run theirs where no pod scan can
	// see them, which M23 demonstrated by watching GKE remove a node while
	// nothing in the cluster admitted to being an autoscaler.
	everStats, err := tracker.ReclamationSummary(ctx, time.Time{})
	if err != nil {
		s.fail(w, err)
		return
	}
	detected := ""
	if rec, err := s.store.Latest(ctx); err == nil {
		detected, _ = reclaim.DetectReclaimer(rec.Snapshot)
	}

	// Seconds rather than the Go duration's nanoseconds: every consumer here
	// is JavaScript or jq, and 180000000000 is not a number anyone reads.
	writeJSON(w, http.StatusOK, map[string]any{
		"tracking": true,
		"awaiting": pending,
		"recent":   recent,
		"reclaimer": map[string]any{
			"observedWorking": everStats.Reclaimed > 0,
			"detected":        detected,
		},
		"stats": map[string]any{
			"awaiting":                 stats.Awaiting,
			"reclaimed":                stats.Reclaimed,
			"returned":                 stats.Returned,
			"medianReclamationSeconds": stats.MedianTime.Seconds(),
			"windowDays":               int(reclamationWindow.Hours() / 24),
			// The ledger: measured, not estimated — summed from capacity
			// captured at drain time. See store.ReclamationStats.
			"reclaimedCpuMilli": stats.ReclaimedCPUMilli,
			"reclaimedMemBytes": stats.ReclaimedMemBytes,
			"uncountedNodes":    stats.UncountedNodes,
		},
	})
}
