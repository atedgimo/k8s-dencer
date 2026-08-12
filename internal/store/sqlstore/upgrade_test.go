package sqlstore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/store"
	"github.com/atedgimo/k8s-dencer/internal/store/sqlstore"
)

// Every other test in this package migrates an empty file, which exercises
// v0 to current and nothing else. The path an operator actually takes is
// different: they have months of plans, runs and reclamations sitting in a
// database written by the version they are upgrading from, and five ALTER
// TABLE statements are about to run against it.
//
// That path was untested when v0.5.0 shipped. The fixture is a real database
// written by v0.4.0's own code — a plan, a run and a reclamation — rather
// than a hand-built approximation of what v0.4.0 might have produced, because
// an approximation would only ever test my memory of the old schema.
func TestUpgradeFromV040(t *testing.T) {
	src := filepath.Join("..", "..", "..", "test", "fixtures", "stores", "v0.4.0-schema-v8.db")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	// Copied, because migration rewrites it and a fixture that mutates is a
	// test that passes exactly once.
	path := filepath.Join(t.TempDir(), "upgraded.db")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := sqlstore.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrating a v0.4.0 database failed — every upgrading operator hits this: %v", err)
	}

	// The plan an operator would still be looking at.
	rec, err := db.Latest(ctx)
	if err != nil {
		t.Fatalf("plan unreadable after upgrade: %v", err)
	}
	if rec.Plan.NodesBefore != 6 || len(rec.Plan.Steps) != 1 {
		t.Errorf("plan changed across the upgrade: %d nodes before, %d steps; want 6 and 1",
			rec.Plan.NodesBefore, len(rec.Plan.Steps))
	}

	// The ledger. New columns must default to the truth about old rows:
	// nobody recorded a machine type at the time, and it was our own drain.
	rs, err := db.Reclamations(ctx, 10)
	if err != nil {
		t.Fatalf("reclamations unreadable after upgrade: %v", err)
	}
	if len(rs) != 1 {
		t.Fatalf("reclamations = %d, want 1 — the ledger lost a row", len(rs))
	}
	if rs[0].CPUMilli != 3920 {
		t.Errorf("measured capacity changed: %dm, want 3920m", rs[0].CPUMilli)
	}
	if rs[0].External {
		t.Error("an old drain was marked external; it was this product's own work")
	}
	if rs[0].InstanceType != "" {
		t.Errorf("instance type = %q; nobody recorded one at the time, so it must stay empty rather than be invented", rs[0].InstanceType)
	}

	// The audit trail, and the stop columns that did not exist when it ran.
	run, err := db.RunByID(ctx, "oldrun")
	if err != nil {
		t.Fatalf("run unreadable after upgrade: %v", err)
	}
	if run.Status != store.RunSucceeded {
		t.Errorf("run status = %q, want Succeeded", run.Status)
	}
	if run.StopRequested {
		t.Error("an old run reads as having been asked to stop; nobody could have asked")
	}
}
