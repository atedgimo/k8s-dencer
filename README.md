# k8s-dencer

k8s-dencer continuously analyzes your Kubernetes cluster's constraints (PDBs, topology spread, affinity, taints) and builds a node consolidation plan — broken into discrete, impact-rated steps you can inspect and run on your own terms, on request or during a maintenance window. Nothing executes without explicit approval.

**Phase 1 is read-only by construction.** It plans and explains; it never cordons, drains or evicts. The ServiceAccount is not granted `pods/eviction`, and `make lint` fails the build if any values profile ever grants it.

Full design: [docs/k8s-consolidation-agent-architecture.md](docs/k8s-consolidation-agent-architecture.md)

---

## Status

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
| M7 | UI: before/after canvas, step timeline scrubber, constraint inspector | next |
| M8 | Kagent agent: read-only MCP tools + `Agent` CR | pending |

Deferred to later phases: Executor, Safety Guard, Scheduler Simulator, `MaintenanceWindow` CRD, Postgres store, multi-agent orchestration.

### What actually runs today

The planner watches the cluster through informers and publishes an immutable
`ClusterSnapshot` every resync period:

```
snapshot:    nodes=31 nodesOccupied=24 pods=117 pdbs=0 pdbsBlocking=0
             cpuRequestedPct=37.0% memRequestedPct=18.8% usageData=false
constraints: movable=116 blocked=1 stuck=26 antiAffinity=0 spreadBound=1
             controllerPinned=1 nodesUndrainable=1
plan:        id=8bb7900e46db steps=15 nodesBefore=28 nodesAfter=13 reclaims=15
             green=6 yellow=5 red=4
```

Plans are rated, explained and persisted. They are not yet visualised — the UI
is still a placeholder, and the graph payload it will consume is already served.

Three debug endpoints on the planner's health port expose current state as
YAML: `/debug/snapshot`, `/debug/constraints` and `/debug/plan`.

---

## Quick start

**Prerequisites:** a Kubernetes cluster, Docker, Helm 3.8+, Go 1.26+, Node 22+. Kagent is optional and, if wanted, must be installed separately (see [Kagent](#kagent) below).

```bash
make demo     # KWOK fabric + 30-node topology + build images + install the chart
make ui       # port-forward and print the URLs
make down     # remove all three releases
```

`make` with no target lists everything.

---

## Layout

```
api/v1alpha1/      CRD types (ConsolidationPolicy) — importable, not internal/
cmd/               planner, ui-backend — one dir per shipped image
internal/
  model/           domain types; NO k8s imports, so the planner is testable from a YAML snapshot
  cluster/         informer-backed collector, k8s->model conversion, MetricsSource
  constraints/     effective per-pod constraints + explanations; placement feasibility
  planner/         Strategy interface + greedy first-fit-decreasing packer
  impact/          Green/Yellow/Red classifier + rationale composition
  store/           Store interface + SQLite implementation and migrations
  api/             rest/ (+ SSE events), graph/ (Cytoscape payload), agenttools/ (M8)
ui/                React + Vite + Cytoscape.js
charts/k8s-dencer/ THE product deliverable — see Chart below
demo/              POC only: KWOK values + the synthetic topology chart
build/             Dockerfile.go (parameterised by COMPONENT) + Dockerfile.ui
hack/              lint-chart.sh — the portability gate
test/fixtures/     ClusterSnapshots captured from a live cluster for golden tests
```

Structural rules worth preserving:

- `internal/model` has **zero** Kubernetes imports. That is what lets the planner be tested against a fixture snapshot with no cluster.
- `api/` stays out of `internal/` so CRD types remain importable by other tools.
- `demo/` is never referenced by the product chart. It installs as a separate release and can be torn down independently.
- The no-Kubernetes-imports rule on `internal/model` is enforced by a test, not a convention. If it breaks, snapshots stop being plain data and the planner can no longer be tested without a cluster.
- CRD YAML is generated into `config/crd/bases` and copied into the chart by make — never hand-maintained in two places.

---

## The chart is the product

`charts/k8s-dencer` must install on any conformant cluster (EKS/GKE/AKS/on-prem). Local concerns live in a values overlay under `charts/k8s-dencer/ci/`, never in the defaults.

**Defaults are vendor-neutral:** `ClusterIP` + optional Ingress (never LoadBalancer), restricted-PSS security contexts, per-component `resources`/probes/`nodeSelector`/`tolerations`/`affinity`/`topologySpreadConstraints`, `serviceAccount.annotations` for EKS IRSA and GKE Workload Identity, configurable `persistence` with `storageClass: ""` inheriting the cluster default, and a `kubeVersion: ">=1.27.0-0"` floor.

**Forbidden in chart defaults:** LoadBalancer services, `hostPath`, `imagePullPolicy: Always`, hardcoded storage classes or node names, any reference to OrbStack or KWOK.

### Values profiles

| Profile | Purpose |
|---|---|
| `values.yaml` | vendor-neutral production defaults |
| `ci/minimal-values.yaml` | every optional feature off — must still be a valid restricted-PSS install |
| `ci/production-values.yaml` | cloud-shaped: Ingress + TLS, PVC, IRSA, PDB, NetworkPolicy, ServiceMonitor |
| `ci/orbstack-values.yaml` | the local POC overlay — the only place OrbStack assumptions are allowed |

### The portability gate

```bash
make lint
```

Runs `helm lint` and `kubeconform --strict` across every profile, then asserts the contract:

1. production renders Ingress, PVC, PDB, NetworkPolicy, ServiceMonitor and an IRSA-annotated ServiceAccount
2. production greps clean for `orbstack`, `kwok`, `hostPath`, `LoadBalancer`, `imagePullPolicy: Always`
3. minimal keeps `runAsNonRoot` / `readOnlyRootFilesystem` / `allowPrivilegeEscalation: false` with all optionals off
4. **no profile grants `pods/eviction`**
5. `database.type=sqlite` with `uiBackend.replicaCount=2` is rejected by `values.schema.json`
6. the packaged chart excludes the `ci/` fixtures

### Known constraints

- **SQLite is single-writer.** `uiBackend.replicaCount` is pinned to 1 and enforced by the schema; the planner is co-scheduled with ui-backend via a `requiredDuringScheduling` podAffinity, because a ReadWriteOnce claim only permits multiple pods on the same node. Both constraints disappear when the Postgres store lands.
- **PDBs use `maxUnavailable`, never `minAvailable`.** A `minAvailable` PDB on a single-replica Deployment makes its own pod undrainable — the exact pathology this product exists to detect.

---

## Test fabric (KWOK)

A node-consolidation planner cannot be tested on a single-node cluster. [KWOK](https://kwok.sigs.k8s.io/) provides fake nodes with no kubelet, so a laptop presents a 30-node topology while the **real** scheduler, PDB accounting and eviction API all apply.

```bash
make kwok-up                        # upstream kwok + stage-fast charts (pinned 0.3.0 / app v0.8.0)
make demo-up                        # 30 fake nodes across 3 zones + the base workload
make scenario S=b-pdb-blocked       # switch constraint scenario
make demo-down && make kwok-down
```

> KWOK's docs advertise OCI coordinates at `oci://registry.k8s.io/kwok/charts/*`, but that path currently publishes no tags. The Makefile uses the classic repo `https://kwok.sigs.k8s.io/charts/`.

### Scenarios

The base filler workload deploys in **every** scenario, so there is always something to consolidate; the scenario layers the constraint that should change how steps are rated.

| Scenario | Shape | Expected planner behaviour |
|---|---|---|
| `a-fragmented` | 90 pods at ~37% requested over 30 nodes | collapses toward ~12 nodes, all steps Green |
| `b-pdb-blocked` | `payments` minAvailable 3/3, `catalog` 1/3 | payments steps Red naming the PDB; catalog stays Green |
| `c-topology-spread` | 6 replicas, `maxSkew: 1` over 3 zones | moves preserve 2-per-zone |
| `d-anti-affinity` | 5 replicas, required anti-affinity on hostname | each pins a node open; rationale names the rule |
| `e-tainted-pool` | 3 nodes tainted `dedicated=batch` | pool neither packed into nor drained out of |
| `f-stateful` | StatefulSet + a bare unmanaged pod | the only scenario producing **Red** steps |

Verified against the live cluster: evicting a `payments` pod is refused with `TooManyRequests`, while `catalog` succeeds — real PDB enforcement on fake nodes.

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

## Plan store and API

Plans live in SQLite on the chart's PVC, not in a CRD. Doc §6 makes the case: a plan is refreshed continuously and read almost entirely by the UI, and pushing that write volume through etcd is a well-known way to hurt a cluster. Nothing external *desires* a specific plan, so there is nothing for Kubernetes' reconciliation model to do.

**Each stored plan carries the snapshot and constraint analysis it was computed from.** The UI needs to draw a graph and explain constraints for the plan it's displaying; pairing them guarantees the three agree. Fetching live state instead would show a graph that has already drifted from the plan drawn over it, and history could never be reviewed at all.

**Writes are deduplicated on the content hash.** A stable cluster re-plans to the same ID every cycle, and writing that row every 30 seconds would fill the volume and bury the moments the plan actually changed. `planner.retainPlans` prunes older versions — history is an audit trail, but the volume is a fixed size.

### Endpoints

```
GET /api/v1/version
GET /api/v1/plans                                    list, newest first
GET /api/v1/plans/{id|latest}                        full plan
GET /api/v1/plans/{id}/steps/{seq}                   step + the constraints of the pods it moves
GET /api/v1/plans/{id}/graph                         Cytoscape elements + stat tiles
GET /api/v1/plans/{id}/snapshot                      the cluster state it was planned against
GET /api/v1/plans/{id}/constraints[/{ns}/{pod}]      constraint analysis
GET /api/v1/events                                   live plan changes (SSE)
```

`latest` is an alias, so the UI can deep-link without knowing an ID. **There is no mutating route** — not even a disabled one, because a "not implemented" execute endpoint is an invitation. A test asserts none exists.

Live updates use **Server-Sent Events rather than WebSockets**: the traffic is strictly one-way since the API is read-only, `EventSource` reconnects on its own, and it needs no dependency and no protocol upgrade. The stream sends current state on connect, so a client joining a stable cluster isn't left blank.

The graph payload is shaped for Cytoscape's compound-node model — cluster nodes are parents, pods are children — and every pod carries **both** its current node and where the plan would move it, so the frontend can build the before/after view and animate the step scrubber from a single request.

## Kagent

Kagent is a **prerequisite**, not installed by this repo. Chart resources are gated behind `kagent.enabled` (default `false`) so the product installs cleanly on clusters without the `kagent.dev` CRDs.

When enabled, the chart creates a `RemoteMCPServer` pointing at the ui-backend's `/mcp` endpoint and an `Agent` CR scoped to explanation only. The MCP tools themselves land in M8, so until then the `RemoteMCPServer` will show `ACCEPTED: False` — expected, and a useful signal for when M8 is actually complete.

---

## Development

```bash
make build          # compile Go
make test           # go vet + go test + UI typecheck
make images         # native-arch images for the local cluster
make images-release # multi-arch linux/amd64,linux/arm64
make deploy         # helm upgrade --install with the provider overlay
make status         # pods, services, PVCs
make logs           # tail the planner
```

**Images carry no registry by default locally.** OrbStack's Kubernetes reads the local Docker image store, so `docker build` + `helm upgrade` is the whole loop — no registry, no push. `IMAGE_TAG` is the git short SHA plus a `-dirty` suffix, which changes the podspec on every build so pods roll without a manual restart.

> `docker buildx` cannot `--load` a multi-platform build into the local image store. `make images` builds native-only for the dev loop; `make images-release` does the multi-arch build.

### Targeting a different cluster

`CLUSTER_PROVIDER` (`orbstack` | `k3d` | `kind` | `minikube`) selects both the `images-load` implementation and the values overlay. Only `orbstack` is exercised today; the others exist so a migration is a variable change, not a refactor. Nothing outside the Makefile and `charts/k8s-dencer/ci/` may assume a provider.

```bash
make demo CLUSTER_PROVIDER=k3d
```
