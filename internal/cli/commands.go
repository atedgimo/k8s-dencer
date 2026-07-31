package cli

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// PlanEnvelope is what GET /api/v1/plans/{id} returns.
type PlanEnvelope struct {
	Plan     *model.Plan    `json:"plan"`
	Strategy string         `json:"strategy"`
	StoredAt time.Time      `json:"storedAt"`
	Ratings  map[string]int `json:"ratings"`
	// What the reclaimable count is worth — the summed allocatable of every
	// node the plan drains. Server-computed, so the CLI and the UI cannot
	// disagree about it.
	CPUReclaimableMilli int64 `json:"cpuReclaimableMilli"`
	MemReclaimableBytes int64 `json:"memReclaimableBytes"`
	// How the reclaimable nodes are bought, when the platform says.
	ReclaimableByCapacity map[string]int `json:"reclaimableByCapacity,omitempty"`
}

type RunEnvelope struct {
	Run    *store.Run       `json:"run"`
	Events []store.RunEvent `json:"events"`
}

type StepEnvelope struct {
	PlanID      string                       `json:"planId"`
	Step        model.PlanStep               `json:"step"`
	Constraints []constraints.PodConstraints `json:"constraints"`
}

// Plan fetches a plan. id may be "latest".
func (c *Client) Plan(ctx context.Context, id string) (*PlanEnvelope, error) {
	var out PlanEnvelope
	if err := c.get(ctx, "/api/v1/plans/"+id, &out); err != nil {
		return nil, err
	}
	if out.Plan == nil {
		return nil, fmt.Errorf("no plan available yet")
	}
	return &out, nil
}

// Step fetches one step with the constraints of the pods it moves.
func (c *Client) Step(ctx context.Context, planID string, seq int) (*StepEnvelope, error) {
	var out StepEnvelope
	err := c.get(ctx, fmt.Sprintf("/api/v1/plans/%s/steps/%d", planID, seq), &out)
	return &out, err
}

// PodConstraints explains why one pod can or cannot move.
func (c *Client) PodConstraints(ctx context.Context, planID, ns, pod string) (*constraints.PodConstraints, error) {
	var out constraints.PodConstraints
	err := c.get(ctx, fmt.Sprintf("/api/v1/plans/%s/constraints/%s/%s", planID, ns, pod), &out)
	return &out, err
}

// Execute queues steps and returns the run id.
func (c *Client) Execute(ctx context.Context, planID string, steps []int, dryRun bool) (string, error) {
	var out struct {
		RunID string `json:"runId"`
	}
	body := map[string]any{"steps": steps, "dryRun": dryRun}
	if err := c.do(ctx, "POST", "/api/v1/plans/"+planID+"/execute", body, &out); err != nil {
		return "", err
	}
	return out.RunID, nil
}

// Run fetches a run and its audit trail.
func (c *Client) Run(ctx context.Context, id string) (*RunEnvelope, error) {
	var out RunEnvelope
	if err := c.get(ctx, "/api/v1/runs/"+id, &out); err != nil {
		return nil, err
	}
	if out.Run == nil {
		return nil, fmt.Errorf("run %s not found", id)
	}
	return &out, nil
}

// ActiveRun returns the run in flight, or nil.
func (c *Client) ActiveRun(ctx context.Context) (*store.Run, error) {
	var out struct {
		Active *store.Run `json:"active"`
	}
	err := c.get(ctx, "/api/v1/runs", &out)
	return out.Active, err
}

// Wait polls a run until it reaches a terminal state, reporting events as they
// appear.
//
// Polling rather than the SSE stream: a run is minutes long and a poll is one
// request every two seconds, whereas a streaming client has to handle
// reconnection, and getting that subtly wrong in a tool people watch a drain
// through is a poor trade for the latency it saves.
func (c *Client) Wait(ctx context.Context, runID string, onEvent func(store.RunEvent)) (*store.Run, error) {
	seen := 0
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		env, err := c.Run(ctx, runID)
		if err != nil {
			return nil, err
		}
		for _, ev := range env.Events[min(seen, len(env.Events)):] {
			if onEvent != nil {
				onEvent(ev)
			}
		}
		seen = len(env.Events)

		switch env.Run.Status {
		case store.RunSucceeded, store.RunFailed, store.RunBlocked:
			return env.Run, nil
		}

		select {
		case <-ctx.Done():
			// The run keeps going server-side; say so rather than implying
			// Ctrl-C stopped a drain that is still evicting pods.
			return nil, fmt.Errorf("stopped watching run %s; it is still running in the cluster", runID)
		case <-ticker.C:
		}
	}
}

// ParseSteps accepts "1,3,5" and "1-4" and any mixture.
func ParseSteps(spec string) ([]int, error) {
	var out []int
	seen := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		if !isRange {
			n, err := strconv.Atoi(lo)
			if err != nil {
				return nil, fmt.Errorf("%q is not a step number", part)
			}
			if !seen[n] {
				seen[n], out = true, append(out, n)
			}
			continue
		}
		a, err1 := strconv.Atoi(strings.TrimSpace(lo))
		b, err2 := strconv.Atoi(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%q is not a step range", part)
		}
		if a > b {
			return nil, fmt.Errorf("range %q counts backwards", part)
		}
		for n := a; n <= b; n++ {
			if !seen[n] {
				seen[n], out = true, append(out, n)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no steps selected")
	}
	sort.Ints(out)
	return out, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ReclamationsEnvelope is what GET /api/v1/reclamations returns.
type ReclamationsEnvelope struct {
	Tracking bool                `json:"tracking"`
	Awaiting []store.Reclamation `json:"awaiting"`
	Recent   []store.Reclamation `json:"recent"`
	Stats    ReclamationStats    `json:"stats"`
}

// ReclamationStats mirrors the API's summary. Seconds, not a Go duration: the
// wire format is what jq and the browser see.
type ReclamationStats struct {
	Awaiting                 int     `json:"awaiting"`
	Reclaimed                int     `json:"reclaimed"`
	Returned                 int     `json:"returned"`
	MedianReclamationSeconds float64 `json:"medianReclamationSeconds"`
	WindowDays               int     `json:"windowDays"`
	// The ledger: capacity actually returned, measured from drain-time records.
	ReclaimedCPUMilli int64 `json:"reclaimedCpuMilli"`
	ReclaimedMemBytes int64 `json:"reclaimedMemBytes"`
	UncountedNodes    int   `json:"uncountedNodes"`
}

// Reclamations reports what became of drained nodes.
func (c *Client) Reclamations(ctx context.Context) (*ReclamationsEnvelope, error) {
	var out ReclamationsEnvelope
	if err := c.get(ctx, "/api/v1/reclamations", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Converge queues a closed-loop run and returns the run id. The caller has
// already confirmed the policy; this just carries it.
func (c *Client) Converge(ctx context.Context, maxNodes int, maxImpact string, dryRun bool) (string, error) {
	var out struct {
		RunID string `json:"runId"`
	}
	body := map[string]any{"maxNodes": maxNodes, "maxImpact": maxImpact, "dryRun": dryRun}
	if err := c.do(ctx, "POST", "/api/v1/converge", body, &out); err != nil {
		return "", err
	}
	return out.RunID, nil
}

// PreflightEnvelope is what GET /api/v1/preflight returns.
type PreflightEnvelope struct {
	TakenAt   time.Time       `json:"takenAt"`
	PlanID    string          `json:"planId"`
	Nodes     []PreflightNode `json:"nodes"`
	Drainable int             `json:"drainable"`
	Total     int             `json:"total"`
}

type PreflightNode struct {
	Node      string             `json:"node"`
	Ready     bool               `json:"ready"`
	Cordoned  bool               `json:"cordoned"`
	Pods      int                `json:"pods"`
	Drainable bool               `json:"drainable"`
	Blockers  []PreflightBlocker `json:"blockers"`
}

type PreflightBlocker struct {
	Pod         string `json:"pod"`
	Kind        string `json:"kind"`
	Explanation string `json:"explanation"`
}

// Preflight reports per-node drainability — the upgrade question.
func (c *Client) Preflight(ctx context.Context) (*PreflightEnvelope, error) {
	var out PreflightEnvelope
	if err := c.get(ctx, "/api/v1/preflight", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResilienceEnvelope is what GET /api/v1/resilience returns.
type ResilienceEnvelope struct {
	TakenAt  time.Time           `json:"takenAt"`
	PlanID   string              `json:"planId"`
	Findings []ResilienceFinding `json:"findings"`
	Pods     int                 `json:"pods"`
}

type ResilienceFinding struct {
	Kind        string `json:"kind"`
	Pod         string `json:"pod"`
	Node        string `json:"node,omitempty"`
	Explanation string `json:"explanation"`
}

// Resilience reports what cannot survive a node loss, and why.
func (c *Client) Resilience(ctx context.Context) (*ResilienceEnvelope, error) {
	var out ResilienceEnvelope
	if err := c.get(ctx, "/api/v1/resilience", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Drain queues a guarded drain of one named node.
func (c *Client) Drain(ctx context.Context, node string, dryRun bool) (string, error) {
	var out struct {
		RunID string `json:"runId"`
	}
	if err := c.do(ctx, "POST", "/api/v1/drain", map[string]any{"node": node, "dryRun": dryRun}, &out); err != nil {
		return "", err
	}
	return out.RunID, nil
}
