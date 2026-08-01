import type { ReactElement } from "react";
import { Surface, SURFACE_LABELS } from "../view";

/**
 * The left rail: where am I, and where else can I be.
 *
 * Navigation was living inside the field-view switch, which conflated two
 * different questions — "how should nodes be drawn" (a rendering choice that
 * belongs to the Plan surface) and "what am I looking at" (this rail). A
 * product grows a spine the day those separate.
 *
 * Marks are drawn, not emoji: three glyphs in the house ink, each a tiny
 * statement of its destination — the plan is a packed box, history is a
 * rising line, advice is a check-in-progress.
 */

interface Props {
  surface: Surface;
  onSurface: (s: Surface) => void;
  /** Advice count, shown as a quiet badge when nonzero. */
  adviceCount?: number;
}

const MARKS: Record<Surface, ReactElement> = {
  plan: (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <rect x="1.5" y="3.5" width="13" height="9" rx="1" fill="none" stroke="currentColor" />
      <rect x="3.5" y="5.5" width="3" height="5" fill="currentColor" />
      <rect x="8" y="5.5" width="3" height="5" fill="currentColor" opacity="0.45" />
    </svg>
  ),
  history: (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <path d="M1.5 12.5 L6 8 L9 10.5 L14.5 3.5" fill="none" stroke="currentColor" strokeWidth="1.5" />
    </svg>
  ),
  advice: (
    <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <path d="M2.5 8.5 L6 12 L13.5 4" fill="none" stroke="currentColor" strokeWidth="1.5" strokeDasharray="2 2" />
    </svg>
  ),
};

export default function NavRail({ surface, onSurface, adviceCount = 0 }: Props) {
  return (
    <nav className="rail" aria-label="Sections">
      {(Object.keys(SURFACE_LABELS) as Surface[]).map((s) => (
        <button
          key={s}
          type="button"
          className={"rail-item" + (s === surface ? " is-on" : "")}
          aria-current={s === surface ? "page" : undefined}
          onClick={() => onSurface(s)}
        >
          <span className="rail-mark">{MARKS[s]}</span>
          <span className="rail-label">{SURFACE_LABELS[s]}</span>
          {s === "advice" && adviceCount > 0 && (
            <span className="rail-badge num">{adviceCount}</span>
          )}
        </button>
      ))}
    </nav>
  );
}
