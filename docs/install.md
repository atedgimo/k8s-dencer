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

## Known constraints

- **SQLite is single-writer.** `uiBackend.replicaCount` is pinned to 1 and enforced by the schema; the planner is co-scheduled with ui-backend via a `requiredDuringScheduling` podAffinity, because a ReadWriteOnce claim only permits multiple pods on the same node. Both constraints disappear when the Postgres store lands.

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