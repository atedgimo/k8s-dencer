/**
 * The top bar — plan identity and freshness (assets/design/README.md, 1a).
 *
 * 60px: what plan this is (name + short hash), whether it still describes
 * the cluster (the freshness dot), which algorithm produced it, and the one
 * action that follows from staleness — Recompute. The theme toggle and a
 * settings stub sit on the right.
 *
 * "Recompute" surfaces what the planner already does: it recomputes every
 * resync, and this button shows the latest plan when the view is pinned on
 * an older one, or refetches otherwise. The freshness dot is the honest
 * version of the old "the cluster may have moved on" apology — green while
 * the server keeps confirming this plan, amber the moment a newer one
 * exists.
 */

import { useEffect, useState } from "react";
import { Theme, applyTheme, storedTheme } from "../theme";

interface Props {
  /** The plan's full id; the bar shows the short hash. */
  planId?: string | null;
  strategy?: string;
  /** When the server last confirmed the plan on screen. */
  confirmedAt?: string | null;
  /** A newer plan exists; this one is pinned. */
  stale?: boolean;
  onRecompute?: () => void;
}

export default function TopBar({ planId, strategy, confirmedAt, stale, onRecompute }: Props) {
  return (
    <header className="topbar">
      {planId && (
        <>
          <div className="topbar-plan">
            <span className="topbar-title">Consolidation plan</span>
            <span className="topbar-hash mono">{planId.slice(0, 7)}</span>
          </div>
          <span className="topbar-sep" aria-hidden="true" />
          <Freshness confirmedAt={confirmedAt} stale={stale} />
          {strategy && <span className="topbar-strategy mono">{strategy}</span>}
        </>
      )}
      <div className="topbar-actions">
        {onRecompute && (
          <button type="button" className="topbar-btn" onClick={onRecompute}>
            <svg viewBox="0 0 16 16" className="topbar-btn-icon" aria-hidden="true">
              <path
                d="M13.5 8a5.5 5.5 0 1 1-1.7-4M13.5 2.5V6h-3.4"
                stroke="currentColor"
                strokeWidth="1.4"
                fill="none"
                strokeLinecap="round"
              />
            </svg>
            Recompute
          </button>
        )}
        <ThemeToggle />
        {/* Settings is designed as a destination but not yet specified;
            the stub keeps its place in the frame without pretending. */}
        <button type="button" className="topbar-square" disabled title="Settings — not yet available">
          <svg viewBox="0 0 16 16" className="topbar-btn-icon" aria-hidden="true">
            <circle cx="8" cy="8" r="2.6" stroke="currentColor" strokeWidth="1.4" fill="none" />
            <path
              d="M8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2"
              stroke="currentColor"
              strokeWidth="1.4"
              strokeLinecap="round"
            />
          </svg>
        </button>
      </div>
    </header>
  );
}

/**
 * "Fresh · confirmed 41s ago" — live, because a freshness claim with a
 * stopped clock is the thing it is trying to replace. Re-renders on a
 * 10-second tick; the confirmation itself is polled by useVersion.
 */
function Freshness({ confirmedAt, stale }: { confirmedAt?: string | null; stale?: boolean }) {
  const [, setTick] = useState(0);
  useEffect(() => {
    const t = setInterval(() => setTick((n) => n + 1), 10_000);
    return () => clearInterval(t);
  }, []);

  if (stale) {
    return (
      <span className="topbar-fresh" role="status">
        <span className="topbar-dot topbar-dot-stale" aria-hidden="true" />
        Superseded · a newer plan exists
      </span>
    );
  }
  if (!confirmedAt) return null;

  const age = Math.max(0, Date.now() - Date.parse(confirmedAt));
  return (
    <span className="topbar-fresh">
      <span className="topbar-dot" aria-hidden="true" />
      Fresh · confirmed {relative(age)} ago
    </span>
  );
}

function relative(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s < 90) return `${s}s`;
  const m = Math.round(s / 60);
  if (m < 90) return `${m}m`;
  return `${Math.round(m / 60)}h`;
}

/**
 * Dark or light, the operator's choice rather than their desktop's.
 *
 * A glyph and the target theme's name, not an unlabelled sun/moon: this
 * surface asks colours to carry exact meanings, and an ambiguous pictogram
 * would be that mistake in miniature.
 */
function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(storedTheme);

  const next: Theme = theme === "dark" ? "light" : "dark";
  return (
    <button
      type="button"
      className="topbar-btn"
      aria-label={`Switch to the ${next} theme`}
      title={`Switch to the ${next} theme`}
      onClick={() => {
        applyTheme(next);
        setTheme(next);
      }}
    >
      <span aria-hidden="true">{theme === "dark" ? "◐" : "◑"}</span>
      {next}
    </button>
  );
}
