# CLI

`dencer` is a client of the REST API, not a second planner. Everything it
shows was computed by the planner, and everything it asks for is authorised by
the same SubjectAccessReview the UI goes through.

That is the whole design decision. A CLI that talked to the cluster directly
would need its own copy of the constraint analyzer and the Safety Guard, and
the interesting failure would be the two copies disagreeing about whether a
step is Red.

## Install

```bash
make cli-install    # installs dencer and kubectl-dencer into $GOBIN
```

Prebuilt binaries are attached to GitHub releases from the first tag after
v0.1.0 — that release published the images and the chart but predates the CLI,
so building from a checkout is the only route until then.

The symlink is what makes `kubectl dencer plan` work — kubectl treats any
`kubectl-*` binary on PATH as a plugin. Every global flag is spelled the way
kubectl spells it, so muscle memory carries over.

## Connecting

With no flags, `dencer` finds the `ui-backend` Service through your kubeconfig
and port-forwards to it, the same way `kubectl port-forward` would. Nothing to
set up on a fresh install.

```bash
dencer plan                                   # port-forward via kubeconfig
dencer plan --server https://dencer.example   # an Ingress, no port-forward
dencer plan -n platform --release dencer      # a differently-named release
```

### Authentication

The backend verifies identity with `TokenReview`, which only understands
**bearer tokens**. If your kubeconfig authenticates with a client certificate —
the default on k3d, kind and OrbStack — there is no token to send, and the CLI
says so rather than letting the server answer "unauthenticated":

```bash
export DENCER_TOKEN="$(kubectl create token dencer-operator -n k8s-dencer)"
dencer plan
```

With OIDC single sign-on the ID token in your kubeconfig is already a
Kubernetes credential and is used directly.

## Commands

### `dencer plan`

The current plan, one line per step, leading with the next action.

```
plan 754f04015304  4s old, greedy-first-fit-decreasing
28 nodes now, 13 after, 15 reclaimable

STEP  IMPACT    DRAINS        PODS  WHY
1     ■ Red     kwok-node-12  3     Draining kwok-node-12 moves 3 pod(s). Rated Red…
2     ● Green   kwok-node-16  3     Draining kwok-node-16 moves 3 pod(s). No disrup…

Next: drain kwok-node-16 (● Green)
  dencer run --steps 2

■ 3 step(s) are Red and need an open MaintenanceWindow.
```

### `dencer explain <step>`

Why a step is rated as it is, what it moves, and which pods on that node
cannot.

### `dencer why <namespace>/<pod>`

Whether a pod can move and what is stopping it. Constraint explanations are
printed **verbatim** — the analyzer's `Explanation` is the single canonical
wording, and the UI and the Kagent agent quote the same string. If the CLI
paraphrased, one constraint could be described three ways and an operator would
have no way to tell which to believe.

### `dencer run --steps 1,3-5`

Executes the selected steps and follows them. Ranges and lists both work.

Before evicting anything it shows what is about to happen and asks. `--yes`
skips the prompt for scripts; `--dry-run` runs every guard check and emits the
same events without cordoning or evicting.

```
run 9d251edd  Succeeded
  ▲ 14:22:07 Guard    kwok-node-3   refused [PDBHeadroom]
```

**Exit codes carry the outcome.** A run refused by the Safety Guard exits
non-zero, because a pipeline must not treat a refused consolidation as a
completed one. Ctrl-C stops watching but does not stop the run — the executor
owns it, and the CLI says so on the way out.

### `dencer reclamations`

What actually became of the nodes you drained — the only figure in the product
derived from observation rather than from the plan.

The awaiting list comes first because it is the one with teeth: those are nodes
this tool told someone to drain, which nothing has removed. Anything over a day
old is flagged, since a node still sitting there after 24 hours is not waiting
for an autoscaler that is about to act.

### `dencer status`

The run in flight, or a specific one with `--run <id>`, plus its audit trail.

## Scripting

Every command takes `-o json` or `-o yaml`, and colour turns itself off when
output is not a terminal (`NO_COLOR` is honoured too).

```bash
# Fail a pipeline if the plan has grown a Red step
dencer plan -o json | jq -e '[.plan.steps[] | select(.impact=="Red")] | length == 0'

# Run every Green step, unattended
dencer run --yes --steps "$(dencer plan -o json \
  | jq -r '[.plan.steps[] | select(.impact=="Green") | .sequenceNumber] | join(",")')"
```

Risk is never carried by colour alone: each rating prints a glyph (`●` `▲` `■`)
and its word, so the output survives a pipe, a CI log and a reader who cannot
distinguish red from green.

---

[← Documentation index](README.md) · [Project README](../README.md)

## `dencer converge` — closed-loop consolidation

```
dencer converge --max-nodes 5 --max-impact Green [--dry-run] [--yes]
```

Instead of executing a plan's steps, the executor repeats: observe the
cluster, plan **one** drain against live state, run the full Safety Guard,
drain, wait for recovery — until nothing worthwhile remains or a bound is hit.

You are approving a **policy, not a list**, and the prompt says so. The
bounds are the consent:

- `--max-nodes` — the most nodes the run may drain (required, no default)
- `--max-impact` — `Green` or `Yellow`; nothing rated above it is executed.
  Red always requires a maintenance window, converge or not.

Two rails hold regardless: every round must actually free a node (measured
after the drain settles, not predicted) or the run stops, and no node is
drained twice in one run.

`--dry-run` rehearses exactly one round. A converge rehearsal cannot honestly
simulate where evicted pods land, so it does not pretend to.
## `dencer preflight` — will the rotation wedge?

```
dencer preflight [-o json]
```

The question every team asks before a node-pool rotation, answered before
anything is touched instead of discovered mid-upgrade through a stuck PDB:
**will every node drain, and if not, which pod is in the way and why?**

Blocked nodes come first, each with the analyzer's own explanation per
blocking pod — the same engine that plans consolidations, asked a different
question, so the preflight always agrees with the plan beside it.

State changes; re-run immediately before upgrading.

## `dencer audit` — what cannot survive a node loss

```
dencer audit [-o json]
```

The consolidation analysis read in the opposite mood: the PDB that blocks a
voluntary drain is the same PDB a dying node violates, and the pod with no
controller that eviction would delete permanently is deleted just as
permanently by hardware. Findings are grouped by kind, each quoting the
analyzer's own explanation.

## `dencer drain <node>` — kubectl drain, with the rails

```
dencer drain worker-3 [--dry-run] [--yes]
```

The drain everyone does with kubectl, but guarded: the step is **rated with
the same impact thresholds as a planned step** (a Red drain needs a
maintenance window — naming the node is not a side-channel), every eviction
passes the PDB pre-check against fresh state, recovery is verified on
readiness, aborting uncordons, and the whole thing lands in the audit trail.

## `dencer whatif` — can I lose this?

```
dencer whatif --without-zone eu-west-1b
dencer whatif --without-nodes worker-3,worker-4
```

The question capacity planning answers with a spreadsheet, answered instead by
the same constraint engine that plans consolidations: the latest snapshot
minus the removed nodes, every displaced pod re-homed by the analyzer, and the
ones with **nowhere legal to go named, with reasons**. Exit status is non-zero
when the simulated cluster cannot hold its workloads, so it works as a CI
gate. A fit is the engine's answer, not a promise about the scheduler on the
day — and the report says so.
## `dencer rightsizing` — requests vs what is actually used

```
dencer rightsizing [-o json]
```

Consolidation packs *requests* — the scheduler's input — so oversized
requests hold capacity no drain can free: a workload asking 4 cores and using
200m keeps its slice of every bin it lands in. This report names those
workloads, **measured on both sides** (requires
`planner.usageSource=metrics-server`; without measurements it refuses to
estimate). Usage is a point-in-time sample, not a peak — shrink requests
against your own percentiles, not this single reading.

## `dencer recommend` — what is missing, with fixes

```
dencer recommend [-o json]
```

`audit` reports what cannot survive; this reports what to *change*: the
multi-replica workload with no PDB (paste-ready YAML included), the
single-replica Deployment where every drain is an outage, missing resource
requests (suggested from measured usage when available), zero-headroom
budgets with the concrete adjustment, and hands-off annotations explained so
future-you remembers why nothing moves. Severity is impact on consolidation —
these are chores, not alarms.
