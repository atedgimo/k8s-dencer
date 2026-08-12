package sqlstore_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

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

// Schema v14 replaced SQLite's rowid with an explicit seq column, and
// migration backfills it from rowid so that upgrading cannot silently reorder
// anybody's history. That claim is only worth making if something checks it,
// and the single-row fixture cannot: with one plan every ordering agrees.
//
// So the fixture is given a second plan sharing the first one's stored_at —
// the case the tiebreaker exists for — and the upgrade has to keep the newer
// one newer. Before v14 that answer came from rowid; after it, from seq; an
// operator must not be able to tell the difference.
func TestUpgradePreservesPlanOrder(t *testing.T) {
	src := filepath.Join("..", "..", "..", "test", "fixtures", "stores", "v0.4.0-schema-v8.db")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("fixture not available: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ordered.db")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// Cloned column-by-column from whatever the fixture actually holds rather
	// than from a written-out v8 column list, so regenerating the fixture
	// cannot leave this test asserting against a schema that no longer exists.
	raw2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	cols, err := raw2.Query(`SELECT name FROM pragma_table_info('plans')`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for cols.Next() {
		var n string
		if err := cols.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if err := cols.Err(); err != nil {
		t.Fatal(err)
	}
	_ = cols.Close()

	// Same row, new id, same stored_at: only insertion order separates them.
	sel := make([]string, len(names))
	for i, n := range names {
		sel[i] = n
		if n == "id" {
			sel[i] = `'newer'`
		}
	}
	list := strings.Join(names, ", ")
	if _, err := raw2.Exec(
		`INSERT INTO plans (` + list + `) SELECT ` + strings.Join(sel, ", ") + ` FROM plans`,
	); err != nil {
		t.Fatalf("seeding a second plan: %v", err)
	}
	if err := raw2.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sqlstore.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	rec, err := db.Latest(ctx)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if rec.Plan.ID != "newer" {
		t.Errorf("Latest = %q, want \"newer\" — the backfill reordered history, and an operator would open the upgrade on a plan they had already superseded", rec.Plan.ID)
	}

	// The list is what the UI's history pane reads, and it sorts by the same
	// pair; a backfill that satisfied one and not the other would be worse
	// than either, because the two views would disagree.
	sums, err := db.List(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sums) != 2 {
		t.Fatalf("List = %d plans, want 2", len(sums))
	}
	if sums[0].ID != "newer" {
		t.Errorf("List newest = %q, want \"newer\"; Latest and List disagree about the same two rows", sums[0].ID)
	}
}
