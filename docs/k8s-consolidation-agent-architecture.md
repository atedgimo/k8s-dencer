# k8s-dencer — Architecture & Implementation Plan

> **Historical document.** This is the original design, kept as the record of
> what was intended and why. Where it disagrees with the code, the code won —
> see [architecture.md](architecture.md) for what actually runs, and
> [roadmap.md](roadmap.md) for what changed along the way and the reasons.


**Project name**: `k8s-dencer`

| Artifact | Name |
|---|---|
| GitHub repo | `k8s-dencer` |
| Helm chart | `k8s-dencer` |
| Docker images | `k8s-dencer-planner`, `k8s-dencer-executor`, `k8s-dencer-ui-backend`, `k8s-dencer-ui-frontend` (agent runs in Kagent, not a separate k8s-dencer image) |
| CRD API group | `dencer.io` |

## 1. Problem Statement

Kubernetes clusters lose node-utilization efficiency over time due to accumulated
scheduling constraints (PDBs, topology spread, affinity/anti-affinity, taints).
The built-in descheduler is slow, purely rule-based/deterministic, and teams are
often reluctant to let it run unattended because its actions are opaque.

## 2. Goals

- Continuously **plan** node consolidation in the background, unconstrained by
  execution risk — i.e. planning always considers the *ideal* packing, not just
  what's safe to do right now.
- **Execute** only under explicit permission: on-demand request or a defined
  maintenance window.
- Full **transparency**: every constraint that shaped the plan (PDBs, topology
  spread, affinity, resource requests/limits, taints/tolerations) is visible in
  the UI, along with a live relationship graph.
- **Safety by construction**: hard caps on blast radius, PDB-aware sequencing,
  dry-run/simulation before any real drain.
- Ship as a proper **Kubernetes-native** system: Helm chart, container images,
  CRDs, and support for running as a Kagent agent.

## 3. Non-Goals (v1)

- Not a general-purpose autoscaler (no node provisioning/deprovisioning
  decisions — that's cluster-autoscaler/Karpenter's job). This agent decides
  *which pods should move where* to free up nodes that the autoscaler can then
  scale down.
- Not a scheduler replacement — it doesn't intercept the scheduling path, it
  proposes moves and executes via standard `cordon`/`drain`/eviction APIs.

## 4. High-Level Architecture

```
                    ┌─────────────────────────────────────────┐
                    │              UI (Frontend)               │
                    │  graph view · live plan · window config  │
                    └───────────────┬───────────────────────────┘
                                    │ REST/gRPC/WebSocket
                    ┌───────────────▼───────────────────────────┐
                    │            UI/API Backend                  │
                    └───────────────┬───────────────────────────┘
                                    │
        ┌───────────────────────────┼───────────────────────────┐
        │                           │                           │
┌───────▼────────┐        ┌─────────▼─────────┐       ┌─────────▼─────────┐
│ Cluster State    │        │  Consolidation     │       │     Executor       │
│ Collector         │───────▶│    Planner         │──────▶│  (gated: window /  │
│ (informers,       │        │  (bin-packing +    │ Plan  │   on-request)      │
│  metrics)         │        │   constraint model) │       │ drain/cordon ctrl  │
└───────────────────┘        └─────────┬─────────┘       └─────────┬─────────┘
                                        │                            │
                              ┌─────────▼─────────┐        ┌─────────▼─────────┐
                              │ Scheduler Simulator│        │   Safety Guard     │
                              │  (dry-run scoring)  │        │ (PDB/blast-radius) │
                              └─────────────────────┘        └────────────────────┘
```

Two independent control loops sharing a data model:

1. **Planner loop** — always running, read-only against the cluster, produces a
   `ConsolidationPlan` object continuously refreshed.
2. **Executor loop** — dormant by default; wakes only when a `MaintenanceWindow`
   is active or an on-request execution is approved via the UI/API. Consumes
   the latest valid plan, re-validates it (cluster state may have drifted),
   and executes step by step with safety checks between every step.

## 5. Core Components

| Component | Responsibility |
|---|---|
| **Cluster State Collector** | Watches nodes, pods, PDBs via informers (not polling) for near-real-time state. Also scrapes metrics-server/Prometheus for actual utilization vs requested. |
| **Constraint Analyzer** | Builds the effective constraint set per pod: PDB membership, topology spread constraints, affinity/anti-affinity, taints/tolerations, resource requests. |
| **Consolidation Planner** | Core algorithm: bin-packing / graph optimization to find a target layout that reduces node count while respecting *all* constraints. Runs unconstrained by "is this safe to do right now" — it's allowed to propose an ideal end-state. Output is not a single blob but an **ordered sequence of discrete steps** (e.g. step 1 of 70), each step being one atomic unit of work (typically: drain one node / move a small set of pods) that can be executed independently of the others. |
| **Impact Classifier** | Rates every individual step **Green / Yellow / Red** based on risk factors: PDB `minAvailable` headroom, whether the node hosts stateful/singleton workloads, topology-spread risk if moved, blast radius of the step alone, and any historical-incident signal. Red = only permitted inside an approved maintenance window (policy-enforced, not just a suggestion). Yellow = executable on request but flagged/needs confirmation. Green = safe to run anytime on request. Attaches a human-readable rationale to each rating so the UI and the agent can both surface *why*. |
| **Scheduler Simulator** | Before any plan is trusted, simulate re-scheduling proposed pod moves against a shadow scheduler to confirm the plan is actually realizable (not just theoretically valid). |
| **Plan Store** | Persists the current and historical plans — as an ordered list of steps with their impact rating and rationale — in a regular DB (not a CRD — see §6) for audit and UI display. |
| **Safety Guard** | Hard-coded, non-overridable-by-LLM rails: max nodes touched per window, PDB `minAvailable` enforcement, abort-on-drift checks, and **hard block on executing Red-rated steps outside an active maintenance window** regardless of what's requested via UI/API. |
| **Executor** | Performs `cordon` → wait/verify → `drain` → verify pods rescheduled. Supports **partial execution**: the operator can request execution of an arbitrary subset/range of steps (e.g. steps 1–3 of 70) rather than only "run the whole plan" or "run nothing." Checks Safety Guard before every individual step, independent of how many steps were requested together. |
| **UI Backend/API** | Exposes plan, graph data, constraint explanations, window CRUD, on-request execution trigger. |
| **UI Frontend** | Node/pod relationship graph, live plan view **rendered as a numbered, color-coded step list (Green/Yellow/Red)**, letting the operator select any subset or range of steps (e.g. steps 1–3 of 70) to run on request, constraint inspector (PDBs, topology spread), maintenance window scheduler, and a chat panel for conversing with the Kagent-hosted agent. |
| **Agent Layer (MVP-lite)** | A single LLM with read-only tool-calling access to the Plan Store, Constraint Analyzer, and **Impact Classifier** — no subagents/ADK in the MVP. Runs as a Kagent agent (not a bespoke service), so it's deployed/managed the k8s-native way from day one. Capabilities: explain the current plan in natural language, answer ad-hoc questions ("why isn't node X being drained?"), and **explain why a given step is rated Green/Yellow/Red** (which constraint/risk factor drove the rating). Exposed in the UI as a chat panel that talks to the Kagent agent (via Kagent's API/A2A interface); decoupled from the core controller so a failure here can't affect planning. |

> **Design note**: keep the core planning/execution logic deterministic and
> algorithmic (testable, auditable). Use the LLM/agent layer for explanation,
> natural-language interaction, and handling ad-hoc/fuzzy requests — not for
> the actual packing decisions. This also makes the Google ADK / subagent
> question easier: subagents make sense for the *interaction* layer (e.g. "why
> did node X get picked", "simulate what happens if I exclude pods with label
> Y"), less so for the core solver.

## 6. Proposed CRDs

> **Design note on CRD vs. DB**: only use a CRD where the object represents
> low-frequency, declarative *desired state* that benefits from being part of
> Kubernetes' own reconciliation model — e.g. something GitOps can manage, or
> that other tools/RBAC need to interact with. A `ConsolidationPlan` is
> refreshed continuously by the planner (every few seconds/minutes) and is
> read almost entirely by the UI — pushing that volume of writes through
> etcd is a well-known anti-pattern (etcd write/watch pressure from
> chatty controllers) and buys nothing since there's no external actor
> "desiring" a specific plan. So:
> - `ConsolidationPlan` → **regular DB** (Postgres/SQLite), owned by the UI
>   backend. Gives cheap history/diffing too.
> - `MaintenanceWindow`, `ConsolidationPolicy` → **CRDs**, since they're
>   low-frequency config that benefits from GitOps management, k8s RBAC
>   (who's allowed to approve/edit a window), and Kagent integration.

**Plan Store schema sketch** (DB, not CRD):

```
ConsolidationPlan
  id, generatedAt, status (Valid|Stale|Invalid)

PlanStep
  id, planId, sequenceNumber        # e.g. 1 of 70
  moves: [{ pod, fromNode, toNode }]
  targetNode (node being drained by this step, if any)
  impactRating: Green | Yellow | Red
  impactRationale: <human-readable text, also used by the agent>
  requiresMaintenanceWindow: bool    # true for Red, policy-enforced
  executedAt, executedBy, result     # audit trail once run
```

Steps are independently addressable and independently executable — the UI
and API let an operator request execution of any subset/range (e.g. steps
1–3), not just "all" or "nothing."

```yaml
apiVersion: dencer.io/v1alpha1
kind: MaintenanceWindow
spec:
  schedule: <cron>
  duration: <duration>
  maxNodesPerWindow: <int>
  autoApprove: true|false
---
apiVersion: dencer.io/v1alpha1
kind: ConsolidationPolicy
spec:
  maxBlastRadius: <int nodes concurrently drained>
  respectPDBs: true
  minNodeAge: <duration>          # avoid draining brand-new nodes
  excludeNamespaces: [...]
  excludeLabels: {...}
```

## 7. Tech Stack Recommendation

- **Core controller/planner/executor**: Go, using `client-go` + `controller-runtime`
  — the standard for k8s-native operators, gives you CRD reconciliation loops
  for free and is what the ecosystem (and Kagent) expects.
- **Scheduler simulation**: reuse `kube-scheduler`'s scheduling framework
  libraries where possible rather than reimplementing predicate/priority logic.
- **Agent layer**: deployed and run as a **Kagent** agent from the MVP onward
  (not a bespoke Python/TS service) — Kagent handles the agent runtime,
  lifecycle, and exposes an API/A2A interface the UI backend proxies chat
  messages through.
- **UI frontend**: React; graph via `react-force-graph` or Cytoscape.js.
- **UI backend**: Go (same binary/module as controller, or separate service
  behind the same API).
- **Packaging**: multi-image Helm chart — `planner`, `executor`, `ui-backend`,
  `ui-frontend` (or combined ui image), each independently scalable; executor
  should run with a dedicated ServiceAccount with tightly scoped RBAC
  (cordon/evict only, nothing else).

## 8. Suggested MVP Roadmap

**MVP scope = Phase 1 in full** — Cluster State Collector, Constraint Analyzer,
Planner, the complete read-only UI (including the relationship graph, not
just a table/JSON view), and a lightweight single-agent explanation/Q&A
service (see §5, Agent Layer). No execution capability at all in the MVP,
and no subagents/ADK orchestration yet — that's still Phase 4.

1. **Phase 1 — Visibility + explainability (MVP)**: Cluster State Collector +
   Constraint Analyzer + Planner + read-only UI (graph, plan) + a Kagent-hosted
   single agent, exposed via a chat panel in the UI, for explanation/Q&A. No
   execution capability at all. This alone is valuable, de-risks
   trust-building, and is demo-able end to end.
2. **Phase 2 — On-request execution**: add Executor + Safety Guard + Scheduler
   Simulator, gated behind manual approval in the UI.
3. **Phase 3 — Maintenance windows**: `MaintenanceWindow` CRD, scheduled
   automatic execution within policy limits.
4. **Phase 4 — Multi-agent orchestration**: expand the MVP's single-agent layer
   into subagents/ADK if usage shows real need for task decomposition (e.g. a
   dedicated PDB-analysis agent, a dedicated topology agent), plus Kagent
   packaging.

## 9. Safety Mechanisms (non-negotiable, enforced in code not prompts)

- Hard cap on concurrently-drained nodes per window.
- PDB `minAvailable` re-checked immediately before every single pod eviction.
- Plan re-validated against live cluster state immediately before execution
  (reject if stale).
- **Red-rated steps can only execute inside an active, approved maintenance
  window** — this is enforced by the Safety Guard itself, not left to UI/API
  input validation, so it can't be bypassed by a crafted request.
- Abort-and-rollback path if a drain step fails or takes longer than expected.
- Full audit log of every action taken, tied to the plan version and specific
  step IDs that authorized it.

## 10. Open Questions to Resolve During Implementation

- Bin-packing algorithm choice: greedy heuristic vs. constraint solver (e.g.
  OR-Tools) — trade-off between speed (continuous re-planning) and optimality.
- How to represent "soft" vs "hard" constraints in the planner scoring model.
- Multi-tenancy: single cluster-wide policy vs. per-namespace policies.
- Plan Store DB choice: Postgres (if already running one in-cluster) vs.
  embedded SQLite for simplicity — depends on expected UI query load and
  whether HA of the UI backend is required.
- Exact thresholds/rules for Green vs. Yellow vs. Red classification (e.g.
  where PDB headroom stops being "Green") — likely needs to be a tunable
  policy rather than hardcoded, since risk tolerance differs per cluster.

---

[← Documentation index](README.md) · [Project README](../README.md)
