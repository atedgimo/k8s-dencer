/**
 * The run screens (assets/design/README.md, 2a/2b/2c): when a run exists,
 * the Review destination becomes the run — rehearsal result, execution in
 * progress, or halted by the Safety Guard. One component, because the
 * executor deliberately emits identical events for a rehearsal and a real
 * run: what an operator previews is what they will see.
 *
 * 2b ships without Abort or "Pause after this step" — there is no backend
 * for either yet, and a control that does nothing would be worse than its
 * absence (user decision; feasibility discussion parked).
 *
 * 2c is the trust screen. It states what drifted, what was kept, that the
 * cordon was reverted, and offers exactly two ways forward. Deliberately no
 * force-drain: there is no override in this UI. A refusal is the product
 * working.
 */

import { useState } from "react";
import { GraphPayload, PlanStep, Reclamation, Run, RunEvent, api, formatBytes, formatCPU } from "../../api";

export type RunPhase = "pending" | "live" | "done" | "refused";

interface Props {
  run: Run;
  events: RunEvent[];
  active: boolean;
  graph: GraphPayload;
  steps: PlanStep[];
  /** True while the plan on screen is still the run's plan. */
  planMatches: boolean;
  /** Read-only session: the approve-into-a-real-run path is hidden. */
  readOnly?: boolean;
  /** node → its reclamation record; capacity measured at drain time. */
  reclaimed: Map<string, Reclamation>;
  onDismiss: () => void;
  onDrain: () => void;
  onRehearse: () => void;
  onRecompute: () => void;
  onOpenRecommendations: () => void;
}

export default function RunScreen(props: Props) {
  const { run, active } = props;
  if (active) return <Execution {...props} />;
  if (run.dryRun) return <RehearsalResult {...props} />;
  if (run.status === "Blocked" || run.status === "Failed") return <Halted {...props} />;
  return <Execution {...props} />; // a finished, successful run: 2b at rest
}

/* ------------------------------------------------------------- shared bits */

/**
 * The run's own view of its steps. Nodes come from the run's EVENTS first:
 * a finished run reloads the plan underneath this screen, and joining the
 * run's sequence numbers against the fresh plan silently renames every row
 * to some other node — found live, the trail saying himem-1 while the row
 * said spot-7. The plan is consulted only while it is still the run's plan
 * (planMatches), and then only for steps the events have not reached.
 */
interface StepModel {
  seq: number;
  node?: string;
  /** The plan's step, only while the plan on screen is the run's plan. */
  planned?: PlanStep;
}

function runSteps(run: Run, events: RunEvent[], steps: PlanStep[], planMatches: boolean): StepModel[] {
  return run.steps.map((seq) => {
    const fromEvents = events.find((e) => e.step === seq && e.node)?.node;
    const planned = planMatches ? steps.find((s) => s.sequenceNumber === seq) : undefined;
    return { seq, node: fromEvents ?? planned?.targetNode, planned };
  });
}

function stepEvents(events: RunEvent[], seq: number): RunEvent[] {
  return events.filter((e) => e.step === seq);
}

function drained(events: RunEvent[], seq: number): boolean {
  return stepEvents(events, seq).some((e) => e.action === "Drained");
}

function refused(events: RunEvent[], seq: number): boolean {
  return stepEvents(events, seq).some((e) => e.level === "Blocked" || e.level === "Error");
}

/** Pods this step moves: the plan's number while it exists, else the events'. */
function podsOf(m: StepModel, events: RunEvent[]): number {
  if (m.planned) return m.planned.moves.length;
  return stepEvents(events, m.seq).filter((e) => e.action === "Evict").length;
}

/**
 * What a drained node was worth. The reclamation ledger's measured capture
 * wins — a reclaimed node is gone from the live graph, and reading the graph
 * for it would quietly shrink the total as the run succeeds. The graph is
 * the fallback for nodes still present.
 */
function nodeWorth(
  graph: GraphPayload,
  reclaimed: Map<string, Reclamation>,
  node?: string,
): { cpu: number; mem: number } {
  if (node) {
    const r = reclaimed.get(node);
    if (r?.cpuMilli) return { cpu: r.cpuMilli, mem: r.memBytes ?? 0 };
    const n = graph.elements.find((e) => e.data.kind === "node" && e.data.label === node);
    if (n) return { cpu: n.data.cpuAllocatable ?? 0, mem: n.data.memAllocatable ?? 0 };
  }
  return { cpu: 0, mem: 0 };
}

function fmtTime(iso?: string): string {
  return iso ? new Date(iso).toLocaleTimeString([], { hour12: false }) : "";
}

/** The live trail, right-hand column of 2a and 2b. */
function Trail({ events, streaming }: { events: RunEvent[]; streaming: boolean }) {
  const download = () => {
    const text = events
      .map((e) => `${fmtTime(e.at)}  ${e.level === "Info" ? "" : e.level.toUpperCase() + " "}${e.message}`)
      .join("\n");
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `dencer-run-${events[0]?.runId ?? "trail"}.txt`;
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <div className="trailpane">
      <div className="trailpane-head">
        <span className="eyebrow mono">{streaming ? "Live trail" : "Trail"}</span>
        {streaming ? (
          <span className="trailpane-streaming">
            <span className="trailpane-dot" aria-hidden="true" />
            streaming
          </span>
        ) : (
          <button type="button" className="trailpane-copy" onClick={download}>
            Download
          </button>
        )}
      </div>
      <div className="trailpane-lines">
        {events.map((e) => (
          <div key={e.sequence} className="trailline">
            <span className="trailline-t">{fmtTime(e.at)}</span>
            <span
              className={
                "trailline-text" +
                (e.level === "Blocked"
                  ? " is-blocked"
                  : e.level === "Error"
                    ? " is-error"
                    : e.action === "Drained" || e.action === "Verify"
                      ? " is-strong"
                      : "")
              }
            >
              {e.message}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

/* -------------------------------------------------------------- 2a result */

function RehearsalResult({
  run,
  events,
  graph,
  steps,
  planMatches,
  reclaimed,
  readOnly,
  onDismiss,
  onDrain,
  onRehearse,
}: Props) {
  const mine = runSteps(run, events, steps, planMatches);
  const clean = mine.filter((m) => drained(events, m.seq));
  const blockedStep = mine.find((m) => refused(events, m.seq));
  const allClean = !blockedStep && clean.length === mine.length;
  const pods = mine.reduce((n, m) => n + podsOf(m, events), 0);
  let cpu = 0;
  let mem = 0;
  for (const m of clean) {
    const w = nodeWorth(graph, reclaimed, m.node);
    cpu += w.cpu;
    mem += w.mem;
  }
  const placed = events.filter((e) => e.action === "Evict").length;

  return (
    <div className="runscreen">
      <div className="runhead">
        <button type="button" className="runhead-back" onClick={onDismiss}>
          ← Back to review
        </button>
        <span className="runhead-sep" aria-hidden="true" />
        <span className="runhead-title">Rehearsal</span>
        <span className="runhead-meta mono">
          plan {run.planId.slice(0, 7)} · {mine.length} steps
        </span>
        <span className="runhead-when mono">{fmtTime(run.finishedAt)}</span>
      </div>

      <div className="runhero">
        <div className="runhero-lead">
          <div className={"runhero-eyebrow " + (allClean ? "is-safe" : "is-held")}>
            <span className="runhero-eyedot" aria-hidden="true" />
            <span className="eyebrow mono">
              {allClean
                ? `All ${mine.length} steps would succeed`
                : `Step ${blockedStep?.seq ?? "?"} would be refused`}
            </span>
          </div>
          <h2 className="runhero-headline">Nothing was cordoned. Nothing was evicted.</h2>
          <p className="runhero-sub">
            The Safety Guard ran in full against the live cluster
            {allClean
              ? " and every check passed. Placement was simulated against real node capacity — every pod found a home."
              : ". One step was refused; the trail names the rule, and the step goes back to the ledger as held back."}
          </p>
        </div>
        <div className="runhero-tiles">
          <div className={"runtile " + (allClean ? "runtile-safe" : "runtile-held")}>
            <span className="runtile-figure mono">
              {clean.length} / {mine.length}
            </span>
            <span className="runtile-label">steps clean</span>
          </div>
          <div className="runtile">
            <span className="runtile-figure mono">{placed}</span>
            <span className="runtile-label">pods placed</span>
          </div>
          <div className="runtile">
            <span className="runtile-figure mono">{formatCPU(cpu)} cores</span>
            <span className="runtile-label">would return</span>
          </div>
          <div className="runtile">
            <span className="runtile-figure mono">{formatBytes(mem)}</span>
            <span className="runtile-label">would return</span>
          </div>
        </div>
      </div>

      <div className="runbody">
        <div className="runsteps">
          <span className="eyebrow mono">Step by step</span>
          {mine.map((m) => {
            const ok = drained(events, m.seq);
            const bad = refused(events, m.seq);
            const d = stepEvents(events, m.seq).find((e) => e.action === "Drained");
            const n = podsOf(m, events);
            return (
              <div key={m.seq} className="runstep">
                <span className="runstep-n mono">{String(m.seq).padStart(2, "0")}</span>
                <div className="runstep-body">
                  <div className="runstep-line">
                    <span className="runstep-node mono">{m.node}</span>
                    <span className="runstep-pods mono">
                      {n} pod{n === 1 ? "" : "s"}
                    </span>
                  </div>
                  <span className="runstep-detail">{d?.message ?? ""}</span>
                </div>
                <span
                  className={"runstep-outcome " + (bad ? "is-held" : ok ? "is-safe" : "is-muted")}
                >
                  {bad ? "Would be refused" : ok ? "Would drain cleanly" : "Not reached"}
                </span>
              </div>
            );
          })}
          <div className="runnote">
            A rehearsal is a snapshot. If the cluster changes before you run, the Safety Guard
            re-checks each step and halts on the first refusal.
          </div>
        </div>
        <Trail events={events} streaming={false} />
      </div>

      <footer className="runfooter">
        <div className="runfooter-summary">
          <span className="runfooter-line">
            Rehearsal held for {mine.length} step{mine.length === 1 ? "" : "s"} · {pods} pod
            {pods === 1 ? "" : "s"}
          </span>
          <span className="runfooter-sub">
            {allClean
              ? "Approving carries this selection straight into a real run."
              : "Resolve the refusal, or drop that step from the selection."}
          </span>
        </div>
        <div className="runfooter-actions">
          <button type="button" className="btn" onClick={onDismiss}>
            Discard
          </button>
          <button type="button" className="btn reviewfooter-rehearse" onClick={onRehearse}>
            Rehearse again
          </button>
          {allClean && planMatches && !readOnly && (
            <button type="button" className="reviewfooter-drain" onClick={onDrain}>
              Drain {mine.length} node{mine.length === 1 ? "" : "s"}
            </button>
          )}
        </div>
      </footer>
    </div>
  );
}

/* ---------------------------------------------------------- 2b execution */

const PHASES = ["cordon", "evict", "verify", "reclaim"] as const;

function phaseStates(
  events: RunEvent[],
  seq: number,
  node: string | undefined,
  reclaimed: Map<string, Reclamation>,
  active: boolean,
): Record<(typeof PHASES)[number], RunPhase> {
  const mine = stepEvents(events, seq);
  const has = (action: string) => mine.some((e) => e.action === action && e.level === "Info");
  const bad = mine.some((e) => e.level === "Blocked" || e.level === "Error");
  const isDrained = has("Drained");
  const cordon = has("Cordon");
  const verify = has("Verify");
  const gone = node != null && reclaimed.get(node)?.outcome === "reclaimed";

  const started = mine.length > 0;
  const st = (done: boolean, liveWhen: boolean): RunPhase =>
    done ? "done" : bad ? "refused" : active && liveWhen ? "live" : "pending";

  return {
    cordon: st(cordon || isDrained, started && !cordon),
    evict: st(isDrained, cordon && !isDrained),
    verify: st(verify, isDrained && !verify),
    reclaim: st(gone, verify && !gone),
  };
}

/**
 * One control, not two.
 *
 * The design called for an Abort in the top bar and a "Pause after this step"
 * in the footer. Building both would imply a distinction that does not exist:
 * a pod already evicted cannot be un-evicted, so there is no aborting
 * mid-step, only declining to start the next one. An "Abort" that quietly
 * behaved like a pause would be this product promising an undo it does not
 * have — the one thing it must never do.
 *
 * So the label says exactly what happens, and the confirmation says what it
 * will not do.
 */
function StopButton({ runId, stopping }: { runId: string; stopping?: boolean }) {
  const [asked, setAsked] = useState(stopping ?? false);
  const [failed, setFailed] = useState(false);

  if (asked) {
    return (
      <span className="runhead-stopping" role="status">
        Stopping after this step — evictions already in flight will finish
      </span>
    );
  }

  return (
    <>
      <button
        type="button"
        className="btn runhead-stop"
        onClick={() => {
          setAsked(true);
          api.stopRun(runId).catch(() => {
            setAsked(false);
            setFailed(true);
          });
        }}
      >
        Stop after this step
      </button>
      {failed && <span className="runhead-stopfailed">Could not reach the backend — not stopped.</span>}
    </>
  );
}

function Execution({ run, events, active, graph, steps, planMatches, reclaimed, onDismiss }: Props) {
  const mine = runSteps(run, events, steps, planMatches);
  const done = mine.filter((m) => drained(events, m.seq));
  const current = active
    ? mine.find((m) => !drained(events, m.seq) && !refused(events, m.seq))
    : undefined;
  const podsTotal = mine.reduce((n, m) => n + podsOf(m, events), 0);
  const podsMoved = events.filter((e) => e.action === "Evict").length;
  let cpu = 0;
  let mem = 0;
  for (const m of done) {
    const w = nodeWorth(graph, reclaimed, m.node);
    cpu += w.cpu;
    mem += w.mem;
  }
  const phaseOf = current ? phaseStates(events, current.seq, current.node, reclaimed, active) : null;
  const currentPhase = phaseOf ? (PHASES.find((p) => phaseOf[p] === "live") ?? "cordon") : null;

  return (
    <div className="runscreen">
      <div className="runhead">
        {!active && (
          <>
            <button type="button" className="runhead-back" onClick={onDismiss}>
              ← Back to review
            </button>
            <span className="runhead-sep" aria-hidden="true" />
          </>
        )}
        <span className="runhead-title">
          {active ? `Draining ${mine.length} nodes` : `Run ${run.planId.slice(0, 7)}`}
        </span>
        <span className="runhead-meta mono">
          plan {run.planId.slice(0, 7)} · started {fmtTime(run.startedAt ?? run.requestedAt)} by{" "}
          {run.actor}
        </span>
        {active && <StopButton runId={run.id} stopping={run.stopRequested} />}
      </div>

      <div className="runhero runhero-col">
        <div className="runhero-row">
          <div className="runhero-lead">
            <span className="eyebrow mono">
              {active && current
                ? `Step ${current.seq} of ${mine.length}${currentPhase ? ` · ${currentPhase === "evict" ? "evicting" : currentPhase}` : ""}`
                : `Run ${run.status.toLowerCase()}`}
            </span>
            <h2 className="runhero-headline">
              {done.length} node{done.length === 1 ? "" : "s"} reclaimed,{" "}
              {mine.length - done.length} to go
            </h2>
          </div>
          <div className="runhero-stats">
            <div className="hero-stat">
              <span className="hero-stat-figure mono runstat-safe">{formatCPU(cpu)} cores</span>
              <span className="hero-stat-label">returned so far</span>
            </div>
            <span className="hero-stat-sep" aria-hidden="true" />
            <div className="hero-stat">
              <span className="hero-stat-figure mono runstat-safe">{formatBytes(mem)}</span>
              <span className="hero-stat-label">memory freed</span>
            </div>
            <span className="hero-stat-sep" aria-hidden="true" />
            <div className="hero-stat">
              <span className="hero-stat-figure mono">
                {podsMoved} of {podsTotal}
              </span>
              <span className="hero-stat-label">pods moved</span>
            </div>
          </div>
        </div>
        <div className="runprogress" aria-hidden="true">
          {done.length > 0 && <div className="runprogress-done" style={{ flex: done.length }} />}
          {active && current && <div className="runprogress-live" style={{ flex: 1 }} />}
          {mine.length - done.length - (active && current ? 1 : 0) > 0 && (
            <div
              className="runprogress-rest"
              style={{ flex: mine.length - done.length - (active && current ? 1 : 0) }}
            />
          )}
        </div>
      </div>

      <div className="runbody">
        <div className="runsteps">
          <span className="eyebrow mono">Steps</span>
          {mine.map((m) => {
            const ph = phaseStates(events, m.seq, m.node, reclaimed, active);
            const isDone = drained(events, m.seq);
            const isBad = refused(events, m.seq);
            const isLive = current?.seq === m.seq;
            const gone = m.node != null && reclaimed.get(m.node)?.outcome === "reclaimed";
            const n = podsOf(m, events);
            return (
              <div key={m.seq} className="runstep runstep-col">
                <div className="runstep-line">
                  <span className="runstep-n mono">{String(m.seq).padStart(2, "0")}</span>
                  <span className="runstep-node mono">{m.node}</span>
                  <span className="runstep-meta">
                    {n} pod{n === 1 ? "" : "s"}
                    {gone && " · node gone from the cluster"}
                  </span>
                  <span
                    className={
                      "runstep-outcome " +
                      (isBad ? "is-held" : isDone ? "is-safe" : isLive ? "is-live" : "is-muted")
                    }
                  >
                    {isBad
                      ? "Refused"
                      : gone
                        ? "Reclaimed"
                        : isDone
                          ? "Drained"
                          : isLive
                            ? "In progress"
                            : "Queued"}
                  </span>
                </div>
                <div className="runphases">
                  {PHASES.map((p) => (
                    <span key={p} className={"runphase runphase-" + ph[p]}>
                      {p}
                    </span>
                  ))}
                </div>
              </div>
            );
          })}
        </div>
        <Trail events={events} streaming={active} />
      </div>

      <footer className="runfooter">
        <div className="runfooter-summary">
          <span className="runfooter-line">The Safety Guard re-runs before every step</span>
          <span className="runfooter-sub">
            If a check fails, the run halts and the cordon on the current node is reverted.
          </span>
        </div>
        <div className="runfooter-actions">
          {!active && (
            <button type="button" className="btn" onClick={onDismiss}>
              Back to review
            </button>
          )}
        </div>
      </footer>
    </div>
  );
}

/* -------------------------------------------------------------- 2c halted */

function Halted({
  run,
  events,
  graph,
  steps,
  planMatches,
  reclaimed,
  onDismiss,
  onRecompute,
  onOpenRecommendations,
}: Props) {
  const mine = runSteps(run, events, steps, planMatches);
  const done = mine.filter((m) => drained(events, m.seq));
  const badStep = mine.find((m) => refused(events, m.seq));
  const guard = events.find((e) => e.level === "Blocked" && e.action === "Guard");
  const fail = events.find((e) => e.level === "Error");
  let cpu = 0;
  let mem = 0;
  for (const m of done) {
    const w = nodeWorth(graph, reclaimed, m.node);
    cpu += w.cpu;
    mem += w.mem;
  }
  const goneCount = done.filter((m) => m.node && reclaimed.get(m.node)?.outcome === "reclaimed").length;
  const nodesNow = graph.stats.nodesBefore - goneCount;

  return (
    <div className="runscreen">
      <div className="runhead">
        <span className="runhead-title">Run {run.planId.slice(0, 7)}</span>
        <span className="runhead-meta mono">
          {fmtTime(run.startedAt ?? run.requestedAt)} → {fmtTime(run.finishedAt)} · {run.actor}
        </span>
      </div>

      <div className="runhero">
        <div className="runhero-lead">
          <div className="runhero-eyebrow is-held">
            <span className="runhero-eyedot runhero-eyedot-square" aria-hidden="true" />
            <span className="eyebrow mono">
              {guard ? `Halted at step ${badStep?.seq ?? "?"} of ${mine.length}` : "Run failed"}
            </span>
          </div>
          <h2 className="runhero-headline">
            {done.length} node{done.length === 1 ? "" : "s"} reclaimed.{" "}
            {guard ? "The next was refused and reverted." : "Then a step failed."}
          </h2>
          <p className="runhero-sub runhero-sub-strong">
            {guard?.message ?? fail?.message ?? run.summary ?? ""}{" "}
            <strong>No pod was evicted for the refused step</strong> and the cordon was removed.
          </p>
        </div>
        <div className="runhero-cards">
          <div className="runcard runcard-safe">
            <span className="eyebrow mono">Kept</span>
            <span className="runcard-value">
              {done.length} node{done.length === 1 ? "" : "s"} reclaimed · {formatCPU(cpu)} cores,{" "}
              {formatBytes(mem)}
            </span>
          </div>
          <div className="runcard">
            <span className="eyebrow mono">Cluster now</span>
            <span className="runcard-value">{nodesNow} nodes · no cordons left behind</span>
          </div>
        </div>
      </div>

      <div className="runbody">
        <div className="runsteps">
          <span className="eyebrow mono">What ran</span>
          {mine.map((m) => {
            const isDone = drained(events, m.seq);
            const isBad = refused(events, m.seq);
            const n = podsOf(m, events);
            return (
              <div key={m.seq} className="runstep">
                <span className="runstep-n mono">{String(m.seq).padStart(2, "0")}</span>
                <span className="runstep-node mono">{m.node}</span>
                <span className="runstep-meta">
                  {n} pod{n === 1 ? "" : "s"}
                </span>
                <span
                  className={"runstep-outcome " + (isBad ? "is-held" : isDone ? "is-safe" : "is-muted")}
                >
                  {isBad ? "Refused · reverted" : isDone ? "Reclaimed" : "Not reached"}
                </span>
              </div>
            );
          })}

          {(guard || fail) && (
            <div className="refusal">
              <div className="refusal-head">
                <span className="refusal-tag">{guard ? "REFUSAL" : "FAILURE"}</span>
                <span className="refusal-rule mono">{guard?.rule ?? fail?.action}</span>
              </div>
              <p className="refusal-text mono">{guard?.message ?? fail?.message}</p>
            </div>
          )}
        </div>

        <aside className="waysforward">
          <div className="waysforward-head">
            <span className="eyebrow mono">Two ways forward</span>
            <span className="waysforward-sub">
              The remaining step is not lost — it goes back to the ledger as held back, with this
              refusal attached.
            </span>
          </div>
          <div className="waysforward-body">
            <div className="wayscard wayscard-primary">
              <span className="wayscard-title">Wait for the cluster to recover</span>
              <span className="wayscard-text">
                The refusal names live state. When it heals, the next plan offers this node again —
                nothing needs to be undone.
              </span>
            </div>
            <div className="wayscard">
              <span className="wayscard-title">Change the rule</span>
              <span className="wayscard-text">
                If the constraint is stricter than the service needs, relaxing it is a real
                availability trade — make it in Recommendations, where the trade is priced.
              </span>
              <button type="button" className="wayscard-link" onClick={onOpenRecommendations}>
                Open in Recommendations
              </button>
            </div>
            <div className="wayscard wayscard-refused">
              <span className="wayscard-title">Not offered: force the drain</span>
              <span className="wayscard-text">
                There is no override in this UI. A refusal is the product working.
              </span>
            </div>
          </div>
          <div className="waysforward-foot">
            <button type="button" className="btn stepdetail-add" onClick={onDismiss}>
              Back to review
            </button>
            <button type="button" className="reviewfooter-drain" onClick={onRecompute}>
              Recompute
            </button>
          </div>
        </aside>
      </div>
    </div>
  );
}
