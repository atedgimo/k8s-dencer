# Product roadmap

What k8s-dencer can do today, and what it will do next — as capabilities, not
milestones. The engineering history behind each item, with measurements and
design reasoning, is in **[roadmap.md](roadmap.md)**; this page is the view for
deciding what to build next and telling users what they get.

*Last updated: 2026-07-31.*

## Shipped

### Analysis and planning
- **Full constraint analysis** — PDBs, topology spread, pod/node affinity,
  taints and tolerations, controller ownership, local storage; every immovable
  pod explained in words, not codes
- **Consolidation plans as ordered, discrete steps** — one node drained per
  step, each step independently executable
- **Risk rating per step** (Green / Yellow / Red) with the reasoning attached;
  ratings are glyph + word, never colour alone
- **Fullness measured the way the scheduler packs** — worst of CPU and memory
  requests over allocatable, identical to the planner's own ranking
- **Scales to 5,000+ pods** — constraint analysis 298 ms, planning 466 ms at
  5,026 pods, measured; no cluster required to benchmark

### Execution
- **Off by default**, and the chart refuses to enable it without
  authentication and persistence
- **Runs only the steps a human picked**, through the eviction API — the API
  server enforces PDBs, not code in this repo
- **Safety Guard re-checks live state before every action** and refuses on a
  named rule; refusals are auditable outcomes, not errors
- **Red steps require an open maintenance window**; window evaluation fails
  closed
- **Recovery judged on readiness, not phase** — a replacement pod that fails
  its probes aborts the run
- **Dry run** — the full guard chain and the same event trail, touching nothing
- **Privilege split held by construction** — the network-reachable component
  cannot evict; the evicting component has no Service; lint fails the build if
  `pods/eviction` leaks to any other role

### Observing reality (not just predicting it)
- **Reclamation tracking** — a drained node is watched until something
  actually removes it (*awaiting → reclaimed | returned*), proven against
  GKE's own autoscaler (removal observed and timed at 11m9s)
- **Observed node states on the field** — cordoned, NotReady, awaiting
  removal, actually reclaimed; facts hold still while the plan's animation
  runs over them
- **Live run trail** — cordons, evictions and drains stream into the UI as
  the executor performs them; dry runs are visibly rehearsals
- **Plan freshness that means something** — "confirmed just now" is refreshed
  every planner cycle; the warning fires only when confirmations *stop*

### Interfaces
- **Web UI** — dark instrument-style design; three field renderings chosen by
  cluster size (Rack ≤120 nodes, Wells ≤600, Panel above), each switchable and
  persistent; step scrubber; per-node and per-pod inspector; verified identity
  and cluster label in the chrome
- **CLI** — `dencer` and `kubectl dencer` plugin; everything the UI does, with
  `-o json` for pipelines; finds the backend through kubeconfig, no
  port-forward
- **Natural-language agent** (optional, via Kagent) — answers questions by
  quoting the analyzer rather than re-deriving anything
- **Prometheus metrics** on every component; Grafana-ready
- **Auth is your cluster's auth** — TokenReview + SubjectAccessReview
  delegation, OIDC/SSO; no credential store of its own

### Delivery
- **Published images and Helm chart** on ghcr.io, installable in two commands
- **Every release e2e-verified in CI** — five-node cluster, PodSecurity
  `restricted` enforcing, real ingress, real drains, reclamation observed
- **Zero-cost demo** — KWOK fake-node fabric, no real workloads needed

## Next

Ordered by intent; items move up when a user need pulls them.

1. **Reclaimed capacity, not just node count** — "reclaim 15 nodes, 340
   cores, 1.2 TiB" instead of a bare count. Nodes are not fungible; the count
   alone cannot say whether a plan is worth running.
2. **Closed-loop consolidation** (M25, designed) — re-plan from observed state
   after every step instead of executing a forecast; bounded by an explicit
   operator-approved envelope (max nodes, window, impact ceiling). Ends the
   gap where a run aborts on divergence a fresh plan would simply absorb.
3. **Per-pod placement during a run** — draw where pods actually landed, not
   where the plan predicted; first consumer of the closed loop's live data.
4. **Cost awareness** — instance type and capacity type (spot/on-demand) in
   the model, so "better plan" can mean "cheaper estate", not "fewer nodes".
5. **Reclaimer detection** — warn *before* draining when no autoscaler or
   Karpenter is present to remove the emptied node (drained-but-not-removed is
   pure cost).
6. **Planner lookahead** — evaluate whether draining A forecloses draining B
   and C; adopt only if measurably better plans result.

## Explicitly not planned

Decisions, not omissions — recorded so they are not re-litigated by accident:

- **High availability / Postgres** — a consolidation planner is not a serving
  path; the run queue is crash-safe, the planner replans on restart
- **Fully autonomous operation** — the product's premise is that a human
  approves eviction; scheduled unattended execution stays out until the
  closed-loop envelope model proves itself
- **Multi-cluster orchestration** — one cluster per install

---

[← Documentation index](README.md) · [Engineering roadmap](roadmap.md) · [Project README](../README.md)
