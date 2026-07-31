package executor

import (
	"context"
	"errors"
	"fmt"

	"github.com/atedgimo/k8s-dencer/internal/constraints"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/safety"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// drainNode performs a guarded drain of one operator-named node: kubectl
// drain with the rails. The step is synthesized rather than taken from a
// plan, because the operator chose the node — but it is synthesized honestly:
// classified with the same impact thresholds as everything else, so a drain
// that would move thirty pods is Red here exactly as it would be in a plan,
// and Red still needs a maintenance window. Naming the node is not a
// side-channel around the ratings.
func (e *Executor) drainNode(ctx context.Context, run store.Run) {
	if run.Node == "" {
		e.fail(ctx, run, "drain run names no node")
		return
	}

	e.event(ctx, run, store.RunEvent{
		Action: "Claim", Node: run.Node,
		Message: fmt.Sprintf("guarded drain of %s claimed by %s for %s%s",
			run.Node, e.opts.Worker, run.Actor, dryRunSuffix(run.DryRun)),
	})

	live, err := e.cluster.Snapshot(ctx)
	if err != nil {
		e.fail(ctx, run, fmt.Sprintf("read cluster state: %v", err))
		return
	}
	found := false
	for _, n := range live.Nodes {
		if n.Name == run.Node {
			found = true
			break
		}
	}
	if !found {
		e.fail(ctx, run, fmt.Sprintf("node %s does not exist", run.Node))
		return
	}

	step := model.PlanStep{SequenceNumber: 1, TargetNode: run.Node}
	// Rated like any planned step, against live state. The moves list is
	// what the classifier weighs, so it is filled with what is actually
	// there rather than left empty — an empty list would rate every drain
	// Green regardless of what it moves.
	for _, pod := range movablePodsOn(live, run.Node) {
		step.Moves = append(step.Moves, model.Move{
			Namespace: pod.Namespace, Pod: pod.Name, FromNode: run.Node,
			CPUMilli: pod.Requests.MilliCPU, MemoryBytes: pod.Requests.MemoryBytes,
		})
	}
	analysis := constraints.Analyze(live)
	plan := &model.Plan{Steps: []model.PlanStep{step}}
	e.opts.Classifier.ClassifyPlan(plan, live, analysis)
	step = plan.Steps[0]

	e.event(ctx, run, store.RunEvent{
		Step: 1, Node: run.Node, Action: "Plan",
		Message: fmt.Sprintf("drain of %s rated %s (%d movable pods)%s",
			run.Node, step.Impact, len(step.Moves), dryRunSuffix(run.DryRun)),
	})

	state := safety.RunState{}
	stepCtx, cancel := context.WithTimeout(ctx, e.opts.StepTimeout)
	err = e.performStep(stepCtx, run, step, &state)
	cancel()
	if err != nil {
		var blocked *safety.Blocked
		if errors.As(err, &blocked) {
			e.finish(ctx, run, store.RunBlocked, fmt.Sprintf(
				"drain of %s stopped: %s", run.Node, blocked.Reason))
			return
		}
		e.finish(ctx, run, store.RunFailed, fmt.Sprintf(
			"drain of %s failed: %v", run.Node, err))
		return
	}

	e.finish(ctx, run, store.RunSucceeded, fmt.Sprintf(
		"drained %s%s", run.Node, dryRunSuffix(run.DryRun)))
}
