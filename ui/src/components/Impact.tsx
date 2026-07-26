import { Impact } from "../api";

/**
 * Rating chip.
 *
 * The palette validator reports critical <-> good at CVD deltaE 4.1 for
 * deuteranopia, against a floor of 8 — Red and Green are the same colour to a
 * large minority of readers. So a rating is never colour alone: each carries a
 * distinct glyph *and* the word. The glyphs differ in silhouette (disc,
 * triangle, square), not just fill, so they survive greyscale and low
 * resolution too.
 */
const GLYPH: Record<Impact, string> = {
  Green: "●",
  Yellow: "▲",
  Red: "■",
};

const DESCRIPTION: Record<Impact, string> = {
  Green: "Safe to run at any time",
  Yellow: "Executable on request, flagged",
  Red: "Maintenance window only",
};

export function ImpactChip({ impact, compact = false }: { impact: Impact; compact?: boolean }) {
  return (
    <span
      className={`chip chip-${impact.toLowerCase()}${compact ? " chip-compact" : ""}`}
      title={DESCRIPTION[impact]}
    >
      <span aria-hidden="true" className="chip-glyph">
        {GLYPH[impact]}
      </span>
      <span className="chip-label">{impact}</span>
    </span>
  );
}

export function ImpactLegend({ counts }: { counts: Record<Impact, number> }) {
  const order: Impact[] = ["Green", "Yellow", "Red"];
  return (
    <div className="legend" aria-label="Steps by impact rating">
      {order.map((impact) => (
        <div className="legend-item" key={impact}>
          <ImpactChip impact={impact} compact />
          <span className="legend-count">{counts[impact] ?? 0}</span>
        </div>
      ))}
    </div>
  );
}

export { GLYPH as impactGlyph, DESCRIPTION as impactDescription };
