import { FormEvent, useEffect, useState } from "react";
import { AuthInfo, authInfo, readOnlySession, storageUnavailable, token } from "../auth";
import { completeSignIn, isCallback, restore, signIn } from "../oidc";

/**
 * Sign in (assets/design/README.md, 3a): the left panel carries the lockup,
 * "It produces the plan and stops", and the one ambient animation the whole
 * product allows — a 6s loop of pods leaving two nodes and arriving on
 * three, which is the entire pitch drawn once. prefers-reduced-motion
 * renders the loop's end state statically.
 *
 * The auth mechanics are unchanged: OIDC when an issuer is configured, a
 * pasted token otherwise, both ending as a bearer token the ui-backend hands
 * to TokenReview. The read-only checkbox hides the drain affordances for
 * this session — an affordance, not a boundary; RBAC enforces regardless.
 */
export function SignIn({ onDone, clusterLabel }: { onDone: () => void; clusterLabel?: string }) {
  const [info, setInfo] = useState<AuthInfo | null>(null);
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [readOnly, setReadOnly] = useState(readOnlySession.get);

  // Being shown this form while a token is already held means that token was
  // rejected. Saying so beats re-presenting an identical empty form, which
  // reads as the button doing nothing.
  const [rejected] = useState(() => token.get() !== null);

  useEffect(() => {
    let live = true;

    void (async () => {
      const i = await authInfo();
      if (!live) return;
      setInfo(i);

      try {
        // Returning from the issuer with a code to exchange.
        if (isCallback()) {
          setBusy(true);
          const idToken = await completeSignIn(i);
          if (idToken) {
            token.set(idToken);
            onDone();
            return;
          }
          setFailure("The issuer redirected back without a usable token.");
          return;
        }
        // A session from earlier in this tab.
        const existing = await restore(i);
        if (existing) {
          token.set(existing);
          onDone();
        }
      } catch (err) {
        if (live) setFailure(err instanceof Error ? err.message : String(err));
      } finally {
        if (live) setBusy(false);
      }
    })();

    return () => {
      live = false;
    };
  }, [onDone]);

  const start = async () => {
    if (!info) return;
    setFailure(null);
    setBusy(true);
    readOnlySession.set(readOnly);
    try {
      await signIn(info);
    } catch (err) {
      setFailure(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  const submitToken = (e: FormEvent) => {
    e.preventDefault();
    if (!value.trim()) return;
    readOnlySession.set(readOnly);
    token.set(value);
    setValue("");
    onDone();
  };

  const sso = info?.oidc.enabled && info.oidc.issuerUrl;

  return (
    <div className="signin">
      <div className="signin-brand">
        <div className="signin-lockup">
          <div className="signin-mark">
            <div className="signin-halo" aria-hidden="true" />
            <svg viewBox="0 0 512 512" aria-hidden="true">
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
          </div>
          <div className="signin-wordmark">
            <span className="signin-name">k8s-dencer</span>
            <span className="signin-tag eyebrow mono">Consolidation planner</span>
          </div>
        </div>

        <div className="signin-pitch">
          <h2 className="signin-thesis">
            It produces the plan
            <br />
            and stops.
          </h2>
          <p className="signin-pitch-text">
            Every consolidation is named before it happens: which pods move, which node is
            freed, and which rule would refuse. Nothing is drained until you say so.
          </p>
          <AmbientLoop />
        </div>
      </div>

      <div className="signin-panel">
        <div className="signin-card">
          <div className="signin-cardhead">
            <h3 className="signin-title">Sign in</h3>
            <p className="signin-detail">
              Your Kubernetes identity is the identity k8s-dencer acts as. Nothing is
              impersonated.
            </p>
          </div>

          {clusterLabel && (
            <div className="signin-field">
              <span className="signin-label">Cluster</span>
              <div className="signin-cluster">
                <span className="mono signin-cluster-name">{clusterLabel}</span>
                <span className="signin-cluster-side">context</span>
              </div>
            </div>
          )}

          {failure && (
            <p className="signin-failure" role="alert">
              {failure}
            </p>
          )}
          {!failure && rejected && (
            <p className="signin-failure" role="alert">
              That credential was rejected — it may have expired, or it may lack permission to
              read plans. <code>make token</code> mints a fresh one.
            </p>
          )}
          {storageUnavailable && (
            <p className="signin-detail">
              This browser is blocking session storage, so the token will not survive a reload.
              Signing in still works for this page.
            </p>
          )}

          {sso && (
            <>
              <button className="signin-sso" onClick={start} disabled={busy}>
                <span>{busy ? "Signing in…" : "Continue with OIDC"}</span>
                <span className="signin-sso-issuer mono">{issuerHost(info.oidc.issuerUrl)}</span>
              </button>
              <div className="signin-or" aria-hidden="true">
                <span />
                or
                <span />
              </div>
            </>
          )}

          <form onSubmit={submitToken} className="signin-field">
            <label className="signin-label" htmlFor="token">
              Service-account token
            </label>
            <input
              id="token"
              className="signin-input mono"
              type="password"
              autoComplete="off"
              spellCheck={false}
              value={value}
              onChange={(e) => setValue(e.target.value)}
              placeholder="eyJhbGciOi…"
            />
            <label className="signin-readonly">
              <input
                type="checkbox"
                checked={readOnly}
                onChange={(e) => setReadOnly(e.target.checked)}
              />
              Read-only session — plan and rehearse, never drain
            </label>
            <button type="submit" className="btn signin-tokenbtn" disabled={!value.trim()}>
              Sign in with token
            </button>
          </form>

          <div className="signin-note">
            <span aria-hidden="true">▲</span>
            <span>
              Sessions expire with your token. A run in progress survives a sign-out and keeps
              its own audit entry.
            </span>
          </div>

          <details className="signin-help">
            <summary>How do I get a token?</summary>
            <pre className="mono">kubectl create token dencer-operator -n k8s-dencer</pre>
            <p>
              Or run <code>make token</code> from the repository, which mints one and prints it.
            </p>
          </details>
        </div>
      </div>
    </div>
  );
}

function issuerHost(url?: string): string {
  try {
    return url ? new URL(url).host : "";
  } catch {
    return url ?? "";
  }
}

/**
 * The one animation the product allows: pods lift off two nodes, arrive on
 * three, and the emptied tiles go green — 6s, ambient, honest about what the
 * tool does. Reduced motion renders the end state (signin.css).
 */
function AmbientLoop() {
  const strip: Array<{ pods: number[]; arriving?: number; freeing?: boolean; delay: string }> = [
    { pods: [2, 1], arriving: 1, delay: "0s" },
    { pods: [1, 2], arriving: 1, delay: "0.25s" },
    { pods: [2, 1], arriving: 2, delay: "0.5s" },
    { pods: [1, 2], freeing: true, delay: "0s" },
    { pods: [2, 1], freeing: true, delay: "0.3s" },
  ];
  return (
    <div className="ambient" aria-hidden="true">
      {strip.map((n, i) => (
        <div
          key={i}
          className={"ambient-node" + (n.freeing ? " ambient-free" : "")}
          style={n.freeing ? { animationDelay: n.delay } : undefined}
        >
          <span
            className={"ambient-dot" + (n.freeing ? " ambient-dot-free" : "")}
            style={n.freeing ? { animationDelay: n.delay } : undefined}
          />
          <div className="ambient-pods">
            {n.pods.map((flex, j) => (
              <div
                key={j}
                className={"ambient-pod" + (n.freeing ? " ambient-out" : "")}
                style={{ flex, animationDelay: n.freeing ? `calc(${n.delay} + ${j * 0.12}s)` : undefined }}
              />
            ))}
            {n.arriving != null && (
              <div
                className="ambient-pod ambient-in"
                style={{ flex: n.arriving, animationDelay: n.delay }}
              />
            )}
          </div>
        </div>
      ))}
    </div>
  );
}
