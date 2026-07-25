import { useEffect, useState } from "react";
import { runtimeConfig } from "./runtime-config";

interface VersionInfo {
  version: string;
  database: string;
}

type Status =
  | { state: "loading" }
  | { state: "ok"; info: VersionInfo }
  | { state: "error"; message: string };

/**
 * M0 placeholder. It deliberately calls the backend rather than rendering a
 * static page: that makes deploying the chart a real end-to-end check of
 * frontend -> nginx proxy -> ui-backend Service wiring.
 *
 * The graph canvas, step timeline and constraint inspector land in M7.
 */
export default function App() {
  const { apiBaseUrl, kagentUrl } = runtimeConfig();
  const [status, setStatus] = useState<Status>({ state: "loading" });

  useEffect(() => {
    const controller = new AbortController();

    fetch(`${apiBaseUrl}/api/v1/version`, { signal: controller.signal })
      .then(async (res) => {
        if (!res.ok) {
          throw new Error(`backend returned ${res.status}`);
        }
        return (await res.json()) as VersionInfo;
      })
      .then((info) => setStatus({ state: "ok", info }))
      .catch((err: unknown) => {
        if (controller.signal.aborted) return;
        setStatus({
          state: "error",
          message: err instanceof Error ? err.message : String(err),
        });
      });

    return () => controller.abort();
  }, [apiBaseUrl]);

  return (
    <main className="shell">
      <header className="header">
        <div>
          <h1>k8s-dencer</h1>
          <p className="tagline">Node consolidation planning, explained.</p>
        </div>
        {kagentUrl && (
          <a className="link" href={kagentUrl} target="_blank" rel="noreferrer">
            Ask the agent →
          </a>
        )}
      </header>

      <section className="panel">
        <h2>Backend</h2>
        {status.state === "loading" && <p className="muted">Connecting…</p>}
        {status.state === "ok" && (
          <dl className="facts">
            <dt>Status</dt>
            <dd>
              <span className="dot dot-ok" aria-hidden="true" />
              Connected
            </dd>
            <dt>Version</dt>
            <dd>{status.info.version}</dd>
            <dt>Plan store</dt>
            <dd>{status.info.database}</dd>
          </dl>
        )}
        {status.state === "error" && (
          <p className="error">
            <span className="dot dot-err" aria-hidden="true" />
            Cannot reach ui-backend: {status.message}
          </p>
        )}
      </section>

      <section className="panel">
        <h2>Milestone</h2>
        <p className="muted">
          M0 — delivery skeleton. The relationship graph, step timeline and
          constraint inspector arrive in M7.
        </p>
      </section>
    </main>
  );
}
