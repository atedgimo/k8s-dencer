export interface RuntimeConfig {
  /** Base URL for the ui-backend API. Empty means same-origin via nginx. */
  apiBaseUrl: string;
  /** Deep link to the Kagent chat UI. Empty hides the header link. */
  kagentUrl: string;
}

declare global {
  interface Window {
    __DENCER_CONFIG__?: Partial<RuntimeConfig>;
  }
}

const defaults: RuntimeConfig = {
  apiBaseUrl: "",
  kagentUrl: "",
};

/**
 * Reads configuration injected at pod start. Falls back to same-origin
 * defaults so `npm run dev` works without a ConfigMap.
 */
export function runtimeConfig(): RuntimeConfig {
  return { ...defaults, ...(window.__DENCER_CONFIG__ ?? {}) };
}
