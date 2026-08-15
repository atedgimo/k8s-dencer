/**
 * The sticky action footer (assets/design/README.md, 1a): the selection
 * priced in nodes and pods, what a run does, and ONE filled button.
 *
 * The old UI ran Run / Dry run / Run to optimum as three equal buttons; the
 * destructive path must be unambiguous, so Drain is the only filled action,
 * Rehearse is its outlined sibling, and Run to optimum lives in an overflow
 * menu — a ceiling, never a call to action.
 *
 * A selection containing any non-Green step turns the button amber-outlined
 * and requires the node count typed back: the cheapest honest version of
 * "do you know what you are approving".
 */

import { useMemo, useRef, useState } from "react";
import { PlanStep } from "../../api";

interface Props {
  planId: string;
  picked: PlanStep[];
  /** A read-only session hides the drain affordances; RBAC enforces anyway. */
  readOnly?: boolean;
  stale: boolean;
  busy: boolean;
  onRehearse: () => void;
  /** All-Green path; goes through the ordinary confirmation sheet. */
  onDrain: () => void;
  /** Post-typed-gate path; the amber gate was the confirmation. */
  onDrainConfirmed: () => void;
  onConverge: () => void;
}

export default function ReviewFooter({
  planId,
  picked,
  readOnly,
  stale,
  busy,
  onRehearse,
  onDrain,
  onDrainConfirmed,
  onConverge,
}: Props) {
  const [confirming, setConfirming] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [copied, setCopied] = useState(false);

  const pods = picked.reduce((n, s) => n + s.moves.length, 0);
  const nonGreen = picked.filter((s) => s.impact !== "Green").length;
  const yaml = useMemo(() => selectionYAML(planId, picked), [planId, picked]);

  const drain = () => {
    if (nonGreen > 0) setConfirming(true);
    else onDrain();
  };

  return (
    <footer className="reviewfooter">
      <div className="reviewfooter-summary">
        <span className="reviewfooter-line">
          {picked.length} step{picked.length === 1 ? "" : "s"} selected · {pods} pod
          {pods === 1 ? "" : "s"} move
        </span>
        <span className="reviewfooter-sub">
          {readOnly
            ? "Read-only session. Plan and rehearse; draining is hidden by your own choice at sign-in."
            : stale
            ? "A newer plan exists — Recompute before running."
            : picked.length === 0
              ? // An empty selection is not "all green" — there is no all.
                // The plan can be entirely Held back and this line would still
                // have claimed everything was safe, which is the opposite of
                // what the screen above it says.
                "Nothing selected. Choose the steps you want to run."
              : nonGreen > 0
                ? `${nonGreen} non-Green in the selection. The Safety Guard re-runs before every step.`
                : "All Green. Safety Guard re-runs per step; the run halts on the first refusal."}
        </span>
      </div>

      <div className="reviewfooter-actions">
        <button
          type="button"
          className="btn"
          disabled={picked.length === 0}
          onClick={() => {
            void navigator.clipboard.writeText(yaml);
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          }}
        >
          {copied ? "Copied" : "Copy as YAML"}
        </button>
        <button
          type="button"
          className="btn reviewfooter-rehearse"
          disabled={picked.length === 0 || busy}
          onClick={onRehearse}
        >
          Rehearse
        </button>
        {!readOnly && (
          <button
            type="button"
            className={
              "btn reviewfooter-drain" + (nonGreen > 0 ? " reviewfooter-drain-caution" : "")
            }
            disabled={picked.length === 0 || busy || stale}
            onClick={drain}
          >
            Drain {picked.length} node{picked.length === 1 ? "" : "s"}
          </button>
        )}
        {!readOnly && (
        <div className="reviewfooter-overflow">
          <button
            type="button"
            className="btn reviewfooter-more"
            aria-label="More actions"
            aria-expanded={menuOpen}
            onClick={() => setMenuOpen(!menuOpen)}
          >
            ⋯
          </button>
          {menuOpen && (
            <div className="reviewfooter-menu" role="menu">
              <button
                type="button"
                role="menuitem"
                className="reviewfooter-menuitem"
                disabled={busy}
                onClick={() => {
                  setMenuOpen(false);
                  onConverge();
                }}
              >
                Run to optimum…
              </button>
            </div>
          )}
        </div>
        )}
      </div>

      {confirming && (
        <TypedConfirm
          count={picked.length}
          nonGreen={nonGreen}
          onConfirm={() => {
            setConfirming(false);
            onDrainConfirmed();
          }}
          onCancel={() => setConfirming(false)}
        />
      )}
    </footer>
  );
}

/**
 * The amber gate. Typing the node count is deliberately clumsy — the
 * clumsiness is the feature: a selection that includes "Needs a call" steps
 * is a judgement, and a judgement should cost one deliberate act more than
 * a click.
 */
function TypedConfirm({
  count,
  nonGreen,
  onConfirm,
  onCancel,
}: {
  count: number;
  nonGreen: number;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const [typed, setTyped] = useState("");
  const ref = useRef<HTMLInputElement>(null);
  const ok = typed.trim() === String(count);

  return (
    <div className="sheet-backdrop" onClick={onCancel} role="presentation">
      <div
        className="sheet sheet-caution"
        role="dialog"
        aria-modal="true"
        aria-labelledby="typedconfirm-title"
        onClick={(e) => e.stopPropagation()}
      >
        <h2 className="sheet-title" id="typedconfirm-title">
          Drain {count} nodes, {nonGreen} of them not Green
        </h2>
        <p className="sheet-detail">
          The selection includes steps the Safety Guard flagged for a call. It re-runs before
          every step and halts on the first refusal — but approving these is your judgement.
          Type the node count to confirm.
        </p>
        <input
          ref={ref}
          className="sheet-input mono"
          inputMode="numeric"
          placeholder={String(count)}
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && ok) onConfirm();
            if (e.key === "Escape") onCancel();
          }}
          aria-label={`Type ${count} to confirm`}
        />
        <div className="sheet-actions">
          <button className="btn" onClick={onCancel}>
            Cancel
          </button>
          <button className="btn btn-caution" disabled={!ok} onClick={onConfirm}>
            Drain {count} nodes
          </button>
        </div>
      </div>
    </div>
  );
}

/**
 * The selection as a reviewable document — what gets pasted into a change
 * ticket. Deliberately not a kubectl manifest: nothing applies it; it is
 * the plan excerpt a colleague reads.
 */
function selectionYAML(planId: string, picked: PlanStep[]): string {
  const lines = [`# k8s-dencer plan ${planId} — selected steps`, `plan: ${planId}`, "steps:"];
  for (const s of picked) {
    lines.push(`  - step: ${s.sequenceNumber}`);
    lines.push(`    node: ${s.targetNode}`);
    lines.push(`    verdict: ${s.impact}`);
    if (s.moves.length > 0) {
      lines.push("    moves:");
      for (const m of s.moves) {
        lines.push(`      - pod: ${m.namespace}/${m.pod}`);
        lines.push(`        to: ${m.toNode}`);
      }
    }
  }
  return lines.join("\n") + "\n";
}
