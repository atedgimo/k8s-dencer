import { useEffect, useRef } from "react";
import { PlanStep } from "../api";
import { impactGlyph } from "./Impact";

interface Props {
  steps: PlanStep[];
  step: number;
  onStep: (n: number) => void;
  playing: boolean;
  onPlayingChange: (playing: boolean) => void;
}

/**
 * Timeline scrubber.
 *
 * Position 0 is the cluster as it stands; the maximum is the fully
 * consolidated end state. Dragging between them is the before/after
 * comparison, with every intermediate state available — which a pair of static
 * panes cannot show.
 *
 * A native range input carries the keyboard and screen-reader behaviour for
 * free; a custom-drawn track would have to reimplement all of it and would
 * still be worse.
 */
export default function Scrubber({ steps, step, onStep, playing, onPlayingChange }: Props) {
  const max = steps.length;
  const timer = useRef<number | null>(null);

  useEffect(() => {
    if (!playing) return;
    if (step >= max) {
      onPlayingChange(false);
      return;
    }
    timer.current = window.setTimeout(() => onStep(step + 1), 620);
    return () => {
      if (timer.current) window.clearTimeout(timer.current);
    };
  }, [playing, step, max, onStep, onPlayingChange]);

  const current = steps.find((s) => s.sequenceNumber === step);

  return (
    <section className="scrubber" aria-label="Step timeline">
      <button
        type="button"
        className="scrub-btn"
        onClick={() => onPlayingChange(!playing)}
        aria-label={playing ? "Pause" : "Play through the plan"}
        disabled={max === 0}
      >
        {playing ? "❚❚" : "▶"}
      </button>
      <button
        type="button"
        className="scrub-btn"
        onClick={() => {
          onPlayingChange(false);
          onStep(0);
        }}
        aria-label="Back to the current cluster"
        disabled={step === 0}
      >
        ↺
      </button>

      <div className="scrub-track">
        <input
          type="range"
          min={0}
          max={max}
          step={1}
          value={step}
          aria-label="Consolidation step"
          aria-valuetext={
            step === 0 ? "Cluster as it is now" : `After step ${step} of ${max}`
          }
          onChange={(e) => {
            onPlayingChange(false);
            onStep(Number(e.target.value));
          }}
        />
        {/* Rating ticks. Glyphs rather than coloured dots, because Red and
            Green are indistinguishable under deuteranopia. */}
        <div className="scrub-ticks" aria-hidden="true">
          {steps.map((s) => (
            <span
              key={s.id}
              className={`tick tick-${s.impact.toLowerCase()}${s.sequenceNumber <= step ? " tick-done" : ""}`}
              style={{ left: `${(s.sequenceNumber / Math.max(max, 1)) * 100}%` }}
            >
              {impactGlyph[s.impact]}
            </span>
          ))}
        </div>
      </div>

      <div className="scrub-readout">
        {step === 0 ? (
          <>
            <strong>Now</strong>
            <span className="scrub-sub">drag to preview the plan</span>
          </>
        ) : (
          <>
            <strong>
              Step {step} of {max}
            </strong>
            <span className="scrub-sub">{current?.targetNode ?? ""}</span>
          </>
        )}
      </div>
    </section>
  );
}
