package sqlstore_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/store"
	"github.com/atedgimo/k8s-dencer/internal/store/sqlstore"
)

func nodeStore(t *testing.T) *sqlstore.Store {
	t.Helper()
	db, err := sqlstore.Open(filepath.Join(t.TempDir(), "n.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The distinction the whole thing exists for: a node that sits at 30% for a
// fortnight is a different decision from one that spikes to 90% nightly, and
// a single instant cannot tell them apart.
func TestStabilitySeparatesSteadyFromSpiky(t *testing.T) {
	db := nodeStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-6 * time.Hour)

	var samples []store.NodeSample
	for i := 0; i < 20; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		// steady: 300m of 1000m, every time.
		samples = append(samples, store.NodeSample{
			Node: "steady", TakenAt: at, CPUUsedMilli: 300, CPUAllocMilli: 1000,
		})
		// spiky: 300m usually, 900m on one reading in five.
		used := int64(300)
		if i%5 == 0 {
			used = 900
		}
		samples = append(samples, store.NodeSample{
			Node: "spiky", TakenAt: at, CPUUsedMilli: used, CPUAllocMilli: 1000,
		})
	}
	if err := db.SaveNodeSamples(ctx, samples); err != nil {
		t.Fatal(err)
	}

	got, err := db.NodeStabilitySince(ctx, base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]store.NodeStability{}
	for _, n := range got {
		by[n.Node] = n
	}

	if s := by["steady"]; s.MedianPct != 30 || s.PeakPct != 30 {
		t.Errorf("steady = median %d%% peak %d%%, want 30/30", s.MedianPct, s.PeakPct)
	}
	// The median is what the node usually does; the peak is what it does at
	// its worst. Reporting only one of them hides the decision.
	if s := by["spiky"]; s.MedianPct != 30 || s.PeakPct != 90 {
		t.Errorf("spiky = median %d%% peak %d%%, want 30/90", s.MedianPct, s.PeakPct)
	}
	if s := by["steady"]; s.Samples != 20 {
		t.Errorf("steady samples = %d, want 20", s.Samples)
	}
}

// A node whose allocatable was never recorded produces no percentage rather
// than an infinity.
func TestStabilitySkipsNodesWithoutCapacity(t *testing.T) {
	db := nodeStore(t)
	ctx := context.Background()
	at := time.Now().UTC()

	if err := db.SaveNodeSamples(ctx, []store.NodeSample{
		{Node: "sized", TakenAt: at, CPUUsedMilli: 100, CPUAllocMilli: 1000},
		{Node: "unsized", TakenAt: at, CPUUsedMilli: 100, CPUAllocMilli: 0},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := db.NodeStabilitySince(ctx, at.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range got {
		if n.Node == "unsized" {
			t.Error("a node with no allocatable produced a percentage")
		}
	}
	if len(got) != 1 {
		t.Errorf("summarised %d nodes, want 1", len(got))
	}
}

// One row per node per resync is millions a day on a large fleet. Retention
// is the difference between a database and a disk-full alert.
func TestPruneNodeSamplesDropsOnlyTheOld(t *testing.T) {
	db := nodeStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := db.SaveNodeSamples(ctx, []store.NodeSample{
		{Node: "a", TakenAt: now.Add(-20 * 24 * time.Hour), CPUUsedMilli: 1, CPUAllocMilli: 1000},
		{Node: "a", TakenAt: now.Add(-1 * time.Hour), CPUUsedMilli: 1, CPUAllocMilli: 1000},
	}); err != nil {
		t.Fatal(err)
	}

	n, err := db.PruneNodeSamples(ctx, now.Add(-14*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	got, err := db.NodeStabilitySince(ctx, now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Samples != 1 {
		t.Errorf("after pruning: %+v, want one node with one sample", got)
	}
}
