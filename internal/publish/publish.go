// Package publish owns one planning cycle: snapshot, analyze, plan, classify,
// publish, persist, observe reclamations.
//
// Extracted from cmd/planner precisely because it was the least-tested code
// with the most consequential semantics. Two of this repo's worst bugs were
// not in any algorithm — they were in this wiring: what gets published when
// the plan does not change, and what still has to happen after the store's
// dedup says "nothing new". Those properties are now pinned by tests in this
// package instead of living as unstated behaviour in a main().
//
// The invariants, stated once:
//
//   - Save is called every cycle, changed plan or not. The store touches
//     stored_at on the dedup path, and that touch is the "still confirmed"
//     signal the whole freshness display rests on. Skipping Save on an
//     unchanged plan would silently resurrect the backwards staleness warning.
//   - Reclamations are observed before the dedup early-return, because a node
//     disappearing is exactly the kind of change that does not alter the plan.
//   - A store failure never stops planning. The in-memory plan is still
//     correct, and the next cycle retries the write.
//   - A snapshot failure leaves the previously published state in place.
//     Publishing nothing is recoverable; publishing an empty cluster would
//     invite a plan to drain everything.
package publish

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/impact"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/planner"
	"github.com/atedgimo/k8s-dencer/internal/reclaim"
	"github.com/atedgimo/k8s-dencer/internal/store"
	"github.com/atedgimo/k8s-dencer/internal/telemetry"
)

// Source produces cluster snapshots. Satisfied by *cluster.Collector; a stub
// in tests, which is the point of the indirection.
type Source interface {
	Snapshot(ctx context.Context) (*model.ClusterSnapshot, error)
}

// Publisher runs planning cycles and holds the latest published state.
type Publisher struct {
	Log        *slog.Logger
	Source     Source
	Strategy   planner.Strategy
	Options    planner.Options
	Classifier impact.Classifier
	DB         store.Store
	// Retain bounds plan history; passed through to Prune after each stored
	// plan.
	Retain  int
	Metrics *telemetry.Metrics

	latest         atomic.Pointer[model.ClusterSnapshot]
	latestAnalysis atomic.Pointer[constraints.Analysis]
	latestPlan     atomic.Pointer[model.Plan]

	// seenNodes is the previous cycle's fleet, kept so a node that leaves
	// without this product draining it can be noticed at all. Only the
	// planning loop touches it, and only from the one goroutine that runs
	// Cycle, so it needs no lock.
	seenNodes map[string]model.Node
}

// LatestSnapshot returns the last successfully collected snapshot, or nil.
func (p *Publisher) LatestSnapshot() *model.ClusterSnapshot { return p.latest.Load() }

// LatestAnalysis returns the last constraint analysis, or nil.
func (p *Publisher) LatestAnalysis() *constraints.Analysis { return p.latestAnalysis.Load() }

// LatestPlan returns the last computed plan, or nil.
func (p *Publisher) LatestPlan() *model.Plan { return p.latestPlan.Load() }

// Cycle performs one complete planning cycle. It never returns an error:
// every failure mode has a defined degraded behaviour, logged and counted,
// because the planning loop must survive anything short of process death.
func (p *Publisher) Cycle(ctx context.Context) {
	cycleStart := time.Now()

	snap, err := p.Source.Snapshot(ctx)
	if err != nil {
		p.Log.Error("snapshot failed", "error", err)
		p.Metrics.SnapshotFailure.Inc()
		return
	}
	p.latest.Store(snap)

	allocatable, requested := snap.Totals()
	cpu, mem, pods := requested.Ratio(allocatable)

	// Summed observed usage, zero when unmeasured — the sample records
	// HasUsage so zero can never masquerade as idle.
	var usedCPU, usedMem int64
	if snap.HasUsageData {
		for i := range snap.Nodes {
			if u := snap.Nodes[i].Usage; u != nil {
				usedCPU += u.MilliCPU
				usedMem += u.MemoryBytes
			}
		}
	}

	occupied := 0
	for _, n := range snap.Nodes {
		if !snap.RequestedOnNode(n.Name).IsZero() {
			occupied++
		}
	}

	blocking := 0
	for _, pdb := range snap.PDBs {
		if pdb.Blocks() {
			blocking++
		}
	}

	p.Log.Info("snapshot",
		"nodes", len(snap.Nodes),
		"nodesOccupied", occupied,
		"pods", len(snap.Pods),
		"pdbs", len(snap.PDBs),
		"pdbsBlocking", blocking,
		"cpuRequestedPct", pct(cpu),
		"memRequestedPct", pct(mem),
		"podSlotsUsedPct", pct(pods),
		"usageData", snap.HasUsageData,
	)
	p.Metrics.SnapshotNodes.Set(float64(len(snap.Nodes)))
	p.Metrics.SnapshotPods.Set(float64(len(snap.Pods)))

	analysis := constraints.Analyze(snap)
	p.latestAnalysis.Store(analysis)

	cs := analysis.Summarize()
	undrainable := 0
	for _, n := range snap.Nodes {
		if drainable, _ := analysis.NodeDrainable(n.Name); !drainable {
			undrainable++
		}
	}

	p.Log.Info("constraints",
		"movable", cs.Movable,
		"blocked", cs.Blocked,
		"stuck", cs.Stuck,
		"pdbBlocked", cs.PDBBlocked,
		"antiAffinity", cs.AntiAffinity,
		"spreadBound", cs.SpreadBound,
		"controllerPinned", cs.ControllerPin,
		"nodesUndrainable", undrainable,
	)

	plan, err := p.Strategy.Plan(snap, analysis, p.Options)
	if err != nil {
		p.Log.Error("planning failed", "error", err)
		return
	}
	// Rating happens after planning, never during it: the plan is the ideal
	// end state, and risk is a separate judgement laid over it.
	p.Classifier.ClassifyPlan(plan, snap, analysis)
	p.latestPlan.Store(plan)

	byRating := plan.CountByRating()
	p.Log.Info("plan",
		"id", plan.ID,
		"strategy", p.Strategy.Name(),
		"steps", len(plan.Steps),
		"nodesBefore", plan.NodesBefore,
		"nodesAfter", plan.NodesAfter,
		"reclaims", plan.ReclaimedNodes(),
		"green", byRating[model.ImpactGreen],
		"yellow", byRating[model.ImpactYellow],
		"red", byRating[model.ImpactRed],
	)

	// Set every rating explicitly, including the zeroes. Leaving a label unset
	// makes the series vanish from the scrape, and a missing series reads as a
	// gap in a graph rather than as "no Red steps" — the opposite of the
	// reassurance it should give.
	for _, r := range []model.ImpactRating{model.ImpactGreen, model.ImpactYellow, model.ImpactRed} {
		p.Metrics.PlanSteps.WithLabelValues(string(r)).Set(float64(byRating[r]))
	}
	p.Metrics.NodesReclaimed.Set(float64(plan.ReclaimedNodes()))
	p.Metrics.PlanProduced(time.Now())
	p.Metrics.PlanCycleTime.Observe(time.Since(cycleStart).Seconds())

	// One point on the timeline, every cycle — dedup included, because a
	// steady cluster still has a timeline and the History view exists to
	// draw it. Failures degrade to a gap in the chart, never to a stopped
	// planner.
	if ts, ok := p.DB.(store.SampleStore); ok {
		at := snap.TakenAt
		if at.IsZero() {
			// The collector always stamps TakenAt; a zero here (synthetic
			// snapshots, a future source that forgets) must not write a row
			// in year one that every range query then misses.
			at = time.Now().UTC()
		}
		if err := ts.SaveSample(ctx, store.Sample{
			TakenAt: at, Nodes: len(snap.Nodes), Pods: len(snap.Pods),
			CPUReqMilli: requested.MilliCPU, CPUAllocMilli: allocatable.MilliCPU,
			MemReqBytes: requested.MemoryBytes, MemAllocBytes: allocatable.MemoryBytes,
			CPUUsedMilli: usedCPU, MemUsedBytes: usedMem, HasUsage: snap.HasUsageData,
			Reclaimable: plan.ReclaimedNodes(),
		}); err != nil {
			p.Log.Warn("saving timeline sample failed", "error", err)
		}
		if pruned, err := ts.PruneSamples(ctx, time.Now().Add(-30*24*time.Hour)); err == nil && pruned > 0 {
			p.Log.Info("pruned timeline", "removed", pruned)
		}
	}

	// The planner is the only component watching nodes continuously, so it is
	// the one that can tell whether a node the executor drained was actually
	// removed. Done against the snapshot just taken, so it costs no API calls
	// — and done BEFORE Save's dedup early-return, because a node
	// disappearing is exactly the kind of change that does not alter the plan.
	p.observeReclamations(ctx, snap)

	stored, err := p.DB.Save(ctx, store.Record{
		Plan:     plan,
		Snapshot: snap,
		Analysis: analysis,
		Strategy: p.Strategy.Name(),
	})
	if err != nil {
		// A store failure must not stop planning: the in-memory plan is still
		// correct and the next cycle will retry the write.
		p.Log.Error("storing plan failed", "error", err)
		return
	}
	if !stored {
		// Same content hash as the previous write, so the cluster has not
		// changed in any way that alters the plan. Save has still touched
		// stored_at — that touch is the freshness signal, and it is why Save
		// is called unconditionally rather than guarded by a comparison here.
		return
	}
	p.Log.Info("plan stored", "id", plan.ID)

	if pruned, err := p.DB.Prune(ctx, p.Retain); err != nil {
		p.Log.Warn("pruning plan history failed", "error", err)
	} else if pruned > 0 {
		p.Log.Info("pruned plan history", "removed", pruned, "retained", p.Retain)
	}
}

// observeReclamations resolves drained nodes against the snapshot just taken:
// still present and cordoned means still awaiting, gone means reclaimed,
// present and schedulable again means returned.
func (p *Publisher) observeReclamations(ctx context.Context, snap *model.ClusterSnapshot) {
	tracker, ok := p.DB.(store.ReclamationStore)
	if !ok {
		return
	}

	p.recordExternalReclaims(ctx, tracker, snap)

	pending, err := tracker.PendingReclamations(ctx)
	if err != nil {
		p.Log.Error("could not read pending reclamations", "error", err)
		return
	}
	p.Metrics.NodesAwaitingReclamation.Set(float64(len(pending)))
	if len(pending) == 0 {
		return
	}

	now := time.Now().UTC()
	for _, t := range reclaim.Resolve(pending, snap, now) {
		if err := tracker.ResolveReclamation(ctx, t.Reclamation.Node, t.Reclamation.DrainedAt, t.Outcome, t.At); err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				p.Log.Error("could not resolve reclamation", "node", t.Reclamation.Node, "error", err)
			}
			continue
		}
		switch t.Outcome {
		case store.ReclaimedGone:
			// The moment the product exists to produce, and the first time it
			// has ever been recorded rather than assumed.
			p.Log.Info("node reclaimed", "node", t.Reclamation.Node,
				"after", t.Took.Round(time.Second), "run", t.Reclamation.RunID)
			p.Metrics.ReclamationSeconds.Observe(t.Took.Seconds())
		case store.ReclaimedReturned:
			p.Log.Info("drained node returned to service", "node", t.Reclamation.Node,
				"run", t.Reclamation.RunID)
			p.Metrics.NodesReturnedTotal.Inc()
		}
	}

	// Recount rather than subtract: a drain recorded by the executor between
	// the read above and now would make arithmetic wrong, and this gauge is
	// the one an operator alerts on.
	if remaining, err := tracker.PendingReclamations(ctx); err == nil {
		p.Metrics.NodesAwaitingReclamation.Set(float64(len(remaining)))
	}
}

// recordExternalReclaims notices nodes that left the cluster without this
// product draining them.
//
// Every managed cluster runs an autoscaler with its own opinion. On a real GKE
// cluster one marked two nodes for deletion and removed them in 64 seconds,
// while converge had just declined to touch either — and `dencer
// reclamations` went on reporting "No nodes have been drained yet" as the
// fleet shrank from six nodes to four in front of the operator.
//
// The ledger's contract is that it never overstates. Silence about capacity
// that genuinely left the cluster is the opposite failure, so these are
// recorded and held apart: what the cluster did, without claiming it.
func (p *Publisher) recordExternalReclaims(ctx context.Context, tracker store.ReclamationStore, snap *model.ClusterSnapshot) {
	current := make(map[string]model.Node, len(snap.Nodes))
	for _, n := range snap.Nodes {
		current[n.Name] = n
	}

	// The first cycle after a restart has no previous fleet to compare
	// against. Treating every node as newly absent would invent a reclamation
	// for the entire cluster, so the first cycle only observes.
	if p.seenNodes == nil {
		p.seenNodes = current
		return
	}

	// Ours are already tracked; a node the executor drained resolves through
	// the normal path and must not be counted twice.
	ours := map[string]bool{}
	if pending, err := tracker.PendingReclamations(ctx); err == nil {
		for _, r := range pending {
			ours[r.Node] = true
		}
	}

	now := time.Now().UTC()
	for name, was := range p.seenNodes {
		if _, still := current[name]; still || ours[name] {
			continue
		}
		// Capacity from the last snapshot that still had the node — the same
		// reason the executor captures it at drain time, for the same reason:
		// a departed node takes its capacity record with it.
		rec := store.Reclamation{
			Node:       name,
			DrainedAt:  now,
			ResolvedAt: &now,
			Outcome:    store.ReclaimedGone,
			CPUMilli:   was.Allocatable.MilliCPU,
			MemBytes:   was.Allocatable.MemoryBytes,
			// The shape leaves with the machine, so it is taken from the last
			// snapshot that still had it — same reason as the capacity.
			InstanceType: was.InstanceType(),
			CapacityType: was.CapacityType(),
			External:     true,
		}
		if err := tracker.RecordDrain(ctx, rec); err != nil {
			p.Log.Error("could not record externally reclaimed node", "node", name, "error", err)
			continue
		}
		if err := tracker.ResolveReclamation(ctx, name, rec.DrainedAt, store.ReclaimedGone, now); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			p.Log.Error("could not resolve externally reclaimed node", "node", name, "error", err)
		}
		p.Log.Info("node reclaimed by something else",
			"node", name, "cpuMilli", rec.CPUMilli,
			"note", "not drained by k8s-dencer; recorded separately")
	}

	p.seenNodes = current
}

func pct(f float64) string {
	return fmt.Sprintf("%.1f%%", f*100)
}
