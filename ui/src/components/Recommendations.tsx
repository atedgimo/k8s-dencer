import { useEffect, useState } from "react";
import { api, Recommendation } from "../api";

/**
 * The fix list, in the sidebar under the plan: what is missing — the PDB
 * nobody wrote, the second replica, the requests — each with its why and,
 * where the fix is YAML, the YAML on a copy button.
 *
 * Severity is impact-on-consolidation, not risk: words and weight carry it,
 * never the rating glyphs or their colours. Collapsed by default to a count,
 * because the plan is what the sidebar is for and advice is a guest here.
 */
export default function Recommendations() {
  const [recs, setRecs] = useState<Recommendation[] | null>(null);
  const [open, setOpen] = useState(false);
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
          // Advice is supplementary; a failure must never blank the sidebar.
        });
    };
    load();
    const t = setInterval(load, 60_000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);

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
      {open && (
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
      )}
    </section>
  );
}
