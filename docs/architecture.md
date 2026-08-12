# Architecture

How k8s-dencer is put together, and why each piece is where it is. Start with [the design document](k8s-consolidation-agent-architecture.md) for the reasoning behind the whole shape; this page is the map of what exists today.

## Layout

```
cmd/               planner, ui-backend, executor — one dir per shipped image
internal/
  model/           domain types; NO k8s imports, so the planner is testable from a YAML snapshot
  cluster/         informer-backed collector, k8s->model conversion, MetricsSource
  constraints/     effective per-pod constraints + explanations; placement feasibility
  planner/         Strategy interface + greedy first-fit-decreasing packer
  impact/          Green/Yellow/Red classifier + rationale composition
  safety/          the non-negotiable rails; refuses Red, caps blast radius, re-checks PDBs
  executor/        the ONLY package that mutates a cluster: cordon, evict, verify, abort
  store/           Store interface + one SQLite/Postgres implementation and migrations
  auth/            TokenReview authn + SubjectAccessReview authz; no credential store of our own
  telemetry/       structured logger + the Prometheus series each component publishes
  api/             rest/ (+ SSE events), graph/ (Cytoscape payload), agenttools/ (MCP)
ui/                React + Vite; packing field in plain DOM, bundled typefaces
charts/k8s-dencer/ THE product deliverable — see Chart below
demo/              POC only: KWOK values + the synthetic topology chart
build/             Dockerfile.go (parameterised by COMPONENT) + Dockerfile.ui
hack/              lint-chart.sh — the portability gate
.github/workflows/ ci.yml (every gate, on every PR) + release.yml (images and chart)
test/fixtures/     ClusterSnapshots captured from a live cluster for golden tests
test/palette/      guards the CVD mitigation: glyph+word per rating, chroma only for risk
test/ui/           guards that every request carries the token, and the token never leaves the tab
```

Structural rules worth preserving:

- `internal/model` has **zero** Kubernetes imports. That is what lets the planner be tested against a fixture snapshot with no cluster.
- `demo/` is never referenced by the product chart. It installs as a separate release and can be torn down independently.
- The no-Kubernetes-imports rule on `internal/model` is enforced by a test, not a convention. If it breaks, snapshots stop being plain data and the planner can no longer be tested without a cluster.
- CRD types live in `api/`, outside `internal/`, so they remain importable by other tools. CRD YAML is generated into `config/crd/bases` and copied into the chart by make — never hand-maintained in two places.

---

---

## Constraints and explanations

`internal/constraints` derives the effective constraint set for every pod — PDB membership and live headroom, topology spread, node and pod affinity, taints and tolerations, resource requests — and answers `NodeDrainable(node)` for the planner.

**Every explanation string is produced once, here.** The UI's constraint inspector and the Kagent agent both surface these exact strings rather than deriving their own, so the two can never disagree about why a pod cannot move. Explanations name the object responsible and include live numbers, because *"a PDB blocks this"* is not an explanation:

```
PodDisruptionBudget dencer-demo/dencer-demo-payments currently allows 0
disruptions (3 healthy, 3 required). The API server will refuse to evict this pod.

PodDisruptionBudget dencer-demo/dencer-demo-catalog allows 2 more concurrent
disruption(s) (3 healthy, 1 required).
```

The same package holds `Placement`, the feasibility evaluator — taints, node selector, node affinity, resources, pod affinity/anti-affinity across topology domains, and hard topology spread. It lives here rather than in the packer so that the analyzer's explanations and the planner's decisions come from the same code; an explanation that disagrees with the plan is worse than none.

Only **hard** constraints affect feasibility. A `ScheduleAnyway` spread constraint or a preferred affinity is recorded and explained but never reported as a blocker — treating a preference as a blocker would make the planner refuse moves the scheduler allows.

---

## Planning

`internal/planner` turns a snapshot plus its constraint analysis into an ordered sequence of atomic steps, each one draining a single node. On the demo fabric:

```
plan: id=c64d1364c6c0 steps=11 nodesBefore=24 nodesAfter=13 reclaims=11
```

**Greedy first-fit-decreasing**, behind a `Strategy` interface so the OR-Tools comparison in doc §10 stays open. Three choices drive the result:

- **Drain candidates emptiest-first.** The least-loaded node is both cheapest to evacuate and most likely to succeed, so this frees the most nodes for a given amount of disruption.
- **Place the largest pods first.** Big pods are hardest to fit; placing them last strands them in space the easy pods have already fragmented.
- **Prefer the *fullest* destination that still fits.** This is what makes it consolidation. A plain first-fit over an arbitrary node order spreads load evenly and frees nothing.

A node is accepted only if **every** movable pod on it finds a home. A partial evacuation frees no node, so a step that cannot complete is never proposed.

The planner is deterministic by construction — the plan ID is a content hash of the steps, so re-planning an unchanged cluster yields the same ID and "has the plan changed?" is a string comparison. It also plans the *ideal* end state regardless of whether a step is safe to run right now; risk is scored separately (M5) and enforced separately (Phase 2), so the plan's shape never depends on the time of day.

Policy inputs are chart values today and become the `ConsolidationPolicy` CRD later: `planner.minNodeAge` (skip fresh nodes so consolidation doesn't fight the autoscaler), `planner.maxSteps`, `planner.excludeNamespaces`. Control-plane nodes are excluded by default.

Ten tests cover it, including the ones that matter most: every proposed move must still be feasible when the plan is *applied in order*, every step must fully empty its target, and no step may drain a node holding a pod the analyzer says cannot be evicted.

---

## Impact ratings

`internal/impact` rates every step and explains the rating. The rating is **policy, not advice**: doc §5 confines Red steps to an approved maintenance window, and Phase 2's safety guard will enforce that in code rather than trusting UI input validation. So a rating has to be defensible, which is why each one names the specific object and number behind it:

```
Draining kwok-node-19 moves 1 pod(s). Rated Red because: dencer-demo/dencer-demo-orphan
has no controller. Evicting it deletes it permanently; nothing will recreate it.
Red steps may only execute inside an approved maintenance window.
```

| Rating | Driven by |
|---|---|
| **Red** | unmanaged pod (eviction deletes it permanently), StatefulSet pod, PDB at zero headroom, blast radius ≥ `redPodsMoved` |
| **Yellow** | PDB headroom ≤ `tightPDBHeadroom`, PersistentVolumeClaim, required anti-affinity, hard topology spread, blast radius ≥ `yellowPodsMoved` |
| **Green** | none of the above |

The worst factor decides, and the rationale **leads with it** — that's the question an operator is actually asking — then lists supporting factors. Anti-affinity and spread rationales quote the analyzer's explanation verbatim rather than re-deriving it, so the two can't drift.

Only **hard** constraints count. A `ScheduleAnyway` spread constraint never affects a rating: flagging a preference the scheduler would happily violate is a false alarm, and false alarms are how a tool gets ignored.

Thresholds are chart values (`planner.impact.*`) because doc §10 is explicit that where PDB headroom stops being acceptable differs per cluster. Defaults are deliberately cautious — an operator who finds them noisy can loosen them; one surprised by an outage they were told was Green will not trust the tool again.

> Red steps only appear under scenario `f-stateful`. The planner already refuses to drain nodes holding unevictable pods, so a zero-headroom PDB never reaches a step — that scenario exists specifically so the Red path is exercised end to end.

---

## Plan store and API

Plans live in a SQL database — SQLite on the chart's PVC by default, or Postgres — not in a CRD. Doc §6 makes the case: a plan is refreshed continuously and read almost entirely by the UI, and pushing that write volume through etcd is a well-known way to hurt a cluster. Nothing external *desires* a specific plan, so there is nothing for Kubernetes' reconciliation model to do.

**One store speaks both dialects rather than two implementations.** They differ in four places — placeholder spelling, `BLOB`/`BYTEA`, `REAL`/`DOUBLE PRECISION`, and where the schema version is kept — and every query, migration and test is shared. The alternative is two things that must agree, kept apart, and discovered to disagree by a user. The same suite runs against both backends in CI, which is how a claim that let two Postgres executors take the same run was found before it reached anyone.

**Each stored plan carries the snapshot and constraint analysis it was computed from.** The UI needs to draw a graph and explain constraints for the plan it's displaying; pairing them guarantees the three agree. Fetching live state instead would show a graph that has already drifted from the plan drawn over it, and history could never be reviewed at all.

**Writes are deduplicated on the content hash.** A stable cluster re-plans to the same ID every cycle, and writing that row every 30 seconds would fill the volume and bury the moments the plan actually changed. `planner.retainPlans` prunes older versions — history is an audit trail, but the volume is a fixed size.

### Endpoints

```
GET /api/v1/authinfo                                 how to sign in — the only open route
GET /api/v1/version
GET /api/v1/plans                                    list, newest first
GET /api/v1/plans/{id|latest}                        full plan
GET /api/v1/plans/{id}/steps/{seq}                   step + the constraints of the pods it moves
GET /api/v1/plans/{id}/graph                         Cytoscape elements + stat tiles
GET /api/v1/plans/{id}/snapshot                      the cluster state it was planned against
GET /api/v1/plans/{id}/constraints[/{ns}/{pod}]      constraint analysis
GET /api/v1/events                                   live plan changes and run progress (SSE)
GET /api/v1/reclamations                             what became of drained nodes (observed)
GET /api/v1/runs                                     the in-flight run, if any
GET /api/v1/runs/{runId}                             one run plus its full audit trail
GET /api/v1/plans/{id}/runs                          runs against a plan

POST /api/v1/plans/{id}/execute                      queue steps for execution
```

Everything but `authinfo` requires `get plans.dencer.io` — see
[Authentication](security.md). `latest` is an alias, so the UI can deep-link
without knowing an ID. **There is no mutating route** — not even a disabled one,
because a "not implemented" execute endpoint is an invitation. A test asserts
none exists.

Live updates use **Server-Sent Events rather than WebSockets**: the traffic is strictly one-way since the API is read-only, and it needs no dependency and no protocol upgrade. The stream sends current state on connect, so a client joining a stable cluster isn't left blank.

The frontend consumes that stream with **`fetch` rather than `EventSource`**, which costs us the reconnect logic `EventSource` gives away. `EventSource` cannot send an `Authorization` header, and the alternative — putting the token in the query string — would write a working credential into every access log between the browser and the backend.

The graph payload is shaped for Cytoscape's compound-node model — cluster nodes are parents, pods are children — and every pod carries **both** its current node and where the plan would move it, so the frontend can build the before/after view and animate the step scrubber from a single request.

---

## The UI

```bash
make ui     # port-forwards and prints the URLs
```

**A header carries the context, quietly.** Which cluster (`uiBackend.clusterLabel`,
empty by default) and the identity the API server verified — plus sign-out and a
dark/light toggle. Dark is the default rather than following the OS: the ground
going black is what leaves a saturated rating as the only lit thing on the
surface, and that is the premise the whole palette rests on.

**Cordoned is drawn, because reality and prediction are different things.** A
node cordoned in the cluster right now gets dashed hatching and the word; a node
the *plan* would drain at the scrubber's position gets the reclaimed treatment.
Conflating them is how the field once showed nothing at all after a real drain
— see [M24](roadmap.md), which is about closing the rest of that gap.

**The capacity ribbon is the page's argument.** One bar per node, ordered fullest-first, filled to requested CPU — the long tail of barely-used bars *is* the case for consolidating. Dragging the scrubber drains them in place. A big number over a small label was the first draft; it states a conclusion where the evidence should be, so the only display-size figure now sits inside a sentence that gives it meaning.

**One morphing canvas, not two before/after panes.** Scrubbing shows the same comparison plus every intermediate state, and makes the causality visible — this pod leaves, so this node empties. Two half-width panes of 30 nodes would be unreadable, and the ribbon already does the at-a-glance job.

**Cytoscape's own layouts are not used.** Positions are computed: node boxes on a grid, pods in a sub-grid inside them. A force layout says nothing about packing and jitters on every relayout; deterministic positions are also what make the animation possible. Node boxes are plain nodes rather than compound parents, because a compound parent collapses to nothing when emptied — exactly the node an operator most wants to see.

### Accessibility is load-bearing here, not a checkbox

The data-viz palette validator reports **`#d03b3b` ↔ `#0ca30c` at CVD ΔE 4.1 for deuteranopia**, against a floor of 8. Red and Green are the same colour to a large minority of readers — for a Green/Yellow/Red product that is the central design constraint, not a footnote.

So nothing encodes a rating by colour alone: every chip carries a distinct glyph (●▲■ — different silhouettes, not just fills) **and** the word; scrubber ticks use the glyphs; blocked pods are diamonds. Node utilisation uses the sequential blue ramp — one hue, light→dark — validated against this page plane.

### Typefaces are bundled, not fetched

Archivo for structure and every figure, IBM Plex Sans for prose, IBM Plex Mono for machine identifiers (a node name is an identifier and shouldn't read as a word). Latin subsets only — the full packages ship Cyrillic, Greek and Vietnamese, 3.3MB of assets for a UI with no text in any of them.

They are bundled rather than CDN-fetched because **a cluster may have no route to the internet**, and type that silently falls back to `system-ui` in an air-gapped install is not designed type. It also means no third-party request from an operator's browser.

---

## Kagent

Kagent is a **prerequisite**, not installed by this repo. Chart resources are gated behind `kagent.enabled` (default `false`) so the product installs cleanly on clusters without the `kagent.dev` CRDs.

When enabled, the chart creates a `RemoteMCPServer` pointing at the ui-backend's `/mcp` endpoint and an `Agent` CR scoped to explanation only.

### The agent quotes; it never re-derives

Four read-only tools, served over MCP by the ui-backend — no separate image, because the agent itself runs inside Kagent:

| Tool | Answers |
|---|---|
| `list_plan_steps` | the plan as a whole — how many nodes it reclaims, how many steps are Red |
| `explain_step` | why step N is rated as it is, which pods move where |
| `get_node_constraints` | a node's occupancy, utilisation, and the constraints on its pods |
| `why_not_drained` | why a node is *not* being drained, naming the responsible constraint |

Every answer is composed from strings the constraint analyzer and impact classifier already produced. A test asserts `explain_step` returns the stored rationale **byte-for-byte** — if the tool paraphrased, the agent and the UI's inspector could describe the same step differently and an operator would have no way to know which to trust.

The surface is read-only by construction, and a test asserts that exactly these four tools exist — no fifth tool can appear without someone deliberately changing that assertion. Asked to drain a node, the agent declines:

> This release of k8s-dencer is read-only by design and does not have the capability to execute actions such as draining nodes.

Verified end to end: the agent calls the tool, receives the classifier's exact words, and answers with them.

---

---

## Snapshots and fixtures

The planner exposes the latest `ClusterSnapshot` as YAML on its health port, which is how golden-test fixtures are captured:

```bash
make scenario S=b-pdb-blocked
# wait one resync period for the planner to observe the change
make capture-fixture S=b-pdb-blocked   # -> test/fixtures/b-pdb-blocked.yaml
```

Fixtures come from a real cluster rather than being hand-written: hand-built inputs drift toward whatever the planner already does and stop catching the cases that matter. They include the real node and real workloads (kagent, kube-system, k8s-dencer itself) alongside the synthetic ones, because a planner that only ever sees tidy input will not cope with a real cluster.

Two conversion details are worth knowing, since both are easy to get subtly wrong and both are covered by tests:

- **Effective requests** follow Kubernetes' own rule — per resource, the greater of the summed app-container requests and the largest init container, plus pod overhead, with sidecars (init containers with `restartPolicy: Always`) added rather than maxed.
- **PDB headroom comes from `status.disruptionsAllowed`, not `spec.minAvailable`.** The spec says what was asked for; the status says what the API server will actually permit right now. That difference is what separates "a PDB exists" from "this drain will be refused".

---

[← Documentation index](README.md) · [Project README](../README.md)
