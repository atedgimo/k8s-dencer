// Package reclaim decides what became of a drained node.
//
// k8s-dencer cordons a node and empties it; it never deletes one, and its
// ServiceAccount holds no delete verb on nodes. Something else does the
// removing — Karpenter, cluster-autoscaler, a managed node pool, a person.
//
// Until this existed, nothing checked whether anything did. The plan reported
// "15 reclaimable" and the UI called a merely-drained node reclaimed, so the
// product's central claim was a prediction wearing the clothes of an outcome.
//
// Rather than predict which reclaimer will act — vendor-specific, and wrong in
// ways a user cannot check — this observes whether one did. The rule is
// deliberately small enough to state in a sentence: the node is gone, the node
// came back, or we are still waiting.
package reclaim

import (
	"time"

	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// Transition is one pending reclamation reaching an outcome.
type Transition struct {
	Reclamation store.Reclamation
	Outcome     store.ReclamationOutcome
	At          time.Time
	// Took is how long the node waited. Meaningful only for ReclaimedGone.
	Took time.Duration
}

// Resolve decides which pending reclamations have reached an outcome.
//
// Pure, and takes the snapshot the planner already has, so the whole rule is
// testable from a fixture with no cluster — the same reason internal/model has
// no Kubernetes imports.
//
// Three cases, and the third is the one worth naming:
//
//   - the node is absent from the snapshot: something removed it. Reclaimed.
//   - the node is present and schedulable again: someone uncordoned it, whether
//     by hand or through an abort. Returned — not a saving, and leaving it
//     pending forever would make "awaiting" grow without bound and mean nothing.
//   - the node is present and still cordoned: still waiting. On a cluster with
//     no reclaimer at all this is the permanent, correct answer, and saying so
//     is the entire point.
func Resolve(pending []store.Reclamation, snap *model.ClusterSnapshot, now time.Time) []Transition {
	if len(pending) == 0 || snap == nil {
		return nil
	}

	// Cordoned state by node name, from the snapshot the planner just took.
	present := make(map[string]bool, len(snap.Nodes))
	cordoned := make(map[string]bool, len(snap.Nodes))
	for _, n := range snap.Nodes {
		present[n.Name] = true
		cordoned[n.Name] = n.Unschedulable
	}

	var out []Transition
	for _, r := range pending {
		if !r.Pending() {
			// Already resolved. Not expected from PendingReclamations, but
			// resolving one twice would attribute a second outcome to the same
			// observation, so it is refused here rather than trusted upstream.
			continue
		}

		switch {
		case !present[r.Node]:
			out = append(out, Transition{
				Reclamation: r,
				Outcome:     store.ReclaimedGone,
				At:          now,
				Took:        now.Sub(r.DrainedAt),
			})
		case !cordoned[r.Node]:
			out = append(out, Transition{
				Reclamation: r,
				Outcome:     store.ReclaimedReturned,
				At:          now,
			})
		}
	}
	return out
}

// Awaiting counts pending reclamations older than the given age.
//
// Used for the gauge and for the CLI's "drained N days ago and still here"
// line, which is the observation an operator most needs and never had.
func Awaiting(pending []store.Reclamation, olderThan time.Duration, now time.Time) []store.Reclamation {
	var out []store.Reclamation
	for _, r := range pending {
		if r.Pending() && r.Age(now) >= olderThan {
			out = append(out, r)
		}
	}
	return out
}
