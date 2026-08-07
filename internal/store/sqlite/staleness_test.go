package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
	"github.com/atedgimo/k8s-dencer/internal/store/sqlite"
)

func planStore(t *testing.T) *sqlite.Store {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// planID hashes the steps and nothing else, which is right — a plan is its
// steps. But it means an empty plan keeps its identity while the fleet
// changes size underneath it, and the dedup touch used to leave the stored
// counts describing a cluster that no longer existed.
//
// On a real GKE cluster the CLI reported "6 nodes now, 6 after" for twelve
// minutes after two nodes were removed, while the planner logged
// nodesBefore:4 every cycle. Fresh by timestamp, stale by content.
func TestDedupTouchRefreshesTheFleetSize(t *testing.T) {
	db := planStore(t)
	ctx := context.Background()

	// The same empty plan, seen first on six nodes and then on four.
	empty := func(before, after int) store.Record {
		return store.Record{
			Plan: &model.Plan{
				ID: "same-id", Steps: []model.PlanStep{},
				NodesBefore: before, NodesAfter: after,
			},
			Snapshot: &model.ClusterSnapshot{TakenAt: time.Now().UTC()},
		}
	}

	if changed, err := db.Save(ctx, empty(6, 6)); err != nil || !changed {
		t.Fatalf("first save: changed=%v err=%v", changed, err)
	}
	// The second save dedups — same steps, same id — which is the path that
	// used to leave the counts alone.
	changed, err := db.Save(ctx, empty(4, 4))
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("an identical plan was stored as new; the dedup path is not being exercised")
	}

	rec, err := db.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Plan.NodesBefore != 4 || rec.Plan.NodesAfter != 4 {
		t.Errorf("stored plan says %d nodes now, %d after — want 4/4; the fleet shrank and the row did not",
			rec.Plan.NodesBefore, rec.Plan.NodesAfter)
	}
}
