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
| M2 | Domain model + cluster state collector (informers over nodes/pods/PDBs) | next |
| M3 | Constraint analyzer (PDB headroom, topology, affinity, taints) | pending |
| M4 | Consolidation planner (greedy first-fit-decreasing) | pending |
| M5 | Impact classifier (Green/Yellow/Red + rationale) | pending |
| M6 | Plan store (SQLite) + REST/WS API + graph payload | pending |
| M7 | UI: before/after canvas, step timeline scrubber, constraint inspector | pending |
| M8 | Kagent agent: read-only MCP tools + `Agent` CR | pending |

Deferred to later phases: Executor, Safety Guard, Scheduler Simulator, `MaintenanceWindow` CRD, Postgres store, multi-agent orchestration.

### What actually runs today

The three components deploy and serve, but the planner is a heartbeat and the UI is a placeholder that verifies backend connectivity — the real planning and visualisation land in M4 and M7.

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
  cluster/         informer-backed collector + MetricsSource interface
  constraints/     PDB / topology / affinity / taint analysis
  planner/         Strategy interface + greedy bin-packing
  impact/          Green/Yellow/Red classifier + rationale
  store/           Store interface, sqlite/, migrations/
  api/             rest/ ws/ graph/ agenttools/
ui/                React + Vite + Cytoscape.js
charts/k8s-dencer/ THE product deliverable — see Chart below
demo/              POC only: KWOK values + the synthetic topology chart
build/             Dockerfile.go (parameterised by COMPONENT) + Dockerfile.ui
hack/              lint-chart.sh — the portability gate
```

Structural rules worth preserving:

- `internal/model` has **zero** Kubernetes imports. That is what lets the planner be tested against a fixture snapshot with no cluster.
- `api/` stays out of `internal/` so CRD types remain importable by other tools.
- `demo/` is never referenced by the product chart. It installs as a separate release and can be torn down independently.
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

Verified against the live cluster: evicting a `payments` pod is refused with `TooManyRequests`, while `catalog` succeeds — real PDB enforcement on fake nodes.

---

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
