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
// It deliberately does NOT judge Red. It used to, as a courtesy — but once
// maintenance windows existed that courtesy became a false negative, refusing
// steps a window had legitimately authorised. Whether a Red step may run
// depends on live window state evaluated against the step's own node, and the
// Safety Guard inside the executor is the only place that knows both.
//
// So a Red step is queued and then either runs or comes back Blocked with the
// window's own explanation. One authority, checked against live state, exactly
// as doc §9 requires — rather than two that can disagree.
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

	for _, seq := range requested {
		if _, ok := bySeq[seq]; !ok {
			return nil, fmt.Errorf("plan %s has no step %d", plan.ID, seq)
		}
		if seen[seq] {
			continue // a duplicate is a client slip, not an error
		}
		seen[seq] = true
		out = append(out, seq)
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
	// A JSON list is never null — the same doctrine planResponse states for
	// steps, missed here. A run fetched before its first event (Pending, or
	// claimed moments ago) has zero events, the nil slice marshalled as
	// null, and the UI crashed on null.length. The window was always there;
	// starting a run from the UI and following it immediately is what made
	// it common enough to hit.
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
// handleActiveRun answers "is anything running, and if not, what happened
// last".
//
// The second half matters: "no run in flight" is a true but useless answer to
// someone who just watched a drain get halted by the Safety Guard and wants to
// know why. latest is nil only when nothing has ever run.
func (s *Server) handleActiveRun(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeJSON(w, http.StatusOK, map[string]any{"active": nil, "latest": nil})
		return
	}
	body := map[string]any{"active": nil, "latest": nil}

	run, err := s.runs.ActiveRun(r.Context())
	switch {
	case err == nil:
		body["active"] = run
	case !errors.Is(err, store.ErrNotFound):
		s.fail(w, err)
		return
	}

	if recent, err := s.runs.RecentRuns(r.Context(), 1); err == nil && len(recent) > 0 {
		body["latest"] = recent[0]
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) failRun(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such run")
		return
	}
	if store.IsUnavailable(err) {
		s.log.Error("plan store unavailable", "error", err)
		writeError(w, http.StatusServiceUnavailable,
			"plan store unavailable — the run may still be executing; the executor owns it, not this request")
		return
	}
	s.log.Error("run request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// convergeRequest is the body of POST /api/v1/converge.
//
// It is an envelope, not a step list. A steps request approves concrete
// nodes; this approves a policy — "keep consolidating inside these bounds" —
// and both bounds are required, because a defaulted consent is not consent.
type convergeRequest struct {
	MaxNodes  int    `json:"maxNodes"`
	MaxImpact string `json:"maxImpact"`
	DryRun    bool   `json:"dryRun"`
}

// handleConverge queues a closed-loop run. Same shape as handleExecute — this
// route authorizes and writes a row; only the unreachable executor drains.
func (s *Server) handleConverge(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeError(w, http.StatusNotImplemented,
			"this deployment has no executor; install with executor.enabled=true")
		return
	}

	var req convergeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}
	if req.MaxNodes < 1 || req.MaxNodes > maxStepsPerRequest {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"maxNodes must be between 1 and %d; an unbounded loop is not a grantable consent", maxStepsPerRequest))
		return
	}
	ceiling := model.ImpactRating(req.MaxImpact)
	if ceiling != model.ImpactGreen && ceiling != model.ImpactYellow {
		writeError(w, http.StatusBadRequest,
			"maxImpact must be Green or Yellow; Red always requires a maintenance window and cannot be pre-consented here")
		return
	}

	// One consolidation at a time, converge included.
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

	actor := auth.Identity{Username: "auth-disabled", Source: auth.SourceAnonymous}
	if id, ok := auth.IdentityFrom(r.Context()); ok {
		actor = id
	}

	// The latest plan ID is recorded for audit context — "what was on the
	// screen when this policy was approved" — not as an execution input; the
	// whole point of converge is that it re-plans for itself.
	planID := ""
	if rec, err := s.store.Latest(r.Context()); err == nil {
		planID = rec.Plan.ID
	}

	runID, err := s.runs.Enqueue(r.Context(), store.Run{
		PlanID: planID,
		Mode:   store.RunModeConverge,
		Envelope: &store.Envelope{
			MaxNodes:  req.MaxNodes,
			MaxImpact: ceiling,
		},
		DryRun: req.DryRun,
		Actor:  actor.Username, ActorGroups: actor.Groups,
	})
	if err != nil {
		s.fail(w, err)
		return
	}

	s.log.Info("converge requested", "run", runID, "maxNodes", req.MaxNodes,
		"maxImpact", req.MaxImpact, "dryRun", req.DryRun, "actor", actor.Username)

	s.events.Publish(Event{Type: "run", Data: map[string]any{
		"runId": runID, "planId": planID, "status": store.RunPending,
		"mode": store.RunModeConverge, "dryRun": req.DryRun,
	}})

	writeJSON(w, http.StatusAccepted, map[string]any{
		"runId": runID, "planId": planID, "mode": store.RunModeConverge,
		"maxNodes": req.MaxNodes, "maxImpact": req.MaxImpact,
		"dryRun": req.DryRun, "status": store.RunPending,
	})
}

// drainRequest is the body of POST /api/v1/drain.
type drainRequest struct {
	Node   string `json:"node"`
	DryRun bool   `json:"dryRun"`
}

// handleDrain queues a guarded drain of one named node — kubectl drain with
// the rails. Same shape as every execution route: authorize, validate, write
// a row; only the unreachable executor touches the cluster.
func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeError(w, http.StatusNotImplemented,
			"this deployment has no executor; install with executor.enabled=true")
		return
	}

	var req drainRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "request body is not valid JSON")
		return
	}
	if req.Node == "" {
		writeError(w, http.StatusBadRequest, "which node? a drain names exactly one")
		return
	}

	// Validated against the latest snapshot so a typo fails here with a clear
	// message, not in the executor with an audit row. The executor re-checks
	// against live state regardless; this is courtesy, not the guard.
	if rec, err := s.store.Latest(r.Context()); err == nil {
		known := false
		for _, n := range rec.Snapshot.Nodes {
			if n.Name == req.Node {
				known = true
				break
			}
		}
		if !known {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("node %q is not in the latest snapshot", req.Node))
			return
		}
	}

	if active, err := s.runs.ActiveRun(r.Context()); err == nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": fmt.Sprintf("run %s is already %s", active.ID, active.Status),
			"code":  "run_in_progress", "runId": active.ID, "status": active.Status,
		})
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err)
		return
	}

	actor := auth.Identity{Username: "auth-disabled", Source: auth.SourceAnonymous}
	if id, ok := auth.IdentityFrom(r.Context()); ok {
		actor = id
	}

	planID := ""
	if rec, err := s.store.Latest(r.Context()); err == nil {
		planID = rec.Plan.ID
	}

	runID, err := s.runs.Enqueue(r.Context(), store.Run{
		PlanID: planID, Mode: store.RunModeDrain, Node: req.Node, DryRun: req.DryRun,
		Actor: actor.Username, ActorGroups: actor.Groups,
	})
	if err != nil {
		s.fail(w, err)
		return
	}

	s.log.Info("drain requested", "run", runID, "node", req.Node,
		"dryRun", req.DryRun, "actor", actor.Username)
	s.events.Publish(Event{Type: "run", Data: map[string]any{
		"runId": runID, "status": store.RunPending, "mode": store.RunModeDrain,
		"node": req.Node, "dryRun": req.DryRun,
	}})
	writeJSON(w, http.StatusAccepted, map[string]any{
		"runId": runID, "mode": store.RunModeDrain, "node": req.Node,
		"dryRun": req.DryRun, "status": store.RunPending,
	})
}

// handleStopRun asks a run to end at its next safe point.
//
// One control, not two. The design called for an Abort and a "Pause after
// this step", and building both would have implied a distinction that does
// not exist: a pod already evicted cannot be un-evicted, so there is no
// aborting mid-step — only declining to start the next one. Offering an
// "Abort" that quietly behaves like a pause would be the product promising an
// undo it does not have, which is the one thing it must never do.
//
// Guarded by ExecuteConsolidations, the same grant that starts a run.
// Stopping something you were permitted to start is not a new power, and
// requiring a second one would mean an operator could begin a drain they
// could not call off.
func (s *Server) handleStopRun(w http.ResponseWriter, r *http.Request) {
	if s.runs == nil {
		writeError(w, http.StatusNotImplemented, "this deployment has no executor")
		return
	}
	run, err := s.runs.RunByID(r.Context(), r.PathValue("runId"))
	if err != nil {
		s.failRun(w, err)
		return
	}

	actor := "auth-disabled"
	if id, ok := auth.IdentityFrom(r.Context()); ok {
		actor = id.Username
	}
	if err := s.runs.RequestStop(r.Context(), run.ID, actor); err != nil {
		s.fail(w, err)
		return
	}
	s.log.Info("stop requested", "run", run.ID, "actor", actor, "status", run.Status)

	// Deliberately not an error when the run has already finished: that is a
	// slow click, not a mistake, and scolding somebody for it in a moment
	// they are already anxious would be poor manners.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"runId":  run.ID,
		"status": run.Status,
		"note": "The run will stop before its next step. Evictions already in " +
			"flight complete — a pod that has been evicted cannot be recalled.",
	})
}
