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

const GIB = 1 << 30;

/**
 * Memory is a real dimension here, not filler.
 *
 * It used to be `memAllocatable: 1, memRequested: 1` — a placeholder from when
 * the field drew CPU only. The moment fullness started meaning "the dimension
 * that limits packing", that placeholder said every node was 100% memory-bound
 * and the fixture stopped describing any cluster that could exist.
 */
const node = (
  name: string, podCount: number, cpuReq: number, memReq: number,
  pinned = 0, blocked = 0,
) => ({
  data: { id: `node:${name}`, kind: "node" as const, label: name,
          cpuAllocatable: 8000, cpuRequested: cpuReq,
          memAllocatable: 32 * GIB, memRequested: memReq,
          podCount, pinnedCount: pinned, blockedCount: blocked, drainStep: 0 },
});

const graph: GraphPayload = {
  planId: "p", aggregated: true, stats: {} as never,
  elements: [
    node("a", 4, 2000, 4 * GIB),   // cpu 25%, mem 12.5% — CPU binds
    node("b", 3, 1500, 2 * GIB),   // cpu 18.75%, mem 6.25% — CPU binds
    node("c", 0, 0, 0),
    // The case the old fixture could not express: plenty of CPU free, almost
    // no memory. Drawing CPU alone showed this node as 12% empty and invited a
    // plan to pack it, which the planner would then refuse.
    node("m", 2, 1000, 30 * GIB),  // cpu 12.5%, mem 93.75% — MEMORY binds
  ],
};

const steps: PlanStep[] = [{
  id: "s1", sequenceNumber: 1, targetNode: "a", impact: "Green", rationale: "",
  moves: [
    { namespace: "d", pod: "p1", fromNode: "a", toNode: "b", cpuMilli: 500, memoryBytes: GIB },
    { namespace: "d", pod: "p2", fromNode: "a", toNode: "b", cpuMilli: 500, memoryBytes: GIB },
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
check("podCount read from payload", model.nodes.map(n => n.podCount), [4, 3, 0, 2]);

// Step 0 — the cluster as it stands.
const c0 = densityCounts(model, steps, 0);
check("step 0 counts", [c0.get("a"), c0.get("b")], [4, 3]);

// Step 1 — two pods move a -> b.
const c1 = densityCounts(model, steps, 1);
check("step 1 counts after 2 moves", [c1.get("a"), c1.get("b")], [2, 5]);

const cols = gridColumns(model.nodes.length);
const l0 = computeLayout(model, new Map(), 1, cols, steps, 0);
const l1 = computeLayout(model, new Map(), 1, cols, steps, 1);

check("a: cpu 2000/8000 binds over mem 4/32", l0.nodeFill.get("a"),
      { cpu: 0.25, mem: 0.125, dominant: 0.25, bound: "cpu" });
check("b: cpu 1500/8000 binds over mem 2/32", l0.nodeFill.get("b"),
      { cpu: 0.1875, mem: 0.0625, dominant: 0.1875, bound: "cpu" });

// The regression this dimension exists to prevent. Drawn on CPU alone this
// node reads 13% — nearly empty — while the planner treats it as 94% full and
// will not pack it. The field and the engine must agree.
check("m: memory binds, and the field says so", l0.nodeFill.get("m"),
      { cpu: 0.125, mem: 0.9375, dominant: 0.9375, bound: "mem" });
check("m is drawn as nearly full, not nearly empty",
      l0.nodeFill.get("m")!.dominant > 0.9, true);

// The point of putting cpuMilli and memoryBytes on Move: the receiving node
// must visibly fill on both dimensions.
check("a drains by 1000 milli", l1.nodeLoad.get("a"), 1000);
check("b GAINS 1000 milli", l1.nodeLoad.get("b"), 2500);
check("b cpu fill rises", l1.nodeFill.get("b")!.cpu > l0.nodeFill.get("b")!.cpu, true);
check("b mem fill rises too", l1.nodeFill.get("b")!.mem > l0.nodeFill.get("b")!.mem, true);
check("a mem fill falls", l1.nodeFill.get("a")!.mem < l0.nodeFill.get("a")!.mem, true);
check("boxes are positioned", l0.nodes.size, 4);
check("density boxes are square-ish", l0.nodeWidth === l0.nodeHeight, true);
check("empty node counts as emptied", l0.emptied.has("c"), true);
check("occupied node not emptied at step 0", l0.emptied.has("a"), false);

console.log(failures === 0 ? "\nALL DENSITY CHECKS PASSED" : `\n${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);
