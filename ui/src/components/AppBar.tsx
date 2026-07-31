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

interface Props {
  /** Operator-set. Empty renders nothing rather than a guess. */
  clusterLabel?: string;
  /** The identity the API server verified, not one the browser asserted. */
  identity?: string;
  onSignOut?: () => void;
}

export default function AppBar({ clusterLabel, identity, onSignOut }: Props) {
  return (
    <header className="appbar">
      <div className="appbar-brand">
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
