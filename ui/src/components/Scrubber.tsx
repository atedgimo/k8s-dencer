import { useEffect } from "react";
import { Impact, PlanStep } from "../api";
import { GLYPH } from "./Impact";

/**
 * Drives the packing field through the plan.
 *
 * The scrubbing *is* the argument: a static before/after picture shows that
 * consolidation happened, but dragging through it shows how — which nodes
 * empty first, where their pods land, and how much of the field goes dark.
 *
 * Position 0 is the cluster as it stands; the maximum is the fully
 * consolidated end state. The track doubles as a rating histogram, so an
 * operator can see where the risky steps sit before dragging into them.
 */

interface Props {
  steps: PlanStep[];
  step: number;
  playing: boolean;
  onStep: (n: number) => void;
  onPlayingChange: (playing: boolean) => void;
  onSelect: (seq: number | null) => void;
}

export default function Scrubber({
  steps,
  step,
  playing,
  onStep,
  onPlayingChange,
  onSelect,
}: Props) {
  const total = steps.length;

  useEffect(() => {
    if (!playing) return;
    if (step >= total) {
      onPlayingChange(false);
      return;
    }
    // Slow enough to read what moved. Faster and the field is just a flicker,
    // which defeats the point of animating it at all.
    const t = setTimeout(() => onStep(step + 1), 620);
    return () => clearTimeout(t);
  }, [playing, step, total, onStep, onPlayingChange]);

  const atEnd = step >= total;

  return (
    <div className="scrub">
      <button
        className="btn btn-icon"
        onClick={() => {
          if (atEnd) onStep(0);
          onPlayingChange(!playing);
        }}
        aria-label={playing ? "Pause" : atEnd ? "Replay from the start" : "Play the plan"}
      >
        {playing ? "❙❙" : atEnd ? "↺" : "▶"}
      </button>

      <div className="scrub-track">
        {/* Ticks are the plan's shape at a glance: where the Red steps are,
            and how far through you have dragged. */}
        <div className="scrub-ticks" aria-hidden="true">
          {steps.map((s) => (
            <span
              key={s.id}
              className={[
                "tick",
                `tick-${s.impact.toLowerCase()}`,
                s.sequenceNumber <= step ? "tick-past" : "",
              ]
                .filter(Boolean)
                .join(" ")}
              title={`Step ${s.sequenceNumber} — ${s.impact}`}
            />
          ))}
        </div>

        <input
          type="range"
          min={0}
          max={total}
          value={step}
          onChange={(e) => {
            onPlayingChange(false);
            const n = Number(e.target.value);
            onStep(n);
            onSelect(n > 0 ? n : null);
          }}
          aria-label={`Step ${step} of ${total}`}
          aria-valuetext={stepLabel(steps, step)}
        />
      </div>

      <p className="scrub-read">
        <span className="num scrub-now">{String(step).padStart(2, "0")}</span>
        <span className="scrub-of mono">/{String(total).padStart(2, "0")}</span>
        <span className="scrub-label mono">{stepLabel(steps, step)}</span>
      </p>
    </div>
  );
}

function stepLabel(steps: PlanStep[], step: number): string {
  if (step === 0) return "cluster as it stands";
  const s = steps.find((x) => x.sequenceNumber === step);
  if (!s) return "plan complete";
  return `${GLYPH[s.impact as Impact]} drain ${s.targetNode ?? "?"}`;
}
