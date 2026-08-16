/**
 * What the stored samples say about the estate over time.
 *
 * History already draws needed-versus-spare bars and never says what they
 * mean. "Your fleet has been 89% reserved and 6% used for the past six days"
 * is the sentence that gets a budget conversation started, and every number
 * in it is already in the store.
 *
 * Two claims, deliberately kept apart, because the GKE run showed how easily
 * they are confused: on `e2-medium` nodes the fleet was ~890m requested and
 * ~50m used of 940m allocatable — **full by reservation and idle by usage**.
 * Consolidation only ever addresses the first. Collapsing both into one
 * "over-provisioned" percentage would point an operator at the wrong lever,
 * which is exactly the confusion Rightsizing exists to clear up.
 *
 * And it declines to speak when it cannot. A trend drawn over four hours of
 * samples is not a trend; a window where measurement started halfway through
 * is two different claims averaged together.
 */
import type { HistorySample } from "./api";

export interface EstateTrend {
  /** Hours actually covered by the samples, not the range that was asked for. */
  hours: number;
  samples: number;
  /** Requests as a percentage of allocatable, meaned over the window. */
  reservedPct: number;
  /**
   * Measured usage as a percentage of allocatable. Absent unless every
   * sample in the window carried a measurement — a window that begins
   * unmeasured and ends measured would otherwise report the average of a
   * real number and a zero that only meant "nobody was looking".
   */
  usedPct?: number;
}

/**
 * A trend needs a window long enough to be one.
 *
 * Half a day, and enough points to have watched it rather than glanced twice.
 * Both are floors rather than targets: the sentence names the window it
 * actually had, so a reader weighs a claim made over half a day differently
 * from one made over a month.
 *
 * Twelve rather than twenty-four because the shortest range the History
 * screen offers IS twenty-four hours, and the samples inside a window always
 * span slightly less than the window itself — a 7-day range measures 167.99
 * hours, a 24-hour one just under 24. A floor equal to the smallest
 * selectable range would refuse on that range every time, by a rounding
 * error, which is the least defensible reason to say nothing.
 */
export const MIN_HOURS = 12;
export const MIN_SAMPLES = 12;

export function estateTrend(samples: HistorySample[]): EstateTrend | null {
  // A sample from a moment when the fleet reported no allocatable CPU says
  // nothing about reservation, and dividing by it says something false.
  const usable = samples.filter((s) => s.cpuAllocMilli > 0);
  if (usable.length < MIN_SAMPLES) return null;

  const t0 = Date.parse(usable[0].takenAt);
  const t1 = Date.parse(usable[usable.length - 1].takenAt);
  const hours = (t1 - t0) / 36e5;
  if (!Number.isFinite(hours) || hours < MIN_HOURS) return null;

  const mean = (f: (s: HistorySample) => number) =>
    usable.reduce((acc, s) => acc + f(s), 0) / usable.length;

  const reservedPct = mean((s) => (s.cpuReqMilli / s.cpuAllocMilli) * 100);

  const measuredThroughout = usable.every((s) => s.hasUsage);
  const usedPct = measuredThroughout
    ? mean((s) => (s.cpuUsedMilli / s.cpuAllocMilli) * 100)
    : undefined;

  return {
    hours,
    samples: usable.length,
    reservedPct,
    usedPct,
  };
}

/** "6 days" / "31 hours" — the window, in the unit a person would say it in. */
export function windowLabel(hours: number): string {
  if (hours >= 48) {
    const d = Math.round(hours / 24);
    return `${d} day${d === 1 ? "" : "s"}`;
  }
  const h = Math.round(hours);
  return `${h} hour${h === 1 ? "" : "s"}`;
}
