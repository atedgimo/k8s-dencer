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
