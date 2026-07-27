import { runtimeConfig } from "./runtime-config";

/** Public description of how this install expects callers to authenticate. */
export interface AuthInfo {
  enabled: boolean;
  anonymousRead: boolean;
  oidc: {
    enabled: boolean;
    issuerUrl?: string;
    clientId?: string;
    scopes?: string[];
  };
}

const STORAGE_KEY = "dencer.token";

/**
 * Holds the caller's bearer token for the lifetime of the tab.
 *
 * sessionStorage rather than localStorage: a token in localStorage outlives the
 * browsing session and is readable by every script on the origin for as long as
 * it sits there. Scoping it to the tab means closing the tab ends the session,
 * which is the behaviour an operator expects from something that can drain
 * nodes.
 *
 * M12 replaces manual entry with an OIDC redirect flow, at which point this
 * becomes the fallback for installs without an issuer configured.
 */
export const token = {
  get(): string | null {
    try {
      return sessionStorage.getItem(STORAGE_KEY);
    } catch {
      // Storage can be blocked outright by browser settings. Losing the token
      // on reload is a far better outcome than failing to render.
      return null;
    }
  },

  set(value: string): void {
    try {
      sessionStorage.setItem(STORAGE_KEY, value.trim());
    } catch {
      /* non-fatal; see get() */
    }
    notify();
  },

  clear(): void {
    try {
      sessionStorage.removeItem(STORAGE_KEY);
    } catch {
      /* non-fatal */
    }
    notify();
  },
};

const listeners = new Set<() => void>();

function notify() {
  for (const fn of listeners) fn();
}

/** Subscribes to token changes so views can re-fetch after a sign-in. */
export function onTokenChange(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

/** Adds the Authorization header when a token is held. */
export function authHeaders(init?: HeadersInit): Headers {
  const headers = new Headers(init);
  const value = token.get();
  if (value) headers.set("Authorization", `Bearer ${value}`);
  return headers;
}

let cached: Promise<AuthInfo> | null = null;

/**
 * Fetches how this install authenticates. Served unauthenticated by design —
 * a client cannot sign in without first learning where to sign in.
 *
 * Cached for the page's lifetime: the answer is fixed at deploy time.
 */
export function authInfo(): Promise<AuthInfo> {
  cached ??= fetch(`${runtimeConfig().apiBaseUrl}/api/v1/authinfo`)
    .then((res) => (res.ok ? (res.json() as Promise<AuthInfo>) : Promise.reject(new Error(String(res.status)))))
    .catch(
      // An older backend has no authinfo route. Assuming auth is off matches
      // how that build actually behaves.
      (): AuthInfo => ({ enabled: false, anonymousRead: true, oidc: { enabled: false } }),
    );
  return cached;
}
