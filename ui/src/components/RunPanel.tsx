import { useEffect, useRef, useState } from "react";
import { PlanStep } from "../api";
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

  // Unreachable by construction, and deliberately not implemented twice:
  // App renders this only while a run is neither active nor done, because
  // RunScreen owns both of those states. The event list that used to live
  // here was a second rendering of a run in flight that nothing could ever
  // display — the same drift, in the UI, that the store and the chart each
  // had to have removed.
  return null;
}

interface ConvergeProps {
  /** The current plan's reclaimable count — context only, and labelled so. */
  planReclaims: number;
  onConfirm: (maxNodes: number, maxImpact: "Green" | "Yellow", dryRun: boolean) => void;
  onCancel: () => void;
}

export function ConfirmConverge({ planReclaims, onConfirm, onCancel }: ConvergeProps) {
  const [maxNodes, setMaxNodes] = useState("");
  const [ceiling, setCeiling] = useState<"Green" | "Yellow">("Green");
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => ref.current?.focus(), []);

  const n = parseInt(maxNodes, 10);
  const valid = Number.isFinite(n) && n >= 1 && n <= 200;

  return (
    <div className="sheet-backdrop" onClick={onCancel} role="presentation">
      <div
        className="sheet"
        role="dialog"
        aria-modal="true"
        aria-label="Approve a closed-loop consolidation policy"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => e.key === "Escape" && onCancel()}
      >
        <h2 className="sheet-title">Run to optimum</h2>

        <p className="sheet-detail">
          <strong>You are approving a policy, not a list of steps.</strong> The executor will
          repeatedly observe the cluster, plan <em>one</em> drain against live state, run the full
          Safety Guard, drain, and wait for recovery — until nothing worthwhile remains or a bound
          below is reached. Every round must free a node or the run stops, and no node is drained
          twice.
        </p>

        <div className="sheet-bounds">
          <label className="sheet-bound">
            <span className="eyebrow">at most</span>
            <input
              ref={ref}
              className="sheet-input num"
              inputMode="numeric"
              placeholder="?"
              value={maxNodes}
              onChange={(e) => setMaxNodes(e.target.value)}
              aria-label="Maximum nodes this run may drain"
            />
            <span className="sheet-bound-unit">nodes drained</span>
          </label>
          <div className="sheet-bound">
            <span className="eyebrow">nothing above</span>
            <div className="sheet-seg" role="radiogroup" aria-label="Impact ceiling">
              {(["Green", "Yellow"] as const).map((c) => (
                <button
                  key={c}
                  type="button"
                  role="radio"
                  aria-checked={ceiling === c}
                  className={ceiling === c ? "sheet-seg-on" : ""}
                  onClick={() => setCeiling(c)}
                >
                  {c}
                </button>
              ))}
            </div>
            <span className="sheet-bound-unit">is executed. Red always needs a window.</span>
          </div>
        </div>

        {planReclaims > 0 && (
          <p className="sheet-context">
            For context only — targets are re-chosen live: the current plan frees {planReclaims}{" "}
            node{planReclaims === 1 ? "" : "s"}.
          </p>
        )}

        <div className="sheet-actions">
          <button className="btn" onClick={onCancel}>
            Cancel
          </button>
          <button className="btn" disabled={!valid} onClick={() => onConfirm(n, ceiling, true)}>
            Rehearse one round
          </button>
          <button
            className="btn btn-primary"
            disabled={!valid}
            onClick={() => onConfirm(n, ceiling, false)}
          >
            Approve this policy
          </button>
        </div>
      </div>
    </div>
  );
}
