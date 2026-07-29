/**
 * Density-mode checks.
 *
 * There is no test runner in this project's UI, and adding one for a handful
 * of pure functions was not worth the dependency. esbuild is already present
 * for the build, so this bundles and runs under node: enough to hold the
 * arithmetic that density mode depends on.
 *
 * It exists because density mode fails silently. Every value it reads is
 * optional in the payload type, so a field that stops arriving reads as
 * undefined and renders as zero — a node showing "0 pods" looks like a
 * successful drain rather than a bug. This codebase has already shipped a
 * blank page from exactly that shape of mistake.
 *
 *   npm run test:density
 */
import { toModel, computeLayout, densityCounts, gridColumns } from "../src/layout";
import type { GraphPayload, PlanStep } from "../src/api";

const node = (name: string, podCount: number, cpuReq: number, pinned = 0, blocked = 0) => ({
  data: { id: `node:${name}`, kind: "node" as const, label: name,
          cpuAllocatable: 8000, cpuRequested: cpuReq, memAllocatable: 1, memRequested: 1,
          podCount, pinnedCount: pinned, blockedCount: blocked, drainStep: 0 },
});

const graph: GraphPayload = {
  planId: "p", aggregated: true, stats: {} as never,
  elements: [node("a", 4, 2000), node("b", 3, 1500), node("c", 0, 0)],
};

const steps: PlanStep[] = [{
  id: "s1", sequenceNumber: 1, targetNode: "a", impact: "Green", rationale: "",
  moves: [
    { namespace: "d", pod: "p1", fromNode: "a", toNode: "b", cpuMilli: 500, memoryBytes: 1 },
    { namespace: "d", pod: "p2", fromNode: "a", toNode: "b", cpuMilli: 500, memoryBytes: 1 },
  ],
}];

let failures = 0;
const check = (label: string, got: unknown, want: unknown) => {
  const ok = JSON.stringify(got) === JSON.stringify(want);
  if (!ok) { failures++; console.log(`  FAIL ${label}: got ${JSON.stringify(got)} want ${JSON.stringify(want)}`); }
  else console.log(`  ok   ${label} = ${JSON.stringify(got)}`);
};

const model = toModel(graph);
check("aggregated flag survives toModel", model.aggregated, true);
check("no pod elements", model.pods.length, 0);
check("podCount read from payload", model.nodes.map(n => n.podCount), [4, 3, 0]);

// Step 0 — the cluster as it stands.
const c0 = densityCounts(model, steps, 0);
check("step 0 counts", [c0.get("a"), c0.get("b")], [4, 3]);

// Step 1 — two pods move a -> b.
const c1 = densityCounts(model, steps, 1);
check("step 1 counts after 2 moves", [c1.get("a"), c1.get("b")], [2, 5]);

const cols = gridColumns(model.nodes.length);
const l0 = computeLayout(model, new Map(), 1, cols, steps, 0);
const l1 = computeLayout(model, new Map(), 1, cols, steps, 1);

check("fill at step 0 (a: 2000/8000)", l0.nodeFill.get("a"), 0.25);
check("fill at step 0 (b: 1500/8000)", l0.nodeFill.get("b"), 0.1875);
// The point of putting cpuMilli on Move: the receiving node must visibly fill.
check("a drains by 1000 milli", l1.nodeLoad.get("a"), 1000);
check("b GAINS 1000 milli", l1.nodeLoad.get("b"), 2500);
check("b fill rises", l1.nodeFill.get("b")! > l0.nodeFill.get("b")!, true);
check("boxes are positioned", l0.nodes.size, 3);
check("density boxes are square-ish", l0.nodeWidth === l0.nodeHeight, true);
check("empty node counts as emptied", l0.emptied.has("c"), true);
check("occupied node not emptied at step 0", l0.emptied.has("a"), false);

console.log(failures === 0 ? "\nALL DENSITY CHECKS PASSED" : `\n${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);
