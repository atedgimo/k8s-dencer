import { FormEvent, useEffect, useState } from "react";
import { AuthInfo, authInfo, storageUnavailable, token } from "../auth";
import { completeSignIn, isCallback, restore, signIn } from "../oidc";

/**
 * Gets the operator a credential.
 *
 * Single sign-on when the install has an issuer configured, a pasted token
 * otherwise. Both end in the same place — a bearer token on every request,
 * which ui-backend hands to TokenReview — so the rest of the app never learns
 * which path was taken.
 */
export function SignIn({ onDone }: { onDone: () => void }) {
  const [info, setInfo] = useState<AuthInfo | null>(null);
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);

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
    token.set(value);
    setValue("");
    onDone();
  };

  const sso = info?.oidc.enabled && info.oidc.issuerUrl;

  return (
    <div className="signin">
      <h2>Sign in</h2>
      <p className="signin-detail">
        This install requires a Kubernetes credential. Permission is granted by RBAC, so any
        identity your cluster already trusts will work.
      </p>

      {failure && (
        <p className="signin-failure" role="alert">
          {failure}
        </p>
      )}

      {!failure && rejected && (
        <p className="signin-failure" role="alert">
          That credential was rejected — it may have expired, or it may lack
          permission to read plans. <code>make token</code> mints a fresh one.
        </p>
      )}

      {storageUnavailable && (
        <p className="signin-detail">
          This browser is blocking session storage, so the token will not
          survive a reload. Signing in still works for this page.
        </p>
      )}

      {sso && (
        <>
          <button className="signin-sso" onClick={start} disabled={busy}>
            {busy ? "Signing in…" : "Sign in with single sign-on"}
          </button>
          <p className="signin-issuer mono">{info.oidc.issuerUrl}</p>
          <p className="signin-or">or paste a token</p>
        </>
      )}

      <form onSubmit={submitToken}>
        <label htmlFor="token">Bearer token</label>
        <input
          id="token"
          type="password"
          autoComplete="off"
          spellCheck={false}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder="eyJhbGciOi…"
        />
        <button type="submit" disabled={!value.trim()}>
          Continue
        </button>
      </form>

      <details className="signin-help">
        <summary>How do I get one?</summary>
        <pre>kubectl create token dencer-operator -n k8s-dencer</pre>
        <p>
          Or run <code>make token</code> from the repository, which mints one and prints it.
        </p>
      </details>
    </div>
  );
}
