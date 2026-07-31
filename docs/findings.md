# Findings

Bugs and gaps found by running the thing, kept together because the pattern
matters more than any single entry.

Almost none of these were found by reading code. They were found by putting the
product on a cluster it had not met, or by a person looking at a screen and
saying *"I don't see it."* The ones that stayed hidden longest were the ones
where **something reported success while doing nothing** — a test that could not
fail, a check whose own reader was broken, a payload field nothing consumed.

## Fixed

### Things that reported success while doing nothing

| What | Why it hid |
|---|---|
| `kubectl auth can-i create pods/eviction` in NOTES.txt | Answers "no" for everyone without `--subresource=eviction`. In the repo since Phase 1 and **could never have failed.** |
| A readiness lint assertion | The profiles it checked had the executor off, so nothing rendered and nothing was asserted. |
| A recovery-comparison fixture | Nothing in it was ever Ready, so the comparison was vacuous. |
| Two of four new chart assertions | One matched a *commented-out* registration; the other matched `health.Register(mux)` instead of the metrics one. Caught by mutating the source and checking each actually failed. |
| The Spot-quota check | Written specifically to catch a zero quota, it used a `gcloud --format` filter that returns nothing, reported "could not read", and waved the failure through into a five-minute cluster create. |

**The habit that catches these:** after writing a guard, break the thing it
guards and confirm it screams. Every assertion above passed until it was
deliberately attacked.

### Predictions presented as outcomes

The product's central claim was a guess wearing the clothes of a measurement.

- **`"15 reclaimable"`** came from counting plan steps. The UI marked a node
  `box-reclaimed` from `steps[].targetNode <= step`. **Nothing anywhere checked
  whether a single node ever went away** — and on the KWOK fabric none ever do.
  Fixed by the reclamation loop: *reclaimable → awaiting → reclaimed | returned*.
- **`Stats.Reclaimed` → `Reclaimable`**, since it always described what a plan
  *would* free.
- **A drained node looked untouched in the UI.** `cordoned` was parsed into the
  model and rendered nowhere, so the only way to see a real drain was to scrub
  into a prediction that happened to match reality. First slice fixed; the rest
  is [M24](roadmap.md).

### Signals that meant the opposite of what they said

**The staleness warning fired hardest when nothing was wrong.** The verdict line
read *"confirmed 72 minutes ago — the cluster may have moved on."* Measured on
the live cluster at that moment:

```
planGeneratedAt:  4648s old   ← what was displayed
planConfirmedAt:    12s old   ← the truth
```

Every part had been built correctly and in isolation. The store deliberately
touches `stored_at` on an unchanged plan — with a comment explaining that not
doing so had already made a re-verified plan read as nineteen hours old. The UI
deliberately ages against `storedAt` rather than `generatedAt`, with a comment
saying why. **Neither was wrong. Nothing connected them.** A plan only
re-publishes when its *content* changes, so the client kept the `storedAt` it
was handed at page load while the column behind it was refreshed every 30
seconds.

The result inverted the signal: a steady cluster with a healthy planner
confirming continuously was *the one state guaranteed to trip the warning*,
because stability is exactly what stops a plan from being republished.

Fixed by polling the version endpoint — the client's only liveness signal — and
carrying `planConfirmedAt` on it. The wording changed too, because the old text
described a different failure: silence here never meant the cluster drifted, it
meant **nothing is watching it any more**. It now says so.

The lesson is not "add a poll". It is that **two correct components with correct
comments can still compose into a lie**, and no test of either one catches it.
Guarded now by mutation-tested assertions on both halves.

### Data shipped that nothing read

- **45% of the graph payload was edges** — move, anti-affinity, PDB — unread
  since M11 replaced the node-link graph. Building the PDB edges also walked
  every pod for every PDB, so they were a quadratic term as well as dead weight.
- `utilization`, `drained`, `targetNode`, `moveStep`, then `nodesAfter`,
  `cpuReclaimedMilli`, `memoryReclaimedBytes`.
- A guard now parses the payload struct and fails on any field the UI never
  reads — **and the reverse**, on any `api.ts` field the payload stopped
  sending, because every field is optional and a stale declaration reads as
  `undefined` rather than failing to compile. That reverse check caught eight
  fields immediately, and would have caught the `Reclaimable` rename shipping
  as a blank headline figure.

### Deployment and delivery

- **`index.html` had no `Cache-Control`.** Browsers cached it heuristically, and
  since it names the hashed asset files, a stale copy pinned the browser to the
  *previous release*. Every UI deploy was invisible to a returning tab. The
  nginx comment already said the entry HTML is not immutable; only the assets
  half had been implemented.
- **`make help` hid every target with a digit** — its regex was `[a-zA-Z_-]+`,
  so `e2e` was invisible from the day it was added.
- **The chart described a product from Phase 1**: *"read-only: it plans and
  explains, it never evicts"*, twelve milestones after execution shipped, in the
  text Artifact Hub renders.

### Cloud, on first contact

Everything here was found within one hour of a real GCP project existing.

- **`gke-gcloud-auth-plugin` present, installed, and invisible.** Homebrew links
  `gcloud` but leaves components off `PATH`. The failure is worse than a missing
  binary: the first kubectl calls *succeed* on the token `get-credentials`
  leaves behind, so a run gets five minutes and a built cluster in before
  failing in a way that looks like a mid-run break.
- **A new project's `PREEMPTIBLE_CPUS` quota is zero.** Normal, not a
  misconfiguration. Now falls back to on-demand rather than blocking.
- **`gke-setup` hung ten minutes** on a budget alert: gcloud prompts to enable
  the Billing Budgets API and waits on stdin that never comes.
- **`e2e.sh clean` never worked** — it called `cluster_delete` seventy lines
  before the function was defined.
- **The chart refused `image.tag: latest`**, correctly. The GKE path was
  defaulting to it.

### Rails that looked like failures

Worth recording because each cost time before being understood as correct:

- **`MinReadyNodes`** refused to drain 1 of 3 nodes. The fix was five nodes, not
  a lowered floor — a test that weakens a safety rail is testing a
  configuration nobody runs.
- **`minNodeAge`** produced no plan at all on a fresh cluster. It will not touch
  a node younger than ten minutes, which means **a newly built cluster shows
  nothing for ten minutes and looks broken.**
- **A `kube-system` pod without a PDB** pins its node and the autoscaler
  correctly refuses to remove it.

### Mistakes made while working, and what stopped them recurring

- **An unpinned `helm upgrade` hit the GKE cluster** and killed a run in
  progress. `get-credentials` makes its context *current*, silently redirecting
  every terminal on the machine. Fixed twice over: the run hands the context
  straight back, and `make deploy` refuses any non-local context.
- **A running script was edited.** Bash reads lazily by byte offset, so a mid-run
  edit can execute garbage — on this script, possibly skipping the teardown that
  stops a cluster billing. Caught by luck; restored byte-for-byte.
- **Image tag drift**, repeatedly: `IMAGE_TAG` is content-hashed, so editing a
  file between `make images` and `make deploy` produces `ImagePullBackOff`.

## Open

| Gap | Where |
|---|---|
| **M24 — per-pod placement during a run** | nodes done (observed overlay: NotReady, awaiting, reclaimed, run-event cordons/drains); pods still drawn where the plan put them |
| **Karpenter / cluster-autoscaler detection** | deliberately skipped: warn *before* draining that nothing will remove the node |
| **Never run against a production cluster** | a throwaway GKE cluster is not someone's estate |
| **Workload Identity / IRSA half-proven** | annotations render and are accepted; k8s-dencer makes no cloud API calls, so no credential path is exercised |
| **The `returned` branch is k3d-only** | uncordoning races a real autoscaler |
| **Community files** | `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `GOVERNANCE.md` — `SECURITY.md` and `LICENSE` exist |

Dropped by decision: Postgres and HA. Deferred: scheduled automatic execution,
multi-agent orchestration.

---

[← Documentation index](README.md) · [Project README](../README.md)
