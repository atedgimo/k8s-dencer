# Snapshot fixtures

Captured `ClusterSnapshot`s, serialised from a live cluster by the planner's
`/debug/snapshot` endpoint.

These are the inputs to the golden planner tests from M4 onward. They are
captured from a real cluster rather than hand-written on purpose: hand-built
fixtures drift toward whatever the planner already does, and stop catching the
cases that matter. `internal/model` imports nothing from `k8s.io`, which is
what makes replaying them possible with no API server.

## Regenerating

```bash
make demo                              # fabric + topology + product
make scenario S=b-pdb-blocked          # pick the scenario
# wait one resync period for the planner to observe the change
make capture-fixture S=b-pdb-blocked
```

## Contents

| File | Scenario | Notable |
|---|---|---|
| `a-fragmented.yaml` | `a-fragmented` | 31 nodes (30 fake + the real one), 117 pods, 37% CPU requested, no PDBs |
| `b-pdb-blocked.yaml` | `b-pdb-blocked` | 2 PDBs — `payments` with `disruptionsAllowed: 0`, `catalog` with headroom |

## Caveats

Fixtures include the **real** node and the real workloads (kagent, kube-system,
k8s-dencer itself) alongside the synthetic ones. That is deliberate: a planner
that only ever sees tidy synthetic input will not cope with a real cluster.

`disruptionsAllowed` is a live status value. Re-capturing after the PDB
controller has reconciled a change will produce a different number, so a
fixture reflects one moment, not a permanent truth.
