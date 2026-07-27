package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"

	"github.com/atedgimo/k8s-dencer/internal/auth"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// maxStepsPerRequest bounds one request. Not a safety rail — the Safety Guard
// owns those and enforces them per step inside the executor — just a limit on
// how much nonsense a single call can carry.
const maxStepsPerRequest = 200

// executeRequest is the body of POST /api/v1/plans/{id}/execute.
type executeRequest struct {
	// Steps are sequence numbers, in any order. Doc §5 requires an arbitrary
	// subset or range rather than all-or-nothing.
	Steps []int `json:"steps"`
	// DryRun runs the full guard chain and emits the same event stream without
	// cordoning or evicting anything.
	DryRun bool `json:"dryRun"`
}

// handleExecute accepts an execution request and queues it.
//
// This route does not execute anything. It authorizes, validates, and writes a
// row that the executor claims — so the component reachable from the network
// holds no eviction permission, and the component holding eviction permission
// is not reachable from the network.
//
// Authorization happens here, once. The executor then works under its own
// ServiceAccount, which is why a run outlives the token that authorized it: a
// 15-minute ID token can start a 40-minute consolidation. The requester's
// identity is recorded on the run so it stays attributable afterwards.
func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeError(w, http.StatusNotImplemented,
			"this deployment has no executor; install with executor.enabled=true")
		return
	}

	rec, err := s.record(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}

	var req executeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}

	steps, err := validateSteps(req.Steps, rec.Plan)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// One consolidation at a time. Two in flight would each be making
	// placement decisions the other invalidates.
	if active, err := s.runs.ActiveRun(r.Context()); err == nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":  fmt.Sprintf("run %s is already %s", active.ID, active.Status),
			"code":   "run_in_progress",
			"runId":  active.ID,
			"status": active.Status,
		})
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}

	// Identity is absent only when auth is disabled, which the chart's schema
	// refuses to combine with an enabled executor.
	actor := auth.Identity{Username: "auth-disabled", Source: auth.SourceAnonymous}
	if id, ok := auth.IdentityFrom(r.Context()); ok {
		actor = id
	}

	runID, err := s.runs.Enqueue(r.Context(), store.Run{
		PlanID: rec.Plan.ID, Steps: steps, DryRun: req.DryRun,
		Actor: actor.Username, ActorGroups: actor.Groups,
	})
	if err != nil {
		s.fail(w, err)
		return
	}

	s.log.Info("execution requested", "run", runID, "plan", rec.Plan.ID,
		"steps", steps, "dryRun", req.DryRun, "actor", actor.Username)

	// Tell connected clients immediately rather than making the UI wait for
	// its next poll to discover a run it just started.
	s.events.Publish(Event{Type: "run", Data: map[string]any{
		"runId": runID, "planId": rec.Plan.ID, "status": store.RunPending,
		"steps": steps, "dryRun": req.DryRun,
	}})

	writeJSON(w, http.StatusAccepted, map[string]any{
		"runId": runID, "planId": rec.Plan.ID, "steps": steps,
		"dryRun": req.DryRun, "status": store.RunPending,
	})
}

// validateSteps rejects a request before it reaches the queue.
//
// Rejecting Red here is a courtesy, not a control: the Safety Guard refuses it
// again inside the executor against live state, which is the check that
// actually cannot be bypassed. Failing early just means the operator learns
// now rather than after a run starts and immediately blocks.
func validateSteps(requested []int, plan *model.Plan) ([]int, error) {
	if len(requested) == 0 {
		return nil, errors.New("no steps requested")
	}
	if len(requested) > maxStepsPerRequest {
		return nil, fmt.Errorf("too many steps requested (%d); the maximum is %d",
			len(requested), maxStepsPerRequest)
	}

	bySeq := make(map[int]model.PlanStep, len(plan.Steps))
	for _, s := range plan.Steps {
		bySeq[s.SequenceNumber] = s
	}

	seen := map[int]bool{}
	out := make([]int, 0, len(requested))
	var red []int

	for _, seq := range requested {
		step, ok := bySeq[seq]
		if !ok {
			return nil, fmt.Errorf("plan %s has no step %d", plan.ID, seq)
		}
		if seen[seq] {
			continue // a duplicate is a client slip, not an error
		}
		seen[seq] = true
		if step.RequiresMaintenanceWindow() {
			red = append(red, seq)
		}
		out = append(out, seq)
	}

	if len(red) > 0 {
		return nil, fmt.Errorf(
			"step(s) %v are rated Red and may only run inside an approved maintenance window, "+
				"which this release does not implement", red)
	}

	// Ascending regardless of how they were sent. Steps are ordered for a
	// reason — later ones assume earlier ones freed capacity — and honouring a
	// caller's shuffled order would quietly make the plan wrong.
	sort.Ints(out)
	return out, nil
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeError(w, http.StatusNotImplemented, "this deployment has no executor")
		return
	}
	run, err := s.runs.RunByID(r.Context(), r.PathValue("runId"))
	if err != nil {
		s.failRun(w, err)
		return
	}
	events, err := s.runs.Events(r.Context(), run.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if events == nil {
		events = []store.RunEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "events": events})
}

func (s *Server) handleRunsForPlan(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"runs": []store.Run{}})
		return
	}
	rec, err := s.record(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	runs, err := s.runs.RunsForPlan(r.Context(), rec.Plan.ID, 50)
	if err != nil {
		s.fail(w, err)
		return
	}
	if runs == nil {
		runs = []store.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"planId": rec.Plan.ID, "runs": runs})
}

// handleActiveRun lets the UI discover an in-flight run on load, so refreshing
// the page during a consolidation does not lose sight of it.
func (s *Server) handleActiveRun(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"active": nil})
		return
	}
	run, err := s.runs.ActiveRun(r.Context())
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"active": nil})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": run})
}

func (s *Server) failRun(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such run")
		return
	}
	s.log.Error("run request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
