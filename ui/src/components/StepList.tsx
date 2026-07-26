import { PlanStep } from "../api";
import { ImpactChip } from "./Impact";

interface Props {
  steps: PlanStep[];
  /** How far the scrubber has advanced; steps at or below this are applied. */
  appliedThrough: number;
  selected: number | null;
  onSelect: (seq: number | null) => void;
}

/**
 * The numbered, impact-rated step list.
 *
 * Selecting a step highlights it on the canvas, and clicking a node on the
 * canvas selects its step here — the same relationship read from either
 * direction, so an operator can start from "what does step 7 do?" or from
 * "why is this node going away?".
 */
export default function StepList({ steps, appliedThrough, selected, onSelect }: Props) {
  return (
    <aside className="steps" aria-label="Consolidation steps">
      <header className="steps-header">
        <h2>Steps</h2>
        <span className="steps-count">{steps.length}</span>
      </header>

      <ol className="step-list">
        {steps.map((step) => {
          const applied = step.sequenceNumber <= appliedThrough;
          const isSelected = selected === step.sequenceNumber;
          return (
            <li key={step.id}>
              <button
                type="button"
                className={`step${isSelected ? " step-selected" : ""}${applied ? " step-applied" : ""}`}
                aria-current={isSelected ? "step" : undefined}
                onClick={() => onSelect(isSelected ? null : step.sequenceNumber)}
              >
                <span className="step-seq">{step.sequenceNumber}</span>
                <span className="step-body">
                  <span className="step-title">
                    <span className="step-node">{step.targetNode}</span>
                    <ImpactChip impact={step.impact} compact />
                  </span>
                  <span className="step-meta">
                    {step.moves.length} pod{step.moves.length === 1 ? "" : "s"} to move
                  </span>
                  {isSelected && <span className="step-rationale">{step.rationale}</span>}
                </span>
              </button>
            </li>
          );
        })}
      </ol>
    </aside>
  );
}
