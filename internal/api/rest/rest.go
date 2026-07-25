// Package rest serves the k8s-dencer read API.
//
// Read-only by construction. Phase 1 has no executor, so there is no endpoint
// that mutates anything — not even a disabled one. The absence is the
// guarantee; a "not implemented" execute route would be an invitation.
package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/api/graph"
	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// Server serves the API over a plan store.
type Server struct {
	store   store.Store
	log     *slog.Logger
	version string
	events  *Broker
}

// New builds a server.
func New(s store.Store, log *slog.Logger, version string) *Server {
	return &Server{store: s, log: log, version: version, events: NewBroker(log)}
}

// Events exposes the broker so the poller can publish plan changes.
func (s *Server) Events() *Broker { return s.events }

// Routes registers every endpoint on mux.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)
	mux.HandleFunc("GET /api/v1/plans", s.handleListPlans)
	mux.HandleFunc("GET /api/v1/plans/latest", s.handleLatestPlan)
	mux.HandleFunc("GET /api/v1/plans/{id}", s.handlePlan)
	mux.HandleFunc("GET /api/v1/plans/{id}/steps/{seq}", s.handleStep)
	mux.HandleFunc("GET /api/v1/plans/{id}/graph", s.handleGraph)
	mux.HandleFunc("GET /api/v1/plans/{id}/snapshot", s.handleSnapshot)
	mux.HandleFunc("GET /api/v1/plans/{id}/constraints", s.handleConstraints)
	mux.HandleFunc("GET /api/v1/plans/{id}/constraints/{namespace}/{pod}", s.handlePodConstraints)
	mux.HandleFunc("GET /api/v1/events", s.events.ServeHTTP)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	latest, err := s.store.Latest(r.Context())
	resp := map[string]any{
		"version":  s.version,
		"readOnly": true,
	}
	if err == nil {
		resp["latestPlanId"] = latest.Plan.ID
		resp["planGeneratedAt"] = latest.Plan.GeneratedAt
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListPlans(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	plans, err := s.store.List(r.Context(), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	if plans == nil {
		plans = []store.Summary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": plans})
}

func (s *Server) handleLatestPlan(w http.ResponseWriter, r *http.Request) {
	rec, err := s.store.Latest(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, planResponse(rec))
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	rec, err := s.record(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, planResponse(rec))
}

// handleStep returns one step with the constraint detail for every pod it
// moves. Doc §5 requires steps to be independently addressable, and "why is
// step 7 Red?" should be answerable in one request rather than by the caller
// stitching together three.
func (s *Server) handleStep(w http.ResponseWriter, r *http.Request) {
	rec, err := s.record(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	seq, err := strconv.Atoi(r.PathValue("seq"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "step sequence must be a number")
		return
	}

	var step *model.PlanStep
	for i := range rec.Plan.Steps {
		if rec.Plan.Steps[i].SequenceNumber == seq {
			step = &rec.Plan.Steps[i]
			break
		}
	}
	if step == nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("plan %s has no step %d", rec.Plan.ID, seq))
		return
	}

	podConstraints := make([]constraints.PodConstraints, 0, len(step.Moves))
	if rec.Analysis != nil {
		for _, m := range step.Moves {
			if pc, ok := rec.Analysis.ForPod(m.Namespace + "/" + m.Pod); ok {
				podConstraints = append(podConstraints, pc)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"planId":      rec.Plan.ID,
		"step":        step,
		"constraints": podConstraints,
	})
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	rec, err := s.record(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, graph.Build(rec.Plan, rec.Snapshot, rec.Analysis))
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	rec, err := s.record(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec.Snapshot)
}

func (s *Server) handleConstraints(w http.ResponseWriter, r *http.Request) {
	rec, err := s.record(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec.Analysis)
}

func (s *Server) handlePodConstraints(w http.ResponseWriter, r *http.Request) {
	rec, err := s.record(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	key := r.PathValue("namespace") + "/" + r.PathValue("pod")
	if rec.Analysis == nil {
		writeError(w, http.StatusNotFound, "plan has no constraint analysis")
		return
	}
	pc, ok := rec.Analysis.ForPod(key)
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("pod %s is not in plan %s", key, rec.Plan.ID))
		return
	}
	writeJSON(w, http.StatusOK, pc)
}

// record resolves "latest" as an alias so callers can deep-link without
// knowing an ID.
func (s *Server) record(ctx context.Context, id string) (store.Record, error) {
	if id == "latest" || id == "" {
		return s.store.Latest(ctx)
	}
	return s.store.ByID(ctx, id)
}

func planResponse(rec store.Record) map[string]any {
	return map[string]any{
		"plan":     rec.Plan,
		"strategy": rec.Strategy,
		"storedAt": rec.StoredAt,
		"ratings":  rec.Plan.CountByRating(),
		"readOnly": true,
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no plan available yet")
		return
	}
	s.log.Error("request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	// Plans reflect live cluster state; a cached response would show an
	// operator a plan that no longer matches their cluster.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already written, so this can only be logged.
		_ = err
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// PollStore watches for a new plan and publishes it to connected clients.
//
// The ui-backend does not share memory with the planner — they are separate
// processes over a shared volume — so change detection is a cheap poll of the
// latest plan ID rather than an event feed. The ID is a content hash, which
// makes "has it changed?" a string comparison rather than a diff.
func (s *Server) PollStore(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	var lastID string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rec, err := s.store.Latest(ctx)
			if err != nil {
				continue
			}
			if rec.Plan.ID == lastID {
				continue
			}
			lastID = rec.Plan.ID
			s.events.Publish(Event{
				Type: "plan",
				Data: map[string]any{
					"planId":      rec.Plan.ID,
					"steps":       len(rec.Plan.Steps),
					"nodesBefore": rec.Plan.NodesBefore,
					"nodesAfter":  rec.Plan.NodesAfter,
					"ratings":     rec.Plan.CountByRating(),
				},
			})
		}
	}
}
