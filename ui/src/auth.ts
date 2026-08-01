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
// Memory is the source of truth; sessionStorage only lets a token survive a
// reload.
//
// It used to be storage-only, with a swallowed exception. Any browser that
// blocks storage — private mode, strict privacy settings, an embedded webview
// — therefore dropped the token silently: you pasted, clicked Continue, and
// the form came back with no explanation, forever. Holding it in memory means
// the session works regardless, and only persistence is lost.
let inMemory: string | null = null;

/** True when the token could not be persisted, so a reload will lose it. */
export let storageUnavailable = false;

export const token = {
  get(): string | null {
    if (inMemory !== null) return inMemory;
    try {
      inMemory = sessionStorage.getItem(STORAGE_KEY);
    } catch {
      storageUnavailable = true;
      inMemory = null;
    }
    return inMemory;
  },

  set(value: string): void {
    inMemory = value.trim();
    try {
      sessionStorage.setItem(STORAGE_KEY, inMemory);
    } catch {
      // The session still works; it just will not outlive a reload.
      storageUnavailable = true;
    }
    notify();
  },

  clear(): void {
    inMemory = null;
    try {
      sessionStorage.removeItem(STORAGE_KEY);
    } catch {
      storageUnavailable = true;
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

/**
 * A read-only session hides the drain affordances client-side. This is an
 * affordance, not a boundary: the server enforces execute permission through
 * RBAC regardless of what this flag says. It exists for the operator who
 * wants to review a plan with the safety on — same tab-lifetime as the token,
 * because outliving the credential it qualifies would be nonsense.
 */
const READONLY_KEY = "dencer.readOnly";

export const readOnlySession = {
  get(): boolean {
    try {
      return sessionStorage.getItem(READONLY_KEY) === "1";
    } catch {
      return false;
    }
  },
  set(on: boolean) {
    try {
      if (on) sessionStorage.setItem(READONLY_KEY, "1");
      else sessionStorage.removeItem(READONLY_KEY);
    } catch {
      // Applies for this page; simply will not survive a reload.
    }
  },
};
