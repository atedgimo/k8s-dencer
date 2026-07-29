# Development

The local loop, the fake-node fabric, the gates that run in CI, and how a release is cut.

## Development

```bash
make build          # compile Go
make test           # go vet + go test + UI typecheck (CI adds -race)
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

---

---

## Test fabric (KWOK)

A node-consolidation planner cannot be tested on a single-node cluster. [KWOK](https://kwok.sigs.k8s.io/) provides fake nodes with no kubelet, so a laptop presents a 30-node topology while the **real** scheduler, PDB accounting and eviction API all apply.

```bash
make kwok-up                        # upstream kwok + stage-fast charts (pinned 0.3.0 / app v0.8.0)
make demo-up                        # 30 fake nodes across 3 zones + the base workload
make demo-up SCALE=medium           # 200 nodes / 2000 pods — the largest safe locally
make scenario S=b-pdb-blocked       # switch constraint scenario
make demo-down && make kwok-down
```

### The fabric has a ceiling, on purpose

KWOK nodes are free — no kubelets — but **the pods on them are not**. Every pod
is a real API object held in the API server's watch cache and again in
k8s-dencer's own informer cache, and the planner will try to analyse all of
them. `constraints.Analyze` is roughly cubic today
([docs/benchmarks.md](benchmarks.md)), so a fabric of tens of thousands
does not run slowly — it pegs a core for hours while the executor lists every
pod every two seconds, and takes the control plane with it.

The chart therefore refuses to build one:

| tier | size | for |
|---|---|---|
| default | 30 nodes, 90 pods | the everyday demo |
| `SCALE=medium` | 200 nodes, 2000 pods | validating informer and executor read paths |
| ceiling | 200 nodes / 3000 pods | `--set fabric.acknowledgeLarge=true` to override |

**Scale numbers come from `make bench`, not from a large fabric.** It exercises
the same code paths over generated clusters with no cluster at all, which is why
5,000 pods can be measured in seconds on a laptop that could not host them.

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

---

## Continuous integration

`.github/workflows/ci.yml` runs on every push and pull request: `go vet`,
`go test -race`, `gofmt`, `make lint`, the UI typecheck and build, and the
benchmarks once through.

Worth being blunt about why this arrived at M20 rather than M0. Much of this
project's safety story is "there is a test that fails if you break it" — the
palette guards that keep the risk scale colour-blind-safe, the ast check that no
API route escapes authorization, the assertion that only the executor holds
`pods/eviction`, the transform test that the informer cache cannot drop a field
the planner reads. **None of those protected anything until CI existed**, because
nothing ran them except whoever remembered to.

The benchmark job is not a performance gate. A shared runner's timings are far
too noisy to assert against, and a flaky red build teaches people to ignore CI.
It catches the benchmarks rotting: the numbers in
[docs/benchmarks.md](benchmarks.md) drive Phase 4 decisions and are
worthless if nobody can reproduce them.

---

## Releases

`.github/workflows/release.yml` publishes on a `v*` tag: four multi-arch images
(`linux/amd64,linux/arm64`, with provenance and SBOM) to
`ghcr.io/atedgimo/k8s-dencer-*`, then the packaged chart to
`oci://ghcr.io/atedgimo/charts`.

Until this existed the chart defaulted to those image references and nothing had
ever pushed to them, so `helm install` could not work for anyone. The release
job refuses to publish if `Chart.yaml`'s `version` and `appVersion` do not match
the tag — `appVersion` selects the image tag, so a stale one would install a
previous release's images without saying so.

arm64 is not optional: the project is developed on Apple silicon, and an
amd64-only image would fail to run on the machine that built it.

---

---

[← Documentation index](README.md) · [Project README](../README.md)
