# Release history

What changed in each release, and — where it matters — whether you should
hurry. Newest first.

This lived in the project README until it was longer than the rest of the page
put together. The README now carries the current state; this carries how it got
there.

Milestone-level engineering history, with measurements and the reasoning behind
design changes, is in [roadmap.md](roadmap.md). Capabilities as features are in
[product-roadmap.md](product-roadmap.md). The bugs themselves, grouped by how
they hid rather than by release, are in [findings.md](findings.md).

---

## v0.9.0 — 2026-08-15

The release the first GKE run produced. That run answered the question it
existed to ask — the product plans on a real managed cluster, 4 steps, 6 nodes
to 2 — and then found what a laptop could not.

**Upgrade if you have ever read a rationale that repeated itself.** A plan is
identified by its *actions*: the strategy, the sequence, the target nodes and
the moves. That is the right identity, and it meant a planner that explained
the same drain better produced the same id, took the deduplication path, and
left the old wording in the store. The v0.8.1 fix for a stuttering rationale
shipped, ran, and changed nothing on any plan that already existed. The store
now refreshes what a step *says* as well as what it *is*.

Also in it:

- **Nodes already free stopped being invisible.** A node carrying only
  DaemonSets read as empty, so "N nodes now" described the subset the packer
  had work for rather than the fleet — and a node you could take today was
  never offered.
- **The money moved to the screen where the decision is made** — a monthly rate
  on Review and a per-step forecast, with the doctrine intact: unknown is never
  zero, spot is never on-demand, a rate is never a total.
- **A plan exports as a change record** — Markdown for a PR body or a ticket:
  what changes, why each step is rated as it is, what the run does, how to
  reverse it.
- **The read path survives a narrow viewport.** Three tiers; below the smallest,
  everything that evicts a pod disappears. Nobody drains a node from a phone —
  but someone gets paged, looks at their phone, and needs to know whether the
  cluster is fine.
- **The landing screen leads with the answer**, the rail separates deciding from
  understanding, and an empty plan reads as a cluster packed as tightly as its
  rules allow rather than as an absence.

## v0.8.1 — 2026-08-15

Three bugs found by driving the UI rather than reading it:

- the fix queue reported **0 findings for a full minute after sign-in** on a
  cluster with 34, because three polling hooks never re-read when a token
  arrived
- a step's rationale printed the same reason twice when several pods shared one
  rule
- the review footer called an empty selection "All Green" while the screen
  above it said nothing was safe

**Upgrade from v0.8.0 if you use `--reuse-values`.** That release added
`database.postgres.sslRootCert`, and `helm upgrade --reuse-values` carries
stored values forward without merging defaults for keys added since — so the
template hit a nil pointer and the upgrade would not render at all. It is
nil-safe now.

## v0.8.0 — 2026-08-14

The release that went looking. Every fix in it came from running the product
rather than reading it:

- the CLI could not find its own backend unless the Helm release happened to be
  named `k8s-dencer`
- it reported a plan the planner had just re-confirmed as twenty hours old,
  contradicting the UI about the same plan
- a ui-backend whose database had gone away answered every request with
  `internal error`, which named nothing. The API now returns `503 plan store
  unavailable`

The e2e also runs against **both** plan stores from here on — the gap that let
v0.6.0 ship a Postgres backend unable to record a drain.

## v0.7.0 — 2026-08-12

Repaired the Postgres store that v0.6.0 shipped.

**If you are running v0.6.0 on Postgres, upgrade.** The reclamation ledger
there could not record a single drain, and per-node history was silently dead.
Three bugs: a bool bound into an integer column, a statement that skipped
placeholder rewriting, and SQLite's 64-bit `INTEGER` translated to Postgres's
32-bit one — which capped every memory column at 2 GiB. Schema v15 widens those
columns on upgrade.

The underlying cause was a test suite that claimed to run against both backends
and did not. It does now, which is what found the third bug.

Migration also takes a cross-process lock, so the planner and ui-backend no
longer race on a fresh install, and `database.postgres.sslRootCert` lets an
install verify the server's certificate rather than only encrypting the
connection.

## v0.6.0 — 2026-08-12

Added the **Postgres plan store**. Set `database.type=postgres` and the
PersistentVolumeClaim, the `/data` mount, the required pod affinity and the
PriorityClass all disappear, because all four exist only to keep two writers off
one file. `uiBackend.replicaCount` becomes an ordinary value.

The reason is not availability, which is what the roadmap had assumed when it
recorded Postgres as dropped. SQLite's ReadWriteOnce claim permits multiple pods
only on the same node, so the planner is co-scheduled onto the ui-backend's node
and has exactly one node it may occupy — on a packed cluster it loses the
scheduling race and sits `Pending`.

It is one store speaking both dialects rather than two implementations. That
suite failed fourteen tests the first time it met a real Postgres, one of which
let two executors claim the same run.

> Superseded by v0.7.0 — see above before installing this.

## v0.5.0 — 2026-08-01

The release that made the product work on managed clusters. A real GKE run
found that it could not plan at all there ([see below](#what-the-cloud-runs-found)),
and fixing that brought the rest with it:

- the savings ledger in money, when you say what your machines cost
- rightsizing, maintenance windows and resilience simulation given screens
  after being built and never surfaced
- an empty plan that explains itself rather than rendering blank
- per-node history, so a node that is quietly idle can be told from one that
  spikes nightly
- a single Stop control on a run in flight — one rather than the designed two,
  because a pod that has been evicted cannot be un-evicted and two buttons
  would promise an undo that does not exist

**The UI was rebuilt against a full design handoff** in the same window: twelve
mockup frames, a dual-theme token system with computed-contrast guards, and the
two capabilities the design demanded of the backend — a real packing ceiling
(`planner.packCeiling`, default 0.85, recorded on every plan) and per-node
measured usage in the graph. The handoff lives in
[assets/design/](../assets/design/), and `make demo-video` re-records the
walkthrough of the real product
([assets/demo/walkthrough.webm](../assets/demo/walkthrough.webm)).

---

## What the cloud runs found

Three runs against real managed clusters, and each one earned its cost.

### 2026-07-31 — the reclamation loop, against a reclaimer nobody here wrote

`make cloud-e2e` stands up a five-node GKE cluster, installs the published
images, drains a node, and then waits — touching nothing — while **Google's own
cluster autoscaler** decides the node is unneeded and removes it. Observed and
timed at **11m9s**.

That is the one thing k3d structurally cannot prove.

### 2026-08-07 — it could not plan on a managed cluster at all

The most useful twenty cents this project has spent.

GKE runs kube-proxy as a static pod owned by the node itself. The analyser
recognised only DaemonSets as pinned, so it tried to reschedule a pod nothing
can move, failed, and marked every node undrainable. Converge reported nothing
to do while Google's own autoscaler reclaimed two nodes from the same cluster
three minutes later.

None of it was reachable from CI, because kwok nodes run no system pods at all
— 0m of system CPU locally against 276m minimum on GKE, where the daemons take
between 29% and 82% of a node's allocatable before a workload is scheduled.
[`test/fixtures/gke-managed.yaml`](../test/fixtures/gke-managed.yaml) now
reproduces that fleet to within 2m per node, so the whole class of bug is a
millisecond test rather than a cloud bill.

### 2026-08-15 — it plans, and the numbers off a laptop are different

First plan on contact: **4 steps, 6 nodes → 2**, with GKE's DaemonSets and
static pods correctly read as pinned rather than movable, and the resilience
audit reporting 0 findings — so none of them are misreported as at-risk either.

Postgres was verified on real infrastructure in the same run: schema 15, 0
`int4` columns, 2/2 ui-backend replicas, 0 restarts across three components
migrating at once. That last line is the advisory lock's first test against real
network latency.

Two measurements worth keeping:

| | |
|---|---|
| **Over-provisioning** | 940m allocatable, ~845–931m requested, ~45–55m used — roughly **18× over-requested**. Full by reservation and idle by usage, which no amount of consolidation fixes and is precisely what Rightsizing exists to name. |
| **Managed-node overhead** | system CPU per node min 301m, max 595m, mean 474m of 940m allocatable; kube-dns alone is 270m. Up to 63% of a 2 vCPU machine is gone before any workload lands. |

**It did not execute a drain.** The cluster was 88.5% requested and there was
nowhere to move pods to. Tracked as
[#225](https://github.com/atedgimo/k8s-dencer/issues/225); `run.sh` now measures
that before the window is spent rather than after.

---

[← Documentation index](README.md) · [Project README](../README.md)
