// Default runtime configuration. In-cluster this file is replaced by a
// ConfigMap mount, so the same image works against any backend without a
// rebuild. Keep this a plain script, not a module: index.html loads it before
// the bundle so the values are available at first render.
window.__DENCER_CONFIG__ = {
  // Same-origin by default: nginx proxies /api to the ui-backend Service.
  apiBaseUrl: "",
  // Deep link to the Kagent chat UI; empty hides the header link.
  kagentUrl: "",
};
