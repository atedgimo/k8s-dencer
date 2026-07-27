import { useEffect, useRef } from "react";
import { Impact, PlanStep } from "../api";
import { GLYPH } from "./Impact";

/**
 * The plan as an ordered ledger.
 *
 * Numbered because the content genuinely is a sequence: step 7 assumes the
 * capacity steps 1–6 freed, and running them out of order would make the plan
 * wrong. That is the rare case where a numeric marker carries information
 * rather than decorating a list.
 *
 * Each row is a scrub target. Selecting one moves the packing field to the
 * moment just before that step runs, so the pods it is about to move are still
 * sitting on the node being drained.
 */

interface Props {
  steps: PlanStep[];
  selected: number | null;
  current: number;
  focusedRating: Impact | null;
  onSelect: (seq: number | null) => void;
  /** Steps ticked for execution. */
  checked: Set<number>;
  onToggle: (seq: number, shiftKey: boolean) => void;
}

export default function StepLedger({
  steps,
  selected,
  current,
  focusedRating,
  onSelect,
  checked,
  onToggle,
}: Props) {
  const listRef = useRef<HTMLOListElement>(null);

  // Keep the selected row in view when selection comes from elsewhere — the
  // scrubber, or a click on the packing field.
  useEffect(() => {
    if (selected == null || !listRef.current) return;
    const row = listRef.current.querySelector(`[data-seq="${selected}"]`);
    row?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [selected]);

  const visible = focusedRating ? steps.filter((s) => s.impact === focusedRating) : steps;

  return (
    <section className="ledger" aria-label="Consolidation steps">
      <div className="ledger-head">
        <h2 className="panel-title mono">steps</h2>
        {checked.size > 0 && (
          <span className="ledger-picked mono">{checked.size} picked</span>
        )}
        {focusedRating && (
          <button className="ledger-clear mono" onClick={() => onSelect(null)}>
            {visible.length} {focusedRating.toLowerCase()}
          </button>
        )}
      </div>

      <ol className="ledger-list" ref={listRef}>
        {visible.map((step) => {
          const done = step.sequenceNumber <= current;
          return (
            <li key={step.id} className="ledger-item">
              {/* Red cannot be ticked at all. The Safety Guard refuses it
                  against live state regardless, so offering the checkbox
                  would only be a way to be told no later. */}
              <input
                type="checkbox"
                className="row-check"
                checked={checked.has(step.sequenceNumber)}
                disabled={step.impact === "Red"}
                onChange={(e) =>
                  onToggle(step.sequenceNumber, (e.nativeEvent as MouseEvent).shiftKey)
                }
                onClick={(e) => e.stopPropagation()}
                aria-label={
                  step.impact === "Red"
                    ? `Step ${step.sequenceNumber} is Red and cannot be run: ${step.rationale}`
                    : `Select step ${step.sequenceNumber}, ${step.impact}`
                }
                title={
                  step.impact === "Red"
                    ? "Red steps may only run inside an approved maintenance window, which this release does not implement."
                    : undefined
                }
              />
              <button
                data-seq={step.sequenceNumber}
                className={[
                  "row",
                  `row-${step.impact.toLowerCase()}`,
                  selected === step.sequenceNumber ? "row-selected" : "",
                  done ? "row-done" : "",
                ]
                  .filter(Boolean)
                  .join(" ")}
                onClick={() =>
                  onSelect(selected === step.sequenceNumber ? null : step.sequenceNumber)
                }
                aria-pressed={selected === step.sequenceNumber}
              >
                <span className="row-seq num">{String(step.sequenceNumber).padStart(2, "0")}</span>
                {/* Glyph and word, never colour alone. */}
                <span className="row-glyph" aria-hidden="true">
                  {GLYPH[step.impact]}
                </span>
                <span className="row-node mono">{step.targetNode ?? "—"}</span>
                <span className="row-moves num">{step.moves.length}</span>
                <span className="row-rating">{step.impact}</span>
              </button>

              {selected === step.sequenceNumber && (
                <p className="row-why">
                  {/* The classifier's exact words. The UI, the API and the
                      Kagent agent all quote this same string, so an operator
                      never sees two different explanations of one rating. */}
                  {step.rationale}
                </p>
              )}
            </li>
          );
        })}
      </ol>

      {visible.length === 0 && (
        <p className="ledger-empty">No {focusedRating?.toLowerCase()} steps in this plan.</p>
      )}
    </section>
  );
}
