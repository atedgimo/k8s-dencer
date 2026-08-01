/**
 * The left rail — the redesign's spine (assets/design/README.md, screen 1a).
 *
 * 224px: lockup, the four destinations, and at the bottom the two facts the
 * old UI once forgot to show anywhere — which cluster this is, and who you
 * are. For a tool whose pitch is that a human approves before pods are
 * evicted, both belong on every screen, which is why they live in the frame
 * and not on a settings page.
 *
 * Badges carry work, not decoration: Review counts the plan's steps,
 * Recommendations counts high-severity findings. A destination with nothing
 * to do shows no number.
 */

import { Surface } from "../view";

interface Props {
  surface: Surface;
  onSurface: (s: Surface) => void;
  /** Steps in the current plan; the Review badge. */
  stepCount?: number;
  /** High-severity findings; the Recommendations badge. */
  highFindings?: number;
  /** Operator-set. Empty renders nothing rather than a guess. */
  clusterLabel?: string;
  /** The identity the API server verified, not one the browser asserted. */
  identity?: string;
  onSignOut?: () => void;
  /** A run in flight — the one fact worth showing on every destination. */
  runNote?: { label: string; value: string };
}

const ICONS: Record<Surface, React.ReactNode> = {
  review: (
    <svg viewBox="0 0 16 16" className="rail-icon" aria-hidden="true">
      <path d="M2.5 4h11M2.5 8h11M2.5 12h7" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" fill="none" />
    </svg>
  ),
  cluster: (
    <svg viewBox="0 0 16 16" className="rail-icon" aria-hidden="true">
      <rect x="2" y="2.5" width="5" height="5" rx="1" stroke="currentColor" strokeWidth="1.4" fill="none" />
      <rect x="9" y="2.5" width="5" height="5" rx="1" stroke="currentColor" strokeWidth="1.4" fill="none" />
      <rect x="2" y="9" width="5" height="5" rx="1" stroke="currentColor" strokeWidth="1.4" fill="none" />
      <rect x="9" y="9" width="5" height="5" rx="1" stroke="currentColor" strokeWidth="1.4" fill="none" />
    </svg>
  ),
  recommendations: (
    <svg viewBox="0 0 16 16" className="rail-icon" aria-hidden="true">
      <path d="M8 2v3M8 11v3M3.5 8h-2M14.5 8h-2" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
      <circle cx="8" cy="8" r="3" stroke="currentColor" strokeWidth="1.4" fill="none" />
    </svg>
  ),
  history: (
    <svg viewBox="0 0 16 16" className="rail-icon" aria-hidden="true">
      <circle cx="8" cy="8" r="6" stroke="currentColor" strokeWidth="1.4" fill="none" />
      <path d="M8 5v3.2l2.2 1.4" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" fill="none" />
    </svg>
  ),
};

const LABELS: Record<Surface, string> = {
  review: "Review",
  cluster: "Cluster",
  recommendations: "Recommendations",
  history: "History",
};

export default function Rail({
  surface,
  onSurface,
  stepCount,
  highFindings,
  clusterLabel,
  identity,
  onSignOut,
  runNote,
}: Props) {
  return (
    <nav className="rail" aria-label="Destinations">
      <div className="rail-lockup">
        {/* The mark on its accent disc, per the lockup spec. White strokes on
            the disc in both themes — the disc carries the theme change. */}
        <svg className="rail-logo" viewBox="0 0 512 512" aria-hidden="true">
          <circle cx="256" cy="256" r="256" fill="var(--accent)" />
          <polygon
            points="238,174 330.3,218.4 353,318.3 289.2,398.3 186.8,398.3 123,318.3 145.7,218.4"
            fill="none"
            stroke="#fff"
            strokeWidth="40"
            strokeLinejoin="round"
          />
          <rect x="332" y="96" width="40" height="322" rx="20" fill="#fff" />
        </svg>
        <span className="rail-wordmark">k8s-dencer</span>
      </div>

      <div className="rail-nav">
        <div className="rail-group eyebrow mono">Plan</div>
        {(Object.keys(LABELS) as Surface[]).map((s) => (
          <button
            key={s}
            type="button"
            className={"rail-item" + (s === surface ? " is-on" : "")}
            aria-current={s === surface ? "page" : undefined}
            onClick={() => onSurface(s)}
          >
            {ICONS[s]}
            {LABELS[s]}
            {s === "review" && stepCount != null && stepCount > 0 && (
              <span className="rail-badge mono">{stepCount}</span>
            )}
            {s === "recommendations" && highFindings != null && highFindings > 0 && (
              <span className="rail-badge rail-badge-pill mono">{highFindings}</span>
            )}
          </button>
        ))}
      </div>

      <div className="rail-foot">
        {runNote && (
          <div className="rail-run" role="status">
            <span className="rail-run-label eyebrow mono">{runNote.label}</span>
            <span className="rail-run-value mono">{runNote.value}</span>
          </div>
        )}
        {clusterLabel && (
          <div className="rail-cluster" title="The cluster this view is showing">
            <span className="rail-cluster-label eyebrow mono">Cluster</span>
            <span className="rail-cluster-name mono">{clusterLabel}</span>
          </div>
        )}
        {identity && (
          <div className="rail-identity">
            <span className="rail-avatar mono" aria-hidden="true">
              sa
            </span>
            {/* The full RBAC principal is long and gets truncated; the title
                carries the whole thing, because the part that gets cut is
                often the part that distinguishes two accounts. */}
            <span className="rail-identity-name mono" title={identity}>
              {identity}
            </span>
            {onSignOut && (
              <button type="button" className="rail-signout" onClick={onSignOut}>
                Sign out
              </button>
            )}
          </div>
        )}
      </div>
    </nav>
  );
}
