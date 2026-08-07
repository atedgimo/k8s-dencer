// Package store persists consolidation plans.
//
// Plans live in a regular database rather than a CRD. Architecture doc §6
// makes the case: a plan is refreshed continuously by the planner and read
// almost entirely by the UI, and pushing that write volume through etcd is a
// well-known way to hurt a cluster. Nothing external "desires" a specific
// plan, so there is nothing for Kubernetes' reconciliation model to do.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// ErrNotFound is returned when a plan does not exist.
var ErrNotFound = errors.New("not found")

// Record is a plan together with the cluster state it was computed from.
//
// The snapshot and analysis are stored alongside the plan deliberately. The UI
// needs to draw the graph and explain constraints for the plan it is
// displaying, and pairing them guarantees the three agree. Fetching live state
// instead would show a graph that has already drifted from the plan drawn over
// it, and would mean history could never be reviewed at all.
type Record struct {
	Plan     *model.Plan
	Snapshot *model.ClusterSnapshot
	Analysis *constraints.Analysis
	Strategy string
	StoredAt time.Time
}

// Summary is the cheap listing form, without the heavy payloads.
type Summary struct {
	ID              string           `json:"id"`
	GeneratedAt     time.Time        `json:"generatedAt"`
	SnapshotTakenAt time.Time        `json:"snapshotTakenAt"`
	Status          model.PlanStatus `json:"status"`
	Strategy        string           `json:"strategy"`
	Steps           int              `json:"steps"`
	NodesBefore     int              `json:"nodesBefore"`
	NodesAfter      int              `json:"nodesAfter"`
	Ratings         map[string]int   `json:"ratings"`
	StoredAt        time.Time        `json:"storedAt"`
}

// ReclamationOutcome is how a drained node's story ended.
type ReclamationOutcome string

const (
	// ReclaimedGone means the Node object disappeared: something actually
	// removed the machine, which is the outcome the product exists to produce.
	ReclaimedGone ReclamationOutcome = "reclaimed"

	// ReclaimedReturned means the node was uncordoned and put back into
	// service instead. Not a failure — an operator changed their mind, or an
	// abort reversed the cordon — but it is emphatically not a saving, and
	// counting it as one would be the same overstatement this whole mechanism
	// exists to remove.
	ReclaimedReturned ReclamationOutcome = "returned"
)

// Reclamation records what happened to a node after it was drained.
//
// Draining is not removing. k8s-dencer cordons a node and empties it; something
// else — Karpenter, cluster-autoscaler, a managed node pool, a human — removes
// the machine, and until this existed nothing ever checked whether anything
// did. A plan reporting "15 reclaimable" was a prediction presented as an
// outcome.
//
// Rather than predict which reclaimer will act, which is vendor-specific and
// can be wrong in ways a user cannot check, this observes whether one did.
type Reclamation struct {
	Node      string    `json:"node"`
	DrainedAt time.Time `json:"drainedAt"`

	// Provenance, so a pending reclamation can be traced to the run that
	// caused it.
	RunID  string `json:"runId,omitempty"`
	PlanID string `json:"planId,omitempty"`
	Step   int    `json:"step,omitempty"`

	// ResolvedAt and Outcome are zero while the node is still awaiting
	// reclamation.
	ResolvedAt *time.Time         `json:"resolvedAt,omitempty"`
	Outcome    ReclamationOutcome `json:"outcome,omitempty"`

	// The node's allocatable, captured AT DRAIN TIME by the executor — the
	// last moment it can be captured, because a reclaimed node takes its
	// capacity record with it. This is what lets the ledger say "340 cores
	// returned" as a measurement rather than an estimate. Zero on rows
	// recorded before the ledger existed; sums treat those as zero and the
	// ledger says so rather than guessing.
	CPUMilli int64 `json:"cpuMilli,omitempty"`
	MemBytes int64 `json:"memBytes,omitempty"`

	// InstanceType and CapacityType, captured at drain time for the same
	// reason as the capacity above: a reclaimed node takes its labels with
	// it, and without them the ledger has a size but no price.
	//
	// Both are empty when the platform does not say — a KWOK node is not
	// "on-demand", it is unpriced, and inventing a word here would put a made
	// up number in a cost report.
	InstanceType string `json:"instanceType,omitempty"`
	CapacityType string `json:"capacityType,omitempty"`

	// External marks a node this product did not drain.
	//
	// Every managed cluster runs an autoscaler with its own opinion, and on a
	// real GKE cluster one reclaimed two nodes while converge was declining to
	// touch them. The ledger reported "No nodes have been drained yet" while
	// the fleet visibly shrank, which is the one number this mechanism exists
	// to get right.
	//
	// Recorded, and kept separate. Counting someone else's work as a saving
	// this product produced would be the same overstatement the ledger was
	// built to remove — but pretending it did not happen understates what the
	// cluster actually did.
	External bool `json:"external,omitempty"`
}

// Pending reports whether this node is still drained and still present.
func (r Reclamation) Pending() bool { return r.ResolvedAt == nil }

// Age is how long the node has been waiting, or how long it waited.
func (r Reclamation) Age(now time.Time) time.Duration {
	if r.ResolvedAt != nil {
		return r.ResolvedAt.Sub(r.DrainedAt)
	}
	return now.Sub(r.DrainedAt)
}

// ReclamationStats summarises observed reclamations over a window.
type ReclamationStats struct {
	Awaiting  int `json:"awaiting"`
	Reclaimed int `json:"reclaimed"`
	Returned  int `json:"returned"`
	// Median rather than mean: one node that sat for a week before someone
	// noticed would drag an average into uselessness, and the question being
	// answered is "how long does this normally take".
	MedianTime time.Duration `json:"medianReclamationSeconds"`

	// The ledger: capacity actually returned, summed over reclaimed nodes
	// from their drain-time records. The only measured — not estimated —
	// savings figure a consolidation tool can show. Rows recorded before
	// capacity capture existed contribute zero; UncountedNodes says how many,
	// so the ledger can be honest about its own blind spot instead of
	// silently under-reporting.
	ReclaimedCPUMilli int64 `json:"reclaimedCpuMilli"`
	ReclaimedMemBytes int64 `json:"reclaimedMemBytes"`
	UncountedNodes    int   `json:"uncountedNodes"`

	// ExternallyReclaimed counts nodes that left the cluster without this
	// product draining them — an autoscaler, or a person with kubectl. Held
	// apart from Reclaimed so the ledger can show what the cluster did without
	// claiming credit for it.
	ExternallyReclaimed int `json:"externallyReclaimed"`
}

// ReclamationStore tracks drained nodes until something removes them.
//
// Separate from Store and ExecutionStore because the writers differ: the
// executor records a drain, and the planner — the only component watching nodes
// continuously — observes the outcome.
type ReclamationStore interface {
	// RecordDrain notes that a node was drained and is now awaiting
	// reclamation. Idempotent on (node, drainedAt).
	RecordDrain(ctx context.Context, r Reclamation) error

	// PendingReclamations returns every node still awaiting an outcome.
	PendingReclamations(ctx context.Context) ([]Reclamation, error)

	// ResolveReclamation closes out one pending record.
	ResolveReclamation(ctx context.Context, node string, drainedAt time.Time, outcome ReclamationOutcome, at time.Time) error

	// Reclamations returns recent records, newest first.
	Reclamations(ctx context.Context, limit int) ([]Reclamation, error)

	// ReclamationSummary aggregates records resolved since the given time.
	ReclamationSummary(ctx context.Context, since time.Time) (ReclamationStats, error)
}

// Store persists and retrieves plans.
//
// Implementations must be safe for concurrent readers. The planner is the only
// writer.
type Store interface {
	// Migrate brings the schema up to date. Owned by the ui-backend, which
	// runs it at startup; the planner assumes it has already happened.
	Migrate(ctx context.Context) error

	// Save persists a record. It reports false when the plan is identical to
	// the most recent one, in which case nothing is written.
	//
	// Deduplication is what makes continuous re-planning affordable: a stable
	// cluster produces the same content-hashed plan ID every cycle, and
	// writing an identical row every 30 seconds would fill the volume with
	// noise and bury the moments when the plan actually changed.
	Save(ctx context.Context, rec Record) (bool, error)

	// Latest returns the most recently stored plan.
	Latest(ctx context.Context) (Record, error)

	// ByID returns a specific plan.
	ByID(ctx context.Context, id string) (Record, error)

	// List returns plan summaries, newest first.
	List(ctx context.Context, limit int) ([]Summary, error)

	// Prune keeps the newest keep records and deletes the rest, returning how
	// many were removed. Plan history is an audit trail, but it is not
	// infinite: the PVC is a fixed size.
	Prune(ctx context.Context, keep int) (int, error)

	Close() error
}

// Sample is one point on the cluster's timeline, written by the planner every
// publish cycle. Small on purpose — ten numbers — because it is written every
// resync forever, and because trends need totals, not inventories: the full
// snapshot already exists for the moment anyone wants detail.
type Sample struct {
	TakenAt time.Time `json:"takenAt"`
	Nodes   int       `json:"nodes"`
	Pods    int       `json:"pods"`

	CPUReqMilli   int64 `json:"cpuReqMilli"`
	CPUAllocMilli int64 `json:"cpuAllocMilli"`
	MemReqBytes   int64 `json:"memReqBytes"`
	MemAllocBytes int64 `json:"memAllocBytes"`

	// Zero when no usage source is configured — which is "unmeasured", never
	// "idle". Consumers must check HasUsage before drawing conclusions.
	CPUUsedMilli int64 `json:"cpuUsedMilli"`
	MemUsedBytes int64 `json:"memUsedBytes"`
	HasUsage     bool  `json:"hasUsage"`

	// Reclaimable is the plan's answer at this moment: how many nodes the
	// planner would free. The one series that tells the product's own story.
	Reclaimable int `json:"reclaimable"`
}

// SampleStore persists the timeline. Implemented by the SQLite store;
// optional the same way ReclamationStore is.
type SampleStore interface {
	SaveSample(ctx context.Context, s Sample) error
	// Samples returns points at or after since, oldest first.
	Samples(ctx context.Context, since time.Time) ([]Sample, error)
	// PruneSamples removes points older than before, returning how many.
	PruneSamples(ctx context.Context, before time.Time) (int, error)
}
