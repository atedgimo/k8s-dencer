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
| **M21b** | Real ingress controller and StorageClass | planned |
| **M22** | Reclamation loop — observe whether a drained node was actually removed | **done** |

High availability and a Postgres store were **dropped, not deferred**: a
consolidation planner is not a serving path. The run queue is already crash-safe
and resumes, the planner replans on restart, and a minute of UI downtime costs
nothing.

Deferred: scheduled automatic execution and multi-agent orchestration.

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
