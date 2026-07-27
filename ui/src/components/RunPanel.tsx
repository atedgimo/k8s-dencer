import { useEffect, useRef } from "react";
import { PlanStep, RunEvent, RunStatus } from "../api";
import { RunState } from "../useRun";

/**
 * Confirmation before a run, and the live trail during one.
 *
 * Both live here because they are the same conversation: what is about to
 * happen, then what is happening. The trail is the run's own audit events, not
 * a parallel UI narrative — an operator watching this and an operator reading
 * the audit record afterwards see the identical text, so neither has to work
 * out which one to believe.
 */

/* --------------------------------------------------------- confirmation */

interface ConfirmProps {
  steps: PlanStep[];
  dryRun: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmRun({ steps, dryRun, onConfirm, onCancel }: ConfirmProps) {
  const ref = useRef<HTMLButtonElement>(null);
  useEffect(() => ref.current?.focus(), []);

  const pods = steps.reduce((n, s) => n + s.moves.length, 0);
  const nodes = steps.map((s) => s.targetNode).filter(Boolean);

  return (
    <div className="sheet-backdrop" onClick={onCancel} role="presentation">
      <div
        className="sheet"
        role="dialog"
        aria-modal="true"
        aria-label={dryRun ? "Confirm dry run" : "Confirm consolidation"}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => e.key === "Escape" && onCancel()}
      >
        <h2 className="sheet-title">
          {dryRun ? "Rehearse " : "Drain "}
          {steps.length} node{steps.length === 1 ? "" : "s"}
        </h2>

        <p className="sheet-detail">
          {dryRun ? (
            <>
              Nothing will be cordoned or evicted. The Safety Guard runs in full and you get the
              same trail you would see for a real run.
            </>
          ) : (
            <>
              <strong>{pods}</strong> pod{pods === 1 ? "" : "s"} will be evicted through the
              eviction API, so PodDisruptionBudgets are enforced. Evicted pods are not restored if
              this aborts — draining cannot be undone.
            </>
          )}
        </p>

        <ul className="sheet-list mono">
          {nodes.map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>

        <div className="sheet-actions">
          <button className="btn" onClick={onCancel}>
            Cancel
          </button>
          <button className="btn btn-primary" ref={ref} onClick={onConfirm}>
            {dryRun ? "Run the rehearsal" : `Drain ${steps.length} node${steps.length === 1 ? "" : "s"}`}
          </button>
        </div>
      </div>
    </div>
  );
}

/* ------------------------------------------------------------- live trail */

export function RunTrail({ state, onDismiss }: { state: RunState; onDismiss: () => void }) {
  const listRef = useRef<HTMLOListElement>(null);

  // Follow the tail as events arrive; an operator watching a drain wants the
  // newest line, not the first.
  useEffect(() => {
    const el = listRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [state]);

  if (state.status === "idle") return null;

  if (state.status === "error") {
    return (
      <div className="trail trail-error" role="alert">
        <div className="trail-head">
          <span className="trail-status">Could not start</span>
          <button className="trail-close" onClick={onDismiss} aria-label="Dismiss">
            ✕
          </button>
        </div>
        <p className="trail-summary">{state.message}</p>
        {state.grantWith && <pre className="trail-grant">{state.grantWith}</pre>}
      </div>
    );
  }

  if (state.status === "starting") {
    return (
      <div className="trail" aria-live="polite">
        <div className="trail-head">
          <span className="trail-status">Queueing…</span>
        </div>
      </div>
    );
  }

  const { run, events } = state;
  const done = state.status === "done";

  return (
    <div className={`trail trail-${run.status.toLowerCase()}`} aria-live="polite">
      <div className="trail-head">
        <span className="trail-status">{headline(run.status, run.dryRun)}</span>
        <span className="trail-meta mono">
          {run.dryRun ? "dry run · " : ""}
          {run.steps.length} step{run.steps.length === 1 ? "" : "s"}
        </span>
        {done && (
          <button className="trail-close" onClick={onDismiss} aria-label="Dismiss">
            ✕
          </button>
        )}
      </div>

      <ol className="trail-list" ref={listRef}>
        {events.map((e) => (
          <li key={e.sequence} className={`trail-row trail-row-${e.level.toLowerCase()}`}>
            <span className="trail-action mono">{e.action}</span>
            <span className="trail-subject mono">{e.pod ?? e.node ?? ""}</span>
            <span className="trail-msg">{e.message}</span>
            {/* Which rail refused. The question asked after any incident. */}
            {e.rule && <span className="trail-rule mono">{e.rule}</span>}
          </li>
        ))}
      </ol>

      {run.summary && <p className="trail-summary">{run.summary}</p>}

      {done && run.status === "Succeeded" && !run.dryRun && (
        <p className="trail-note">
          The drained nodes are empty and cordoned. k8s-dencer does not delete nodes — removing the
          machines is your autoscaler&apos;s or node-pool tooling&apos;s job. Run{" "}
          <code>kubectl uncordon</code> to put one back into service.
        </p>
      )}

      {done && run.status === "Blocked" && (
        <p className="trail-note">
          The Safety Guard stopped this run. That is the rails working, not a failure — any node it
          had already cordoned has been made schedulable again.
        </p>
      )}
    </div>
  );
}

function headline(status: RunStatus, dryRun: boolean): string {
  switch (status) {
    case "Pending":
      return "Waiting for the executor";
    case "Running":
      return dryRun ? "Rehearsing" : "Draining";
    case "Succeeded":
      return dryRun ? "Rehearsal complete" : "Drained";
    case "Blocked":
      return "Stopped by the Safety Guard";
    case "Failed":
      return "Run failed";
  }
}

/** Events worth surfacing without expanding — used for the compact readout. */
export function lastMeaningful(events: RunEvent[]): RunEvent | undefined {
  return events[events.length - 1];
}
