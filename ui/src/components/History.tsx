import { useEffect, useMemo, useState } from "react";
import { HistoryResponse, api, formatCPU } from "../api";
import { estateTrend, windowLabel } from "../trend";

/**
 * History (assets/design/README.md, 2e): the estate over time as needed vs
 * spare stacked bars — legible, unlike the near-black line chart it
 * replaces — over an audit ledger of every run and who authorised it.
 *
 * "Spare" is the planner's own testimony: the reclaimable count it published
 * at each sample. The ledger is the product's memory of consent — when, what
 * plan, how much, outcome, and the actor the API server verified.
 */

const RANGES = [
  { label: "24h", hours: 24 },
  { label: "7d", hours: 168 },
  { label: "30d", hours: 720 },
] as const;

export default function History({ pricing }: { pricing?: Pricing }) {
  const [hours, setHours] = useState<number>(24);
  const [data, setData] = useState<HistoryResponse | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    const load = () => {
      api
        .history(hours)
        .then((d) => {
          if (!cancelled) {
            setData(d);
            setError(null);
          }
        })
        .catch((e) => {
          if (!cancelled) setError(e instanceof Error ? e.message : String(e));
        });
    };
    load();
    const t = setInterval(load, 30_000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, [hours]);

  return (
    <div className="historypage">
      <div className="historypage-head">
        <span className="historypage-title">History</span>
        <span className="historypage-counts mono">
          every plan, run and rehearsal on this cluster
        </span>
        <div className="historypage-ranges viewswitch" role="group" aria-label="Time range">
          {RANGES.map((r) => (
            <button
              key={r.hours}
              type="button"
              className={"viewswitch-btn" + (r.hours === hours ? " is-on" : "")}
              onClick={() => setHours(r.hours)}
            >
              {r.label}
            </button>
          ))}
        </div>
      </div>

      {error && <p className="historypage-empty">Could not load the timeline: {error}</p>}
      {data && data.samples.length < 2 && !error && (
        <p className="historypage-empty">
          The timeline needs a few minutes of samples before there is anything to draw. The
          planner writes one point every resync; leave this open.
        </p>
      )}
      {data && data.samples.length >= 2 && (
        <>
          <EstateBars data={data} />
          <Ledger data={data} pricing={pricing} />
        </>
      )}
    </div>
  );
}

/* ----------------------------------------------------------- estate trend */

/**
 * What the bars have been saying, in a sentence.
 *
 * The chart above shows the shape and never states it. This is the line
 * somebody quotes in a budget conversation, which is exactly why it declines
 * to speak on a window too short to support it.
 *
 * Reservation and usage stay separate on purpose. The GKE run measured ~890m
 * requested against ~50m used of 940m allocatable — full by reservation and
 * idle by usage. Consolidation only addresses the first; collapsing both into
 * one "over-provisioned" number would point an operator at the wrong lever.
 */
function EstateTrend({ data }: { data: HistoryResponse }) {
  const trend = useMemo(() => estateTrend(data.samples), [data]);
  if (!trend) return null;

  const period = windowLabel(trend.hours);
  return (
    <p className="estate-trend">
      Over the past <strong>{period}</strong> your workloads reserved{" "}
      <strong>{Math.round(trend.reservedPct)}%</strong> of the fleet
      {trend.usedPct === undefined ? (
        <>
          . Usage is unmeasured, so how much of that was needed is unknown —
          set <code>planner.usageSource</code> to find out.
        </>
      ) : (
        <>
          {" "}
          and used <strong>{Math.round(trend.usedPct)}%</strong> of it. Reserved
          capacity is held whether or not it is used, so the gap is a
          right-sizing question rather than a consolidation one.
        </>
      )}
    </p>
  );
}

/* ------------------------------------------------------------ estate bars */

const BUCKETS = 48;

function EstateBars({ data }: { data: HistoryResponse }) {
  const model = useMemo(() => {
    const pts = data.samples.map((p) => ({ t: Date.parse(p.takenAt), ...p }));
    // Downsample into fixed buckets: a bar per sample would be thousands of
    // slivers at 30 days, and the question is a shape, not a point read.
    const t0 = pts[0].t;
    const t1 = pts[pts.length - 1].t;
    const span = Math.max(1, t1 - t0);
    const buckets: Array<{ nodes: number; spare: number; n: number; t: number }> = [];
    for (const p of pts) {
      const i = Math.min(BUCKETS - 1, Math.floor(((p.t - t0) / span) * BUCKETS));
      buckets[i] = buckets[i] ?? { nodes: 0, spare: 0, n: 0, t: p.t };
      buckets[i].nodes += p.nodes;
      buckets[i].spare += p.reclaimable;
      buckets[i].n++;
    }
    const bars = buckets
      .filter(Boolean)
      .map((b) => ({ t: b.t, nodes: b.nodes / b.n, spare: b.spare / b.n }));
    const maxNodes = Math.max(...bars.map((b) => b.nodes), 1);
    const avgSpare = bars.reduce((n, b) => n + b.spare, 0) / bars.length;
    return { bars, maxNodes, avgSpare, t0, t1 };
  }, [data]);

  const ticks = [1, 0.75, 0.5, 0.25, 0].map((f) => Math.round(model.maxNodes * f));
  const label = (t: number) => {
    const d = new Date(t);
    return model.t1 - model.t0 > 36e5 * 48
      ? `${d.getMonth() + 1}/${d.getDate()}`
      : `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  };

  return (
    <section className="estate">
      <div className="estate-head">
        <div className="estate-lead">
          <span className="eyebrow mono">The fleet you ran vs the fleet you needed</span>
          <h2 className="estate-headline">
            You carried{" "}
            <span className="estate-spare-word">
              {Math.round(model.avgSpare)} spare node{Math.round(model.avgSpare) === 1 ? "" : "s"}
            </span>{" "}
            on average
          </h2>
        </div>
        <div className="wellslegend">
          <span className="wellslegend-item">
            <span className="estate-swatch-needed" aria-hidden="true" />
            needed
          </span>
          <span className="wellslegend-item">
            <span className="estate-swatch-spare" aria-hidden="true" />
            spare
          </span>
        </div>
      </div>

      <EstateTrend data={data} />

      <div className="estate-chart">
        <div className="estate-yaxis mono" aria-hidden="true">
          {ticks.map((v, i) => (
            <span key={i}>{v}</span>
          ))}
        </div>
        <div
          className="estate-bars"
          role="img"
          aria-label="Nodes run versus nodes needed over time"
        >
          {model.bars.map((b) => (
            <div key={b.t} className="estate-bar" title={`${Math.round(b.nodes)} nodes, ${Math.round(b.spare)} spare`}>
              <div
                className="estate-bar-spare"
                style={{ height: `${(b.spare / model.maxNodes) * 100}%` }}
              />
              <div
                className="estate-bar-needed"
                style={{ height: `${((b.nodes - b.spare) / model.maxNodes) * 100}%` }}
              />
            </div>
          ))}
        </div>
      </div>
      <div className="estate-xaxis mono" aria-hidden="true">
        {[0, 0.25, 0.5, 0.75, 1].map((f) => (
          <span key={f}>{label(model.t0 + (model.t1 - model.t0) * f)}</span>
        ))}
      </div>
    </section>
  );
}

/* ---------------------------------------------------------------- ledger */

interface Pricing {
  currency: string;
  perMonth: number;
  pricedNodes: number;
  unpricedNodes: number;
  externalPerMonth: number;
  externalPriced: number;
}

/** No symbol lookup: the operator said "USD", so echo it rather than guess a
 *  glyph. A wrong currency symbol on a cost figure is its own small lie. */
const money = (currency: string, v: number) =>
  `${v.toFixed(2)}${currency ? " " + currency : ""}`;

function Ledger({ data, pricing }: { data: HistoryResponse; pricing?: Pricing }) {
  const rows = useMemo(() => {
    const byRun = new Map<string, { nodes: number; cpu: number }>();
    for (const r of data.reclamations) {
      if (r.outcome !== "reclaimed" || !r.runId) continue;
      const cur = byRun.get(r.runId) ?? { nodes: 0, cpu: 0 };
      cur.nodes++;
      cur.cpu += r.cpuMilli ?? 0;
      byRun.set(r.runId, cur);
    }
    return data.runs
      .filter((r) => r.finishedAt)
      .sort((a, b) => Date.parse(b.finishedAt as string) - Date.parse(a.finishedAt as string))
      .map((r) => ({ ...r, reclaimed: byRun.get(r.id) }));
  }, [data]);

  // Capacity that left without us. The ledger below is per-run and these have
  // no run, so without this line a fleet can visibly halve while every row on
  // screen says the product did nothing — which is what happened on GKE.
  const elsewhere = useMemo(() => {
    let nodes = 0;
    let cpu = 0;
    for (const r of data.reclamations) {
      if (!r.external || r.outcome !== "reclaimed") continue;
      nodes++;
      cpu += r.cpuMilli ?? 0;
    }
    return { nodes, cpu };
  }, [data]);

  return (
    <section className="auditledger">
      <div className="auditledger-head">
        <span className="eyebrow mono">Ledger</span>
        <span className="auditledger-count">
          {rows.length} entr{rows.length === 1 ? "y" : "ies"}
        </span>
        {elsewhere.nodes > 0 && (
          <span className="auditledger-elsewhere">
            {elsewhere.nodes} node{elsewhere.nodes === 1 ? "" : "s"} reclaimed by something else
            {elsewhere.cpu > 0 ? ` (${formatCPU(elsewhere.cpu)} cores)` : ""}
            {pricing && pricing.externalPriced > 0
              ? `, worth ${money(pricing.currency, pricing.externalPerMonth)}/month`
              : ""}{" "}
            — not this tool's doing, and not counted as it
          </span>
        )}
      </div>
      {pricing && pricing.pricedNodes > 0 && (
        // The rate, not a running total: a reclaimed node saves money every
        // hour from now on, and a cumulative figure is small and unimpressive
        // on day one while the rate is the actual result.
        <p className="auditledger-worth">
          <strong>{money(pricing.currency, pricing.perMonth)}/month</strong> no longer spent,
          across {pricing.pricedNodes} reclaimed node
          {pricing.pricedNodes === 1 ? "" : "s"}
          {pricing.unpricedNodes > 0
            ? ` · ${pricing.unpricedNodes} unpriced — add their machine type to uiBackend.pricing`
            : ""}
        </p>
      )}
      <div className="auditrow auditrow-head">
        <span className="eyebrow mono">when</span>
        <span className="eyebrow mono">plan</span>
        <span className="eyebrow mono">mode</span>
        <span className="eyebrow mono">steps</span>
        <span className="eyebrow mono">reclaimed</span>
        <span className="eyebrow mono">outcome</span>
        <span className="eyebrow mono">authorised by</span>
      </div>
      {rows.length === 0 && (
        <p className="historypage-empty">No runs in this window. The ledger fills as they happen.</p>
      )}
      {rows.map((r) => (
        <div key={r.id} className="auditrow">
          <span className="mono auditrow-when">
            {new Date(r.finishedAt as string).toLocaleString([], {
              month: "short",
              day: "numeric",
              hour: "2-digit",
              minute: "2-digit",
              hour12: false,
            })}
          </span>
          <span className="mono auditrow-plan">{(r.planId ?? "").slice(0, 7) || "—"}</span>
          <span className="auditrow-mode">
            {r.dryRun ? "rehearsal" : r.mode || "steps"}
          </span>
          <span className="mono">{r.steps ?? "—"}</span>
          <span className="mono">
            {r.reclaimed
              ? `${r.reclaimed.nodes} node${r.reclaimed.nodes === 1 ? "" : "s"} · ${formatCPU(r.reclaimed.cpu)} cores`
              : r.dryRun
                ? "nothing, by design"
                : "—"}
          </span>
          <span
            className={
              "auditrow-outcome " +
              (r.status === "Succeeded"
                ? "is-safe"
                : r.status === "Blocked"
                  ? "is-held"
                  : r.status === "Failed"
                    ? "is-held"
                    : "")
            }
          >
            {r.status === "Blocked" ? "Halted by the guard" : r.status}
          </span>
          <span className="mono auditrow-actor" title={r.actor}>
            {shortActor(r.actor)}
          </span>
        </div>
      ))}
    </section>
  );
}

/** The recognisable tail of an RBAC principal. */
function shortActor(actor?: string): string {
  if (!actor) return "—";
  const parts = actor.split(":");
  return parts[parts.length - 1] || actor;
}
