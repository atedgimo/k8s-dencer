import { FormEvent, useEffect, useState } from "react";
import { AuthInfo, authInfo, token } from "../auth";

/**
 * Collects a bearer token when the backend has rejected the request.
 *
 * Deliberately plain. M11 rebuilds the header and M12 replaces this with an
 * OIDC redirect flow, so anything more elaborate here would be built twice —
 * but an install with auth on and no way to present a credential is unusable,
 * so it cannot simply wait.
 */
export function SignIn({ onDone }: { onDone: () => void }) {
  const [info, setInfo] = useState<AuthInfo | null>(null);
  const [value, setValue] = useState("");

  useEffect(() => {
    let live = true;
    void authInfo().then((i) => live && setInfo(i));
    return () => {
      live = false;
    };
  }, []);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!value.trim()) return;
    token.set(value);
    setValue("");
    onDone();
  };

  return (
    <div className="signin">
      <h2>Sign in</h2>
      <p className="signin-detail">
        This install requires a Kubernetes credential. Permission to view plans is granted by RBAC,
        so any token your cluster already trusts will work.
      </p>

      {info?.oidc.enabled && info.oidc.issuerUrl && (
        <p className="signin-detail">
          Single sign-on is configured against <code>{info.oidc.issuerUrl}</code>. Browser sign-in
          arrives in the next release; until then, paste a token below.
        </p>
      )}

      <form onSubmit={submit}>
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
        <pre>kubectl create token dencer-viewer -n k8s-dencer</pre>
        <p>
          Or run <code>make token</code> from the repository, which mints one and prints it.
        </p>
      </details>
    </div>
  );
}
