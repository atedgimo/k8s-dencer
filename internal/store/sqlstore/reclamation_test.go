package sqlstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/store"
	sqlitestore "github.com/atedgimo/k8s-dencer/internal/store/sqlstore"
)

// Delegates to openTemp so DENCER_TEST_POSTGRES redirects these tests too.
// They used to open SQLite directly, which meant the store suite only
// *claimed* to run against both backends: node samples, reclamations,
// recoveries and staleness never did. Two Postgres bugs shipped through
// that gap in v0.6.0.
func reclamationStore(t *testing.T) *sqlitestore.Store {
	t.Helper()
	return openTemp(t)
}

func TestReclamationRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := reclamationStore(t)
	drained := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Millisecond)

	rec := store.Reclamation{Node: "n1", DrainedAt: drained, RunID: "run-1", PlanID: "plan-1", Step: 3}
	if err := db.RecordDrain(ctx, rec); err != nil {
		t.Fatal(err)
	}

	pending, err := db.PendingReclamations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1", len(pending))
	}
	got := pending[0]
	if got.Node != "n1" || got.RunID != "run-1" || got.PlanID != "plan-1" || got.Step != 3 {
		t.Errorf("provenance lost: %+v", got)
	}
	if !got.Pending() {
		t.Error("a freshly recorded drain should be pending")
	}
	if !got.DrainedAt.Equal(drained) {
		t.Errorf("drainedAt = %v, want %v", got.DrainedAt, drained)
	}

	resolvedAt := drained.Add(4 * time.Minute)
	if err := db.ResolveReclamation(ctx, "n1", drained, store.ReclaimedGone, resolvedAt); err != nil {
		t.Fatal(err)
	}
	if p, _ := db.PendingReclamations(ctx); len(p) != 0 {
		t.Errorf("still %d pending after resolving", len(p))
	}

	recent, err := db.Reclamations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].Outcome != store.ReclaimedGone {
		t.Fatalf("recent = %+v", recent)
	}
	if recent[0].Age(time.Now()) != 4*time.Minute {
		t.Errorf("age = %v, want 4m (measured drain to resolution, not to now)", recent[0].Age(time.Now()))
	}
}

// An outcome is observed once. Resolving twice would attribute a second event
// to the same observation, and on a restart that replays a cycle it would
// happen routinely.
func TestResolveIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := reclamationStore(t)
	drained := time.Now().Add(-time.Minute).UTC()

	if err := db.RecordDrain(ctx, store.Reclamation{Node: "n1", DrainedAt: drained}); err != nil {
		t.Fatal(err)
	}
	if err := db.ResolveReclamation(ctx, "n1", drained, store.ReclaimedGone, time.Now()); err != nil {
		t.Fatal(err)
	}
	err := db.ResolveReclamation(ctx, "n1", drained, store.ReclaimedReturned, time.Now())
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second resolve returned %v, want ErrNotFound", err)
	}

	recent, _ := db.Reclamations(ctx, 10)
	if recent[0].Outcome != store.ReclaimedGone {
		t.Errorf("outcome was overwritten to %q; the first observation must stand", recent[0].Outcome)
	}
}

// The same node can be drained, uncordoned and drained again. Keying on the
// node alone would make the second attempt overwrite the first, losing the
// record that it came back.
func TestTheSameNodeCanBeDrainedTwice(t *testing.T) {
	ctx := context.Background()
	db := reclamationStore(t)
	first := time.Now().Add(-2 * time.Hour).UTC()
	second := time.Now().Add(-1 * time.Hour).UTC()

	for _, at := range []time.Time{first, second} {
		if err := db.RecordDrain(ctx, store.Reclamation{Node: "n1", DrainedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.ResolveReclamation(ctx, "n1", first, store.ReclaimedReturned, first.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	pending, _ := db.PendingReclamations(ctx)
	if len(pending) != 1 || !pending[0].DrainedAt.Equal(second) {
		t.Fatalf("expected only the second attempt pending, got %+v", pending)
	}
	all, _ := db.Reclamations(ctx, 10)
	if len(all) != 2 {
		t.Errorf("got %d records, want 2 — each drain is its own observation", len(all))
	}
}

// Recording the same drain twice must not fail: the executor may retry, and a
// crash between the eviction and the record is a normal thing to recover from.
func TestRecordDrainIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db := reclamationStore(t)
	at := time.Now().UTC()

	for i := 0; i < 2; i++ {
		if err := db.RecordDrain(ctx, store.Reclamation{Node: "n1", DrainedAt: at, RunID: "r1"}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	if p, _ := db.PendingReclamations(ctx); len(p) != 1 {
		t.Errorf("got %d pending, want 1", len(p))
	}
}

func TestReclamationSummary(t *testing.T) {
	ctx := context.Background()
	db := reclamationStore(t)
	now := time.Now().UTC()

	// Three reclaimed at 2, 4 and 10 minutes; one returned; one still pending.
	for i, d := range []time.Duration{2 * time.Minute, 4 * time.Minute, 10 * time.Minute} {
		at := now.Add(-time.Hour).Add(time.Duration(i) * time.Second)
		if err := db.RecordDrain(ctx, store.Reclamation{Node: nodeName(i), DrainedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := db.ResolveReclamation(ctx, nodeName(i), at, store.ReclaimedGone, at.Add(d)); err != nil {
			t.Fatal(err)
		}
	}
	returnedAt := now.Add(-30 * time.Minute)
	_ = db.RecordDrain(ctx, store.Reclamation{Node: "returned", DrainedAt: returnedAt})
	_ = db.ResolveReclamation(ctx, "returned", returnedAt, store.ReclaimedReturned, returnedAt.Add(time.Hour))
	_ = db.RecordDrain(ctx, store.Reclamation{Node: "waiting", DrainedAt: now.Add(-90 * 24 * time.Hour)})

	stats, err := db.ReclamationSummary(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Reclaimed != 3 {
		t.Errorf("reclaimed = %d, want 3", stats.Reclaimed)
	}
	if stats.Returned != 1 {
		t.Errorf("returned = %d, want 1", stats.Returned)
	}
	// The 90-day-old pending node is the most interesting row in the table and
	// must not be windowed out of the count.
	if stats.Awaiting != 1 {
		t.Errorf("awaiting = %d, want 1 — a long-pending node must still be counted", stats.Awaiting)
	}
	if stats.MedianTime != 4*time.Minute {
		t.Errorf("median = %v, want 4m (median of 2m, 4m, 10m — a mean would give 5m20s)", stats.MedianTime)
	}
}

func nodeName(i int) string { return string(rune('a' + i)) }

// The ledger must sum only what was measured, and must say what it could not
// count. Old rows carry no capacity — summing them as zero would silently
// under-report the one number this feature exists to state.
func TestReclamationLedgerSumsDrainTimeCapacity(t *testing.T) {
	s := reclamationStore(t)
	ctx := context.Background()
	drained := time.Now().UTC().Add(-time.Hour)

	// Two measured nodes and one pre-ledger row without capacity.
	for _, r := range []store.Reclamation{
		{Node: "m1", DrainedAt: drained, CPUMilli: 8000, MemBytes: 32 << 30},
		{Node: "m2", DrainedAt: drained, CPUMilli: 4000, MemBytes: 16 << 30},
		{Node: "old", DrainedAt: drained},
	} {
		if err := s.RecordDrain(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"m1", "m2", "old"} {
		if err := s.ResolveReclamation(ctx, n, drained, store.ReclaimedGone, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	// A returned node must not contribute: it saved nothing.
	if err := s.RecordDrain(ctx, store.Reclamation{Node: "back", DrainedAt: drained, CPUMilli: 9999, MemBytes: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolveReclamation(ctx, "back", drained, store.ReclaimedReturned, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	stats, err := s.ReclamationSummary(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stats.ReclaimedCPUMilli, int64(12000); got != want {
		t.Errorf("ledger cpu = %d, want %d (measured nodes only)", got, want)
	}
	if got, want := stats.ReclaimedMemBytes, int64(48<<30); got != want {
		t.Errorf("ledger mem = %d, want %d", got, want)
	}
	if stats.UncountedNodes != 1 {
		t.Errorf("uncounted = %d, want 1 — the pre-ledger row must be named, not silently zero", stats.UncountedNodes)
	}
}
