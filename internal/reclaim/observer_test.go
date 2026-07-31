package reclaim

import (
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

func snap(nodes ...model.Node) *model.ClusterSnapshot {
	return &model.ClusterSnapshot{Nodes: nodes}
}

func drained(name string, ago time.Duration, now time.Time) store.Reclamation {
	return store.Reclamation{Node: name, DrainedAt: now.Add(-ago), RunID: "r1"}
}

func TestResolve(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	resolved := now.Add(-time.Hour)

	cases := []struct {
		name    string
		pending []store.Reclamation
		snap    *model.ClusterSnapshot
		want    []store.ReclamationOutcome
	}{
		{
			name:    "node gone means something reclaimed it",
			pending: []store.Reclamation{drained("n1", 5*time.Minute, now)},
			snap:    snap(model.Node{Name: "n2"}),
			want:    []store.ReclamationOutcome{store.ReclaimedGone},
		},
		{
			name:    "node uncordoned means it came back",
			pending: []store.Reclamation{drained("n1", 5*time.Minute, now)},
			snap:    snap(model.Node{Name: "n1", Unschedulable: false}),
			want:    []store.ReclamationOutcome{store.ReclaimedReturned},
		},
		{
			// The permanent, correct answer on a cluster with no reclaimer. If
			// this ever resolved, the product would go back to claiming a
			// saving that never happened.
			name:    "still cordoned and present means keep waiting",
			pending: []store.Reclamation{drained("n1", 30*24*time.Hour, now)},
			snap:    snap(model.Node{Name: "n1", Unschedulable: true}),
			want:    nil,
		},
		{
			name: "already resolved is never resolved twice",
			pending: []store.Reclamation{{
				Node: "n1", DrainedAt: now.Add(-time.Hour),
				ResolvedAt: &resolved, Outcome: store.ReclaimedGone,
			}},
			snap: snap(),
			want: nil,
		},
		{
			name: "a mixed batch resolves each on its own facts",
			pending: []store.Reclamation{
				drained("gone", time.Minute, now),
				drained("back", time.Minute, now),
				drained("waiting", time.Minute, now),
			},
			snap: snap(
				model.Node{Name: "back", Unschedulable: false},
				model.Node{Name: "waiting", Unschedulable: true},
			),
			want: []store.ReclamationOutcome{store.ReclaimedGone, store.ReclaimedReturned},
		},
		{
			name:    "no snapshot resolves nothing",
			pending: []store.Reclamation{drained("n1", time.Minute, now)},
			snap:    nil,
			want:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.pending, tc.snap, now)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d transitions, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, want := range tc.want {
				if got[i].Outcome != want {
					t.Errorf("transition %d = %q, want %q", i, got[i].Outcome, want)
				}
			}
		})
	}
}

// The duration is the number the whole feature exists to produce, and it is
// only meaningful for a node that actually went away.
func TestResolveTimesOnlyGenuineReclamations(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

	gone := Resolve([]store.Reclamation{drained("n1", 7*time.Minute, now)}, snap(), now)
	if len(gone) != 1 || gone[0].Took != 7*time.Minute {
		t.Fatalf("reclaimed node should record 7m, got %+v", gone)
	}

	back := Resolve([]store.Reclamation{drained("n1", 7*time.Minute, now)},
		snap(model.Node{Name: "n1"}), now)
	if len(back) != 1 {
		t.Fatalf("expected one transition, got %+v", back)
	}
	if back[0].Took != 0 {
		t.Errorf("an uncordoned node timed %v; that measures how long someone "+
			"took to change their mind, not how long reclamation takes", back[0].Took)
	}
}

func TestAwaitingFiltersByAge(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	resolved := now
	pending := []store.Reclamation{
		drained("fresh", time.Minute, now),
		drained("stale", 48*time.Hour, now),
		{Node: "done", DrainedAt: now.Add(-72 * time.Hour), ResolvedAt: &resolved},
	}

	all := Awaiting(pending, 0, now)
	if len(all) != 2 {
		t.Errorf("got %d awaiting, want 2 (the resolved one must not count)", len(all))
	}
	old := Awaiting(pending, 24*time.Hour, now)
	if len(old) != 1 || old[0].Node != "stale" {
		t.Errorf("got %+v, want only the 48h-old node", old)
	}
}
