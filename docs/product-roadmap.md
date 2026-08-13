# Product roadmap

What k8s-dencer can do today, and what it will do next — as capabilities, not
milestones. The engineering history behind each item, with measurements and
design reasoning, is in **[roadmap.md](roadmap.md)**; this page is the view for
deciding what to build next and telling users what they get.

*Last updated: 2026-08-01.*

## Shipped

### Analysis and planning
- **Recommendations** — `dencer recommend`: what is *missing*, with fixes.
  The PDB nobody wrote (paste-ready YAML), the single replica that makes
  every drain an outage, the absent requests the scheduler is blind
  without, the zero-headroom budget with the concrete change — severity is
  impact-on-consolidation, and every item carries its why. (UI panel:
  next slice.)
- **The ecosystem's hands-off signals are honoured** —
  `karpenter.sh/do-not-disrupt` and
  `cluster-autoscaler.kubernetes.io/safe-to-evict: "false"` pin a pod
  (named constraint, quoted in every explanation) and exclude a node from
  candidacy, exactly as the autoscalers themselves behave
- **Right-sizing report** — `dencer rightsizing`: requests versus *measured*
  usage per workload (metrics-server, opt-in via `planner.usageSource`),
  sorted by absolute CPU excess. Refuses to estimate without measurements,
  skips workloads with no sample rather than printing a damning zero
- **What-if simulation** — `dencer whatif --without-zone b`: the latest
  snapshot minus the removed nodes, every displaced pod re-homed by the
  constraint engine, the homeless ones named with reasons. Non-zero exit when
  it does not fit, so it gates CI
- **Resilience audit** — `dencer audit`: the never-evictable list read as an
  availability report; the PDB that blocks a drain is the PDB a dying node
  violates. Findings quote the analyzer's own explanations
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
- **Closed-loop consolidation** — "Run to optimum" in the UI and `dencer
  converge` in the CLI: the executor re-plans from *observed* state after
  every drain, inside an explicit envelope (max nodes, impact ceiling), with
  three mutation-tested termination rails. Consent is to a policy, and both
  surfaces say so in those words
- **Guarded drain** — `dencer drain <node>`: kubectl drain with the rails.
  Rated with the same impact thresholds as a planned step (Red still needs a
  window — naming the node is not a side-channel), per-eviction PDB checks
  against fresh state, recovery verified, audited
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
- **History** — the cluster as a line, not a moment: the estate with its
  reclaimable slice shaded, the ledger climbing as capacity is *measured*
  returned (run markers on the timeline; hollow ones were rehearsals), and
  requests versus measured usage. One sample per planner cycle, 30-day
  window. The demo fabric can play the missing autoscaler
  (`make demo-reclaim`) so the whole story shows locally
- **Reclaimer evidence, three-valued** — a recorded removal is proof, a
  visible autoscaler pod is a promise, and neither is *not* "no reclaimer"
  (managed control planes hide theirs). When drains are pending and there is
  no evidence at all, the tray and `dencer reclamations` say so: drained
  nodes are pure cost until something removes them
- **Savings ledger** — capacity *actually returned*, measured: the executor
  captures each node's allocatable at drain time (the last moment it can be),
  and the summary sums it over nodes that genuinely disappeared. Pre-ledger
  reclamations are counted as uncounted rather than silently zero. Shown in
  the UI tray and `dencer reclamations`
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
  cluster size (Rack ≤120 nodes, Wells ≤600, Load above), each switchable and
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

### The redesigned UI (2026-08-01)
- **"The ledger"** — the product UI rebuilt against a twelve-frame design
  handoff (vendored in `assets/design/`), shipped as seven stacked PRs:
  foundation tokens and frame, plan review, run screens, the Recommendations
  queue, the three Cluster lenses with History, sign-in, and closeout
- **The step list is the screen** — verdicts say what they mean (Safe now /
  Needs a call / Held back), every held-back step names its rule, one filled
  Drain button behind a typed confirmation for non-Green selections
- **The design forced two backend capabilities into existence** — a real
  packing ceiling (`planner.packCeiling`, default 0.85, recorded on each
  plan and drawn in the Wells lens) and per-node measured usage (the Load
  lens's whole premise)
- **Recommendations became a work queue** — the plan's own blocking rules,
  ranked by nodes unlocked, each naming the steps it holds back
- **First-class dual themes** — the light palette is redrawn, not inverted,
  and `test/palette` computes every documented contrast pair in both

### Delivery
- **The demo records itself** — `make demo-video`: a Playwright-scripted
  walkthrough of the real UI against the live cluster, so the recording can
  never go stale (`assets/demo/walkthrough.webm`)
- **Published images and Helm chart** on ghcr.io, installable in two commands
- **Every release e2e-verified in CI** — five-node cluster, PodSecurity
  `restricted` enforcing, real ingress, real drains, reclamation observed
- **Zero-cost demo** — KWOK fake-node fabric, no real workloads needed

### Managed clusters, and the surfaces that were built and never shown (2026-08-07)
- **It plans on managed clusters at all** — a static pod owned by the node
  (GKE runs kube-proxy this way) is recognised as pinned. Before this the
  analyzer tried to reschedule a pod nothing can move, failed, and marked
  every node undrainable: converge reported nothing to do while the cloud's
  own autoscaler reclaimed two nodes from the same cluster
- **Pinned is not blocking** — a DaemonSet pod cannot move but does not hold
  a drain up, so preflight and audit stopped reporting the worst possible
  answer on every real cluster
- **A GKE-shaped fixture** — the whole class of bug reproduced in CI, because
  KWOK nodes run no system pods at all and never could catch it
- **The ledger in money** — operator-supplied prices, no built-in table, a
  rate rather than a total, and nodes reclaimed by something else counted
  separately rather than claimed
- **Rightsizing, maintenance windows, what-if and preflight** — four
  capabilities the backend already had and the UI never called
- **An empty plan explains itself** — the state a healthy cluster spends most
  of its life in used to render blank
- **Per-node history** — a node stably idle for a fortnight told apart from
  one that peaks nightly
- **Stop a run in flight** — one control, not the designed two: a pod already
  evicted cannot be un-evicted, so abort and pause are the same capability
  and offering both would promise an undo that does not exist

### A Postgres plan store, so the planner stops being pinned to one node (2026-08-12)
- **`database.type=postgres`** — the plan store reached over the network
  instead of through a file, which removes the PersistentVolumeClaim, the
  `/data` mount, the required pod affinity and the PriorityClass in one move.
  `uiBackend.replicaCount` becomes an ordinary value
- **Why, precisely** — not availability. SQLite's ReadWriteOnce claim permits
  multiple pods only on the same node, so the planner is co-scheduled onto the
  ui-backend's node and has exactly one node it may occupy. On a packed
  cluster — the kind this product is installed on — it loses the scheduling
  race and sits `Pending`. v0.5.0's PriorityClass made that less likely, not
  impossible. Both roadmaps had recorded Postgres as dropped-not-deferred, and
  that call is reversed here rather than quietly overwritten
- **One store, both dialects** — thirty-two methods and fourteen schema
  versions shared, differing in four places: placeholder spelling, `BLOB`,
  `REAL`, and where the version is kept. The alternative is two things that
  must agree, kept apart, and found to disagree by a user
- **The same test suite runs against both backends** in CI, rather than a
  second suite written from memory. It failed fourteen tests the first time it
  met a real server, and one of those was a safety bug: `Claim` relied on
  SQLite's single writer for atomicity, and Postgres let two executors take
  the same run — two executors draining the same node
- **Schema v14** — an insertion order the store declares itself, replacing a
  dependency on SQLite's `rowid` that six queries had grown, backfilled so no
  existing history reorders

There is no data path from an existing SQLite store (#181), and e2e still
runs against SQLite only (#180). Both are recorded rather than implied.

### The Postgres store, repaired (2026-08-12)
- **v0.6.0's Postgres backend could not record a drain.** A bool bound into an
  INTEGER column, a statement that skipped placeholder rewriting, and — the one
  no review caught — SQLite's 64-bit `INTEGER` translated unchanged to
  Postgres's 32-bit one, capping every memory column at 2 GiB. Schema v15
  widens them for anyone who installed in that window
- **The cause was a coverage claim that was not true.** Four test helpers
  opened SQLite directly instead of going through the shared one, so node
  samples, reclamations, recoveries and staleness never met Postgres. Pointing
  them at the shared helper failed nine tests immediately
- **Migration takes a cross-process lock** — planner and ui-backend both
  migrate at startup and on a fresh install both pods start together
- **`sslRootCert`** — `sslmode=require` encrypts but does not authenticate the
  server; the docs now say so, and there is a way to fix it
- **Every font size is back on the type scale**, and the scale is guarded by
  membership rather than by a floor

Two guards failed the same way on the same day: one checked three of the four
ways a query reaches the driver, the other checked the type scale's floor but
not the scale. A partial guard reads as coverage, which is worse than none.

### What running it found (2026-08-13)
- **The CLI could not find its own backend** unless the Helm release was named
  `k8s-dencer`. It rebuilt the Service name; the chart derives it from the
  fullname helper, and those agree for exactly one release name. It now finds
  the Service by label, which cannot drift from the chart the way a second copy
  of a naming template does
- **A live plan reported as twenty hours old.** A plan ID is a content hash, so
  a stable cluster produces the same plan every cycle and the first-computed
  time never moves. The CLI printed that and the UI printed the confirmation
  time, so the two surfaces contradicted each other about the same plan
- **`503 plan store unavailable`** replaces `internal error` when the database
  cannot answer — and deliberately *not* by failing readiness, which would take
  every replica out of the Service at once and turn a database blip into a
  total blackout
- **e2e runs against both plan stores**, including an assertion that a drain
  reaches the ledger. That is exactly what v0.6.0 got wrong: the drain
  succeeded, the executor's events looked perfect, and nothing was written

The pattern is the point. The reviews found the store bugs; the cluster found
everything else, including two flaws in the new test itself — it drained the
node its own database was on, then drained the pod its own port-forward was
talking through.

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

1. **Cost awareness, remaining slices** — pricing and a cost-aware planner
   objective. The facts shipped first: instance type and capacity type are on
   every node (Inspector), and the plan states how its reclaimable nodes are
   bought — "7 spot, 4 on-demand" — with unpriced nodes omitted rather than
   invented. Turning "fewer nodes" into "cheaper estate" as the *objective*
   is the remaining, deliberately separate, step.


## Explicitly not planned

Decisions, not omissions — recorded so they are not re-litigated by accident:

- **High availability** — a consolidation planner is not a serving path; the
  run queue is crash-safe, the planner replans on restart. Still true.
- **Postgres** — was recorded here alongside HA, and that was a mistake in
  reasoning rather than a decision that aged: SQLite's ReadWriteOnce claim
  pins the planner to one node, where a packed cluster can leave it `Pending`.
  Shipped as `database.type=postgres`; HA remains out
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
