package rest

import (
	"net/http"
	"strconv"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/store"
)

// History: the cluster's timeline, assembled server-side into chart-ready
// series. Everything here was already being recorded — plan summaries, the
// reclamation ledger, run outcomes, and (new) the per-cycle samples — this
// endpoint is the first thing that reads them as a line through time rather
// than a moment.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	hours := 168
	if h := r.URL.Query().Get("hours"); h != "" {
		v, err := strconv.Atoi(h)
		if err != nil || v < 1 || v > 24*31 {
			writeError(w, http.StatusBadRequest, "hours must be between 1 and 744")
			return
		}
		hours = v
	}
	since := time.Now().Add(-time.Duration(hours) * time.Hour)

	samples := []store.Sample{}
	if ts, ok := s.store.(store.SampleStore); ok {
		got, err := ts.Samples(r.Context(), since)
		if err != nil {
			s.fail(w, err)
			return
		}
		samples = got
	}

	// Plan summaries: List is newest-first and window-agnostic; filter here.
	plansOut := []map[string]any{}
	if sums, err := s.store.List(r.Context(), 200); err == nil {
		for _, p := range sums {
			if p.GeneratedAt.After(since) {
				plansOut = append(plansOut, map[string]any{
					"id": p.ID, "generatedAt": p.GeneratedAt,
					"nodesBefore": p.NodesBefore, "nodesAfter": p.NodesAfter,
				})
			}
		}
	}

	recl := []store.Reclamation{}
	if tracker, ok := s.store.(store.ReclamationStore); ok {
		if got, err := tracker.Reclamations(r.Context(), 500); err == nil {
			for _, rr := range got {
				if rr.DrainedAt.After(since) || (rr.ResolvedAt != nil && rr.ResolvedAt.After(since)) {
					recl = append(recl, rr)
				}
			}
		}
	}

	runsOut := []map[string]any{}
	if s.runs != nil {
		if got, err := s.runs.RecentRuns(r.Context(), 200); err == nil {
			for _, run := range got {
				if run.FinishedAt == nil || run.FinishedAt.Before(since) {
					continue
				}
				// The audit ledger's whole point is "who authorised it" —
				// actor, plan and step count turn a marker into an entry.
				runsOut = append(runsOut, map[string]any{
					"id": run.ID, "status": run.Status, "mode": run.Mode,
					"dryRun": run.DryRun, "finishedAt": run.FinishedAt,
					"actor": run.Actor, "planId": run.PlanID, "steps": len(run.Steps),
				})
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"hours":        hours,
		"samples":      samples,
		"plans":        plansOut,
		"reclamations": recl,
		"runs":         runsOut,
	})
}
