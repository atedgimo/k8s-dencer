# Product roadmap

What k8s-dencer can do today, and what it will do next — as capabilities, not
milestones. The engineering history behind each item, with measurements and
design reasoning, is in **[roadmap.md](roadmap.md)**; this page is the view for
deciding what to build next and telling users what they get.

*Last updated: 2026-08-01.*

## Shipped

### Analysis and planning
- **Upgrade preflight** — `dencer preflight`: will a node-pool rotation
  wedge, on which node, because of which pod, and why — answered before
  anything is touched, by the same analyzer that plans consolidations
- **Full constraint analysis** — PDBs, topology spread, pod/node affinity,
  taints and tolerations, controller ownership, local storage; every immovable
  pod explained in words, not codes
- **Consolidation plans as ordered, discrete steps** — one node drained per
  step, each step independently executable
- **Risk rating per step** (Green / Yellow / Red) with the reasoning attached;
  ratings are glyph + word, never colour alone
- **Fullness measured the way the scheduler packs** — worst of CPU and memory
  requests over allocatable, identical to the planner's own ranking
- **Plans priced in capacity, not just count** — "reclaim 11 of 31 nodes ·
  88 cores · 352 GiB", identical from the UI and the CLI, because nodes are
  not fungible and a count alone cannot say whether a plan matters
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

The list below reflects a deliberate widening: the constraint analyzer is the
product's real asset — the only thing in the cluster that can *explain
evictability* — and consolidation is one question it can answer. Items 2–6
are the widening: preflight, the resilience audit and guarded drain ask the
same engine other questions; the savings ledger and right-sizing monetise
what it already records and packs. Chosen because they reuse the machinery
as it stands, and two of them serve audiences far larger than
consolidation's.

1. **Closed-loop consolidation** (M25, designed) — re-plan from observed state
   after every step instead of executing a forecast; bounded by an explicit
   operator-approved envelope (max nodes, window, impact ceiling). Ends the
   gap where a run aborts on divergence a fresh plan would simply absorb.
   Includes per-pod placement during a run, the first consumer of its live
   data.
2. **Savings ledger** — "reclaimed 340 cores · 1.3 TiB across 23 nodes in 90
   days, median removal 11m", from the reclamation history the product
   already records with timestamps. The only *measured* (not estimated)
   savings number a consolidation tool can show, and it compounds — every day
   unshipped is data lost. Optional `$/node/month` chart value turns it into
   money.
4. **Right-sizing signal** — requests versus actual usage per workload:
   "your top 10 over-requested workloads are holding 6 nodes hostage."
   Multiplies the core value, because consolidation packs requests and most
   clusters' requests are 2–3× usage.
   *Scoped 2026-08: the plumbing exists end to end (`metrics.Source`,
   collector wiring, `Usage` on the model, `HasUsageData`) but the only
   implementation is `Noop` — a `metrics.k8s.io` client is the actual work,
   plus read-only RBAC. Deliberately not built against KWOK: fake nodes have
   no kubelets, so the feature could not be verified here, and shipping an
   unverifiable feature is how confident wrong numbers happen. First
   verifiable on k3d with metrics-server or the GCP run.*
5. **Resilience audit** — the analyzer's never-evictable list, re-sorted into
   an availability risk report: zero-headroom PDBs, single replicas,
   controller-less pods — the cluster's inability to survive a node loss,
   named before the incident.
6. **Guarded drain** — `dencer drain <node>`: the drain everyone does with
   kubectl, but with the PDB pre-check, readiness verification and audit
   trail the executor already has.
7. **Cost awareness** — instance type and capacity type (spot/on-demand) in
   the model, so "better plan" can mean "cheaper estate", not "fewer nodes".
8. **What-if simulation** — the planner against a modified snapshot: "can I
   lose zone B? can this workload fit?" Capacity planning as a question, not
   a spreadsheet.
9. **Reclaimer detection** — warn *before* draining when no autoscaler or
   Karpenter is present to remove the emptied node (drained-but-not-removed
   is pure cost).


## Explicitly not planned

Decisions, not omissions — recorded so they are not re-litigated by accident:

- **High availability / Postgres** — a consolidation planner is not a serving
  path; the run queue is crash-safe, the planner replans on restart
- **Fully autonomous operation** — the product's premise is that a human
  approves eviction; scheduled unattended execution stays out until the
  closed-loop envelope model proves itself
- **Multi-cluster orchestration** — one cluster per install
- **Planner lookahead** — measured before building, 2026-08: greedy already
  averages 0.2 nodes *below* the constraint-free capacity bound across 15
  synthetic runs (worst case +2, small clusters only). A lookahead can only
  recover the gap to that bound, and there is essentially none. The measuring
  test stays in the suite and reopens the question automatically if the
  planner ever drifts

---

[← Documentation index](README.md) · [Engineering roadmap](roadmap.md) · [Project README](../README.md)
