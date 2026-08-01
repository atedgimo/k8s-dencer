/**
 * The frame around the instrument.
 *
 * It exists because of a gap that looked cosmetic and is not: before this, the
 * UI showed neither who you were signed in as nor which cluster you were
 * looking at. For a tool whose entire pitch is that a human approves before
 * pods are evicted, the human was absent from the screen — and so was the
 * answer to "am I about to drain production?"
 *
 * Every console an SRE actually trusts puts account and environment in the top
 * bar, for exactly that reason.
 *
 * Deliberately quiet: a hairline, no fill, no colour. Colour on this surface
 * means risk, and a header competing for that signal would undo the thing that
 * makes a Green/Yellow/Red rating land.
 */

import { useState } from "react";
import { Theme, applyTheme, storedTheme } from "../theme";
import { FieldView, Surface, VIEW_LABELS } from "../view";

interface Props {
  /** Operator-set. Empty renders nothing rather than a guess. */
  clusterLabel?: string;
  /** The identity the API server verified, not one the browser asserted. */
  identity?: string;
  onSignOut?: () => void;
  view?: FieldView;
  onView?: (v: FieldView) => void;
  surface?: Surface;
  onSurface?: (s: Surface) => void;
}

export default function AppBar({ clusterLabel, identity, onSignOut, view, onView, surface, onSurface }: Props) {
  return (
    <header className="appbar">
      <div className="appbar-brand">
        {/* The 1a mark in monochrome, on currentColor. The brand's blue
            deliberately stays off this surface: colour here means risk and
            nothing else, and the handoff ships mono variants for exactly
            this context. Inline so it costs no fetch and inherits theme. */}
        <svg
          className="appbar-icon"
          viewBox="0 0 512 512"
          width="20"
          height="20"
          aria-hidden="true"
        >
          <polygon
            points="238,174 330.3,218.4 353,318.3 289.2,398.3 186.8,398.3 123,318.3 145.7,218.4"
            fill="none"
            stroke="currentColor"
            strokeWidth="40"
            strokeLinejoin="round"
          />
          <rect x="332" y="96" width="40" height="322" rx="20" fill="currentColor" />
        </svg>
        <span className="appbar-mark">k8s-dencer</span>
        {clusterLabel && (
          <>
            <span className="appbar-sep" aria-hidden="true" />
            {/* Not a status dot. It is a neutral marker for "this is the
                cluster", and it stays achromatic so it cannot be read as a
                health signal. */}
            <span className="appbar-cluster mono" title="The cluster this view is showing">
              <span className="appbar-cluster-dot" aria-hidden="true" />
              {clusterLabel}
            </span>
          </>
        )}
      </div>

      <div className="appbar-actions">
        {view && onView && (
          /* Three renderings of the same data, not three themes. Which one
             suits depends mostly on cluster size, so the default follows it —
             this is the override for when it guesses wrong. */
          <div className="viewswitch" role="group" aria-label="Field view">
            {(Object.keys(VIEW_LABELS) as FieldView[]).map((v) => (
              <button
                key={v}
                type="button"
                className={"viewswitch-btn" + (v === view && surface !== "history" ? " is-on" : "")}
                aria-pressed={v === view && surface !== "history"}
                onClick={() => {
                  onSurface?.("field");
                  onView(v);
                }}
              >
                {VIEW_LABELS[v]}
              </button>
            ))}
            {/* History sits in the same switch because it is the same
                gesture — "show me the cluster differently" — even though it
                is a different axis underneath (a question about time, not a
                way of drawing nodes). */}
            {onSurface && (
              <button
                type="button"
                className={"viewswitch-btn" + (surface === "history" ? " is-on" : "")}
                aria-pressed={surface === "history"}
                onClick={() => onSurface("history")}
              >
                History
              </button>
            )}
          </div>
        )}
        <ThemeToggle />
        {identity && (
          /* The full RBAC principal, which for a ServiceAccount is long and
             gets truncated. The title carries the whole thing, because the
             part that gets cut is often the part that distinguishes two
             accounts. */
          <span className="appbar-identity mono" title={identity}>
            {identity}
          </span>
        )}
        {onSignOut && (
          <button type="button" className="appbar-signout" onClick={onSignOut}>
            Sign out
          </button>
        )}
      </div>
    </header>
  );
}

/**
 * Dark or light, the operator's choice rather than their desktop's.
 *
 * A glyph and a label, not an unlabelled icon: this is the same surface that
 * refuses to let colour carry meaning on its own, and an ambiguous sun/moon
 * pictogram would be that mistake in another form.
 */
function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>(storedTheme);

  const next: Theme = theme === "dark" ? "light" : "dark";
  return (
    <button
      type="button"
      className="appbar-theme"
      aria-label={`Switch to the ${next} theme`}
      title={`Switch to the ${next} theme`}
      onClick={() => {
        applyTheme(next);
        setTheme(next);
      }}
    >
      <span aria-hidden="true">{theme === "dark" ? "◐" : "◑"}</span>
      <span className="appbar-theme-word">{next}</span>
    </button>
  );
}
