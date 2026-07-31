# Execution and safety

Execution is opt-in, gated, and off by default. When it is on, this is exactly what happens, what refuses to happen, and why.


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

## What the executor does per step

```
guard.CheckStep      against state read seconds ago, not the plan's snapshot
cordon               merge patch of spec.unschedulable — no `update` verb needed
for each pod:
    guard.CheckEviction   live PDB headroom, re-read before EVERY eviction
    evict                 policy/v1 eviction API, so the API server enforces PDBs
    wait                  until the pod is actually gone
verify               affected workloads regained READY replicas elsewhere
```

Eviction goes through the **eviction subresource**, never a pod delete. A delete
would bypass PodDisruptionBudgets entirely; that single choice is what makes
every PDB guarantee in this product real. The executor's RBAC grants
`create pods/eviction` and deliberately does **not** grant `delete pods`.

## Maintenance windows

Until M14, a Red step was refused outright — doc §9 confines them to "an
approved maintenance window", and with no such object the safe reading was
"never". The `MaintenanceWindow` CRD is that object.

```yaml
apiVersion: dencer.io/v1alpha1
kind: MaintenanceWindow
metadata: {name: sunday-night}
spec:
  schedule: "0 2 * * 0"        # five-field cron; descriptors like @every are rejected
  duration: "4h"
  timeZone: "Europe/London"    # required, no default — see below
  allowRed: true               # off by default; a window does not unlock Red by itself
  nodeSelector: {pool: batch}  # optional; empty means every node
```

```bash
kubectl get mw
# NAME           SCHEDULE    DURATION  ZONE            RED   ACTIVE  CLOSES
# sunday-night   0 2 * * 0   4h        Europe/London   true  false
```

**Everything about this fails closed.** An unparseable schedule, an unknown or
missing timezone, a suspended window, a window that cannot be read at all —
each resolves to *closed*, and each says why. The asymmetry is deliberate: a
window wrongly shut costs an operator some waiting; a window wrongly open costs
an unattended drain of something that should not have been touched.

Three specific choices worth knowing:

- **`timeZone` is required with no default.** `time.LoadLocation("")` returns
  UTC without an error, so a defaulted zone would silently open "Sunday 02:00"
  at the wrong hour for most of the world — and, twice a year, for the person
  who wrote it. A test covers exactly that.
- **`allowRed` defaults to false.** Creating a window must not by itself unlock
  the most dangerous class of step.
- **Status is not the authorisation.** It refreshes on a 30-second sweep, so it
  can read `ACTIVE=true` for a window suspended a moment ago. The Safety Guard
  never consults it; it re-evaluates the spec against the clock on every check.

Windows are read-only to every component — the executor may write their
*status* and nothing else, so the thing that benefits from a permissive window
cannot create one. `make lint` asserts it.

CRDs are installed by Helm on install and **never on upgrade**, which is by
design: a bad CRD change can orphan existing objects. After a chart upgrade
that changes one:

```bash
make crds         # regenerate manifests from the Go types
make crd-upgrade  # apply them to the cluster
```

## The Safety Guard

[`internal/safety`](../internal/safety/safety.go) — architecture doc §9, enforced in
code rather than in API validation, so no crafted request reaches around it.
Each rail is named in the audit event that blocks on it.

| Rule | Refuses |
|---|---|
| `RedRequiresWindow` | a Red step with no open window covering its node that permits Red |
| `MaxNodesPerRun` | draining more than `safety.maxNodesPerRun` in one request |
| `MinReadyNodes` | dropping below `safety.minReadyNodes` schedulable nodes |
| `StepFreshness` | a step whose pods no longer have anywhere to go |
| `PDBHeadroom` | an eviction the budget cannot absorb, re-checked per pod |
| `NodeNotFound` | a target node that has vanished |

**Red is not unlocked by a chart value.** There is no `safety.allowRed`.
Unlocking it takes a `MaintenanceWindow` that is open, covers the step's node,
and sets `allowRed` — a decision scoped to a time and a set of machines, rather
than a global switch someone flips and forgets.

The guard predicts; the executor then **verifies reality**. This is the
deliberate alternative to importing kube-scheduler's framework (doc §7): after
each step, affected workloads must have regained their replicas elsewhere or the
run stops. A prediction that turns out wrong aborts rather than compounding.

## Recovery is judged on readiness, not phase

`executor.readiness` defaults to `Ready`, meaning the pod's Ready condition.
Running only says the kubelet started the containers — a pod can be Running and
failing its probes, and treating that as recovered drains the next node while
the service is still down.

The KWOK fabric is the exception: stage-fast's `pod-ready` Stage selector
matches only `phase In [Pending]`, so a fake pod that reaches Running never
becomes Ready and a strict wait would hang forever. The local POC overlay is the
only profile allowed to set `Running`, and `make lint` fails if any other one
does — or if the overlay stops setting it, which would silently hang every demo
drain.

## Draining is not removing

k8s-dencer cordons a node and empties it. It never deletes one, and its
ServiceAccount holds no `delete` verb on nodes, so it could not if it tried.

That is not an omission. Deleting a `Node` object does not terminate anything —
the kubelet re-registers seconds later. Actually removing the machine means
calling AWS, GCP or Azure, which is provider-specific and exactly the kind of
assumption this chart refuses to bake into its defaults.

So the handoff point is **empty and cordoned**. On a real cluster, Karpenter or
cluster-autoscaler sees an empty cordoned node and reclaims it; on a managed
node pool your own tooling does. On the KWOK fabric nothing does, which is why
drained fake nodes sit there indefinitely — that is correct, not a stall.

To put a drained node back into service:

```bash
kubectl uncordon <node>      # or: make fabric-reset, for the whole KWOK fabric
```

### And k8s-dencer checks whether it happened

The handoff used to be where the product stopped caring. It reported
"15 reclaimable" and the UI called a drained node reclaimed, so a prediction
was presented as an outcome and nothing ever verified it. On a cluster with no
reclaimer at all — the KWOK fabric, a fixed node pool, a cluster whose
autoscaler is at its minimum size — every one of those numbers was wrong and
nothing said so.

Three states now, and only the last is a saving:

| State | Means |
|---|---|
| **reclaimable** | the plan would free this node, if executed |
| **awaiting reclamation** | drained and cordoned; the machine is still there |
| **reclaimed** | the `Node` object is gone — something removed it |
| **returned** | someone uncordoned it instead; not a saving |

The executor opens a record the moment a node is emptied. The planner, which
watches nodes continuously anyway, closes it: a node absent from the snapshot
was reclaimed, a node that is present and schedulable again was returned, and a
node that is present and still cordoned is still waiting. No cloud API, no
autoscaler-specific detection, nothing to configure.

```bash
dencer reclamations
```

```
Awaiting reclamation
  NODE            DRAINED   RUN
▲ ip-10-0-4-21    3d ago    9d251edd

▲ 1 node(s) drained over a day ago and still present.
  Draining frees capacity; something else has to remove the machine.
  If nothing is going to, uncordon them: kubectl uncordon <node>

Observed (last 30 days)
  6 reclaimed, median 3m
```

`dencer_nodes_awaiting_reclamation` is the metric to alert on. Rising without
falling means nothing is reclaiming what you drained.

## Abort means uncordon, not rollback

**Evicted pods are not restored.** Eviction cannot be undone, and calling the
abort path "rollback" would be a lie in the docs and a surprise at 3am. On
failure or timeout the executor uncordons the node and stops — using a context
detached from the step deadline, since the usual reason to abort is that the
deadline expired. If the uncordon itself fails, the audit event says so and
names the `kubectl uncordon` needed.

A run ends in one of three terminal states, and `Blocked` is deliberately
distinct from `Failed`: "the rails protected you" and "something broke" call for
different responses.

## Confirming the split yourself

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

## Audit trail

Every action lands in `run_events` with the plan, the step, the node, the pod,
the action, the rule that refused, and the actor. A plan with a run against it
is **never pruned**, however old — doc §9 ties the audit log to the plan version,
and pruning it would leave the log pointing at nothing.

```bash
curl -H "Authorization: Bearer $TOKEN" localhost:8090/api/v1/runs/<runId> | jq .events
```

## Running steps from a terminal

Everything below is reachable from the [CLI](cli.md) as well as the UI, through
the same API and the same authorization:

```bash
dencer plan                    # what would be reclaimed, and what each step costs
dencer explain 3               # why step 3 is rated as it is
dencer run --steps 1,3-5       # execute, with a confirmation prompt
dencer run --steps 2 --dry-run # every guard check, nothing cordoned or evicted
dencer status                  # the run in flight, plus its audit trail
```

`--dry-run` is worth reaching for first on an unfamiliar cluster: it runs the
full guard chain and emits the same events, so a step that would be refused
tells you which rail refuses it before anything is disrupted.

A run refused by the Safety Guard exits non-zero, so a pipeline cannot mistake
a refused consolidation for a completed one.

---

[← Documentation index](README.md) · [Project README](../README.md)