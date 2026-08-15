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
import { VERDICT_LABEL } from "../Impact";

interface Props {
  planId: string;
  picked: PlanStep[];
  /** Operator-set; names the cluster in an exported change record. */
  clusterLabel?: string;
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
  clusterLabel,
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
  const [recorded, setRecorded] = useState(false);

  const pods = picked.reduce((n, s) => n + s.moves.length, 0);
  const nonGreen = picked.filter((s) => s.impact !== "Green").length;
  const yaml = useMemo(() => selectionYAML(planId, picked), [planId, picked]);
  const record = useMemo(
    () => changeRecord(planId, picked, clusterLabel),
    [planId, picked, clusterLabel],
  );

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
        {/* The overflow renders in a read-only session too. It used to be
            hidden wholesale with the drain controls, which also hid the
            change record — and a reviewer who cannot execute is exactly the
            person who needs the approval to exist somewhere else. Only the
            item that evicts something is gated. */}
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
                disabled={picked.length === 0}
                onClick={() => {
                  void navigator.clipboard.writeText(record);
                  setRecorded(true);
                  setMenuOpen(false);
                  setTimeout(() => setRecorded(false), 1500);
                }}
              >
                {recorded ? "Copied" : "Copy as change record"}
              </button>
              {!readOnly && (
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
              )}
            </div>
          )}
        </div>
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

/**
 * The selection as a change record — Markdown, for a PR body or a ticket.
 *
 * Every team with a change process needs the approval to exist somewhere
 * other than this UI, and until now the audit trail was only inside the
 * store. The plan already carries stable ids, per-step rationale and the
 * guard's verdicts, so this is a transform rather than a feature.
 *
 * It says what changes, why each step is rated as it is, what the run
 * actually does, and how to reverse it — the four things a reviewer who was
 * not looking at this screen has to be told. Verdicts are the product's own
 * words, so the record and the UI cannot describe the same step differently.
 */
function changeRecord(planId: string, picked: PlanStep[], clusterLabel?: string): string {
  const pods = picked.reduce((n, s) => n + s.moves.length, 0);
  const out: string[] = [
    `# Node consolidation — plan \`${planId}\``,
    "",
    ...(clusterLabel ? [`**Cluster:** ${clusterLabel}  `] : []),
    `**Prepared:** ${new Date().toISOString()}  `,
    `**Approving:** ${picked.length} step${picked.length === 1 ? "" : "s"}, ${pods} pod${pods === 1 ? "" : "s"} relocated`,
    "",
    "## What changes",
    "",
    "| Step | Node | Verdict | Pods moved |",
    "| --- | --- | --- | --- |",
  ];
  for (const s of picked) {
    out.push(
      `| ${String(s.sequenceNumber).padStart(2, "0")} | \`${s.targetNode}\` | ` +
        `${VERDICT_LABEL[s.impact]} | ${s.moves.length} |`,
    );
  }

  out.push("", "## Why each step is rated as it is", "");
  for (const s of picked) {
    out.push(
      `### ${String(s.sequenceNumber).padStart(2, "0")} — \`${s.targetNode}\` · ${VERDICT_LABEL[s.impact]}`,
      "",
      s.rationale || "_No rationale recorded._",
      "",
    );
    if (s.moves.length > 0) {
      out.push("Pods relocated:", "");
      for (const m of s.moves) {
        out.push(`- \`${m.namespace}/${m.pod}\` → \`${m.toNode}\``);
      }
      out.push("");
    }
  }

  out.push(
    "## What the run does",
    "",
    "Each step cordons the node, evicts its pods through the Eviction API, and waits",
    "for every one of them to be Ready elsewhere before the next step begins. The",
    "Safety Guard re-runs before every step and halts the run on the first refusal.",
    "There is no override.",
    "",
    "## Reversal",
    "",
    "Uncordon the node. Nothing is deleted — the pods were rescheduled by Kubernetes",
    "and stay where they landed; consolidation is undone by the scheduler filling the",
    "node again, not by moving them back.",
    "",
    "---",
    "",
    `Generated by k8s-dencer from plan \`${planId}\`. A plan is identified by its`,
    "actions, so the same steps against the same nodes produce the same id.",
  );
  return out.join("\n") + "\n";
}

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
