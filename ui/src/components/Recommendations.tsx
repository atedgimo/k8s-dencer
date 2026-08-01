import { useEffect, useState } from "react";
import { api, Recommendation } from "../api";

/**
 * The fix list: what is missing — the PDB nobody wrote, the second replica,
 * the requests — each with its why and, where the fix is YAML, the YAML on a
 * copy button.
 *
 * Two homes, one component. As the Advice surface it is a page: always open,
 * roomy. As a sidebar guest it collapses to a count, because the plan is
 * what the sidebar is for. Severity is impact-on-consolidation, carried by
 * words and weight — never the rating glyphs or their colours.
 */

interface Props {
  layout?: "page" | "sidebar";
}

export default function Recommendations({ layout = "sidebar" }: Props) {
  const [recs, setRecs] = useState<Recommendation[] | null>(null);
  const [open, setOpen] = useState(layout === "page");
  const [copied, setCopied] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    const load = () => {
      api
        .recommendations()
        .then((d) => {
          if (!cancelled) setRecs(d.recommendations);
        })
        .catch(() => {
          // Advice is supplementary; a failure must never blank its host.
        });
    };
    load();
    const t = setInterval(load, 60_000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);

  if (layout === "page") {
    return (
      <div className="advice-page">
        <div className="history-head">
          <span className="eyebrow">what is missing, with fixes</span>
        </div>
        {recs == null && <p className="history-empty">Loading…</p>}
        {recs && recs.length === 0 && (
          <p className="history-empty">
            Nothing to recommend. Every workload has requests, replicas and a governed disruption
            budget.
          </p>
        )}
        {recs && recs.length > 0 && <RecList recs={recs} copied={copied} setCopied={setCopied} />}
      </div>
    );
  }

  if (!recs || recs.length === 0) return null;
  const high = recs.filter((r) => r.severity === "high").length;

  return (
    <section className="recs">
      <button
        type="button"
        className="recs-toggle"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <span className="eyebrow">recommendations</span>
        <span className="recs-count num">
          {recs.length}
          {high > 0 && <span className="recs-high"> · {high} high</span>}
        </span>
        <span className="recs-chevron" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
      </button>
      {open && <RecList recs={recs} copied={copied} setCopied={setCopied} />}
    </section>
  );
}

function RecList({
  recs,
  copied,
  setCopied,
}: {
  recs: Recommendation[];
  copied: string | null;
  setCopied: (k: string | null) => void;
}) {
  return (
    <ul className="recs-list">
      {recs.map((r) => (
        <li key={r.kind + r.workload} className="recs-item">
          <div className="recs-item-head">
            <span className={"recs-sev recs-sev-" + r.severity}>{r.severity}</span>
            <span className="recs-workload mono">{r.workload}</span>
          </div>
          <p className="recs-why">{r.why}</p>
          {r.fix && (
            <button
              type="button"
              className="recs-copy"
              onClick={() => {
                void navigator.clipboard.writeText(r.fix as string);
                setCopied(r.kind + r.workload);
                setTimeout(() => setCopied(null), 1500);
              }}
            >
              {copied === r.kind + r.workload ? "Copied" : "Copy fix YAML"}
            </button>
          )}
        </li>
      ))}
    </ul>
  );
}
