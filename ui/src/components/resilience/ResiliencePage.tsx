/**
 * Resilience — what this cluster does not survive losing a node to.
 *
 * The endpoint has been served since the CLI shipped and the UI never called
 * it, so the question an SRE asks most often was answerable only from a
 * terminal. It is the same analysis the planner runs, read in the opposite
 * mood: the zero-headroom PDB that blocks a drain is the same PDB a dying node
 * violates, and the pod with no controller that eviction would delete
 * permanently is deleted just as permanently by hardware.
 *
 * Explanations are the analyzer's own text, rendered verbatim. The CLI already
 * prints them unparaphrased for exactly this reason — one constraint described
 * two ways leaves an operator with no way to tell which to believe — and a
 * third surface is a third chance to get that wrong.
 *
 * Deliberately no severity ranking. The payload carries none, and inventing
 * one here would mean this screen disagreeing with Recommendations about how
 * bad something is. Findings are grouped by kind and counted; which kind
 * matters most is the operator's judgement and depends on their cluster.
 */

import { useEffect, useMemo, useState } from "react";
import { Resilience, ResilienceFinding, api } from "../../api";

export default function ResiliencePage() {
  const [data, setData] = useState<Resilience | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let cancelled = false;
    const load = () =>
      api
        .resilience()
        .then((d) => {
          if (!cancelled) {
            setData(d);
            setFailed(false);
          }
        })
        .catch(() => !cancelled && setFailed(true));
    load();
    // The analysis is computed fresh per request rather than read from the
    // stored plan, so this is a real question each time and not worth asking
    // faster than the planner changes its mind.
    const t = setInterval(load, 60_000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);

  const groups = useMemo(() => byKind(data?.findings ?? []), [data]);

  if (failed) {
    return (
      <Frame>
        <p className="resilience-empty">Could not read the audit just now. It will retry on its own.</p>
      </Frame>
    );
  }

  if (!data) {
    return (
      <Frame>
        <p className="resilience-empty">Reading the cluster…</p>
      </Frame>
    );
  }

  const total = data.findings?.length ?? 0;

  if (total === 0) {
    return (
      <Frame>
        <p className="resilience-clear">
          <strong>Every pod here can survive losing its node.</strong> Nothing is held by a
          zero-headroom budget, nothing depends on a node that cannot be replaced, and nothing
          would be deleted rather than rescheduled.
        </p>
        <p className="resilience-empty">
          This is the answer you want, and it is worth re-reading after a deploy — it describes
          the cluster as it is now, not as it was configured.
        </p>
      </Frame>
    );
  }

  return (
    <Frame>
      <p className="resilience-lede">
        <strong>
          {total} {total === 1 ? "pod is" : "pods are"} at risk if a node goes away.
        </strong>{" "}
        Each of these is also a reason a drain would be refused — the same constraint, met from
        the other side.
      </p>

      {groups.map(([kind, findings]) => (
        <section className="resilience-group" key={kind}>
          <h3 className="resilience-kind">
            {spaced(kind)}
            <span className="resilience-count mono">{findings.length}</span>
          </h3>
          <ul className="resilience-list">
            {findings.map((f, i) => (
              <li className="resilience-row" key={`${f.pod}-${f.kind}-${i}`}>
                <div className="resilience-subject">
                  <span className="resilience-pod mono">{f.pod}</span>
                  {f.node && <span className="resilience-node mono">on {f.node}</span>}
                </div>
                {/* The analyzer's wording, not ours. */}
                <p className="resilience-why">{f.explanation}</p>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </Frame>
  );
}

function Frame({ children }: { children: React.ReactNode }) {
  return (
    <div className="resilience">
      <header className="resilience-head">
        <div className="eyebrow mono">Resilience</div>
        <h2 className="resilience-headline">What a lost node would cost</h2>
        <p className="resilience-sub">
          Consolidation asks whether a node can be emptied on purpose. This asks what happens
          when one empties itself.
        </p>
      </header>
      {children}
    </div>
  );
}

/**
 * The analyzer's kinds are CamelCase identifiers. Uppercasing them whole, as
 * an eyebrow style does, turned PDBZeroHeadroom into PDBZEROHEADROOM — a word
 * nobody can read at a glance. Splitting on the case boundaries keeps the
 * analyzer's own vocabulary while letting it be read: acronym runs stay
 * together, so PDBZeroHeadroom becomes "PDB Zero Headroom".
 */
function spaced(kind: string): string {
  return kind
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .replace(/([A-Z]+)([A-Z][a-z])/g, "$1 $2");
}

/** Grouped by the analyzer's own constraint kind, largest group first. */
function byKind(findings: ResilienceFinding[]): Array<[string, ResilienceFinding[]]> {
  const m = new Map<string, ResilienceFinding[]>();
  for (const f of findings) {
    const k = f.kind || "Other";
    const list = m.get(k);
    if (list) list.push(f);
    else m.set(k, [f]);
  }
  return [...m.entries()].sort((a, b) => b[1].length - a[1].length || a[0].localeCompare(b[0]));
}
