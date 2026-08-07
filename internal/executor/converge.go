package executor

import (
	"context"
	"errors"
	"fmt"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/planner"
	"github.com/atedgimo/k8s-dencer/internal/safety"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// M25: closed-loop consolidation.
//
// A steps run executes a forecast: steps 2..N were computed against one
// snapshot, assuming 1..N-1 landed exactly as predicted. They may not have,
// because the plan does not tell the scheduler anything — the executor
// cordons and evicts, and where pods land is kube-scheduler's decision. The
// old answer to divergence was abort; converge's answer is to stop
// forecasting: observe, plan ONE step against what is actually true, execute
// it, settle, observe again.
//
// What makes this safe to run at all is that it can always stop and never
// wander:
//
//   - the ENVELOPE is the operator's consent, recorded on the run: at most
//     MaxNodes drained, nothing above MaxImpact executed. A steps run
//     approves a list; a converge run approves a policy, and the policy's
//     bounds are explicit because a defaulted consent is not consent.
//   - MONOTONIC PROGRESS: every round must strictly reduce the number of
//     occupied nodes, measured — not predicted — after the drain settles. The
//     round that fails to ends the run. This is the rail that kills
//     oscillation: drain A, scheduler fills B, "drain B" now looks appealing
//     — but if that dance frees nothing, the count does not fall, and the
//     loop halts instead of walking the cluster.
//   - NO SECOND VISIT: a node this run has already drained is never targeted
//     again, however drainable a re-plan says it looks.
//   - every step still passes the full Safety Guard against fresh state, and
//     Red still requires a maintenance window. The envelope tightens the
//     rails; it cannot loosen them.
//
// A dry-run converge rehearses one round and stops, saying so. Rehearsing
// further rounds would require pretending to know where evicted pods land,
// and a rehearsal built on a simulated scheduler is a forecast wearing a
// safety vest — the exact thing this mode exists to retire.

// maxConvergeRounds is an absolute bound above any envelope, so a bug in the
// progress accounting costs a bounded number of drains, not an estate.
const maxConvergeRounds = 50

// converge runs the closed loop for one claimed run.
func (e *Executor) converge(ctx context.Context, run store.Run) {
	env := run.Envelope
	if env == nil || env.MaxNodes < 1 ||
		(env.MaxImpact != model.ImpactGreen && env.MaxImpact != model.ImpactYellow) {
		e.fail(ctx, run, "converge run has no valid envelope; refusing an unbounded loop")
		return
	}

	rounds := env.MaxNodes
	if rounds > maxConvergeRounds {
		rounds = maxConvergeRounds
	}

	e.event(ctx, run, store.RunEvent{
		Action: "Claim",
		Message: fmt.Sprintf(
			"converge run claimed by %s for %s: up to %d node(s), impact ceiling %s%s",
			e.opts.Worker, run.Actor, env.MaxNodes, env.MaxImpact, dryRunSuffix(run.DryRun)),
	})

	state := safety.RunState{}
	drained := map[string]bool{}

	for round := 1; round <= rounds; round++ {
		// Asked to stop. Honoured here, between rounds, because this is where
		// it can be honoured honestly: nothing is cordoned, nothing is
		// half-evicted, and the cluster is in a state somebody chose.
		if by, asked := e.stopRequested(ctx, run.ID); asked {
			e.finish(ctx, run, store.RunStopped, fmt.Sprintf(
				"stopped by %s after %d node(s); the cluster is left as this round found it",
				by, state.NodesDrainedSoFar))
			return
		}

		live, err := e.cluster.Snapshot(ctx)
		if err != nil {
			e.fail(ctx, run, fmt.Sprintf("round %d: read cluster state: %v", round, err))
			return
		}
		occBefore := occupiedNodes(live)

		step, why := e.planOneStep(live, drained)
		if step == nil {
			e.finish(ctx, run, store.RunSucceeded, fmt.Sprintf(
				"converged after %d node(s): %s", state.NodesDrainedSoFar, why))
			return
		}

		if exceedsCeiling(step.Impact, env.MaxImpact) {
			// The envelope working as designed: the next-best step needs more
			// consent than this run carries. Succeeded, not Blocked — nothing
			// broke, and "come back for a human" is the designed outcome.
			e.finish(ctx, run, store.RunSucceeded, fmt.Sprintf(
				"stopped at the envelope after %d node(s): the next step (drain %s) is rated %s, ceiling is %s",
				state.NodesDrainedSoFar, step.TargetNode, step.Impact, env.MaxImpact))
			return
		}

		e.event(ctx, run, store.RunEvent{
			Step: round, Node: step.TargetNode, Action: "Plan",
			Message: fmt.Sprintf("round %d: planned against live state — drain %s (%s, %d pods)%s",
				round, step.TargetNode, step.Impact, len(step.Moves), dryRunSuffix(run.DryRun)),
		})

		stepCtx, cancel := context.WithTimeout(ctx, e.opts.StepTimeout)
		err = e.performStep(stepCtx, run, *step, &state)
		cancel()
		if err != nil {
			var blocked *safety.Blocked
			if errors.As(err, &blocked) {
				e.finish(ctx, run, store.RunBlocked, fmt.Sprintf(
					"stopped in round %d after %d node(s): %s",
					round, state.NodesDrainedSoFar, blocked.Reason))
				return
			}
			e.finish(ctx, run, store.RunFailed, fmt.Sprintf(
				"failed in round %d after %d node(s): %v",
				round, state.NodesDrainedSoFar, err))
			return
		}
		drained[step.TargetNode] = true

		if run.DryRun {
			// One round is all a rehearsal can honestly claim; see the
			// package comment.
			e.finish(ctx, run, store.RunSucceeded, fmt.Sprintf(
				"dry run rehearsed round 1 (drain %s); a converge rehearsal cannot "+
					"simulate where pods land, so further rounds are not pretended",
				step.TargetNode))
			return
		}

		// The monotonic rail, against reality: re-read the cluster after the
		// drain settled and require the occupied-node count to have fallen.
		after, err := e.cluster.Snapshot(ctx)
		if err != nil {
			e.fail(ctx, run, fmt.Sprintf("round %d: read cluster state after drain: %v", round, err))
			return
		}
		occAfter := occupiedNodes(after)
		if occAfter >= occBefore {
			e.finish(ctx, run, store.RunSucceeded, fmt.Sprintf(
				"stopped after %d node(s): round %d freed no node (%d occupied before, %d after) — "+
					"the workload dance is not consolidating, so continuing would churn pods for nothing",
				state.NodesDrainedSoFar, round, occBefore, occAfter))
			return
		}
	}

	e.finish(ctx, run, store.RunSucceeded, fmt.Sprintf(
		"drained %d node(s), the envelope's limit; re-run to continue if the plan still shows more",
		state.NodesDrainedSoFar))
}

// planOneStep plans against live state and returns the single next step, or
// nil with the reason there is none. Nodes in skip are never proposed.
func (e *Executor) planOneStep(live *model.ClusterSnapshot, skip map[string]bool) (*model.PlanStep, string) {
	analysis := constraints.Analyze(live)
	plan, err := planner.Greedy{}.Plan(live, analysis, e.opts.Planner)
	if err != nil {
		return nil, fmt.Sprintf("planning failed: %v", err)
	}
	e.opts.Classifier.ClassifyPlan(plan, live, analysis)

	for i := range plan.Steps {
		if !skip[plan.Steps[i].TargetNode] {
			return &plan.Steps[i], ""
		}
	}
	if len(plan.Steps) == 0 {
		return nil, "no further step is worth taking; every remaining node is either needed or undrainable"
	}
	return nil, "every remaining candidate was already drained by this run"
}

// occupiedNodes counts nodes holding at least one movable pod — the quantity
// a consolidation exists to reduce, and the one the monotonic rail measures.
func occupiedNodes(snap *model.ClusterSnapshot) int {
	occupied := map[string]bool{}
	for _, p := range snap.Pods {
		if p.NodeName != "" && p.IsMovable() {
			occupied[p.NodeName] = true
		}
	}
	return len(occupied)
}

// exceedsCeiling reports whether a step's rating needs more consent than the
// envelope grants. Green < Yellow < Red, and an unknown rating exceeds
// everything — fail closed.
func exceedsCeiling(rating, ceiling model.ImpactRating) bool {
	rank := func(r model.ImpactRating) int {
		switch r {
		case model.ImpactGreen:
			return 0
		case model.ImpactYellow:
			return 1
		case model.ImpactRed:
			return 2
		default:
			return 3
		}
	}
	return rank(rating) > rank(ceiling)
}
