package sqlstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/store/sqlstore"
)

// Delegates to openTemp so DENCER_TEST_POSTGRES redirects these tests too.
// They used to open SQLite directly, which meant the store suite only
// *claimed* to run against both backends: node samples, reclamations,
// recoveries and staleness never did. Two Postgres bugs shipped through
// that gap in v0.6.0.
func recStore(t *testing.T) *sqlstore.Store {
	t.Helper()
	return openTemp(t)
}

// The median is what to expect; the worst is what to plan for. A mean would
// let one pathological restart speak for both.
func TestRecoveryReportsTypicalAndWorst(t *testing.T) {
	db := recStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	for i, d := range []time.Duration{5 * time.Second, 7 * time.Second, 9 * time.Second, 4 * time.Minute} {
		if err := db.RecordRecovery(ctx, "shop/Deployment/web", base.Add(time.Duration(i)*time.Minute), d); err != nil {
			t.Fatal(err)
		}
	}

	got, err := db.RecoveryFor(ctx, []string{"shop/Deployment/web"})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := got["shop/Deployment/web"]
	if !ok {
		t.Fatal("no recovery recorded for the workload")
	}
	if r.Observations != 4 {
		t.Errorf("observations = %d, want 4", r.Observations)
	}
	if r.WorstSeconds != 240 {
		t.Errorf("worst = %ds, want 240 — the outlier is exactly what to plan for", r.WorstSeconds)
	}
	// Four observations: the upper median is the third, 9s. A mean would be
	// 63s, which describes none of the four drains that happened.
	if r.TypicalSeconds != 9 {
		t.Errorf("typical = %ds, want 9", r.TypicalSeconds)
	}
}

// Never observed and instantaneous are different claims, and only one of
// them should ever reach a screen.
func TestRecoveryOmitsWorkloadsNeverSeen(t *testing.T) {
	db := recStore(t)
	ctx := context.Background()

	if err := db.RecordRecovery(ctx, "shop/Deployment/web", time.Now().UTC(), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	got, err := db.RecoveryFor(ctx, []string{"shop/Deployment/web", "shop/Deployment/never-drained"})
	if err != nil {
		t.Fatal(err)
	}
	if _, present := got["shop/Deployment/never-drained"]; present {
		t.Error("a workload never drained was reported as having a recovery time")
	}
	if len(got) != 1 {
		t.Errorf("returned %d workloads, want 1", len(got))
	}
}

func TestRecoveryHandlesNoWorkloads(t *testing.T) {
	db := recStore(t)
	got, err := db.RecoveryFor(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("returned %d, want 0", len(got))
	}
}
