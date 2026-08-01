package publish_test

// These tests exist because both of this repo's worst bugs lived in the code
// this package was extracted from — not in any algorithm, but in the wiring:
// what still has to happen on a cycle whose plan did not change. Each test
// pins one of those properties. They run against the real SQLite store, not a
// mock of it, because the dedup behaviour under test is the store's own.

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/impact"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/planner"
	"github.com/atedgimo/k8s-dencer/internal/publish"
	"github.com/atedgimo/k8s-dencer/internal/store"
	sqlitestore "github.com/atedgimo/k8s-dencer/internal/store/sqlite"
	"github.com/atedgimo/k8s-dencer/internal/telemetry"
)

// steadySource returns the same snapshot every cycle: a stable cluster, which
// is the healthiest possible state and historically the buggiest code path.
type steadySource struct{ snap *model.ClusterSnapshot }

func (s steadySource) Snapshot(context.Context) (*model.ClusterSnapshot, error) {
	return s.snap, nil
}

type failingSource struct{}

func (failingSource) Snapshot(context.Context) (*model.ClusterSnapshot, error) {
	return nil, errors.New("apiserver went away")
}

// failingSaves wraps the real store and fails only Save, because "a store
// failure must not stop planning" is a claim about what happens around Save,
// not instead of it.
type failingSaves struct {
	store.Store
}

func (f failingSaves) Save(context.Context, store.Record) (bool, error) {
	return false, errors.New("disk full")
}

func newPublisher(t *testing.T, src publish.Source, db store.Store) *publish.Publisher {
	t.Helper()
	return &publish.Publisher{
		Log:        slog.New(slog.DiscardHandler),
		Source:     src,
		Strategy:   planner.Greedy{},
		Options:    planner.DefaultOptions(),
		Classifier: impact.New(impact.Thresholds{}),
		DB:         db,
		Retain:     10,
		Metrics:    telemetry.NewMetrics(telemetry.ComponentPlanner),
	}
}

func openStore(t *testing.T) store.Store {
	t.Helper()
	db, err := sqlitestore.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func snapshot(t *testing.T) *model.ClusterSnapshot {
	t.Helper()
	opts := model.DefaultSynthetic(8)
	snap := model.Synthesize(opts)
	// The synthetic fixture stamps a fixed 2025 TakenAt. Restamp to now:
	// the timeline's 30-day retention would otherwise prune each sample the
	// moment the cycle that wrote it finishes — which is exactly what
	// happened the first time this ran, and the prune log confessed.
	snap.TakenAt = time.Now().UTC()
	return snap
}

// The property whose absence shipped as "confirmed 72 minutes ago" on a
// cluster that had not moved: an unchanged plan must still reach Save every
// cycle, because the store's stored_at touch on the dedup path is the
// "still confirmed" signal the freshness display rests on.
func TestUnchangedPlanStillRefreshesItsConfirmation(t *testing.T) {
	db := openStore(t)
	pub := newPublisher(t, steadySource{snapshot(t)}, db)

	pub.Cycle(t.Context())
	first, err := db.Latest(t.Context())
	if err != nil {
		t.Fatalf("latest after first cycle: %v", err)
	}

	// Same cluster, later. The plan's content hash will be identical.
	time.Sleep(20 * time.Millisecond)
	pub.Cycle(t.Context())
	second, err := db.Latest(t.Context())
	if err != nil {
		t.Fatalf("latest after second cycle: %v", err)
	}

	if first.Plan.ID != second.Plan.ID {
		t.Fatalf("plan changed on a steady cluster: %s -> %s — the fixture no longer tests the dedup path",
			first.Plan.ID, second.Plan.ID)
	}
	if !second.StoredAt.After(first.StoredAt) {
		t.Errorf("stored_at did not advance across an unchanged cycle (%v -> %v); "+
			"the UI's \"confirmed just now\" is frozen and the staleness warning fires backwards again",
			first.StoredAt, second.StoredAt)
	}
}

// A store failure must not stop planning: the in-memory plan is still correct,
// still published to the debug endpoints, and the next cycle retries.
func TestStoreFailureDoesNotStopPublishing(t *testing.T) {
	db := openStore(t)
	pub := newPublisher(t, steadySource{snapshot(t)}, failingSaves{db})

	pub.Cycle(t.Context())

	if pub.LatestPlan() == nil {
		t.Error("no plan published after a Save failure; a full disk has taken the planner's output with it")
	}
	if pub.LatestSnapshot() == nil || pub.LatestAnalysis() == nil {
		t.Error("snapshot or analysis withheld after a Save failure")
	}
}

// A snapshot failure leaves the previously published state in place.
// Publishing nothing is recoverable; overwriting good state with nothing is
// how an empty cluster gets planned against.
func TestSnapshotFailureKeepsLastGoodState(t *testing.T) {
	db := openStore(t)
	good := newPublisher(t, steadySource{snapshot(t)}, db)
	good.Cycle(t.Context())
	planBefore := good.LatestPlan()
	if planBefore == nil {
		t.Fatal("fixture produced no plan")
	}

	good.Source = failingSource{}
	good.Cycle(t.Context())

	if got := good.LatestPlan(); got != planBefore {
		t.Error("a failed snapshot replaced the last good plan")
	}
	if good.LatestSnapshot() == nil {
		t.Error("a failed snapshot cleared the last good snapshot")
	}
}

// Reclamations are observed on every cycle, including one whose plan did not
// change. A node disappearing is exactly the kind of change that does not
// alter the plan — the drained node was already excluded from it — so putting
// the observation after the dedup early-return would blind the tracker on
// stable clusters, which is where it spends its life.
func TestReclamationsResolveOnAnUnchangedCycle(t *testing.T) {
	db := openStore(t)
	tracker, ok := db.(store.ReclamationStore)
	if !ok {
		t.Fatal("sqlite store no longer implements ReclamationStore")
	}

	snap := snapshot(t)
	pub := newPublisher(t, steadySource{snap}, db)

	// Warm the dedup: first cycle stores the plan.
	pub.Cycle(t.Context())

	// A node was drained earlier and its machine has since disappeared — it is
	// in the tracker but absent from the snapshot.
	drainedAt := time.Now().UTC().Add(-10 * time.Minute)
	if err := tracker.RecordDrain(t.Context(), store.Reclamation{
		Node:      "node-that-is-gone",
		DrainedAt: drainedAt,
	}); err != nil {
		t.Fatalf("record drain: %v", err)
	}

	// Second cycle: same snapshot, same plan ID, dedup path. The observation
	// must still happen.
	pub.Cycle(t.Context())

	pending, err := tracker.PendingReclamations(t.Context())
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	for _, p := range pending {
		if p.Node == "node-that-is-gone" {
			t.Error("a reclamation stayed pending across a cycle that saw the node gone; " +
				"observation has moved after the dedup early-return")
		}
	}

	recent, err := tracker.Reclamations(t.Context(), 10)
	if err != nil {
		t.Fatalf("reclamations: %v", err)
	}
	found := false
	for _, r := range recent {
		if r.Node == "node-that-is-gone" && r.Outcome == store.ReclaimedGone {
			found = true
		}
	}
	if !found {
		t.Error("the gone node was not recorded as reclaimed")
	}
}

// The timeline gains a point on EVERY cycle, dedup included — a steady
// cluster still has a timeline, and the History view exists to draw it.
func TestEveryCycleLandsOnTheTimeline(t *testing.T) {
	db := openStore(t)
	pub := newPublisher(t, steadySource{snapshot(t)}, db)

	pub.Cycle(t.Context())
	pub.Cycle(t.Context()) // the dedup path

	ts, ok := db.(store.SampleStore)
	if !ok {
		t.Fatal("sqlite store no longer implements SampleStore")
	}
	samples, err := ts.Samples(t.Context(), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 {
		t.Fatalf("2 cycles produced %d timeline points; the dedup path is skipping the timeline", len(samples))
	}
	if samples[0].Nodes == 0 || samples[0].CPUAllocMilli == 0 {
		t.Error("a sample with zero estate is a chart that lies about an empty cluster")
	}
}
