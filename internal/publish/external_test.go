package publish

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
	"github.com/atedgimo/k8s-dencer/internal/store/sqlstore"
)

// A real store, so the schema migration this needs is exercised too — a fake
// would have happily accepted a column that does not exist.
func newStore(t *testing.T) *sqlstore.Store {
	t.Helper()
	db, err := sqlstore.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func node(name string, cpu int64) model.Node {
	return model.Node{Name: name, Allocatable: model.Resources{MilliCPU: cpu, MemoryBytes: cpu << 20}}
}

func snapOf(nodes ...model.Node) *model.ClusterSnapshot {
	return &model.ClusterSnapshot{Nodes: nodes}
}

func newPublisher(db store.Store) *Publisher {
	return &Publisher{DB: db, Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
}

// The first cycle after a restart has no previous fleet. Treating every node
// as newly absent would invent a reclamation for the whole cluster.
func TestFirstCycleInventsNothing(t *testing.T) {
	db := newStore(t)
	p := newPublisher(db)
	ctx := context.Background()

	p.recordExternalReclaims(ctx, db, snapOf(node("a", 1000), node("b", 1000)))

	got, err := db.Reclamations(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("first cycle recorded %d reclamations; it has nothing to compare against", len(got))
	}
}

// The case from the GKE run: nodes vanish, nobody here drained them.
func TestNodeThatVanishesIsRecordedAsExternal(t *testing.T) {
	db := newStore(t)
	p := newPublisher(db)
	ctx := context.Background()

	p.recordExternalReclaims(ctx, db, snapOf(node("a", 940), node("b", 940), node("c", 940)))
	p.recordExternalReclaims(ctx, db, snapOf(node("a", 940)))

	got, err := db.Reclamations(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("recorded %d, want 2 (b and c left the cluster)", len(got))
	}
	for _, r := range got {
		if !r.External {
			t.Errorf("%s recorded as ours; nothing here drained it", r.Node)
		}
		if r.Outcome != store.ReclaimedGone {
			t.Errorf("%s outcome = %q, want reclaimed", r.Node, r.Outcome)
		}
		if r.ResolvedAt == nil {
			t.Errorf("%s left unresolved; it is already gone, there is nothing to await", r.Node)
		}
		// Capacity comes from the last snapshot that still had the node — a
		// departed node takes its capacity record with it.
		if r.CPUMilli != 940 {
			t.Errorf("%s cpu = %dm, want 940m from the last snapshot holding it", r.Node, r.CPUMilli)
		}
	}
}

// A node the executor drained resolves through the normal path. Counting it
// here as well would attribute our own work to somebody else.
func TestOurOwnDrainIsNotCountedAsExternal(t *testing.T) {
	db := newStore(t)
	p := newPublisher(db)
	ctx := context.Background()

	if err := db.RecordDrain(ctx, store.Reclamation{
		Node: "b", DrainedAt: time.Now().UTC(), RunID: "run-1", CPUMilli: 940,
	}); err != nil {
		t.Fatal(err)
	}

	p.recordExternalReclaims(ctx, db, snapOf(node("a", 940), node("b", 940)))
	p.recordExternalReclaims(ctx, db, snapOf(node("a", 940)))

	got, err := db.Reclamations(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("recorded %d rows, want 1 — the executor's own drain, not a duplicate", len(got))
	}
	if got[0].External {
		t.Error("our own drain was recorded as somebody else's work")
	}
}

// The ledger's contract: it never overstates. External capacity is counted,
// and counted apart.
func TestStatsHoldExternalApartFromOurs(t *testing.T) {
	db := newStore(t)
	p := newPublisher(db)
	ctx := context.Background()

	p.recordExternalReclaims(ctx, db, snapOf(node("a", 940), node("gone", 940)))
	p.recordExternalReclaims(ctx, db, snapOf(node("a", 940)))

	stats, err := db.ReclamationSummary(ctx, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats.ExternallyReclaimed != 1 {
		t.Errorf("externallyReclaimed = %d, want 1", stats.ExternallyReclaimed)
	}
	if stats.Reclaimed != 0 {
		t.Errorf("reclaimed = %d, want 0 — this product drained nothing", stats.Reclaimed)
	}
	if stats.ReclaimedCPUMilli != 0 {
		t.Errorf("ledger claims %dm of savings it did not produce", stats.ReclaimedCPUMilli)
	}
}
