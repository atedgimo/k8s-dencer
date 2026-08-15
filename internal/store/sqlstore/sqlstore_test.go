package sqlstore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
	sqlitestore "github.com/atedgimo/k8s-dencer/internal/store/sqlstore"
)

// openTemp is a migrated, empty store — and the single lever that decides
// which server the whole suite runs against.
//
// With DENCER_TEST_POSTGRES set to a DSN, every test that calls this runs
// against a real Postgres instead of a temp file. Not a second suite of
// Postgres tests: the same assertions, because the promise being made is that
// the two backends behave identically, and a separate suite would only ever
// prove that the tests someone remembered to write twice agree.
func openTemp(t *testing.T) *sqlitestore.Store {
	t.Helper()
	if dsn := os.Getenv("DENCER_TEST_POSTGRES"); dsn != "" {
		return openTempPostgres(t, dsn)
	}
	s, err := sqlitestore.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// openTempPostgres gives each test its own schema on the shared server, which
// is what t.TempDir() buys the SQLite path: tests that cannot see each other's
// rows, and can therefore run in any order.
func openTempPostgres(t *testing.T, dsn string) *sqlitestore.Store {
	t.Helper()
	ctx := context.Background()

	// Derived from the test name so a failure names the schema it left
	// behind, and sanitised because test names contain slashes and spaces
	// that are not identifier characters.
	schema := "t_" + strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		default:
			return '_'
		}
	}, t.Name())
	if len(schema) > 60 {
		schema = schema[:60]
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer func() { _ = admin.Close() }()
	if _, err := admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	s, err := sqlitestore.OpenPostgres(ctx, dsn+sep+"search_path="+schema)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		// Best effort: a left-behind schema costs nothing but a name, and
		// failing cleanup would mask the assertion that actually failed.
		if db, err := sql.Open("pgx", dsn); err == nil {
			_, _ = db.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
			_ = db.Close()
		}
	})
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func record(id string, steps ...model.PlanStep) store.Record {
	return store.Record{
		Plan: &model.Plan{
			ID:              id,
			GeneratedAt:     time.Now().UTC().Truncate(time.Millisecond),
			SnapshotTakenAt: time.Now().UTC().Truncate(time.Millisecond),
			Status:          model.PlanValid,
			Steps:           steps,
			NodesBefore:     10,
			NodesAfter:      10 - len(steps),
		},
		Snapshot: &model.ClusterSnapshot{
			Nodes: []model.Node{{Name: "a", Ready: true,
				Allocatable: model.Resources{MilliCPU: 4000, MemoryBytes: 1 << 33, Pods: 110}}},
			Pods: []model.Pod{{Namespace: "app", Name: "web", NodeName: "a", Phase: model.PodRunning}},
		},
		Analysis: &constraints.Analysis{},
		Strategy: "greedy-first-fit-decreasing",
	}
}

func step(seq int, node string, impact model.ImpactRating) model.PlanStep {
	return model.PlanStep{
		ID:             fmt.Sprintf("step-%d", seq),
		SequenceNumber: seq,
		TargetNode:     node,
		Moves:          []model.Move{{Namespace: "app", Pod: "web", FromNode: node, ToNode: "b"}},
		Impact:         impact,
		Rationale:      "because reasons that are long enough to be useful",
		Reasons:        []model.ImpactReason{{Kind: "BlastRadius", Subject: node, Detail: "moves 1 pod"}},
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := openTemp(t)
	// Runs on every ui-backend start, and the planner runs it too so neither
	// has to start first.
	for i := 0; i < 3; i++ {
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate %d: %v", i, err)
		}
	}
}

func TestSaveAndReadBack(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	in := record("abc123", step(1, "n1", model.ImpactGreen), step(2, "n2", model.ImpactRed))
	stored, err := s.Save(ctx, in)
	if err != nil || !stored {
		t.Fatalf("save: stored=%v err=%v", stored, err)
	}

	out, err := s.Latest(ctx)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if out.Plan.ID != "abc123" {
		t.Errorf("id = %s", out.Plan.ID)
	}
	if out.Strategy != in.Strategy {
		t.Errorf("strategy = %q", out.Strategy)
	}
	if len(out.Plan.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(out.Plan.Steps))
	}
	if out.Plan.Steps[0].SequenceNumber != 1 || out.Plan.Steps[1].SequenceNumber != 2 {
		t.Error("steps came back out of order")
	}
	if out.Plan.Steps[1].Impact != model.ImpactRed {
		t.Errorf("impact = %s", out.Plan.Steps[1].Impact)
	}
	if out.Plan.Steps[0].Rationale == "" {
		t.Error("rationale lost in round-trip")
	}
	if len(out.Plan.Steps[0].Reasons) != 1 {
		t.Error("machine-readable reasons lost in round-trip")
	}
	if len(out.Plan.Steps[0].Moves) != 1 || out.Plan.Steps[0].Moves[0].ToNode != "b" {
		t.Error("moves lost in round-trip")
	}
	// Snapshot and analysis travel with the plan so the UI can draw a graph
	// that matches the plan drawn over it.
	if out.Snapshot == nil || len(out.Snapshot.Nodes) != 1 {
		t.Error("snapshot did not survive")
	}
	if out.Analysis == nil {
		t.Error("analysis did not survive")
	}
}

// A stable cluster re-plans to the same content hash every cycle. Writing that
// row every 30 seconds would fill the volume and bury the moments the plan
// actually changed.
func TestSaveDeduplicatesIdenticalPlans(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	rec := record("same-hash", step(1, "n1", model.ImpactGreen))
	if stored, _ := s.Save(ctx, rec); !stored {
		t.Fatal("first save should store")
	}
	if stored, err := s.Save(ctx, rec); err != nil || stored {
		t.Errorf("identical plan should not be stored again: stored=%v err=%v", stored, err)
	}

	plans, err := s.List(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Errorf("expected 1 stored plan, got %d", len(plans))
	}
}

// A plan that changes, then changes back, must be recorded again — otherwise
// the history would claim nothing happened in between.
func TestPlanReturningToAPreviousShapeIsRecorded(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	a := record("hash-a", step(1, "n1", model.ImpactGreen))
	b := record("hash-b", step(1, "n2", model.ImpactYellow))

	mustSave(t, s, a, true)
	mustSave(t, s, b, true)
	mustSave(t, s, a, true)

	latest, err := s.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Plan.ID != "hash-a" {
		t.Errorf("latest = %s, want hash-a", latest.Plan.ID)
	}
}

func TestListSummarisesRatings(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	mustSave(t, s, record("p1",
		step(1, "n1", model.ImpactGreen),
		step(2, "n2", model.ImpactGreen),
		step(3, "n3", model.ImpactYellow),
		step(4, "n4", model.ImpactRed),
	), true)

	plans, err := s.List(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans = %d", len(plans))
	}
	got := plans[0]
	if got.Steps != 4 {
		t.Errorf("steps = %d", got.Steps)
	}
	if got.Ratings["Green"] != 2 || got.Ratings["Yellow"] != 1 || got.Ratings["Red"] != 1 {
		t.Errorf("ratings = %+v", got.Ratings)
	}
	if got.Strategy == "" {
		t.Error("strategy missing from summary")
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	for i := 0; i < 10; i++ {
		rec := record(fmt.Sprintf("plan-%02d", i), step(1, "n1", model.ImpactGreen))
		rec.StoredAt = time.Now().UTC().Add(time.Duration(i) * time.Second)
		mustSave(t, s, rec, true)
	}

	removed, err := s.Prune(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 7 {
		t.Errorf("pruned %d, want 7", removed)
	}

	plans, err := s.List(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 3 {
		t.Fatalf("remaining = %d, want 3", len(plans))
	}
	if plans[0].ID != "plan-09" {
		t.Errorf("newest retained = %s, want plan-09", plans[0].ID)
	}

	// Cascade must take the steps with the plan, or the table grows forever.
	if _, err := s.ByID(ctx, "plan-00"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("pruned plan still readable: %v", err)
	}
}

func TestPruneWithZeroKeepsEverything(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mustSave(t, s, record("p1", step(1, "n1", model.ImpactGreen)), true)

	if removed, err := s.Prune(ctx, 0); err != nil || removed != 0 {
		t.Errorf("Prune(0) removed=%d err=%v; retention must be opt-in", removed, err)
	}
}

func TestMissingPlanIsNotFound(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	if _, err := s.Latest(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("empty store Latest = %v, want ErrNotFound", err)
	}
	if _, err := s.ByID(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ByID = %v, want ErrNotFound", err)
	}
}

// Plan history is the audit trail. A restart must not lose it.
func TestDataSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "persist.db")

	first, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mustSave(t, first, record("durable", step(1, "n1", model.ImpactRed)), true)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	if err := second.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	got, err := second.Latest(ctx)
	if err != nil {
		t.Fatalf("plan did not survive reopen: %v", err)
	}
	if got.Plan.ID != "durable" || len(got.Plan.Steps) != 1 {
		t.Errorf("recovered plan = %+v", got.Plan)
	}
	if got.Plan.Steps[0].Impact != model.ImpactRed {
		t.Error("step rating did not survive")
	}
}

func TestSaveRejectsNilPlan(t *testing.T) {
	if _, err := openTemp(t).Save(context.Background(), store.Record{}); err == nil {
		t.Error("expected an error for a nil plan")
	}
}

func mustSave(t *testing.T, s *sqlitestore.Store, rec store.Record, wantStored bool) {
	t.Helper()
	stored, err := s.Save(context.Background(), rec)
	if err != nil {
		t.Fatalf("save %s: %v", rec.Plan.ID, err)
	}
	if stored != wantStored {
		t.Fatalf("save %s: stored=%v, want %v", rec.Plan.ID, stored, wantStored)
	}
}

// An unchanged plan is the strongest evidence the plan is current, so
// re-confirming one must refresh stored_at.
//
// It did not, and the UI's staleness warning consequently fired hardest on the
// healthiest possible state: a plan the planner had re-verified seconds ago
// read as nineteen hours old, because dedup skipped the write entirely.
func TestReconfirmingAPlanRefreshesStoredAt(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	rec := record("plan-1", step(1, "a", model.ImpactGreen))
	rec.StoredAt = time.Now().UTC().Add(-2 * time.Hour)
	if written, err := db.Save(ctx, rec); err != nil || !written {
		t.Fatalf("first save: written=%v err=%v", written, err)
	}

	// Same content hash: dedup, no new row — but the timestamp must move.
	again := record("plan-1", step(1, "a", model.ImpactGreen))
	again.StoredAt = time.Now().UTC()
	written, err := db.Save(ctx, again)
	if err != nil {
		t.Fatal(err)
	}
	if written {
		t.Error("an identical plan was written as a new row")
	}

	got, err := db.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if age := time.Since(got.StoredAt); age > time.Minute {
		t.Errorf("storedAt is %s old after re-confirmation; it should be fresh", age.Round(time.Second))
	}
}

// Rows written before compression must still be readable.
//
// gzip has a two-byte magic number, so the blob format is self-describing and
// no migration is needed. That matters more here than usual: plan history is
// an audit trail, and a schema bump would have meant rewriting every
// historical row to read it back.
func TestUncompressedRowsAreStillReadable(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	rec := record("legacy-plan", step(1, "a", model.ImpactGreen))
	if _, err := db.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}

	// Rewrite the blobs as plain JSON, exactly as a pre-compression build did.
	snapJSON, err := json.Marshal(rec.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	analysisJSON, err := json.Marshal(rec.Analysis)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ExecForTest(ctx,
		`UPDATE plans SET snapshot = ?, analysis = ? WHERE id = ?`,
		snapJSON, analysisJSON, "legacy-plan"); err != nil {
		t.Fatal(err)
	}

	got, err := db.ByID(ctx, "legacy-plan")
	if err != nil {
		t.Fatalf("a plain-JSON row became unreadable: %v", err)
	}
	if got.Snapshot == nil || len(got.Snapshot.Nodes) != len(rec.Snapshot.Nodes) {
		t.Errorf("snapshot did not survive: %+v", got.Snapshot)
	}
}

// Compression must not change what a caller sees.
func TestCompressionRoundTripsExactly(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	rec := record("plan-rt", step(1, "a", model.ImpactGreen))
	if _, err := db.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := db.ByID(ctx, "plan-rt")
	if err != nil {
		t.Fatal(err)
	}

	want, _ := json.Marshal(rec.Snapshot)
	back, _ := json.Marshal(got.Snapshot)
	if string(want) != string(back) {
		t.Errorf("snapshot changed through storage:\n want %s\n got  %s", want, back)
	}

	// And the stored bytes really are compressed.
	var size int
	if err := db.QueryRowForTest(ctx,
		`SELECT length(snapshot) FROM plans WHERE id = ?`, "plan-rt").Scan(&size); err != nil {
		t.Fatal(err)
	}
	if size >= len(want) {
		t.Errorf("stored blob is %d bytes against %d of JSON; it is not compressed", size, len(want))
	}
}

// On the dedup path, a snapshot carrying usage data must refresh the stored
// blob: usage changes every cycle while the plan does not — that is what a
// steady cluster IS — so without this, the right-sizing report serves
// measurements from whenever the plan last changed, aging without bound.
func TestDedupRefreshesSnapshotWhenUsageIsCollected(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	snap1 := &model.ClusterSnapshot{
		TakenAt:      time.Now().UTC(),
		HasUsageData: true,
		Nodes:        []model.Node{{Name: "a"}},
	}
	plan := &model.Plan{ID: "same-plan", Status: model.PlanValid,
		GeneratedAt: time.Now().UTC(), SnapshotTakenAt: snap1.TakenAt}
	if _, err := db.Save(ctx, store.Record{Plan: plan, Snapshot: snap1,
		Analysis: &constraints.Analysis{}, Strategy: "t"}); err != nil {
		t.Fatal(err)
	}

	// Same plan, fresher usage: the dedup path must carry the new snapshot.
	snap2 := &model.ClusterSnapshot{
		TakenAt:      time.Now().UTC().Add(30 * time.Second),
		HasUsageData: true,
		Nodes:        []model.Node{{Name: "a", Usage: &model.Resources{MilliCPU: 777}}},
	}
	stored, err := db.Save(ctx, store.Record{Plan: plan, Snapshot: snap2,
		Analysis: &constraints.Analysis{}, Strategy: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if stored {
		t.Fatal("fixture broke: the second save was not a dedup")
	}

	rec, err := db.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Snapshot.Nodes[0].Usage == nil || rec.Snapshot.Nodes[0].Usage.MilliCPU != 777 {
		t.Error("the stored snapshot kept its stale usage; the right-sizing report ages without bound on steady clusters")
	}
}

// An upgraded planner re-verifying an identical plan under a new ceiling must
// not leave the stored row describing the old policy. Found live: the first
// deploy of the packing ceiling drew no Wells line, because the plan's steps
// had not changed and dedup kept the pre-ceiling row.
func TestDedupCarriesTheCeilingForward(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	rec := record("same-hash", step(1, "n1", model.ImpactGreen))
	rec.Plan.PackCeiling = 0
	mustSave(t, s, rec, true)

	rec.Plan.PackCeiling = 0.85
	if stored, err := s.Save(ctx, rec); err != nil || stored {
		t.Fatalf("identical steps must dedup: stored=%v err=%v", stored, err)
	}

	got, err := s.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan.PackCeiling != 0.85 {
		t.Errorf("stored ceiling = %v, want 0.85 — the dedup touch dropped the policy", got.Plan.PackCeiling)
	}
}

// A better explanation of the same drain must reach an existing plan.
//
// planID hashes the strategy, sequence numbers, target nodes and moves — the
// actions — and deliberately not the rationale, impact or reasons, which
// describe those actions. That is the right identity: the same drains are the
// same plan however they are worded.
//
// But it means an upgraded planner that explains a step better produces the
// same id, takes the dedup path, and used to leave the old wording in place
// forever. Observed: a fix that stopped a rationale repeating itself shipped,
// ran against a live cluster, and changed nothing — every plan it applied to
// hashed the same as before, so the store kept the stutter.
func TestDedupRefreshesWhatAStepSays(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	before := record("samesteps", step(1, "n1", model.ImpactYellow))
	before.Plan.Steps[0].Rationale = "Rated Yellow because: X. Also: X."
	mustSave(t, s, before, true)

	// Same actions, better words — and a changed rating, which is the same
	// class of thing: a judgement about the step rather than the step itself.
	after := record("samesteps", step(1, "n1", model.ImpactGreen))
	after.Plan.Steps[0].Rationale = "Rated Green because: X."
	after.Plan.Steps[0].Reasons = []model.ImpactReason{
		{Kind: "BlastRadius", Subject: "n1", Detail: "moves 1 pod"},
	}
	if stored, err := s.Save(ctx, after); err != nil || stored {
		t.Fatalf("identical actions should dedup: stored=%v err=%v", stored, err)
	}

	got, err := s.Latest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Plan.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(got.Plan.Steps))
	}
	if got.Plan.Steps[0].Rationale != "Rated Green because: X." {
		t.Errorf("rationale stale after dedup: %q — a wording fix would never reach an "+
			"existing plan", got.Plan.Steps[0].Rationale)
	}
	if got.Plan.Steps[0].Impact != model.ImpactGreen {
		t.Errorf("impact stale after dedup: %q, want Green", got.Plan.Steps[0].Impact)
	}
	if len(got.Plan.Steps[0].Reasons) != 1 {
		t.Errorf("reasons stale after dedup: %d, want 1", len(got.Plan.Steps[0].Reasons))
	}
}
