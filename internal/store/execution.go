package store

import (
	"context"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/model"
)

// RunStatus is the lifecycle of an execution request.
type RunStatus string

const (
	// RunPending has been accepted and authorized but not yet claimed.
	RunPending RunStatus = "Pending"
	// RunRunning has been claimed by an executor.
	RunRunning RunStatus = "Running"
	// RunSucceeded completed every requested step.
	RunSucceeded RunStatus = "Succeeded"
	// RunBlocked stopped because the Safety Guard refused a step. Not a
	// failure: the rails worked. Kept distinct from Failed so an operator can
	// tell "we protected you" from "something broke".
	RunBlocked RunStatus = "Blocked"
	// RunStopped ended early because a human asked it to. Distinct from
	// Succeeded (it did not finish what it was approved for) and from Failed
	// (nothing went wrong) — the ledger has to be able to tell the difference
	// between a run that ran out of work and one that was called off.
	RunStopped RunStatus = "Stopped"
	// RunFailed stopped on an error.
	RunFailed RunStatus = "Failed"
)

// Terminal reports whether a run has finished.
func (s RunStatus) Terminal() bool {
	return s == RunSucceeded || s == RunBlocked || s == RunFailed
}

// Run is one execution request covering a subset of a plan's steps.
//
// Bound to a specific plan ID. The planner keeps producing new plans while a
// run is in flight, and a run must keep executing the steps that were
// authorized rather than silently following whatever the latest plan says.
type Run struct {
	ID     string    `json:"id"`
	PlanID string    `json:"planId"`
	Steps  []int     `json:"steps"`
	DryRun bool      `json:"dryRun"`
	Status RunStatus `json:"status"`

	// StopRequested is set when a human asked this run to end early. The
	// executor honours it at step boundaries, which is the only place it
	// honestly can: a pod already evicted cannot be un-evicted, so "abort"
	// and "pause" are the same capability wearing different urgency, and
	// offering both would imply an undo that does not exist.
	StopRequested bool `json:"stopRequested,omitempty"`
	// StopRequestedBy records who asked, so the audit trail says who called
	// it off as well as who started it.
	StopRequestedBy string `json:"stopRequestedBy,omitempty"`

	// Mode selects what the executor does with this run. Empty means steps:
	// perform the listed steps of the referenced plan, the shape the product
	// has always had. "converge" means closed-loop: re-plan from observed
	// state after every drained node, inside the Envelope, until no
	// worthwhile step remains.
	Mode string `json:"mode,omitempty"`

	// Node is the target of a drain run — one named node, guarded. Empty for
	// every other mode.
	Node string `json:"node,omitempty"`

	// Envelope is the operator's consent, for converge runs. A steps run
	// approves a concrete list of nodes; a converge run approves a *policy*,
	// and the policy's bounds are recorded on the run so the audit trail
	// shows exactly what was consented to.
	Envelope *Envelope `json:"envelope,omitempty"`

	// Actor is the authenticated identity that requested the run, captured at
	// enqueue. Authorization happens once, up front, so a run outlives the
	// token that authorized it — a 15-minute ID token can start a 40-minute
	// consolidation. This field is how it stays attributable afterwards.
	Actor       string   `json:"actor"`
	ActorGroups []string `json:"actorGroups,omitempty"`

	RequestedAt time.Time  `json:"requestedAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`

	// Worker identifies the executor that claimed this run.
	Worker string `json:"worker,omitempty"`

	// Summary is the closing human-readable outcome.
	Summary string `json:"summary,omitempty"`
}

// RunModeConverge marks a closed-loop run. The zero value of Run.Mode is the
// classic steps run; a named constant for it would suggest other values are
// equally ordinary, and they are not.
const RunModeConverge = "converge"

// RunModeDrain drains one operator-named node through the full guard chain —
// kubectl drain with the rails: PDB pre-checks per eviction, readiness
// verification, audit trail, abort-means-uncordon.
const RunModeDrain = "drain"

// Envelope bounds a converge run. Both fields are the operator's explicit
// choice — there are no defaults here, because a defaulted consent is not
// consent.
type Envelope struct {
	// MaxNodes is the most nodes this run may drain, however inviting the
	// re-planning gets. Doubles as the loop's hard round bound.
	MaxNodes int `json:"maxNodes"`
	// MaxImpact is the highest impact rating the run may execute without
	// coming back for a human. Green or Yellow; Red always needs a window
	// regardless, enforced by the Safety Guard.
	MaxImpact model.ImpactRating `json:"maxImpact"`
}

// EventLevel separates progress from refusals and errors.
type EventLevel string

const (
	EventInfo    EventLevel = "Info"
	EventBlocked EventLevel = "Blocked"
	EventError   EventLevel = "Error"
)

// RunEvent is one entry in the audit trail.
//
// Doc §9 requires a full audit log of every action taken, tied to the plan
// version and the specific step that authorized it. Every field below exists
// to answer a question someone will ask after an incident: what happened, to
// what, under whose authority, and which rule stopped it.
type RunEvent struct {
	RunID    string     `json:"runId"`
	Sequence int        `json:"sequence"`
	At       time.Time  `json:"at"`
	Level    EventLevel `json:"level"`

	// Step is the plan step this event belongs to, 0 for run-level events.
	Step int `json:"step,omitempty"`
	// Node and Pod name what was acted on, when applicable.
	Node string `json:"node,omitempty"`
	Pod  string `json:"pod,omitempty"`

	// Action is a stable identifier: Cordon, Evict, Uncordon, Verify, Claim...
	Action string `json:"action"`
	// Rule names the Safety Guard rail that refused, for Blocked events.
	Rule string `json:"rule,omitempty"`

	Message string `json:"message"`
}

// ExecutionStore persists execution requests and their audit trail.
//
// Separate from Store because the writers differ: ui-backend enqueues, the
// executor claims and appends. Keeping them apart makes it obvious in a
// deployment which component needs which half.
type ExecutionStore interface {
	// Enqueue records an authorized request and returns its run ID.
	Enqueue(ctx context.Context, run Run) (string, error)

	// Claim atomically takes the oldest pending run for worker, or returns
	// ErrNotFound when there is nothing to do.
	//
	// Atomicity is the whole contract: two executors must never take the same
	// run, or a node gets drained twice concurrently.
	Claim(ctx context.Context, worker string) (Run, error)

	// AppendEvent adds one audit entry. The sequence number is assigned by the
	// store so ordering survives concurrent writers.
	AppendEvent(ctx context.Context, ev RunEvent) error

	// Finish closes a run with a terminal status and summary.
	Finish(ctx context.Context, runID string, status RunStatus, summary string) error

	// RunByID returns one run.
	RunByID(ctx context.Context, runID string) (Run, error)

	// RequestStop asks a run to end at its next safe point. Idempotent, and a
	// no-op on a run that has already finished.
	//
	// A request rather than a kill: an eviction in flight cannot be recalled,
	// so the only honest thing to offer is "stop before the next one". See
	// Run.StopRequested.
	RequestStop(ctx context.Context, runID, by string) error

	// RecentRuns returns the newest runs regardless of plan, newest first —
	// the History view's markers.
	RecentRuns(ctx context.Context, limit int) ([]Run, error)

	// Events returns a run's audit trail in order.
	Events(ctx context.Context, runID string) ([]RunEvent, error)

	// RunsForPlan lists runs against a plan, newest first.
	RunsForPlan(ctx context.Context, planID string, limit int) ([]Run, error)

	// ActiveRun returns the run currently pending or running, if any.
	//
	// One at a time is a deliberate constraint, not a limitation of the
	// implementation: concurrent consolidations would each be making placement
	// decisions the other invalidates.
	ActiveRun(ctx context.Context) (Run, error)
}
