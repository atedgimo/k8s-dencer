# v0.5.0 on a real managed cluster — what to check

The 2026-08-07 run found that the product could not plan on GKE at all. Every
fix since was verified against `test/fixtures/gke-managed.yaml` and locally.
**Nobody has watched v0.5.0 plan on a real managed cluster.** That is what this
run is for.

If line 1 fails, stop and treat the fixture as wrong about something —
everything below it is downstream of that one answer.

## 1. The P0 — does it plan at all?

```bash
go run ./cmd/dencer plan --context gke-play
go run ./cmd/dencer converge --context gke-play --max-nodes 2 --max-impact Green --dry-run
```

- **Was:** `nodesUndrainable: 4` every cycle, `steps: 0`, converge reporting
  "every remaining node is either needed or undrainable" while the cloud's own
  autoscaler reclaimed two nodes from the same cluster.
- **Want:** steps proposed, and converge willing to act on them.
- Cross-check the planner's own view:
  `kubectl --context gke-play -n k8s-dencer logs -l app.kubernetes.io/component=planner --tail=5`
  — `nodesUndrainable` should no longer equal the node count.

## 2. Preflight and audit stopped crying wolf

```bash
go run ./cmd/dencer preflight --context gke-play
```

- **Was:** "0 of 6 nodes will drain cleanly", 30 of 32 blockers DaemonSets.
- **Want:** most nodes drainable; no blocker is a DaemonSet or kube-proxy.

## 3. recommend is about workloads you own

```bash
go run ./cmd/dencer recommend --context gke-play
```

- **Was:** 15 findings, 11 HIGH, one of them the operator's.
- **Want:** only `dencer-demo/*`. Nothing from `kube-system`, `gke-managed-*`,
  `gmp-*`, and nothing about k8s-dencer's own Deployments.

## 4. The screens that were never shown

UI at `http://localhost:8092`.

- **Rightsizing** in the rail — requested against observed, sorted by gap.
- **Cluster → Rack** — node names distinguishable (`…-0xk4`, not four
  identical `gke-dencer-play-de…`), clicking a node lists its pods with a
  visible selection.
- **"What if this node were gone?"** in the node pane — a real answer.
- **Top bar** — a maintenance-window chip only if windows exist; expect none.
- **Pool column** should read `default-pool` from GKE's own label, not the
  machine type.

## 5. Money

The playground now installs with a price for the machine it drew, so the
ledger has something to work with.

- **History** should show a monthly rate once anything is reclaimed, and count
  externally reclaimed nodes separately rather than claiming them.
- If GKE's autoscaler removes a node on its own — it did last time, 64 seconds
  from marking to gone — the ledger must say so *without* adding it to the
  savings this product produced.

## 6. Stop a run

```bash
go run ./cmd/dencer converge --context gke-play --max-nodes 2 --max-impact Green --yes &
go run ./cmd/dencer stop --context gke-play
```

- **Want:** it finishes the step in flight and stops before the next; status
  `Stopped`, and the ledger records who asked.
- It must **not** claim to have interrupted an eviction.

## 7. Per-node history

Needs 30 samples, which at a 30s resync is fifteen minutes. Late in the window,
select a node in the Rack lens: it should say what that node *usually* does,
or say nothing at all — never invent a trend from six points.

## 8. The receipt

```bash
./hack/capture/verify-teardown.sh
```

Nothing billable, and no `gke-play` entries left in the kubeconfig.

---

Capture as you go so the answers survive the window:

```bash
RUN_DIR=/tmp/gcprun ./hack/capture/capture.sh baseline
RUN_DIR=/tmp/gcprun ./hack/capture/capture.sh after-converge
```

---

## Phase 2 — the same questions on Postgres (optional, ~0 extra cost)

Run **only after items 1-3 above have passed.** The P0 question deserves an
uncontaminated answer first: a database pod consumes capacity on nodes whose
940m allocatable is already 30-80% spoken for by GKE's own daemons, and this
run exists to measure exactly that.

```bash
./hack/capture/postgres-phase.sh
```

It deploys Postgres on a 10Gi claim — not an emptyDir, because this exercise
drains nodes and the database is an ordinary workload that a drain can evict;
on a claim it comes back with its data — then converts the release with
`uiBackend.replicaCount=2`, which is the constraint SQLite imposes and
Postgres lifts.

Cost: a pod on nodes already running, plus one PD for the rest of the window.
Fractions of a cent against the ~15-20¢ the six e2-medium nodes cost anyway.

| # | Question | Why it is on the list |
|---|---|---|
| P1 | Every pod Running, 0 restarts after conversion | Two ui-backends and a planner migrate at once. Before the advisory lock one died on a duplicate key and CrashLoopBackOffed until another committed |
| P2 | No plan-store `/data` mount anywhere | If it survived, the single-node constraint came with it and Postgres bought nothing |
| P3 | A plan appears, with steps | Item 1 asked again on the other backend |
| P4 | A drain reaches the reclamation ledger | Exactly what v0.6.0 got wrong: the drain succeeded, the executor's events looked perfect, and nothing was written to the store |

The old `k8s-dencer-data` claim survives the switch by design
(`helm.sh/resource-policy: keep`) so a backend change never destroys plan
history. It is removed with the cluster; `verify-teardown.sh` confirms it.

**Rehearsed on a local cluster before this was written**, which found three
things worth having found for free: a chart template that could not be
upgraded into with `--reuse-values`, a script that broke its own database on
a second run, and the fact that the component images are distroless and have
no shell to exec into.
