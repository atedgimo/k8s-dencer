# Roadmap and status

What is built, what is planned, and what was deliberately dropped. Milestones are grouped into the phases they were planned and executed in.


Phase 1 (MVP) — visibility and explainability. No execution capability.

| Milestone | Scope | State |
|---|---|---|
| **M0** | Helm chart to full production contract, three images, delivery skeleton | **done** |
| **M1** | KWOK fake-node fabric + five constraint scenarios as Helm releases | **done** |
| **M2** | Domain model + informer-backed cluster state collector | **done** |
| **M3** | Constraint analyzer + placement feasibility evaluator | **done** |
| **M4** | Consolidation planner (greedy first-fit-decreasing) | **done** |
| **M5** | Impact classifier (Green/Yellow/Red + rationale) | **done** |
| **M6** | Plan store (SQLite) + REST API + graph payload | **done** |
| **M7** | UI: capacity ribbon, packing canvas, scrubber, constraint inspector | **done** |
| **M8** | Kagent agent: read-only MCP tools + `Agent` CR | **done** |

**Phase 1 is complete.**

Phase 2 — on-request execution. Authentication lands first, deliberately: a
window in which an execute endpoint exists unauthenticated is worse than either
state on its own.

| Milestone | Scope | State |
|---|---|---|
| **M9** | AuthN/AuthZ via TokenReview + SubjectAccessReview; OIDC SSO; NetworkPolicy on by default | **done** |
| **M10** | Executor + Safety Guard, own ServiceAccount, `pods/eviction`, audited runs | **done** |
| **M11** | UI rebuild: packing view, motion, action-first header | **done** |
| **M12** | Execution controls, live run, OIDC sign-in flow, plan pinning | **done** |
| **M13** | End-to-end on KWOK: real drain, PDB block, Red refusal, SSO against Dex | **done** |

Phase 3 — maintenance windows.

| Milestone | Scope | State |
|---|---|---|
| **M14** | `MaintenanceWindow` CRD; Red steps unlocked by an open window | **done** |

Phase 4 — production hardening. Target: 1000+ nodes, 50k pods. Measured, not
estimated — every milestone here starts from a number in
[docs/benchmarks.md](benchmarks.md).

| Milestone | Scope | State |
|---|---|---|
| **M15** | Readiness verification — the executor waits for Ready, not Running | **done** |
| **M16** | Measured ceiling — synthesised clusters, `make bench` | **done** |
| **M17** | Data volume — gzipped plan blobs (23.8× at 5k pods), informer cache transform (−30% heap), paginated reads | **done** |
| **M18** | Planner cost — memoised topology-spread counts, **112×** faster analysis at 5k pods | **done** |
| **M19** | UI at scale — aggregated payload (**44×** smaller at 5k pods), density rendering, virtualised field | **done** |
| **M20** | `/metrics` on all three components; monitors that scrape a real path; CI; published images | **done** |
| **M21** | Multi-node k3d e2e in CI, PodSecurity **enforcing**, `readiness: Ready` on real pods | **done** |
| **M21b** | Real ingress controller and StorageClass, exercised in the e2e | **done** |
| **M22** | Reclamation loop — observe whether a drained node was actually removed | **done** |
| **M23** | Cloud test on GKE — GKE's autoscaler reclaimed a drained node in **11m9s**, observed | **done** |
| **M24** | The field shows what *is*, not only what *would be* — observed node states overlaid on the plan | **done for nodes**; per-pod placement waits on M25 |
| **M25** | Closed-loop consolidation — re-plan from observed state after every step, inside an operator-approved envelope | **core shipped**; UI affordance pending |

High availability and a Postgres store were **dropped, not deferred**: a
consolidation planner is not a serving path. The run queue is already crash-safe
and resumes, the planner replans on restart, and a minute of UI downtime costs
nothing.

### M24 — live reality in the field

The packing field draws the plan. Everything on it — a node emptying, a box
going dim — is the scrubber simulating a step, and it is honest about being a
simulation. What it cannot do is show what is *actually true right now*.

Found the obvious way: a node was drained on a real cluster, `kubectl` reported
`SchedulingDisabled`, and the UI looked exactly as it had before. `cordoned`
was parsed into the model and rendered nowhere, so the only way to see a drain
was to scrub forward into a prediction that happened to match reality.

That is the same confusion between predicted and observed that the reclamation
loop existed to remove, surviving one layer up in the view.

| Gap | State |
|---|---|
| **Cordoned** | **shipped** — hatched and worded, in all three views |
| **Node conditions** | **shipped** — NotReady renders as a dotted border and the word; it was parsed and drawn nowhere, the cordoned bug again |
| **Reclamation per node** | **shipped** — the awaiting node itself says *awaiting removal*; an observed-reclaimed node ghosts and says *reclaimed* |
| **Freshness** | **shipped** — `planConfirmedAt`, polled; the warning now names the real failure (confirmations stopping) instead of firing on stability |
| **One dimension of three** | **shipped** — fullness is the dominant of CPU and memory, matching the planner |
| **During a run** | **shipped for nodes** — the run's own event trail marks cordons and drains as they actually happen, with dry runs excluded; **still open for pods**, whose drawn positions follow the plan even while the scheduler decides otherwise |

The mechanism that landed: a per-node **observed overlay** (`ObservedNode`),
derived once in `PackingField` from three sources — the snapshot, the
reclamation tracker, and the current run's event trail — and layered over the
plan so all three views agree on what is real. Observed facts hold still while
the scrubber runs; that is the visible difference between a fact and a
forecast. Priority when facts stack: *reclaimed* over *NotReady* over
*awaiting removal* over *cordoned*, by what an operator must act on first.

Textures distinguish the kind without colour: cordon hatches, NotReady dots,
observed-gone ghosts. The words do the naming; colour stays reserved for risk.

Guarded by mutation-tested source assertions, because this bug class —
**parsed into the model, rendered nowhere** — shipped twice (`cordoned`, then
`ready`) and the payload-parity guard structurally cannot catch it: reading a
field into a struct nothing draws satisfies "the UI reads it".

Remaining for M24: per-pod placement during a run. The run events name evicted
pods but not where they landed; drawing that honestly needs live placement
data the payload does not carry, and it is the natural first consumer of M25's
closed loop.

#### The staleness warning fires backwards

Past five minutes the verdict line reads *"confirmed 5 minutes ago — the cluster
may have moved on."* It is not a cosmetic complaint that this looks unfinished:
**the warning appears precisely when the cluster has not moved on.**

`collect()` returns early when `db.Save` reports the same content hash as the
previous cycle — the correct thing to do for storage, but it also skips the
publish, so SSE subscribers hear nothing. On a stable cluster the planner keeps
snapshotting every 30 seconds and confirming the plan still holds, and says so
to no one. The UI ticks a clock against `generatedAt`, watches it climb, and
eventually apologises. Stability is rendered as doubt.

The fix is to stop conflating two different facts:

| Fact | Ages when |
|---|---|
| `generatedAt` — when this plan was computed | a new plan is produced |
| `confirmedAt` — when the cluster was last observed to still match it | **every successful cycle**, published whether or not the plan changed |

The age line reads `confirmedAt`. On a stable cluster it says *confirmed just
now*, indefinitely, which is true. It only warns when confirmations actually
stop — the planner wedged, snapshots failing, SSE dropped — which is the one
case where the operator genuinely should not trust the screen, and the one case
that is invisible today.

#### Fullness is three dimensions, and the field shows one

`Resources.Fits` checks CPU, memory **and** pod slots; the planner ranks nodes
by `DominantRatio`, the worst of the three, because that is the dimension that
actually limits packing. The agent tool reports that number. The planner logs
`cpuRequestedPct` and `memRequestedPct` side by side. The Inspector shows both
when a node is opened.

The field's headline figure — the box gauge, the well level, the panel bar, the
number an operator actually scans — is CPU alone. A memory-bound node reads as
half empty on a screen whose own planner will refuse to pack it. The two
surfaces of the same product disagree about how full a node is.

The data is already on the wire (`memAllocatable`, `memRequested`) and already
in the client model. What changes is which number is drawn: the dominant ratio,
with the binding dimension named.

Deferred: scheduled automatic execution and multi-agent orchestration.

### M25 — closed-loop consolidation

Today a plan is computed once against one snapshot, and steps 2..N assume steps
1..N-1 landed exactly as predicted. They may not have, because **the plan does
not tell the scheduler anything.**

That is worth stating precisely, because the code is already more honest than
the interface. Every consumer of `step.Moves` was checked: the planner writes
it, the CLI, graph API and impact classifier read it, and the **executor never
reads it at all.** It cordons `step.TargetNode` and evicts whatever movable pods
are genuinely there; where they land is kube-scheduler's decision. Destinations
are advisory in the engine and drawn as fact in the UI.

The sequence is the real gap. The executor is careful — it re-runs the safety
guard against freshly-read state before every step, waits for evicted pods to
recover, and verifies they landed — but its answer to divergence is **abort,
not re-plan**. So a run that a fresh plan would happily continue is thrown away,
and a better target appearing mid-run is never noticed.

The loop this replaces the forecast with:

```
observe → plan one step → gate → execute → settle → observe → …
```

Two things decide whether this is sound, and both are design, not plumbing:

**Termination.** Re-planning after every step can oscillate: drain A, the
scheduler fills B, now B looks drainable. Three rails together — each round must
*strictly reduce the node count* or the loop stops; a node returned to service
during a run is never re-targeted by that run; and a hard round cap above both,
so a bug costs a bounded number of evictions rather than a walk across the
estate.

**What the operator approved.** Today they approve a concrete list of nodes.
Under continuous re-planning they would be approving a *policy* — "keep going
until optimum" — and for a product whose entire pitch is *a human approves
before pods are evicted*, that cannot change silently. The consent becomes an
explicit envelope: up to N nodes, inside this window, at or below a stated
impact rating. Re-plan freely within it; anything outside needs a new approval.

The UI follows from that. Step 1 is a commitment and the rest is a projection
with a shrinking horizon, and the two must not be drawn the same way — which is
the same rule M24 is already establishing for observed versus predicted.

Verification, in the order that matters:

- an oscillation fixture — draining A makes B drainable and vice versa — that
  asserts the loop **terminates**, because a plausible-looking implementation
  that never halts is the failure mode here
- monotonic decrease asserted per round, and the envelope asserted never
  exceeded, including when a re-plan wants to
- KWOK end-to-end: run to fixpoint and compare the result against what the
  one-shot plan predicted, which finally measures the gap this milestone exists
  to close

**What shipped (core):** `dencer converge --max-nodes N --max-impact Green|Yellow`
queues a converge run through `POST /api/v1/converge` (same
`ExecuteConsolidations` grant — one permission, two shapes of consent). The
executor loop plans one step per round against a fresh snapshot, using the
same planner library and the same impact thresholds as the planner component
(one chart values block feeds both), runs the full Safety Guard per step, and
stops on the first of: optimum reached, envelope's node budget, impact
ceiling, a round that freed no node, or a guard refusal.

The termination rails, each mutation-tested: removing the monotonic rail, the
node budget, or the ceiling check makes a named test fail. The
scheduler-divergence fixture is the interesting one — the planner itself
refuses non-consolidating moves, so the naive oscillation fixture *cannot
fail*; the fixture that works has the fake scheduler ignore the plan's target
the way a real scheduler may, which is the exact case the rail exists for.

The consent prompt for converge is deliberately not the steps prompt with
different words: it says "you are approving a policy, not a list", states both
bounds and both rails, and shows the current plan only as explicitly
non-binding context.

A converge dry run rehearses exactly one round and says so. Rehearsing further
rounds would mean pretending to know where evicted pods land — a forecast
wearing a safety vest, the exact thing this mode exists to retire.

**Still open for M25:** a UI affordance for the envelope (the CLI carries it
today), and the KWOK fixpoint-vs-forecast comparison.

## What actually runs today

The planner watches the cluster through informers and publishes an immutable
`ClusterSnapshot` every resync period:

```
snapshot:    nodes=31 nodesOccupied=28 pods=121 pdbs=0 pdbsBlocking=0
             cpuRequestedPct=38.6% memRequestedPct=19.6% usageData=false
constraints: movable=120 blocked=1 stuck=26 antiAffinity=0 spreadBound=1
             controllerPinned=1 nodesUndrainable=1
plan:        id=754f04015304 steps=15 nodesBefore=28 nodesAfter=13 reclaims=15
             green=12 yellow=0 red=3
```

The same numbers are on `/metrics`, which is the point of it — nothing is
derived twice:

```
dencer_plan_age_seconds 4.181680473
dencer_plan_nodes_reclaimable 15
dencer_plan_steps{impact="Green"} 12
dencer_plan_steps{impact="Yellow"} 0
dencer_plan_steps{impact="Red"} 3
dencer_snapshot_nodes 31
dencer_snapshot_pods 121
```

Plans are rated, explained, persisted, visualised, and answerable in natural
language through a Kagent agent. The API is authenticated and authorized
against cluster RBAC.

With `executor.enabled=true`, an operator holding `create consolidations.dencer.io`
can run a chosen subset of steps. Nothing else in the release can cordon, drain
or evict, and `make lint` fails the build if `pods/eviction` reaches any role
but the executor's.

Three debug endpoints on the planner's health port expose current state as
YAML: `/debug/snapshot`, `/debug/constraints` and `/debug/plan`.

---

---

[← Documentation index](README.md) · [Project README](../README.md)
