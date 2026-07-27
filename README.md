# k8s-dencer

k8s-dencer continuously analyzes your Kubernetes cluster's constraints (PDBs, topology spread, affinity, taints) and builds a node consolidation plan — broken into discrete, impact-rated steps you can inspect and run on your own terms, on request or during a maintenance window. Nothing executes without explicit approval.

**Execution is opt-in, gated, and off by default.** Set `executor.enabled=true` and a separate workload — its own image, its own ServiceAccount, no inbound network surface — becomes able to cordon and evict. Everything else stays read-only: `make lint` fails the build if `pods/eviction` appears on any role but the executor's, or in any profile that did not ask for one.

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
| **M11** | UI rebuild: packing view, motion, action-first header | planned |
| **M12** | Execution controls, live run, OIDC sign-in flow, plan pinning | planned |
| **M13** | End-to-end on KWOK incl. real SSO against Dex | planned |

Deferred beyond Phase 2: `MaintenanceWindow` CRD and scheduled execution,
Postgres store, multi-agent orchestration.

### What actually runs today

The planner watches the cluster through informers and publishes an immutable
`ClusterSnapshot` every resync period:

```
snapshot:    nodes=31 nodesOccupied=29 pods=120 pdbs=0 pdbsBlocking=0
             cpuRequestedPct=38.6% memRequestedPct=19.6% usageData=false
constraints: movable=119 blocked=1 stuck=25 antiAffinity=0 spreadBound=1
             controllerPinned=1 nodesUndrainable=1
plan:        id=dcdd608cf7c9 steps=16 nodesBefore=29 nodesAfter=13 reclaims=16
             green=7 yellow=4 red=5
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

## Quick start

**Prerequisites:** a Kubernetes cluster, Docker, Helm 3.8+, Go 1.26+, Node 22+. Kagent is optional and, if wanted, must be installed separately (see [Kagent](#kagent) below).

```bash
make demo          # KWOK fabric + 30-node topology + build images + install the chart
make token         # mint an operator token — the UI asks for one
make ui            # port-forward and print the URLs
make fabric-reset  # uncordon every KWOK node after an executor run
make down          # remove all three releases
```

`make` with no target lists everything.

---

## Layout

```
api/v1alpha1/      CRD types (ConsolidationPolicy) — importable, not internal/
cmd/               planner, ui-backend, executor — one dir per shipped image
internal/
  model/           domain types; NO k8s imports, so the planner is testable from a YAML snapshot
  cluster/         informer-backed collector, k8s->model conversion, MetricsSource
  constraints/     effective per-pod constraints + explanations; placement feasibility
  planner/         Strategy interface + greedy first-fit-decreasing packer
  impact/          Green/Yellow/Red classifier + rationale composition
  safety/          the non-negotiable rails; refuses Red, caps blast radius, re-checks PDBs
  executor/        the ONLY package that mutates a cluster: cordon, evict, verify, abort
  store/           Store interface + SQLite implementation and migrations
  auth/            TokenReview authn + SubjectAccessReview authz; no credential store of our own
  api/             rest/ (+ SSE events), graph/ (Cytoscape payload), agenttools/ (MCP)
ui/                React + Vite + Cytoscape.js; bundled typefaces
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
7. **`pods/eviction` appears only on the executor's ClusterRole**, and only in a profile that enabled it
8. planner and ui-backend hold **no write verb on nodes or pods**, ever
9. the executor renders **no Service** — the component that can evict is unreachable
10. `executor.enabled=true` is rejected without `auth.enabled` and without `persistence.enabled`
11. no container declares a **duplicate env key** (valid YAML, rejected by server-side apply)
12. **no profile disables authentication**
13. `system:auth-delegator` is bound to ui-backend and to nothing else
14. the consolidation-operator ClusterRole ships **unbound**
15. a NetworkPolicy is present by default, and restricts ingress only
16. `auth.oidc.enabled=true` without an issuer and client ID is rejected

### Known constraints

- **SQLite is single-writer.** `uiBackend.replicaCount` is pinned to 1 and enforced by the schema; the planner is co-scheduled with ui-backend via a `requiredDuringScheduling` podAffinity, because a ReadWriteOnce claim only permits multiple pods on the same node. Both constraints disappear when the Postgres store lands.
- **PDBs use `maxUnavailable`, never `minAvailable`.** A `minAvailable` PDB on a single-replica Deployment makes its own pod undrainable — the exact pathology this product exists to detect.

---

## Authentication

k8s-dencer owns **no credential store**. A caller presents a token,
[ui-backend](internal/auth/authenticator.go) validates it with a `TokenReview`,
and permission is decided by a
[`SubjectAccessReview`](internal/auth/authorizer.go). So "who may read the plan"
and "who may drain a node" are ordinary RoleBindings that compose with whatever
your cluster already uses, audit flows through the API server, and there is no
user database to breach.

On by default (`auth.enabled`). Two unbound ClusterRoles ship with the chart —
bind whichever you need:

| ClusterRole | Grants |
|---|---|
| `<release>-viewer` | `get plans.dencer.io` |
| `<release>-consolidation-operator` | the above, plus `create consolidations.dencer.io` |

Neither resource is a CRD, and neither ever will be: RBAC and
SubjectAccessReview match on strings, so a permission can be granted against a
resource name that has no API behind it. metrics-server relies on the same
property.

### Three ways to authenticate

All three converge on the same `SubjectAccessReview` — authentication is
pluggable, authorization is one code path.

**1. OIDC single sign-on** — the recommended deployment. When your API server
runs with `--oidc-issuer-url`, an ID token from that issuer **is already a
Kubernetes credential**, so `TokenReview` validates it for us and returns the
user's IdP groups. SSO therefore costs a redirect flow in the browser and
nothing on the server: no session store, no user database, no client secret.

```yaml
auth:
  oidc:
    enabled: true
    issuerUrl: https://login.example.com/realms/platform   # the SAME issuer the API server trusts
    clientId: k8s-dencer                                   # public client, Authorization Code + PKCE
```

```bash
kubectl create rolebinding dencer-operators -n k8s-dencer \
  --clusterrole=k8s-dencer-consolidation-operator --group=oidc:platform-sre
```

Unavailable on clusters whose API server cannot validate a third-party token —
EKS with IAM auth, GKE with Google auth. Use an auth proxy there.

**2. Auth proxy** — oauth2-proxy, Ingress auth or a service mesh terminates SSO
and asserts the identity in a header. We skip `TokenReview` and go straight to
`SubjectAccessReview`, which accepts `spec.user` and `spec.groups` directly, so
we can ask "may *this* person do this" without ever holding their token.

Off by default, and **while it is off the headers are ignored, not merely
unused** — a request carrying `X-Forwarded-User` is anonymous, not an
administrator. Turning it on means anything that can reach ui-backend can claim
to be anyone, so `networkPolicy.enabled` is what makes the proxy unbypassable.

**3. Bearer token** — for the POC and CI.

```bash
make token     # mints one and prints it; paste into the UI
```

### Notes worth knowing

- **Token expiry cannot break a run.** From M10, authorization happens once at
  enqueue and the executor then works under its own ServiceAccount, so a
  15-minute ID token can authorize a 40-minute consolidation. The authenticated
  username and groups are recorded on the run and on every audit event.
- **A 403 tells you how to fix it** — it names the exact verb, group and
  resource, and quotes the `kubectl create rolebinding` that grants them.
- **Authentication is cached for 30s; authorization never is.** A revoked
  RoleBinding must stop working immediately.
- **`/api/v1/authinfo` is served openly.** A client cannot sign in without
  knowing where to sign in, and the answer is an issuer URL and a public client
  ID. [A test](internal/api/rest/guarded_test.go) fails if any *other* route is
  registered without a guard — which is what will stop an execute endpoint
  shipping unauthenticated in M10.
- **Do not assume your NetworkPolicy is enforced.** A CNI that does not
  implement NetworkPolicy accepts the object and silently ignores it — there is
  no warning and no event. k3s's default CNI is one of these, so on the OrbStack
  POC cluster the policy provides *zero* protection: a pod in `default` reaches
  ui-backend with a 200. Verify on your own cluster before relying on it, and
  note that `auth.trustedProxy` is only safe where it *is* enforced.

  ```bash
  kubectl run probe --rm -i --restart=Never -n default --image=curlimages/curl:8.11.1 \
    --command -- curl -s -o /dev/null -w '%{http_code}\n' \
    http://<release>-ui-backend.<ns>.svc:8080/api/v1/authinfo   # 000 = enforced, 200 = not
  ```

- **The MCP surface can be token-guarded, and on an unenforced CNI it should
  be.** Verified against Kagent 0.9.12: `RemoteMCPServer.headersFrom` presents a
  bearer token and all four tools are discovered through it. Off by default only
  because the secret is yours to create and rotate — the chart will not mint a
  non-expiring ServiceAccount token on your behalf.

  ```bash
  kubectl create serviceaccount dencer-agent -n k8s-dencer
  kubectl create rolebinding dencer-agent -n k8s-dencer \
    --clusterrole=k8s-dencer-viewer --serviceaccount=k8s-dencer:dencer-agent
  kubectl create secret generic dencer-mcp-token -n kagent \
    --from-literal=authorization="Bearer $(kubectl create token dencer-agent -n k8s-dencer --duration=720h)"
  helm upgrade ... --set auth.mcpRequireToken=true \
    --set kagent.authSecret.name=dencer-mcp-token
  ```

  The value must be the whole header — Kagent substitutes it verbatim. The
  surface is read-only either way, and a test fails if a fifth tool appears.
- **The UI signs in by pasting a token.** The OIDC redirect flow lands in M12,
  because M11 rebuilds the header and building the login twice would be waste.

---

## Execution

Off by default. `executor.enabled=true` adds one workload and one route.

```
browser ──POST /api/v1/plans/{id}/execute──► ui-backend
                                              │ SubjectAccessReview
                                              │ create consolidations.dencer.io
                                              ▼
                                        runs table  ◄── shared SQLite volume
                                              │
                                              │ atomic claim
                                              ▼
                                          executor ──► cordon / evict
```

ui-backend authorizes and writes a row; the executor claims it. That split is
the design:

- **The component that answers HTTP cannot evict.** ui-backend's ServiceAccount
  has no write verb on nodes or pods, and never will — a lint assertion holds it
  there.
- **The component that can evict answers nothing.** The executor has no Service
  and no API. Its only inbound path is a row appearing in a table.
- **Authorization happens once, at enqueue.** The executor then works under its
  own identity, so a 15-minute ID token can authorize a 40-minute
  consolidation. The requester's username and groups are recorded on the run.

### What the executor does per step

```
guard.CheckStep      against state read seconds ago, not the plan's snapshot
cordon               merge patch of spec.unschedulable — no `update` verb needed
for each pod:
    guard.CheckEviction   live PDB headroom, re-read before EVERY eviction
    evict                 policy/v1 eviction API, so the API server enforces PDBs
    wait                  until the pod is actually gone
verify               affected workloads regained their replicas elsewhere
```

Eviction goes through the **eviction subresource**, never a pod delete. A delete
would bypass PodDisruptionBudgets entirely; that single choice is what makes
every PDB guarantee in this product real. The executor's RBAC grants
`create pods/eviction` and deliberately does **not** grant `delete pods`.

### The Safety Guard

[`internal/safety`](internal/safety/safety.go) — architecture doc §9, enforced in
code rather than in API validation, so no crafted request reaches around it.
Each rail is named in the audit event that blocks on it.

| Rule | Refuses |
|---|---|
| `RedRequiresWindow` | any Red step — maintenance windows are Phase 3, so there is no window to be inside |
| `MaxNodesPerRun` | draining more than `safety.maxNodesPerRun` in one request |
| `MinReadyNodes` | dropping below `safety.minReadyNodes` schedulable nodes |
| `StepFreshness` | a step whose pods no longer have anywhere to go |
| `PDBHeadroom` | an eviction the budget cannot absorb, re-checked per pod |
| `NodeNotFound` | a target node that has vanished |

**Red is not configurable.** There is no value that permits it. Exposing one
would turn a safety rail into a footgun.

The guard predicts; the executor then **verifies reality**. This is the
deliberate alternative to importing kube-scheduler's framework (doc §7): after
each step, affected workloads must have regained their replicas elsewhere or the
run stops. A prediction that turns out wrong aborts rather than compounding.

### Abort means uncordon, not rollback

**Evicted pods are not restored.** Eviction cannot be undone, and calling the
abort path "rollback" would be a lie in the docs and a surprise at 3am. On
failure or timeout the executor uncordons the node and stops — using a context
detached from the step deadline, since the usual reason to abort is that the
deadline expired. If the uncordon itself fails, the audit event says so and
names the `kubectl uncordon` needed.

A run ends in one of three terminal states, and `Blocked` is deliberately
distinct from `Failed`: "the rails protected you" and "something broke" call for
different responses.

### Confirming the split yourself

```bash
# NOTE --subresource. `can-i create pods/eviction` answers "no" for everyone,
# including accounts that hold the permission — a check that cannot fail.
for sa in planner ui-backend executor; do
  printf '%-12s ' "$sa"
  kubectl auth can-i create pods --subresource=eviction -n k8s-dencer \
    --as=system:serviceaccount:k8s-dencer:k8s-dencer-$sa
done
# planner no / ui-backend no / executor yes

kubectl auth can-i delete pods -n k8s-dencer \
  --as=system:serviceaccount:k8s-dencer:k8s-dencer-executor
# no — a delete would bypass PodDisruptionBudgets
```

### Audit trail

Every action lands in `run_events` with the plan, the step, the node, the pod,
the action, the rule that refused, and the actor. A plan with a run against it
is **never pruned**, however old — doc §9 ties the audit log to the plan version,
and pruning it would leave the log pointing at nothing.

```bash
curl -H "Authorization: Bearer $TOKEN" localhost:8090/api/v1/runs/<runId> | jq .events
```

---

## Test fabric (KWOK)

A node-consolidation planner cannot be tested on a single-node cluster. [KWOK](https://kwok.sigs.k8s.io/) provides fake nodes with no kubelet, so a laptop presents a 30-node topology while the **real** scheduler, PDB accounting and eviction API all apply.

```bash
make kwok-up                        # upstream kwok + stage-fast charts (pinned 0.3.0 / app v0.8.0)
make demo-up                        # 30 fake nodes across 3 zones + the base workload
make scenario S=b-pdb-blocked       # switch constraint scenario
make demo-down && make kwok-down
```

**After an executor run, reset the fabric before switching scenarios.**
Cordoning a node makes the node controller add
`node.kubernetes.io/unschedulable` to `.spec.taints`, owned by the cluster's own
field manager — and the demo chart manages `.spec.taints` too, so its
server-side apply then fails with a field-manager conflict. `make scenario`
calls `make fabric-reset` first for exactly this reason.

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
GET /api/v1/authinfo                                 how to sign in — the only open route
GET /api/v1/version
GET /api/v1/plans                                    list, newest first
GET /api/v1/plans/{id|latest}                        full plan
GET /api/v1/plans/{id}/steps/{seq}                   step + the constraints of the pods it moves
GET /api/v1/plans/{id}/graph                         Cytoscape elements + stat tiles
GET /api/v1/plans/{id}/snapshot                      the cluster state it was planned against
GET /api/v1/plans/{id}/constraints[/{ns}/{pod}]      constraint analysis
GET /api/v1/events                                   live plan changes and run progress (SSE)
GET /api/v1/runs                                     the in-flight run, if any
GET /api/v1/runs/{runId}                             one run plus its full audit trail
GET /api/v1/plans/{id}/runs                          runs against a plan

POST /api/v1/plans/{id}/execute                      queue steps for execution
```

Everything but `authinfo` requires `get plans.dencer.io` — see
[Authentication](#authentication). `latest` is an alias, so the UI can deep-link
without knowing an ID. **There is no mutating route** — not even a disabled one,
because a "not implemented" execute endpoint is an invitation. A test asserts
none exists.

Live updates use **Server-Sent Events rather than WebSockets**: the traffic is strictly one-way since the API is read-only, and it needs no dependency and no protocol upgrade. The stream sends current state on connect, so a client joining a stable cluster isn't left blank.

The frontend consumes that stream with **`fetch` rather than `EventSource`**, which costs us the reconnect logic `EventSource` gives away. `EventSource` cannot send an `Authorization` header, and the alternative — putting the token in the query string — would write a working credential into every access log between the browser and the backend.

The graph payload is shaped for Cytoscape's compound-node model — cluster nodes are parents, pods are children — and every pod carries **both** its current node and where the plan would move it, so the frontend can build the before/after view and animate the step scrubber from a single request.

## The UI

```bash
make ui     # port-forwards and prints the URLs
```

**The capacity ribbon is the page's argument.** One bar per node, ordered fullest-first, filled to requested CPU — the long tail of barely-used bars *is* the case for consolidating. Dragging the scrubber drains them in place. A big number over a small label was the first draft; it states a conclusion where the evidence should be, so the only display-size figure now sits inside a sentence that gives it meaning.

**One morphing canvas, not two before/after panes.** Scrubbing shows the same comparison plus every intermediate state, and makes the causality visible — this pod leaves, so this node empties. Two half-width panes of 30 nodes would be unreadable, and the ribbon already does the at-a-glance job.

**Cytoscape's own layouts are not used.** Positions are computed: node boxes on a grid, pods in a sub-grid inside them. A force layout says nothing about packing and jitters on every relayout; deterministic positions are also what make the animation possible. Node boxes are plain nodes rather than compound parents, because a compound parent collapses to nothing when emptied — exactly the node an operator most wants to see.

### Accessibility is load-bearing here, not a checkbox

The data-viz palette validator reports **`#d03b3b` ↔ `#0ca30c` at CVD ΔE 4.1 for deuteranopia**, against a floor of 8. Red and Green are the same colour to a large minority of readers — for a Green/Yellow/Red product that is the central design constraint, not a footnote.

So nothing encodes a rating by colour alone: every chip carries a distinct glyph (●▲■ — different silhouettes, not just fills) **and** the word; scrubber ticks use the glyphs; blocked pods are diamonds. Node utilisation uses the sequential blue ramp — one hue, light→dark — validated against this page plane.

### Typefaces are bundled, not fetched

Archivo for structure and every figure, IBM Plex Sans for prose, IBM Plex Mono for machine identifiers (a node name is an identifier and shouldn't read as a word). Latin subsets only — the full packages ship Cyrillic, Greek and Vietnamese, 3.3MB of assets for a UI with no text in any of them.

They are bundled rather than CDN-fetched because **a cluster may have no route to the internet**, and type that silently falls back to `system-ui` in an air-gapped install is not designed type. It also means no third-party request from an operator's browser.

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
