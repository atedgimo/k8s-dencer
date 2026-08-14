/**
 * Rightsizing — requests against what is actually used.
 *
 * The endpoint has existed since the CLI shipped and the UI never called it,
 * which put the most actionable number in the product behind a terminal. On a
 * real GKE cluster it read: requested 3.0 cores, observed 0.2. Consolidation
 * moves pods between nodes; over-requesting by that margin is *why* the nodes
 * were too full to consolidate. This is the room, not the furniture.
 *
 * Every row is a measurement or it is absent. With no usage source configured
 * the screen says so and stops, because "0 used" and "not measured" are
 * different claims and only one of them is ever true here.
 */

import { useEffect, useRef, useState } from "react";
import { Recommendation, Rightsizing, api, formatBytes, formatCPU } from "../../api";

/** Below this the gap is noise, not a finding worth a row's attention. */
const WORTH_SHOWING_MILLI = 50;

/** formatCPU drops the unit above a core, so "cores" is only right above it. */
const cores = (milli: number) => (milli >= 1000 ? `${formatCPU(milli)} cores` : formatCPU(milli));

interface Props {
  /** Workload to scroll to and mark, when arriving from a recommendation. */
  focus?: string | null;
  /** Open findings, so a measured workload can say one is waiting for it. */
  recs?: Recommendation[] | null;
}

export default function RightsizingPage({ focus, recs }: Props = {}) {
  const [data, setData] = useState<Rightsizing | null>(null);
  const [failed, setFailed] = useState(false);
  const focusRow = useRef<HTMLTableRowElement>(null);

  // Arriving from a recommendation should land on the row that was asked
  // about, not at the top of a table the operator then has to search.
  useEffect(() => {
    focusRow.current?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [focus, data]);

  useEffect(() => {
    let cancelled = false;
    const load = () =>
      api
        .rightsizing()
        .then((d) => !cancelled && setData(d))
        .catch(() => !cancelled && setFailed(true));
    load();
    const t = setInterval(load, 60_000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);

  if (failed) {
    return (
      <div className="rightsizing">
        <Head />
        <p className="rightsizing-empty">
          Could not read usage just now. It will retry on its own.
        </p>
      </div>
    );
  }

  if (!data) {
    return (
      <div className="rightsizing">
        <Head />
        <p className="rightsizing-empty">Reading usage…</p>
      </div>
    );
  }

  if (!data.available) {
    return (
      <div className="rightsizing">
        <Head />
        <p className="rightsizing-empty">
          {data.reason ||
            "No measured usage. Enable it with planner.usageSource=metrics-server, at install time."}{" "}
          Until then this screen would be guessing, and a guess about capacity is worth less
          than nothing.
        </p>
      </div>
    );
  }

  // Workloads with an open finding on the other screen. These two screens are
  // deliberately not merged — a recommendation carries a paste-ready fix and
  // this one carries none, because a single usage sample is not a number to
  // set requests from — but reading either without knowing the other exists
  // is how an operator ends up doing half the job.
  const advised = new Set((recs ?? []).map((r) => r.workload));

  const rows = [...(data.workloads ?? [])].sort(
    (a, b) => b.requestedMilli - b.usedMilli - (a.requestedMilli - a.usedMilli),
  );
  const req = data.totalRequestedMilli ?? 0;
  const used = data.totalUsedMilli ?? 0;
  const slack = Math.max(req - used, 0);
  const ratio = used > 0 ? req / used : 0;

  return (
    <div className="rightsizing">
      <Head takenAt={data.takenAt} />

      <div className="rightsizing-hero">
        <span className="eyebrow mono">What the cluster asked for, against what it used</span>
        <h2 className="rightsizing-headline">
          {cores(slack)} requested and not used
        </h2>
        <div className="rightsizing-stats">
          <Stat label="requested" value={cores(req)} />
          <Stat label="observed" value={cores(used)} />
          {ratio >= 1.5 && (
            <Stat label="over-requested by" value={`${ratio.toFixed(1)}×`} accent />
          )}
        </div>
        <p className="rightsizing-note">
          Requests are what the scheduler packs against, so unused requests hold nodes open
          whether or not anything runs on them. Nothing here drains anything — these are
          numbers to take to the workload's owner.
        </p>
      </div>

      <div className="rightsizing-tablewrap">
        <table className="rightsizing-table">
          <thead>
            <tr>
              <th scope="col">Workload</th>
              <th scope="col" className="num">Pods</th>
              <th scope="col" className="num">CPU req</th>
              <th scope="col" className="num">CPU used</th>
              <th scope="col" className="rightsizing-barcol">Gap</th>
              <th scope="col" className="num">Mem req</th>
              <th scope="col" className="num">Mem used</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && (
              <tr>
                <td colSpan={7} className="rightsizing-empty">
                  Nothing measured yet.
                </td>
              </tr>
            )}
            {rows.map((r) => {
              const gap = Math.max(r.requestedMilli - r.usedMilli, 0);
              const usedPct = r.requestedMilli > 0 ? (r.usedMilli / r.requestedMilli) * 100 : 0;
              return (
                <tr
                  key={r.workload}
                  ref={r.workload === focus ? focusRow : undefined}
                  className={r.workload === focus ? "is-focused" : undefined}
                >
                  <td className="mono rightsizing-name">
                    {r.workload}
                    {advised.has(r.workload) && (
                      <span className="rightsizing-advised" title="This workload also has an open recommendation">
                        finding open
                      </span>
                    )}
                  </td>
                  <td className="num mono">{r.pods}</td>
                  <td className="num mono">{formatCPU(r.requestedMilli)}</td>
                  <td className="num mono">{formatCPU(r.usedMilli)}</td>
                  <td className="rightsizing-barcol">
                    <div
                      className="rightsizing-bar"
                      role="img"
                      aria-label={`${Math.round(usedPct)} percent of requested CPU in use`}
                    >
                      <div
                        className="rightsizing-bar-used"
                        style={{ width: `${Math.min(usedPct, 100)}%` }}
                      />
                    </div>
                    <span className="rightsizing-gap mono">
                      {gap >= WORTH_SHOWING_MILLI ? `${formatCPU(gap)} spare` : "—"}
                    </span>
                  </td>
                  <td className="num mono">{formatBytes(r.requestedBytes)}</td>
                  <td className="num mono">{formatBytes(r.usedBytes)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function Head({ takenAt }: { takenAt?: string }) {
  return (
    <div className="rightsizing-head">
      <span className="rightsizing-title">Rightsizing</span>
      {takenAt && (
        <span className="rightsizing-taken mono">
          measured {new Date(takenAt).toLocaleTimeString()}
        </span>
      )}
    </div>
  );
}

function Stat({ label, value, accent }: { label: string; value: string; accent?: boolean }) {
  return (
    <div className={"rightsizing-stat" + (accent ? " is-accent" : "")}>
      <span className="rightsizing-stat-value mono">{value}</span>
      <span className="rightsizing-stat-label">{label}</span>
    </div>
  );
}
