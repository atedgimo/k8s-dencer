# GKE run — 2026-08-15, v0.8.1

## The P0 question: PASSED

It plans on a real managed cluster. First plan on contact: **4 steps, 6 nodes → 2**.
On 2026-08-07 this was 0 steps with every node undrainable.

The fix is visibly working: `controllerPinned: 36-42` across the run — GKE's
DaemonSets and node-owned static pods recognised as pinned rather than movable
— and Resilience reported **0 findings**, so none of them are misreported as
at-risk either.

## FINDING 1 (real, user-visible): an empty plan freezes the fleet view

The API served this while the cluster had **7 Ready nodes**:

    plan id        a76339b03288
    nodesBefore    4
    nodesAfter     4
    steps          0
    snapshotAt     16:17:22        (12 minutes stale)
    snapshot nodes 0

Four consecutive planner cycles logged `"nodes":7` in the snapshot and
`"nodesBefore":4` in the plan, under one unchanging plan id.

A plan id is a content hash **of the steps**. An empty plan on a 4-node cluster
hashes identically to an empty plan on a 7-node cluster, so Save takes the
dedup path and never refreshes the node counts or the snapshot.

Why it matters: on a healthy cluster the plan is empty most of the time, which
is exactly when node counts move. Review and Cluster quietly freeze at the last
non-empty plan's fleet while nodes come and go. The Cluster screen had **no
nodes to draw** — `snapshot nodes: 0`.

Known failure mode in this codebase — the dedup touch was fixed once to carry
the ceiling and snapshot forward — but empty plans still slip through.

## FINDING 2: wait-up.sh races the installer

Printed `=== READY ===` over an empty namespace. `kubectl wait --all` with zero
matching deployments errors immediately instead of waiting.

## FINDING 3: run.sh captures then exits, killing the UI

Its `read` also consumed a buffered newline and fired instantly. The port-forward
dies with the script, so the "play" phase has no UI. The forward must outlive it.

## FINDING 4: postrun.sh defaulted to the wrong demo namespace

`play` rather than `dencer-demo`, so the first capture's workload files were empty.

## Confirmed working, first time on real hardware

- **Load lens**: 4 of 4 nodes carrying real per-node usage. On OrbStack only 1
  could, because the rest were simulated.
- **The over-provisioning gap, measured**: alloc 940m, requested ~845-931m,
  used ~45-55m per node. Roughly **18x over-requested** — full by reservation,
  idle by usage.
- **GKE overhead**: 940m allocatable from a 2 vCPU e2-medium.
- **External reclamation**: GKE's autoscaler removed 2 nodes on its own and the
  ledger recorded them `external: true` with instance type and capacity type —
  not claiming credit for work it did not do.

## Numbers

- nodes: 6 → 4 (autoscaler) → 7 (autoscaler, after scale-up) 
- cpuRequested: 47% → 88.5% → 93.8% → 91.0% → 59.8%
- allocatable per node: 940m of a 2 vCPU machine
- no drain was executed by the product this run

## Follow-ups

- [ ] Fix 1: plan identity or the dedup touch must account for fleet size
- [ ] Fix 2: wait for deployments to exist before waiting for availability
- [ ] Fix 3: detach the port-forward from run.sh's lifetime
- [ ] Fix 4: DEMO_NS default → dencer-demo
