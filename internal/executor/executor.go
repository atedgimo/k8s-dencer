// Package executor performs the consolidation steps an operator has approved.
//
// This is the only package in k8s-dencer that changes a cluster, and it runs as
// its own workload with its own ServiceAccount holding the only grant of
// pods/eviction. It has no HTTP surface at all: work arrives by claiming a row
// from the shared store, so the one component that can drain nodes cannot be
// reached over the network.
//
// The execution model is deliberately pessimistic:
//
//   - The Safety Guard is consulted before every step and before every
//     individual eviction, always against freshly-read state.
//   - Eviction goes through the policy/v1 eviction API, so the API server
//     enforces PodDisruptionBudgets. A pod delete would bypass them.
//   - Reality is verified after every step. The guard predicts that pods will
//     fit; this confirms they actually landed, and aborts if they did not.
//   - Abort means cordon-reversal, not rollback. Evicted pods cannot be
//     un-evicted, and pretending otherwise would be a lie in the docs and a
//     surprise at three in the morning.
package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/impact"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/planner"
	"github.com/atedgimo/k8s-dencer/internal/safety"
	"github.com/atedgimo/k8s-dencer/internal/store"
	"github.com/atedgimo/k8s-dencer/internal/telemetry"
)

// Options configure an executor.
type Options struct {
	// Worker identifies this executor in claims and audit events, so a run can
	// be traced to a pod.
	Worker string

	// Limits are the Safety Guard's rails.
	Limits safety.Limits

	// Metrics receives run outcomes, guard refusals and eviction timings.
	// Defaults to a private set, so tests and callers that do not care can
	// leave it nil.
	Metrics *telemetry.Metrics

	// Reclamations records drained nodes so the planner can later observe
	// whether anything removed them. Nil disables tracking.
	Reclamations store.ReclamationStore

	// StepTimeout bounds a single step end to end. Exceeding it aborts.
	StepTimeout time.Duration

	// SettleTimeout bounds waiting for evicted pods to be replaced.
	SettleTimeout time.Duration

	// PollInterval is how often cluster state is re-read while waiting.
	PollInterval time.Duration

	// Windows unlock Red steps. Nil means none are configured, which keeps the
	// Phase 2 behaviour of refusing Red outright.
	Windows safety.Windows

	// Planner configures the local re-planning a converge run does each
	// round. The same knobs as the planner component (min node age, excluded
	// namespaces), because a converge step must be one the planner could have
	// proposed — the loop changes when planning happens, not what is
	// plannable.
	Planner planner.Options

	// Classifier rates the steps a converge run plans, with the same
	// thresholds as the planner component, so a step is Yellow here exactly
	// when the plan on the operator's screen would have called it Yellow.
	Classifier impact.Classifier

	// Readiness is how a replacement pod is judged recovered. Defaults to
	// Ready; only the KWOK overlay sets Running.
	Readiness Readiness
}

// withDefaults fills unset values with conservative ones.
func (o Options) withDefaults() Options {
	if o.Metrics == nil {
		// A private set rather than nil checks at every call site. Tests get
		// working counters they can assert on, and production always passes
		// the real one.
		o.Metrics = telemetry.NewMetrics(telemetry.ComponentExecutor)
	}
	if o.Worker == "" {
		o.Worker = "executor"
	}
	if o.StepTimeout <= 0 {
		o.StepTimeout = 10 * time.Minute
	}
	if o.SettleTimeout <= 0 {
		o.SettleTimeout = 5 * time.Minute
	}
	if o.Classifier == (impact.Classifier{}) {
		// A zero-value Classifier has zero thresholds WITHOUT impact.New's
		// defaulting, which makes RedPodsMoved 0 — every step "at or above"
		// it, everything Red. Unset must never mean the strangest option:
		// default through the constructor, the same way the planner does.
		o.Classifier = impact.New(impact.Thresholds{})
	}
	if o.Planner.MinNodeAge == 0 && o.Planner.ExcludeNodeLabels == nil {
		// The zero Options would plan with no minimum node age and no
		// control-plane exclusion — more permissive than any configured
		// planner. Default to the planner's own defaults instead.
		o.Planner = planner.DefaultOptions()
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 2 * time.Second
	}
	if o.Readiness == "" {
		// Defaults to the strict criterion. An unset value must never mean
		// the weaker one.
		o.Readiness = ReadinessReady
	}
	return o
}

// Executor claims runs and performs them.
type Executor struct {
	cluster Cluster
	runs    store.ExecutionStore
	plans   store.Store
	guard   *safety.Guard
	log     *slog.Logger
	opts    Options
	metrics *telemetry.Metrics
	// reclamations is optional: nil disables tracking rather than failing, so
	// a caller that has not wired a store still drains.
	reclamations store.ReclamationStore
}

// New builds an executor.
func New(cluster Cluster, runs store.ExecutionStore, plans store.Store, log *slog.Logger, opts Options) *Executor {
	opts = opts.withDefaults()
	guard := safety.New(opts.Limits)
	if opts.Windows != nil {
		guard = guard.WithWindows(opts.Windows)
	}
	return &Executor{
		cluster:      cluster,
		runs:         runs,
		plans:        plans,
		guard:        guard,
		log:          log,
		opts:         opts,
		metrics:      opts.Metrics,
		reclamations: opts.Reclamations,
	}
}

// Poll claims and performs at most one run, reporting whether it did any work.
//
// One at a time, by construction: two consolidations in flight would each be
// making placement decisions the other invalidates.
func (e *Executor) Poll(ctx context.Context) (bool, error) {
	run, err := e.runs.Claim(ctx, e.opts.Worker)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	e.log.Info("claimed run", "run", run.ID, "plan", run.PlanID,
		"steps", run.Steps, "mode", run.Mode, "dryRun", run.DryRun, "actor", run.Actor)
	switch run.Mode {
	case store.RunModeConverge:
		e.converge(ctx, run)
	case store.RunModeDrain:
		e.drainNode(ctx, run)
	default:
		e.perform(ctx, run)
	}
	return true, nil
}

// Run polls until the context is cancelled.
func (e *Executor) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				worked, err := e.Poll(ctx)
				if err != nil {
					e.log.Error("poll failed", "error", err)
					break
				}
				if !worked {
					break
				}
			}
		}
	}
}

// perform executes a claimed run and closes it out.
func (e *Executor) perform(ctx context.Context, run store.Run) {
	e.event(ctx, run, store.RunEvent{
		Action: "Claim",
		Message: fmt.Sprintf("run claimed by %s for %s (steps %v of plan %s)%s",
			e.opts.Worker, run.Actor, run.Steps, run.PlanID, dryRunSuffix(run.DryRun)),
	})

	rec, err := e.plans.ByID(ctx, run.PlanID)
	if err != nil {
		e.fail(ctx, run, fmt.Sprintf("plan %s could not be loaded: %v", run.PlanID, err))
		return
	}

	state := safety.RunState{}
	for _, seq := range run.Steps {
		step, ok := findStep(rec.Plan, seq)
		if !ok {
			e.fail(ctx, run, fmt.Sprintf("plan %s has no step %d", run.PlanID, seq))
			return
		}

		stepCtx, cancel := context.WithTimeout(ctx, e.opts.StepTimeout)
		err := e.performStep(stepCtx, run, step, &state)
		cancel()

		if err != nil {
			var blocked *safety.Blocked
			if errors.As(err, &blocked) {
				// The rails did their job. Distinct from a failure so an
				// operator can tell "we protected you" from "something broke".
				e.finish(ctx, run, store.RunBlocked, fmt.Sprintf(
					"stopped at step %d: %s", seq, blocked.Reason))
				return
			}
			e.finish(ctx, run, store.RunFailed, fmt.Sprintf(
				"failed at step %d: %v", seq, err))
			return
		}
	}

	e.finish(ctx, run, store.RunSucceeded, fmt.Sprintf(
		"completed %d step(s), draining %d node(s)%s",
		len(run.Steps), state.NodesDrainedSoFar, dryRunSuffix(run.DryRun)))
}

// performStep drains one node.
func (e *Executor) performStep(ctx context.Context, run store.Run, step model.PlanStep, state *safety.RunState) error {
	live, err := e.cluster.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("read cluster state: %w", err)
	}

	// Gate first, against state read moments ago rather than the plan's.
	if err := e.guard.CheckStep(ctx, step, live, *state); err != nil {
		e.blocked(ctx, run, step, err)
		return err
	}

	if run.DryRun {
		return e.rehearse(ctx, run, step, live, state)
	}

	if err := e.cluster.Cordon(ctx, step.TargetNode); err != nil {
		return fmt.Errorf("cordon %s: %w", step.TargetNode, err)
	}
	e.event(ctx, run, store.RunEvent{
		Step: step.SequenceNumber, Node: step.TargetNode, Action: "Cordon",
		Message: fmt.Sprintf("%s marked unschedulable", step.TargetNode),
	})

	// From here the node is cordoned, so every exit path must either finish the
	// drain or restore schedulability.
	if err := e.drain(ctx, run, step, state); err != nil {
		e.abort(ctx, run, step, err)
		return err
	}

	state.NodesDrainedSoFar++
	e.event(ctx, run, store.RunEvent{
		Step: step.SequenceNumber, Node: step.TargetNode, Action: "Drained",
		Message: fmt.Sprintf("%s drained and left cordoned", step.TargetNode),
	})
	return nil
}

// rehearse performs a dry run: the full guard chain and the same event stream,
// with nothing touched.
//
// Emitting identical events matters — the UI renders a rehearsal and a real run
// with the same component, so what an operator previews is what they will see.
func (e *Executor) rehearse(ctx context.Context, run store.Run, step model.PlanStep,
	live *model.ClusterSnapshot, state *safety.RunState) error {

	pods := movablePodsOn(live, step.TargetNode)
	e.event(ctx, run, store.RunEvent{
		Step: step.SequenceNumber, Node: step.TargetNode, Action: "Cordon",
		Message: fmt.Sprintf("would mark %s unschedulable (dry run)", step.TargetNode),
	})

	for _, pod := range pods {
		if err := e.guard.CheckEviction(pod, live); err != nil {
			e.blocked(ctx, run, step, err)
			return err
		}
		e.event(ctx, run, store.RunEvent{
			Step: step.SequenceNumber, Node: step.TargetNode, Pod: pod.Key(),
			Action:  "Evict",
			Message: fmt.Sprintf("would evict %s (dry run)", pod.Key()),
		})
	}

	state.NodesDrainedSoFar++
	e.event(ctx, run, store.RunEvent{
		Step: step.SequenceNumber, Node: step.TargetNode, Action: "Drained",
		Message: fmt.Sprintf("would drain %s, moving %d pod(s) (dry run)",
			step.TargetNode, len(pods)),
	})
	return nil
}

// drain evicts every movable pod on the node, one at a time.
func (e *Executor) drain(ctx context.Context, run store.Run, step model.PlanStep, state *safety.RunState) error {
	live, err := e.cluster.Snapshot(ctx)
	if err != nil {
		return fmt.Errorf("read cluster state: %w", err)
	}

	// Recorded before anything is evicted, so recovery is measured against the
	// workloads as they were, not as they became mid-drain.
	before := healthyByOwner(live, e.opts.Readiness)
	pods := movablePodsOn(live, step.TargetNode)
	affected := map[string]bool{}

	for _, pod := range pods {
		// Fresh state per pod: evicting the first of a set changes the PDB
		// headroom for the rest, so a batch check would authorise disruptions
		// the budget no longer permits.
		live, err := e.cluster.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("read cluster state: %w", err)
		}
		current, still := findPod(live, pod.Namespace, pod.Name)
		if !still {
			continue // already gone; nothing to do
		}
		if err := e.guard.CheckEviction(current, live); err != nil {
			e.blocked(ctx, run, step, err)
			return err
		}

		evictStart := time.Now()
		if err := e.cluster.Evict(ctx, pod.Namespace, pod.Name); err != nil {
			// The API server can refuse on PDB grounds even when the
			// pre-flight check passed — state can change between the two, and
			// the API server is the authority. That refusal is a guard verdict,
			// not a fault, so it is audited as one.
			var blocked *safety.Blocked
			if errors.As(err, &blocked) {
				e.metrics.EvictionsTotal.WithLabelValues("blocked").Inc()
				e.blocked(ctx, run, step, blocked)
				return blocked
			}
			e.metrics.EvictionsTotal.WithLabelValues("error").Inc()
			return fmt.Errorf("evict %s: %w", pod.Key(), err)
		}
		if k := ownerKey(pod); k != "" {
			affected[k] = true
		}
		e.event(ctx, run, store.RunEvent{
			Step: step.SequenceNumber, Node: step.TargetNode, Pod: pod.Key(),
			Action:  "Evict",
			Message: fmt.Sprintf("evicted %s", pod.Key()),
		})

		if err := e.waitGone(ctx, pod); err != nil {
			e.metrics.EvictionsTotal.WithLabelValues("timeout").Inc()
			return err
		}
		// Measured to the pod actually being gone, not to the API call
		// returning. The call returns as soon as the eviction is accepted; the
		// wait afterwards is where a long grace period or a slow shutdown
		// shows up, and that is the part that stretches a drain.
		e.metrics.EvictionDuration.Observe(time.Since(evictStart).Seconds())
		e.metrics.EvictionsTotal.WithLabelValues("evicted").Inc()
	}

	recoverStart := time.Now()
	if err := e.verifyRecovered(ctx, run, step, before, affected); err != nil {
		return err
	}
	e.metrics.RecoveryWaitSeconds.Observe(time.Since(recoverStart).Seconds())
	e.metrics.NodesDrainedTotal.Inc()
	e.recordDrain(ctx, run, step)
	return nil
}

// recordDrain opens a reclamation record for the node just emptied.
//
// Draining is where this component's responsibility ends: something else
// removes the machine, and whether anything does is the question the product
// could not previously answer. The record is opened here, at the one moment we
// know for certain the node is empty and cordoned, and the planner closes it
// when the node disappears or comes back.
//
// A store failure is logged and swallowed, exactly as the audit trail is: the
// node is already drained, and abandoning a completed step to preserve a
// measurement would be the worse trade.
func (e *Executor) recordDrain(ctx context.Context, run store.Run, step model.PlanStep) {
	if e.reclamations == nil || step.TargetNode == "" {
		return
	}
	// Detached: a cancelled context must not lose the record of work that
	// already happened.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	// The node's allocatable, captured now because there is no later: once
	// something removes the node, its capacity record goes with it, and the
	// savings ledger would be left estimating the very number it exists to
	// measure.
	var cpuMilli, memBytes int64
	if snap, err := e.cluster.Snapshot(writeCtx); err == nil {
		for _, n := range snap.Nodes {
			if n.Name == step.TargetNode {
				cpuMilli, memBytes = n.Allocatable.MilliCPU, n.Allocatable.MemoryBytes
				break
			}
		}
	}

	err := e.reclamations.RecordDrain(writeCtx, store.Reclamation{
		Node:      step.TargetNode,
		DrainedAt: time.Now().UTC(),
		RunID:     run.ID,
		PlanID:    run.PlanID,
		Step:      step.SequenceNumber,
		CPUMilli:  cpuMilli,
		MemBytes:  memBytes,
	})
	if err != nil {
		e.log.Error("could not record the drain for reclamation tracking",
			"node", step.TargetNode, "run", run.ID, "error", err)
	}
}

// waitGone blocks until the pod has left the API server's view.
//
// Asks about the one pod rather than reading the cluster. This used to take a
// full snapshot on every tick — every pod in the cluster, every two seconds,
// to answer a question about a single object.
func (e *Executor) waitGone(ctx context.Context, pod model.Pod) error {
	deadline := time.Now().Add(e.opts.SettleTimeout)
	for {
		present, err := e.cluster.PodPresent(ctx, pod.Namespace, pod.Name)
		if err != nil {
			return fmt.Errorf("check %s: %w", pod.Key(), err)
		}
		if !present {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not terminate within %s", pod.Key(), e.opts.SettleTimeout)
		}
		if err := sleep(ctx, e.opts.PollInterval); err != nil {
			return err
		}
	}
}

// verifyRecovered is the reality check that the design leans on instead of a
// higher-fidelity simulator.
//
// The guard predicted every pod would fit somewhere. This confirms the
// workloads actually got their replicas back — on some other node — and fails
// the step if they did not, rather than moving on to drain a second node while
// the first one's pods are stuck Pending.
func (e *Executor) verifyRecovered(ctx context.Context, run store.Run, step model.PlanStep,
	before map[string]int, affected map[string]bool) error {

	if len(affected) == 0 {
		return nil
	}
	deadline := time.Now().Add(e.opts.SettleTimeout)
	for {
		live, err := e.cluster.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("read cluster state: %w", err)
		}
		now := healthyByOwner(live, e.opts.Readiness)

		lagging := ""
		for owner := range affected {
			if now[owner] < before[owner] {
				lagging = fmt.Sprintf("%s has %d/%d healthy pod(s)", owner, now[owner], before[owner])
				break
			}
		}
		if lagging == "" {
			e.event(ctx, run, store.RunEvent{
				Step: step.SequenceNumber, Node: step.TargetNode, Action: "Verify",
				Message: fmt.Sprintf("all %d affected workload(s) recovered elsewhere", len(affected)),
			})
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("workloads did not recover within %s (%s): %s",
				e.opts.SettleTimeout, e.opts.Readiness, lagging)
		}
		if err := sleep(ctx, e.opts.PollInterval); err != nil {
			return err
		}
	}
}

// abort restores schedulability and records why.
//
// Uncordon uses a context detached from the step's deadline: the usual reason
// to abort is that the step timed out, and inheriting that expired context
// would leave the node cordoned — the one outcome abort exists to prevent.
func (e *Executor) abort(ctx context.Context, run store.Run, step model.PlanStep, cause error) {
	uncordonCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	if err := e.cluster.Uncordon(uncordonCtx, step.TargetNode); err != nil {
		e.log.Error("uncordon failed after abort", "node", step.TargetNode, "error", err)
		e.event(uncordonCtx, run, store.RunEvent{
			Step: step.SequenceNumber, Node: step.TargetNode, Level: store.EventError,
			Action: "Uncordon",
			Message: fmt.Sprintf("could not restore %s to schedulable: %v — it needs "+
				"`kubectl uncordon %s` by hand", step.TargetNode, err, step.TargetNode),
		})
		return
	}
	e.event(uncordonCtx, run, store.RunEvent{
		Step: step.SequenceNumber, Node: step.TargetNode, Action: "Uncordon",
		Message: fmt.Sprintf("aborted after %v; %s is schedulable again. Pods already "+
			"evicted were not restored — eviction cannot be undone",
			cause, step.TargetNode),
	})
}

func (e *Executor) blocked(ctx context.Context, run store.Run, step model.PlanStep, err error) {
	var b *safety.Blocked
	if !errors.As(err, &b) {
		return
	}
	e.metrics.GuardRefusalsTotal.WithLabelValues(string(b.Rule)).Inc()
	e.event(ctx, run, store.RunEvent{
		Step: step.SequenceNumber, Node: step.TargetNode, Level: store.EventBlocked,
		Action: "Guard", Rule: string(b.Rule), Message: b.Reason,
	})
}

func (e *Executor) fail(ctx context.Context, run store.Run, msg string) {
	e.event(ctx, run, store.RunEvent{Level: store.EventError, Action: "Fail", Message: msg})
	e.finish(ctx, run, store.RunFailed, msg)
}

func (e *Executor) finish(ctx context.Context, run store.Run, status store.RunStatus, summary string) {
	e.log.Info("run finished", "run", run.ID, "status", status, "summary", summary)
	e.metrics.RunsTotal.WithLabelValues(string(status)).Inc()
	// Detached: a cancelled context must not leave a run stuck Running forever.
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := e.runs.Finish(closeCtx, run.ID, status, summary); err != nil {
		e.log.Error("could not close run", "run", run.ID, "error", err)
	}
}

func (e *Executor) event(ctx context.Context, run store.Run, ev store.RunEvent) {
	ev.RunID = run.ID
	if err := e.runs.AppendEvent(ctx, ev); err != nil {
		// The audit trail is important but must not stop a drain in progress;
		// leaving a node half-drained to preserve a log entry is the worse
		// trade.
		e.log.Error("could not append audit event", "run", run.ID, "action", ev.Action, "error", err)
	}
}

func findStep(plan *model.Plan, seq int) (model.PlanStep, bool) {
	for _, s := range plan.Steps {
		if s.SequenceNumber == seq {
			return s, true
		}
	}
	return model.PlanStep{}, false
}

func findPod(snap *model.ClusterSnapshot, namespace, name string) (model.Pod, bool) {
	for _, p := range snap.Pods {
		if p.Namespace == namespace && p.Name == name {
			return p, true
		}
	}
	return model.Pod{}, false
}

func movablePodsOn(snap *model.ClusterSnapshot, node string) []model.Pod {
	var out []model.Pod
	for _, p := range snap.Pods {
		if p.NodeName == node && p.IsMovable() {
			out = append(out, p)
		}
	}
	return out
}

func healthyByOwner(snap *model.ClusterSnapshot, criterion Readiness) map[string]int {
	out := map[string]int{}
	for _, p := range snap.Pods {
		if k := ownerKey(p); k != "" && healthy(p, criterion) {
			out[k]++
		}
	}
	return out
}

func dryRunSuffix(dry bool) string {
	if dry {
		return " [dry run]"
	}
	return ""
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
