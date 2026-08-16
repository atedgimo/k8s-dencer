# Installation and configuration

The Helm chart is the product. This covers installing it, the values profiles, and the constraints you inherit with each choice.

## Quick start

**Prerequisites:** a Kubernetes cluster, Docker, Helm 3.8+, Go 1.26+, Node 22+. Kagent is optional and, if wanted, must be installed separately (see [Kagent](architecture.md#kagent) below).

```bash
make demo          # KWOK fabric + 30-node topology + build images + install the chart
make token         # mint an operator token — the UI asks for one
make ui            # port-forward and print the URLs
make fabric-reset  # uncordon every KWOK node after an executor run
make down          # remove all three releases
```

`make` with no target lists everything.

---

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
7. an empty plan serialises `steps` as `[]`, never `null`
8. **`pods/eviction` appears only on the executor's ClusterRole**, and only in a profile that enabled it
9. planner and ui-backend hold **no write verb on nodes or pods**, ever
10. the executor renders **no Service** — the component that can evict is unreachable
11. `executor.enabled=true` is rejected without `auth.enabled` and without `persistence.enabled`
12. no container declares a **duplicate env key** (valid YAML, rejected by server-side apply)
13. **no profile disables authentication**
14. `system:auth-delegator` is bound to ui-backend and to nothing else
15. the consolidation-operator ClusterRole ships **unbound**
16. a NetworkPolicy is present by default, and restricts ingress only
17. `auth.oidc.enabled=true` without an issuer and client ID is rejected
18. the monitors scrape **the path the code actually serves** — read out of `telemetry.MetricsPath`, not repeated in the chart
19. every component the chart scrapes **registers the metrics handler** in its `main.go`
20. scraping **does not give the executor a Service** — planner and executor stay reachable only as pods
21. enabling `serviceMonitor` makes the NetworkPolicy **admit the monitoring namespace**, or the scrape is silently dropped

### Naming the cluster in the UI

```yaml
uiBackend:
  clusterLabel: prod-eu-west-1
```

Shown in the UI header so an operator can see which cluster they are about to
act on. **Empty by default, and the header then shows nothing.** Nothing in a
cluster reliably names itself, and a guessed environment label is worse than
none — the entire value of this field is being believed when it says `prod`.

Beside it the header shows the identity the API server verified through
`TokenReview`, not a claim decoded from whatever token the page is holding.

## Exposing the UI

Two ways, matching what your cluster standardises on — enable either, or both
during a migration. Each routes **all** traffic to the frontend, which
reverse-proxies `/api` to the backend, keeping cookies, CORS and WebSocket
upgrades same-origin.

**Ingress** (the classic):

```bash
--set ingress.enabled=true --set ingress.className=nginx --set ingress.hosts[0].host=dencer.example.com
```

**Gateway API HTTPRoute** (`gateway.networking.k8s.io/v1`; needs the Gateway
API CRDs installed):

```bash
--set httpRoute.enabled=true --set httpRoute.parentRefs[0].name=my-gateway --set httpRoute.parentRefs[0].namespace=infra --set httpRoute.hostnames[0]=dencer.example.com
```

The chart deliberately does **not** create a Gateway — that is cluster
infrastructure with its own owner, address and TLS story. `parentRefs` names
the Gateway this route attaches to, and the schema refuses an enabled route
with no parent, so it cannot dangle.

## Installing without the web UI

`--set uiFrontend.enabled=false` keeps planner + API and drops the frontend —
the install for teams using only the [`dencer` CLI](cli.md#cli-only-install-no-web-ui).

## CRDs

The chart ships **exactly one** CRD: `maintenancewindows.dencer.io` — the
object that says *when* execution is allowed (see
[execution.md](execution.md)). Helm installs it automatically on first
install; nothing else to do.

Two things users reasonably look for and should not:

- **Helm never touches CRDs on upgrade.** That is Helm's design, kept
  deliberately: a bad CRD change can orphan existing objects. If a chart
  upgrade changes the CRD, apply it yourself:

  ```bash
  kubectl apply -f https://raw.githubusercontent.com/atedgimo/k8s-dencer/main/charts/k8s-dencer/crds/dencer.io_maintenancewindows.yaml
  ```

  (From a repo checkout, `make crd-upgrade` does the same.)

- **`dencer.io/plans` and `dencer.io/consolidations` are not CRDs and never
  will be.** They appear in the chart's RBAC because the API authorizes with
  SubjectAccessReview, and SAR permissions do not require the resource to
  exist as an API type — they are named permissions, nothing more. Plans
  live in the product's own store, not in etcd (the reasoning is in
  [architecture.md](architecture.md)), so `kubectl get plans.dencer.io`
  will never work: the UI, the `dencer` CLI, or `GET /api/v1/plans` is how
  plans are read.

The demo fabric additionally needs KWOK's `Stage` CRDs, but only for the
demo — `make demo` installs them; a product install does not.

## Showing savings in money

The ledger measures capacity — cores and bytes actually returned, summed from
what each node was worth at drain time. To see that in currency, tell the
chart what your machines cost:

```yaml
uiBackend:
  pricing:
    currency: USD
    perHour:
      e2-medium: 0.0335
      e2-medium/spot: 0.0100
      n2-standard-4: 0.1942
```

Keys match most specific first, so spot and on-demand of the same shape can
differ. There is no built-in price table and there will not be one: list
prices vary by region, change without notice, and are wrong for anyone with a
committed-use discount, so a shipped default would be a guess wearing the
clothes of a measurement. Machine types you have not priced are reported as
unpriced rather than as free, and the capacity figure is shown either way.

The ledger also counts nodes that left the cluster without k8s-dencer draining
them — an autoscaler, or someone with kubectl — and reports them separately.
The cluster saved that money whoever caused it, and the ledger's job is to be
accurate about what happened rather than flattering about who did it.

## Postgres instead of SQLite

SQLite is the default and needs nothing to exist beforehand. Choose Postgres
when the single-node constraints below are the ones costing you something —
principally a planner that cannot be scheduled because the one node holding
its claim is full.

```bash
kubectl create secret generic dencer-db \
  --namespace k8s-dencer --from-literal=password='…'

helm install k8s-dencer oci://ghcr.io/atedgimo/charts/k8s-dencer \
  --namespace k8s-dencer --create-namespace \
  --set database.type=postgres \
  --set database.postgres.host=postgres.databases.svc \
  --set database.postgres.database=dencer \
  --set database.postgres.user=dencer \
  --set database.postgres.existingSecret=dencer-db
```

The chart creates the **schema**, not the server: the database and the role
must already exist, and the role needs `CREATE` on the target schema for the
first migration. The password is only ever read from a Secret; the chart takes
no password as a value, and none appears in the rendered manifest.

**`sslMode` defaults to `require`, which encrypts but does not authenticate.**
Worth being precise, because the name suggests more than it does: with no root
certificate the driver accepts *any* certificate the server presents, so the
channel is private but you have no assurance about who is on the other end of
it. That matters here more than for a typical application — this store holds
the audit trail and the run queue the executor drains nodes from.

To verify the server as well as encrypt the channel, supply a CA and ask for
it:

```bash
kubectl create secret generic dencer-db-ca \
  --namespace k8s-dencer --from-file=ca.crt=/path/to/ca.crt

helm upgrade k8s-dencer … \
  --set database.postgres.sslMode=verify-full \
  --set database.postgres.sslRootCert.existingSecret=dencer-db-ca
```

A managed cloud database presenting a publicly-trusted certificate needs no CA
— `verify-full` alone will use the system roots. The CA is for an in-cluster
Postgres with a private one. The install notes tell you which of these you are
getting.

Selecting Postgres removes the PersistentVolumeClaim, the `/data` mount, the
co-scheduling affinity and the PriorityClass, because all four exist to keep
two writers off one file. `uiBackend.replicaCount` is then yours to raise.

**There is no migration path from an existing SQLite store.** Plan history and
the reclamation ledger stay in the file they were written to. Nothing is lost
by switching — the planner rebuilds its plan on the next resync, and the
savings ledger starts recording from the switch — but the history before the
switch does not come with it.

### Going back to SQLite

`helm upgrade` in that direction fails, once, on the Deployment strategy:

```
spec.strategy.rollingUpdate: Forbidden: may not be specified when strategy `type` is 'Recreate'
```

Postgres lets the ui-backend roll; SQLite cannot, because a rolling update
would briefly run two writers against one ReadWriteOnce file. The API server
defaults a `rollingUpdate` block onto a RollingUpdate Deployment, server-side
apply merges rather than replaces, and the leftover block is illegal beside
`type: Recreate`.

The chart cannot fix it — Helm owns `strategy.type` and not the block the API
server added. Clear it and the type together, in one operation, then upgrade:

```bash
kubectl -n k8s-dencer patch deploy k8s-dencer-ui-backend --type=merge \
  -p '{"spec":{"strategy":{"type":"Recreate","rollingUpdate":null}}}'
```

Removing `rollingUpdate` on its own does nothing: while the type is still
RollingUpdate the API server immediately puts it back.

## Being told, instead of remembering to look

The planner can POST a small JSON body to an endpoint you supply when
something changes that a person would want to know about. It is off by
default: this is the only outbound connection any component makes.

```yaml
planner:
  notify:
    # A webhook URL is a bearer credential — anyone holding it can post to
    # your channel, and inline values show up in `helm get values`.
    existingSecret: dencer-webhook   # key: url
    linkUrl: https://dencer.internal.example.com
```

```bash
kubectl -n k8s-dencer create secret generic dencer-webhook \
  --from-literal=url='https://hooks.example.com/services/...'
```

**Transitions only.** Two of them:

| | |
|---|---|
| `plan.actionable` | safe steps exist where a moment ago there were none |
| `plan.superseded` | a plan that had safe steps has been replaced |

Not a heartbeat. A message every resync is a message nobody reads, and once
people build a filter for it the one that mattered is filtered too.

**What leaves the cluster** is a plan id, four counts, your `clusterLabel`, a
link and a sentence:

```json
{
  "kind": "plan.actionable",
  "planId": "823b6dad6576",
  "cluster": "orbstack-lab",
  "safeSteps": 9,
  "totalSteps": 10,
  "nodesBefore": 17,
  "nodesAfter": 7,
  "link": "https://dencer.internal.example.com",
  "text": "k8s-dencer on orbstack-lab: 9 steps can be run safely — 17 nodes now, 7 after. Nothing has been executed."
}
```

Never the plan itself. No node names, no workload names, no rationale. The
plan is behind authentication and stays there — a webhook endpoint is readable
by anyone who has the URL, and a chat channel is not an authorization
boundary. The link is useless without a token.

**It cannot fail a planning cycle.** Every send is off the cycle's goroutine,
bounded by a five-second timeout with one retry, and a dead endpoint costs a
log line. A planner that stopped planning because a chat server was down would
be a worse product than one that never notified.

Two consequences worth knowing:

- A planner restart re-announces an actionable plan once, because the
  "was there anything safe last time" state is per-process. A duplicate is
  recoverable; a missed message is the thing this exists to prevent.
- If you restrict planner egress with a NetworkPolicy, it has to allow this.

## Known constraints

- **SQLite is single-writer.** `uiBackend.replicaCount` is pinned to 1 and enforced by the schema; the planner is co-scheduled with ui-backend via a `requiredDuringScheduling` podAffinity, because a ReadWriteOnce claim only permits multiple pods on the same node. Both constraints are lifted by `database.type=postgres` (above) — and only by that; they are properties of the file, not preferences.

  That affinity leaves the planner exactly one node it may occupy, so on a packed cluster it can lose the scheduling race and sit `Pending`. The chart creates a `PriorityClass` for the pair to stop that happening — only when the co-location applies, so a Postgres install gets no cluster-scoped object it has no use for. It is `preemptionPolicy: Never`: winning a queue is worth having, evicting someone else's workload to get there is not. Set `priorityClass.create=false` to opt out, or `planner.priorityClassName` to use your own.
- **PDBs use `maxUnavailable`, never `minAvailable`.** A `minAvailable` PDB on a single-replica Deployment makes its own pod undrainable — the exact pathology this product exists to detect.

---

## The CLI

The chart installs the server side. The CLI is a separate binary an operator
puts on their own machine:

```bash
make cli-install
```

That also drops a `kubectl-dencer` symlink beside it, which is all
`kubectl dencer plan` needs.

> **Prebuilt binaries start with the next release.** v0.1.0 published the
> images and the chart but predates the CLI, so there is nothing to download
> yet. From the next tag onward, static builds for linux and darwin on both
> architectures are attached to the GitHub release with checksums:
>
> ```bash
> curl -sSLo dencer \
>   https://github.com/atedgimo/k8s-dencer/releases/latest/download/dencer-$(uname -s | tr 'A-Z' 'a-z')-$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
> chmod +x dencer && sudo mv dencer /usr/local/bin/
> ```

Full usage in [cli.md](cli.md).

---

[← Documentation index](README.md) · [Project README](../README.md)