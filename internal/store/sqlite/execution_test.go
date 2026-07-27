package sqlite_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/store"
)

func sampleRun(planID string) store.Run {
	return store.Run{
		PlanID: planID, Steps: []int{1, 2, 3},
		Actor: "alice@example.com", ActorGroups: []string{"oidc:platform-sre"},
	}
}

func TestEnqueueAndReadBack(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	id, err := db.Enqueue(ctx, sampleRun("plan-1"))
	if err != nil {
		t.Fatal(err)
	}

	run, err := db.RunByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != store.RunPending {
		t.Errorf("status = %s, want Pending", run.Status)
	}
	if len(run.Steps) != 3 || run.Steps[2] != 3 {
		t.Errorf("steps round-tripped wrong: %v", run.Steps)
	}
	// The actor is the audit trail's answer to "under whose authority?" and
	// must survive the token that authorized it.
	if run.Actor != "alice@example.com" || len(run.ActorGroups) != 1 {
		t.Errorf("actor lost: %+v", run)
	}
}

// The property the whole queue design rests on. Two executors racing must not
// both take the same run, or a node gets drained twice at once.
func TestClaimIsAtomicUnderConcurrency(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	const runs = 20
	for i := range runs {
		if _, err := db.Enqueue(ctx, sampleRun("plan-1")); err != nil {
			t.Fatal(err)
		}
		_ = i
	}

	const workers = 8
	var (
		mu     sync.Mutex
		claims = map[string]string{} // run ID -> worker that claimed it
		dupes  []string
		wg     sync.WaitGroup
	)

	for w := range workers {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for {
				run, err := db.Claim(ctx, "worker-"+string(rune('a'+worker)))
				if errors.Is(err, store.ErrNotFound) {
					return
				}
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				mu.Lock()
				if prev, seen := claims[run.ID]; seen {
					dupes = append(dupes, run.ID+" claimed by "+prev+" and "+run.Worker)
				}
				claims[run.ID] = run.Worker
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	if len(dupes) > 0 {
		t.Fatalf("the same run was claimed twice: %v", dupes)
	}
	if len(claims) != runs {
		t.Errorf("claimed %d runs, want %d", len(claims), runs)
	}
}

func TestClaimTakesOldestFirstAndMarksItRunning(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	first := sampleRun("plan-1")
	first.RequestedAt = time.Now().UTC().Add(-time.Hour)
	firstID, err := db.Enqueue(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Enqueue(ctx, sampleRun("plan-1")); err != nil {
		t.Fatal(err)
	}

	claimed, err := db.Claim(ctx, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != firstID {
		t.Errorf("claimed %s, want the oldest run %s", claimed.ID, firstID)
	}
	if claimed.Status != store.RunRunning || claimed.Worker != "worker-1" {
		t.Errorf("claim did not mark the run running: %+v", claimed)
	}
	if claimed.StartedAt == nil {
		t.Error("claim did not set StartedAt")
	}
}

func TestClaimOnEmptyQueueReportsNotFound(t *testing.T) {
	if _, err := openTemp(t).Claim(context.Background(), "worker-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// Sequence numbers are assigned by the store so the audit trail stays ordered
// and gap-free regardless of who is writing.
func TestEventsAreSequencedAndOrdered(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	id, _ := db.Enqueue(ctx, sampleRun("plan-1"))

	for _, ev := range []store.RunEvent{
		{RunID: id, Action: "Claim", Message: "claimed"},
		{RunID: id, Step: 1, Node: "kwok-node-3", Action: "Cordon", Message: "cordoned"},
		{RunID: id, Step: 1, Node: "kwok-node-3", Pod: "app/web", Action: "Evict", Message: "evicted"},
		{RunID: id, Step: 2, Level: store.EventBlocked, Rule: "PDBHeadroom", Action: "Evict",
			Message: "blocked by PDB"},
	} {
		if err := db.AppendEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}

	events, err := db.Events(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}
	for i, ev := range events {
		if ev.Sequence != i+1 {
			t.Errorf("event %d has sequence %d", i, ev.Sequence)
		}
	}
	// The refusal must record which rail stopped it — that is the question
	// asked after an incident.
	last := events[3]
	if last.Level != store.EventBlocked || last.Rule != "PDBHeadroom" {
		t.Errorf("blocked event lost its rule: %+v", last)
	}
	if events[2].Pod != "app/web" || events[1].Node != "kwok-node-3" {
		t.Errorf("event subjects lost: %+v %+v", events[1], events[2])
	}
}

func TestFinishRejectsNonTerminalStatus(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	id, _ := db.Enqueue(ctx, sampleRun("plan-1"))

	if err := db.Finish(ctx, id, store.RunRunning, "nope"); err == nil {
		t.Error("Finish accepted a non-terminal status")
	}
	if err := db.Finish(ctx, id, store.RunBlocked, "stopped at step 4"); err != nil {
		t.Fatal(err)
	}
	run, _ := db.RunByID(ctx, id)
	if run.Status != store.RunBlocked || run.FinishedAt == nil {
		t.Errorf("run not closed: %+v", run)
	}
}

// Blocked is distinct from Failed on purpose: "the rails protected you" and
// "something broke" call for different responses from an operator.
func TestBlockedIsTerminalButDistinctFromFailed(t *testing.T) {
	if !store.RunBlocked.Terminal() || !store.RunFailed.Terminal() || !store.RunSucceeded.Terminal() {
		t.Error("a terminal status is not reporting as terminal")
	}
	if store.RunPending.Terminal() || store.RunRunning.Terminal() {
		t.Error("an in-flight status is reporting as terminal")
	}
	if store.RunBlocked == store.RunFailed {
		t.Error("Blocked and Failed must stay distinguishable")
	}
}

func TestActiveRunFindsPendingAndRunning(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if _, err := db.ActiveRun(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("empty store reported an active run: %v", err)
	}

	id, _ := db.Enqueue(ctx, sampleRun("plan-1"))
	if run, err := db.ActiveRun(ctx); err != nil || run.ID != id {
		t.Errorf("pending run not active: %v %+v", err, run)
	}
	if _, err := db.Claim(ctx, "worker-1"); err != nil {
		t.Fatal(err)
	}
	if run, err := db.ActiveRun(ctx); err != nil || run.ID != id {
		t.Errorf("running run not active: %v %+v", err, run)
	}
	if err := db.Finish(ctx, id, store.RunSucceeded, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ActiveRun(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("finished run still reported active: %v", err)
	}
}

// Doc §9 ties the audit log to the plan version and step IDs that authorized
// each action. Pruning a plan a run executed would leave that log pointing at
// nothing.
func TestPruneNeverDeletesAPlanThatWasExecuted(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	// Three plans, oldest first; a run against the oldest.
	var ids []string
	for i := range 3 {
		rec := record(string(rune('a' + i)))
		if _, err := db.Save(ctx, rec); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rec.Plan.ID)
	}
	if _, err := db.Enqueue(ctx, sampleRun(ids[0])); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Prune(ctx, 1); err != nil {
		t.Fatal(err)
	}

	// The executed plan survives even though only 1 was to be kept.
	if _, err := db.ByID(ctx, ids[0]); err != nil {
		t.Errorf("pruned a plan with an execution against it: %v", err)
	}
	// The untouched middle plan is gone as normal.
	if _, err := db.ByID(ctx, ids[1]); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected the un-executed plan to be pruned, got %v", err)
	}
}

// The v2 tables must be usable after a re-run of Migrate on a current schema,
// which is what every pod restart does.
func TestExecutionTablesSurviveReMigration(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	rec := record("plan-1")
	if _, err := db.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}
	if _, err := db.Enqueue(ctx, sampleRun(rec.Plan.ID)); err != nil {
		t.Errorf("v2 tables unusable after re-migration: %v", err)
	}
}
