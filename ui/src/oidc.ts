import type { User, UserManager } from "oidc-client-ts";
import { AuthInfo } from "./auth";

/**
 * Single sign-on against the cluster's own OIDC issuer.
 *
 * The whole trick is that we validate nothing ourselves. When the API server
 * runs with --oidc-issuer-url, an ID token from that issuer is *already* a
 * Kubernetes credential — so the browser obtains one and hands it to
 * ui-backend, which passes it to TokenReview, and the API server does the
 * verification. No session store, no user database, no client secret.
 *
 * Which means the token we care about is the **ID token**, not the access
 * token. An access token means nothing to the Kubernetes API server; the ID
 * token is the credential. Getting this backwards is the classic way to make
 * this integration fail with a confusing 401.
 *
 * oidc-client-ts is loaded on demand rather than bundled into the entry: it is
 * ~19 kB gzipped, single sign-on is off by default, and an install using a
 * pasted token should not pay for a library it never calls.
 */

const REDIRECT_PATH = "/oidc/callback";

let manager: UserManager | null = null;

/** Builds the manager, or returns null when OIDC is not configured. */
export async function oidcManager(info: AuthInfo): Promise<UserManager | null> {
  if (!info.oidc.enabled || !info.oidc.issuerUrl || !info.oidc.clientId) return null;
  if (manager) return manager;

  const { UserManager, WebStorageStateStore } = await import("oidc-client-ts");

  manager = new UserManager({
    authority: info.oidc.issuerUrl,
    client_id: info.oidc.clientId,
    redirect_uri: window.location.origin + REDIRECT_PATH,
    post_logout_redirect_uri: window.location.origin,
    response_type: "code",
    scope: (info.oidc.scopes ?? ["openid", "profile", "email", "groups"]).join(" "),

    // A public client using Authorization Code + PKCE. There is no secret,
    // because a secret shipped to a browser is not a secret.
    // oidc-client-ts enables PKCE for response_type=code by default.
    //
    // sessionStorage, not the default localStorage: a credential that can
    // drain nodes should not outlive the tab, and test/ui fails the build if
    // this changes.
    userStore: new WebStorageStateStore({ store: window.sessionStorage }),
    stateStore: new WebStorageStateStore({ store: window.sessionStorage }),

    // Renew quietly before expiry so a long consolidation is not interrupted
    // by a sign-in prompt. Only the interactive session needs this — a run
    // already in flight is authorized once at enqueue and finishes under the
    // executor's own identity.
    automaticSilentRenew: true,
    accessTokenExpiringNotificationTimeInSeconds: 60,

    // We never call a userinfo endpoint: everything we need about the caller
    // comes back from TokenReview, named by the API server rather than
    // self-reported by the browser.
    loadUserInfo: false,
  });

  return manager;
}

/** True when the current URL is the redirect landing. */
export function isCallback(): boolean {
  return window.location.pathname === REDIRECT_PATH;
}

/** Starts the redirect. */
export async function signIn(info: AuthInfo): Promise<void> {
  const m = await oidcManager(info);
  if (!m) throw new Error("single sign-on is not configured for this install");
  await m.signinRedirect();
}

/**
 * Completes the redirect and returns the ID token.
 *
 * Clears the query string afterwards so a reload does not replay a consumed
 * authorization code, which the issuer will refuse.
 */
export async function completeSignIn(info: AuthInfo): Promise<string | null> {
  const m = await oidcManager(info);
  if (!m) return null;
  const user = await m.signinRedirectCallback();
  window.history.replaceState({}, "", "/");
  return idTokenOf(user);
}

/** Restores a session from storage on load, if one is still valid. */
export async function restore(info: AuthInfo): Promise<string | null> {
  const m = await oidcManager(info);
  if (!m) return null;
  const user = await m.getUser();
  if (!user || user.expired) return null;
  return idTokenOf(user);
}

/** Calls back with a fresh ID token whenever one is issued. */
export async function onRenewed(
  info: AuthInfo,
  fn: (idToken: string) => void,
): Promise<() => void> {
  const m = await oidcManager(info);
  if (!m) return () => {};
  const handler = (user: User) => {
    const t = idTokenOf(user);
    if (t) fn(t);
  };
  m.events.addUserLoaded(handler);
  return () => m.events.removeUserLoaded(handler);
}

export async function signOut(info: AuthInfo): Promise<void> {
  const m = await oidcManager(info);
  if (!m) return;
  await m.removeUser();
}

/** The ID token is the Kubernetes credential; the access token is not. */
function idTokenOf(user: User | null): string | null {
  return user?.id_token ?? null;
}
