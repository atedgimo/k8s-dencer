import { useEffect, useRef, useState } from "react";
import { Impact, PlanStep } from "../api";
import { GLYPH } from "./Impact";

/**
 * Progress through the plan, with the full list on request.
 *
 * It used to render every step as an equal choice, which turned a 55-step plan
 * into 55 competing decisions and drowned out the one recommendation the page
 * is trying to make. Measured plan stability is 95–100% after a step executes,
 * so the later steps are real — they are just not decisions anyone needs to
 * take right now.
 *
 * So the default is a progress readout and the next few steps. Arbitrary
 * selection still exists, because it is the only route to running a Yellow
 * step, but it is the advanced path rather than the front door: the ordering
 * is not cosmetic, and cherry-picking step 14 before step 2 packs worse.
 *
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

  const [showAll, setShowAll] = useState(false);

  const done = steps.filter((s) => s.sequenceNumber <= current).length;
  const filtered = focusedRating ? steps.filter((s) => s.impact === focusedRating) : steps;

  // Collapsed by default to the work immediately ahead. Expanding is one
  // click, and any explicit interest — a rating filter, a selection, a chosen
  // step — expands it automatically.
  const expanded = showAll || focusedRating !== null || checked.size > 0 || selected !== null;
  const NEXT = 5;
  const visible = expanded ? filtered : filtered.filter((s) => s.sequenceNumber > current).slice(0, NEXT);
  const hidden = filtered.length - visible.length;

  return (
    <section className="ledger" aria-label="Consolidation steps">
      <div className="ledger-head">
        <h2 className="panel-title mono">
          {done > 0 ? `${done} of ${steps.length} done` : `${steps.length} steps`}
        </h2>
        {checked.size > 0 && (
          <span className="ledger-picked mono">{checked.size} picked</span>
        )}
        {focusedRating && (
          <button className="ledger-clear mono" onClick={() => onSelect(null)}>
            {visible.length} {focusedRating.toLowerCase()}
          </button>
        )}
      </div>

      {/* Progress as a bar rather than a number alone: how far through a
          consolidation you are is the question this panel should answer at a
          glance. */}
      {steps.length > 0 && (
        <div
          className="ledger-progress"
          role="progressbar"
          aria-valuenow={done}
          aria-valuemin={0}
          aria-valuemax={steps.length}
          aria-label={`${done} of ${steps.length} steps complete`}
        >
          <div
            className="ledger-progress-fill"
            style={{ width: `${(done / steps.length) * 100}%` }}
          />
        </div>
      )}

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
        <p className="ledger-empty">
          {focusedRating
            ? `No ${focusedRating.toLowerCase()} steps in this plan.`
            : "Every step in this plan has run."}
        </p>
      )}

      {!expanded && hidden > 0 && (
        <button className="ledger-more" onClick={() => setShowAll(true)}>
          Show all {filtered.length} steps
          <span className="ledger-more-hint"> — pick individually, including Yellow</span>
        </button>
      )}
      {showAll && (
        <button className="ledger-more" onClick={() => setShowAll(false)}>
          Show just what is next
        </button>
      )}
    </section>
  );
}
