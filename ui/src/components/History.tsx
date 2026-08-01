import { useEffect, useMemo, useState } from "react";
import { api, formatBytes, formatCPU, HistoryResponse } from "../api";

/**
 * The cluster's timeline — the first surface that reads what the product has
 * always recorded as a line instead of a moment.
 *
 * Three bands, top to bottom in the order of the product's own argument:
 * what the estate is (nodes, and how many the plan would free), what was
 * actually returned (the ledger, cumulative and measured), and what the
 * machines really do versus what they ask for (requests vs usage, only when
 * measured). Run markers sit on the ledger band because runs are what turn
 * the first band's potential into the second band's fact.
 *
 * Hand-drawn SVG, no chart library: the bundle stays honest and the charts
 * obey the house rules exactly — ink is achromatic, the only colour on this
 * page is a run marker's rating, which already means risk everywhere else.
 */

const RANGES = [
  { label: "24h", hours: 24 },
  { label: "7d", hours: 168 },
  { label: "30d", hours: 720 },
] as const;

export default function History() {
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
    <div className="history">
      <div className="history-head">
        <span className="eyebrow">the cluster, over time</span>
        <div className="history-ranges" role="group" aria-label="Time range">
          {RANGES.map((r) => (
            <button
              key={r.hours}
              type="button"
              className={"history-range" + (r.hours === hours ? " is-on" : "")}
              onClick={() => setHours(r.hours)}
            >
              {r.label}
            </button>
          ))}
        </div>
      </div>

      {error && <p className="history-empty">Could not load the timeline: {error}</p>}
      {data && data.samples.length < 2 && !error && (
        <p className="history-empty">
          The timeline needs a few minutes of samples before there is a line to draw. The planner
          writes one point every resync; leave this open.
        </p>
      )}
      {data && data.samples.length >= 2 && (
        <>
          <EstateBand data={data} />
          <LedgerBand data={data} />
          <UsageBand data={data} />
        </>
      )}
    </div>
  );
}

/* ------------------------------------------------------------- shared svg */

const W = 960;
const H = 180;
const PAD = { l: 44, r: 12, t: 14, b: 22 };

interface Scale {
  x: (t: number) => number;
  y: (v: number) => number;
  t0: number;
  t1: number;
  vMax: number;
}

function makeScale(times: number[], maxValue: number): Scale {
  const t0 = Math.min(...times);
  const t1 = Math.max(...times);
  const span = Math.max(1, t1 - t0);
  const vMax = Math.max(1, maxValue);
  return {
    x: (t) => PAD.l + ((t - t0) / span) * (W - PAD.l - PAD.r),
    y: (v) => H - PAD.b - (v / vMax) * (H - PAD.t - PAD.b),
    t0,
    t1,
    vMax,
  };
}

function linePath(pts: Array<[number, number]>): string {
  return pts.map(([x, y], i) => `${i === 0 ? "M" : "L"}${x.toFixed(1)},${y.toFixed(1)}`).join(" ");
}

function timeTicks(s: Scale): Array<{ x: number; label: string }> {
  const out: Array<{ x: number; label: string }> = [];
  const span = s.t1 - s.t0;
  for (let i = 0; i <= 4; i++) {
    const t = s.t0 + (span * i) / 4;
    const d = new Date(t);
    const label =
      span > 36e5 * 48
        ? `${d.getMonth() + 1}/${d.getDate()}`
        : `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
    out.push({ x: s.x(t), label });
  }
  return out;
}

function Grid({ s, yLabel }: { s: Scale; yLabel: (v: number) => string }) {
  return (
    <g className="chart-grid" aria-hidden="true">
      {[0.25, 0.5, 0.75, 1].map((f) => (
        <g key={f}>
          <line x1={PAD.l} x2={W - PAD.r} y1={s.y(s.vMax * f)} y2={s.y(s.vMax * f)} />
          <text x={PAD.l - 6} y={s.y(s.vMax * f) + 3} textAnchor="end" className="chart-ylabel num">
            {yLabel(s.vMax * f)}
          </text>
        </g>
      ))}
      {timeTicks(s).map((t) => (
        <text key={t.x} x={t.x} y={H - 6} textAnchor="middle" className="chart-xlabel num">
          {t.label}
        </text>
      ))}
    </g>
  );
}

/* ---------------------------------------------------------------- estate */

function EstateBand({ data }: { data: HistoryResponse }) {
  const { s, nodesPath, reclaimArea, latest } = useMemo(() => {
    const pts = data.samples.map((p) => ({ t: Date.parse(p.takenAt), ...p }));
    const s = makeScale(
      pts.map((p) => p.t),
      Math.max(...pts.map((p) => p.nodes)),
    );
    const nodesPath = linePath(pts.map((p) => [s.x(p.t), s.y(p.nodes)]));
    // Reclaimable drawn as the shaded top slice of the estate: the part of
    // the fleet the plan says is not needed.
    const upper = pts.map((p) => [s.x(p.t), s.y(p.nodes)] as [number, number]);
    const lower = pts
      .map((p) => [s.x(p.t), s.y(Math.max(0, p.nodes - p.reclaimable))] as [number, number])
      .reverse();
    const reclaimArea = linePath(upper) + " " + linePath(lower).replace(/^M/, "L") + " Z";
    return { s, nodesPath, reclaimArea, latest: pts[pts.length - 1] };
  }, [data]);

  return (
    <section className="history-band">
      <header className="history-band-head">
        <h2 className="history-band-title">Estate</h2>
        <span className="history-band-now num">
          {latest.nodes} nodes · {latest.reclaimable} reclaimable now
        </span>
      </header>
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Nodes and reclaimable over time">
        <Grid s={s} yLabel={(v) => `${Math.round(v)}`} />
        <path className="chart-area" d={reclaimArea} />
        <path className="chart-line" d={nodesPath} />
      </svg>
      <p className="history-band-note">
        The shaded slice is what the plan would free — the gap between the fleet you run and the
        fleet you need.
      </p>
    </section>
  );
}

/* ---------------------------------------------------------------- ledger */

function LedgerBand({ data }: { data: HistoryResponse }) {
  const model = useMemo(() => {
    const events = data.reclamations
      .filter((r) => r.outcome === "reclaimed" && r.resolvedAt)
      .map((r) => ({ t: Date.parse(r.resolvedAt as string), cpu: r.cpuMilli ?? 0 }))
      .sort((a, b) => a.t - b.t);
    const t0 = data.samples.length ? Date.parse(data.samples[0].takenAt) : Date.now();
    const t1 = data.samples.length
      ? Date.parse(data.samples[data.samples.length - 1].takenAt)
      : Date.now();
    let cum = 0;
    const steps: Array<{ t: number; v: number }> = [{ t: t0, v: 0 }];
    for (const e of events) {
      if (e.t < t0) {
        cum += e.cpu;
        steps[0] = { t: t0, v: cum };
        continue;
      }
      steps.push({ t: e.t, v: cum });
      cum += e.cpu;
      steps.push({ t: e.t, v: cum });
    }
    steps.push({ t: t1, v: cum });
    const s = makeScale(
      [t0, t1],
      Math.max(1000, ...steps.map((p) => p.v)),
    );
    const path = linePath(steps.map((p) => [s.x(p.t), s.y(p.v)]));
    const runs = data.runs
      .filter((r) => r.finishedAt)
      .map((r) => ({ ...r, t: Date.parse(r.finishedAt as string) }))
      .filter((r) => r.t >= t0 && r.t <= t1);
    return { s, path, cum, runs, count: events.length };
  }, [data]);

  return (
    <section className="history-band">
      <header className="history-band-head">
        <h2 className="history-band-title">Ledger</h2>
        <span className="history-band-now num">
          {formatCPU(model.cum)} cores measured as returned
          {model.count > 0 ? ` · ${model.count} node${model.count === 1 ? "" : "s"}` : ""}
        </span>
      </header>
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Cumulative capacity returned">
        <Grid s={model.s} yLabel={(v) => formatCPU(v)} />
        <path className="chart-line chart-line-step" d={model.path} />
        {/* Run markers: the moments potential became fact (or was refused).
            The one place colour appears, because a rating already means risk. */}
        {model.runs.map((r) => (
          <g key={r.id} transform={`translate(${model.s.x(r.t).toFixed(1)}, ${H - PAD.b})`}>
            <line className="chart-run-tick" y2={-(H - PAD.t - PAD.b)} />
            <title>
              {(r.mode || "steps") + (r.dryRun ? " (dry run)" : "") + " — " + r.status}
            </title>
            <text y={-4} textAnchor="middle" className={"chart-run-mark chart-run-" + r.status.toLowerCase()}>
              {r.dryRun ? "◌" : r.status === "Succeeded" ? "●" : r.status === "Blocked" ? "■" : "✕"}
            </text>
          </g>
        ))}
      </svg>
      <p className="history-band-note">
        Measured, not estimated: capacity captured at drain time, counted only when the node
        actually disappeared. Markers are runs — hollow ones were rehearsals.
      </p>
    </section>
  );
}

/* ----------------------------------------------------------------- usage */

function UsageBand({ data }: { data: HistoryResponse }) {
  const measured = data.samples.filter((p) => p.hasUsage);
  const model = useMemo(() => {
    if (measured.length < 2) return null;
    const pts = measured.map((p) => ({ t: Date.parse(p.takenAt), ...p }));
    const s = makeScale(
      pts.map((p) => p.t),
      Math.max(...pts.map((p) => p.cpuReqMilli)),
    );
    return {
      s,
      req: linePath(pts.map((p) => [s.x(p.t), s.y(p.cpuReqMilli)])),
      used: linePath(pts.map((p) => [s.x(p.t), s.y(p.cpuUsedMilli)])),
      latest: pts[pts.length - 1],
    };
  }, [measured]);

  if (!model) {
    return (
      <section className="history-band">
        <header className="history-band-head">
          <h2 className="history-band-title">Requests vs usage</h2>
        </header>
        <p className="history-empty">
          No measured usage in this window. Enable it with planner.usageSource=metrics-server;
          without measurements this band refuses to draw.
        </p>
      </section>
    );
  }

  return (
    <section className="history-band">
      <header className="history-band-head">
        <h2 className="history-band-title">Requests vs usage</h2>
        <span className="history-band-now num">
          {formatCPU(model.latest.cpuReqMilli)} requested · {formatCPU(model.latest.cpuUsedMilli)}{" "}
          used · {formatBytes(model.latest.memReqBytes)} / {formatBytes(model.latest.memUsedBytes)}
        </span>
      </header>
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Requested versus used CPU over time">
        <Grid s={model.s} yLabel={(v) => formatCPU(v)} />
        <path className="chart-line" d={model.req} />
        <path className="chart-line chart-line-dim" d={model.used} />
      </svg>
      <p className="history-band-note">
        The bright line is what workloads ask for — the scheduler's reality. The dim line is what
        they measurably use. The space between them is what right-sizing recovers.
      </p>
    </section>
  );
}
