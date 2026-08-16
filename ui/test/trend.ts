/**
 * Estate-trend arithmetic, and the cases where it must refuse to answer.
 *
 * Same shape as density.ts: no test runner in this project's UI, esbuild is
 * already here for the build, so this bundles and runs under node.
 *
 * The refusals matter more than the arithmetic. A trend sentence is the one
 * thing on the History screen a person would quote in a budget conversation,
 * so a wrong one travels further than any other bug in this product — and
 * every wrong version of it is a plausible-looking percentage rather than a
 * blank space.
 *
 *   npm run test:trend
 */
import { estateTrend, windowLabel, MIN_HOURS, MIN_SAMPLES } from "../src/trend";
import type { HistorySample } from "../src/api";

let failures = 0;
const check = (label: string, got: unknown, want: unknown) => {
  const ok = JSON.stringify(got) === JSON.stringify(want);
  if (!ok) {
    failures++;
    console.log(`  FAIL ${label}: got ${JSON.stringify(got)} want ${JSON.stringify(want)}`);
  } else console.log(`  ok   ${label} = ${JSON.stringify(got)}`);
};

const HOUR = 36e5;

/** n samples, one per hour, with the given reservation and usage fractions. */
const series = (
  n: number,
  opts: { reqFrac: number; usedFrac?: number; alloc?: number } = { reqFrac: 0.9 },
): HistorySample[] => {
  const alloc = opts.alloc ?? 940;
  const start = Date.parse("2026-08-01T00:00:00Z");
  return Array.from({ length: n }, (_, i) => ({
    takenAt: new Date(start + i * HOUR).toISOString(),
    nodes: 6,
    pods: 40,
    cpuReqMilli: Math.round(alloc * opts.reqFrac),
    cpuAllocMilli: alloc,
    memReqBytes: 0,
    memAllocBytes: 0,
    cpuUsedMilli: opts.usedFrac === undefined ? 0 : Math.round(alloc * opts.usedFrac),
    memUsedBytes: 0,
    hasUsage: opts.usedFrac !== undefined,
    reclaimable: 1,
  }));
};

// ---------------------------------------------------------------- refusals

// Four hours of samples is not six weeks of evidence, and the difference is
// the entire worth of the sentence.
check("a short window says nothing", estateTrend(series(5, { reqFrac: 0.9 })), null);

// Two readings a fortnight apart span a long time and observed almost none of
// it. The span test alone would have let this through.
const sparse = [series(1)[0], { ...series(1)[0], takenAt: "2026-08-15T00:00:00Z" }];
check("a long span with too few points says nothing", estateTrend(sparse), null);

check("exactly at the floor is not enough on its own", estateTrend(series(MIN_SAMPLES - 1)), null);

// --------------------------------------------------------------- the claim

const measured = estateTrend(series(30, { reqFrac: 0.9, usedFrac: 0.05 }))!;
check("the window is the one actually observed, in hours", Math.round(measured.hours), 29);
check("samples are counted", measured.samples, 30);
check("reserved is requests over allocatable", Math.round(measured.reservedPct), 90);
check("used is measured usage over allocatable", Math.round(measured.usedPct!), 5);

// The GKE shape: full by reservation, idle by usage. The two numbers must
// stay apart -- collapsing them into one "over-provisioned" percentage points
// an operator at consolidation when the lever is right-sizing.
check(
  "reservation and usage are not the same number",
  measured.reservedPct > 80 && measured.usedPct! < 10,
  true,
);

// ------------------------------------------------------- unmeasured usage

const unmeasured = estateTrend(series(30, { reqFrac: 0.9 }))!;
check("reservation still reported without a usage source", Math.round(unmeasured.reservedPct), 90);
check("usage is absent, not zero", unmeasured.usedPct, undefined);

// A window that begins unmeasured and ends measured would otherwise average a
// real number against a zero that only ever meant "nobody was looking" --
// reporting, say, 2.5% usage for a cluster that was at 5% the whole time it
// was watched.
const halfMeasured = [
  ...series(15, { reqFrac: 0.9 }),
  ...series(15, { reqFrac: 0.9, usedFrac: 0.05 }),
].map((s, i) => ({ ...s, takenAt: new Date(Date.parse("2026-08-01T00:00:00Z") + i * HOUR).toISOString() }));
check(
  "a window only half measured reports no usage rather than half of it",
  estateTrend(halfMeasured)!.usedPct,
  undefined,
);

// --------------------------------------------------------- divide-by-zero

// A moment when the fleet reported no allocatable CPU says nothing about
// reservation, and dividing by it says something false (Infinity, which
// renders as a very confident percentage).
const withZeroes = [
  ...series(30, { reqFrac: 0.9, usedFrac: 0.05 }),
  { ...series(1)[0], cpuAllocMilli: 0, takenAt: "2026-08-03T00:00:00Z" },
];
const survived = estateTrend(withZeroes)!;
check("a zero-allocatable sample is skipped, not divided by", Number.isFinite(survived.reservedPct), true);
check("and it does not distort the mean", Math.round(survived.reservedPct), 90);

// ------------------------------------------------------------ the wording

check("two days reads as days", windowLabel(48), "2 days");
check("six days reads as days", windowLabel(24 * 6), "6 days");
check("one day still reads in hours, where it is honest about being short", windowLabel(30), "30 hours");
check("singular hour", windowLabel(1), "1 hour");
check("the floor is half a day", MIN_HOURS, 12);

// The boundary that prompted the floor to move. Samples inside a window span
// slightly less than the window: measured against the live API, a 7-day range
// covers 167.99 hours and a 24-hour range just under 24. A floor equal to the
// smallest selectable range would therefore refuse on that range every time,
// by a rounding error.
const aDay = series(24, { reqFrac: 0.9 }).slice(0, 24);
check(
  "a 24h range, which spans just under 24h, still gets a sentence",
  estateTrend(aDay) !== null,
  true,
);

console.log(failures === 0 ? "\nALL TREND CHECKS PASSED" : `\n${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);
