# Running the cloud test on GCP

Everything you need to do by hand, once. About fifteen minutes, most of it
waiting for GCP.

The end result is `make cloud-e2e`: a throwaway GKE cluster that installs the
published chart, drains a real node, and waits for **GKE's own cluster
autoscaler** to remove it. That last part is the reason this exists — on k3d the
only thing that has ever removed a drained node is our own test script.

---

## What it will cost

**Nothing, on the free trial.** The $300 / 90-day credit covers it many
thousand times over. Check what you have left with:

```bash
gcloud billing accounts list
```

| | |
|---|---|
| GKE control plane | **free** — the GKE free tier credit covers one *zonal* cluster |
| 5 × `e2-medium` Spot | ~$0.04/hr |
| 5 × 20GB `pd-standard` | ~$0.01/hr |
| **A ~25 minute run** | **about 2–3 cents** |

Two things worth knowing before you start:

- **The free trial does not auto-charge.** When the credit runs out or expires,
  GCP pauses resources and asks you to upgrade. It will not quietly start
  billing your card.
- **The only real risk is a cluster left running.** Five idle nodes is roughly
  $100/month. The script deletes the cluster on every exit path including
  `Ctrl-C`, and shouts if deletion fails — but if a run is ever killed hard
  enough to skip that, see [If something goes wrong](#if-something-goes-wrong).

---

## 1. Create the account

1. Go to **<https://cloud.google.com/free>** and click *Get started for free*.
2. Sign in with a Google account.
3. Accept the **$300 / 90-day** trial.

You will be asked for a **credit card**. This is an identity check —
Google states the trial does not charge it, and does not begin charging when
the credit runs out unless you explicitly upgrade to a paid account.

## 2. Create a project

In the console, top bar, the project dropdown → **New project**.

- **Name**: anything. `dencer-e2e` is fine.
- Note the **project ID** it generates — it may differ from the name, and the
  ID is what you need below.

## 3. Link billing

New projects are not always linked to the trial billing account automatically,
and GKE will not start without one — even though the free-tier credit covers
the cluster fee.

**<https://console.cloud.google.com/billing/linkedaccount>** → select your
project → link the trial billing account.

`make gke-setup` checks this and tells you if it is missing.

## 4. Install the gcloud CLI

```bash
brew install --cask google-cloud-sdk     # macOS
```

Anything else: <https://cloud.google.com/sdk/docs/install>

**Do not skip the Homebrew caveat.** The cask links `gcloud` into `bin` but
leaves the SDK's other components — including `gke-gcloud-auth-plugin`, which
kubectl needs to authenticate to GKE at all — in a directory that is not on
`PATH`. `make gke-setup` checks for it, and `hack/e2e.sh` finds it regardless,
but for your own `kubectl` against the cluster:

```bash
export PATH="$(brew --prefix)/share/google-cloud-sdk/bin:$PATH"
```

Left unfixed this produces a confusing failure rather than a clear one: the
first few kubectl calls succeed on the token `get-credentials` leaves behind,
so a run gets several minutes in and only then reports that it cannot reach the
API server.

## 5. Log in and select the project

```bash
gcloud auth login
gcloud config set project YOUR_PROJECT_ID
```

## 6. Bootstrap

```bash
make gke-setup
```

Idempotent, and safe to re-run. It:

- confirms you are logged in and billing is linked
- enables `container.googleapis.com` and `compute.googleapis.com`
- **checks your preemptible-CPU quota.** A new project's `PREEMPTIBLE_CPUS`
  limit is **zero** — this is normal, not a misconfiguration, and it has to be
  requested. Rather than block on that, the run falls back to on-demand nodes:
  about 7 cents for 25 minutes instead of 2. Request Spot at
  [IAM & Admin → Quotas](https://console.cloud.google.com/iam-admin/quotas) if
  you plan to run this often
- offers to create a **$1 budget alert**
- prints what a run costs

## 7. Run it

```bash
make cloud-e2e
```

About 25 minutes. Most of that is one wait: GKE's `scale-down-unneeded-time`
defaults to ten minutes, so after the drain the script sits watching for the
autoscaler to act. That is the real behaviour of the thing being tested.

Expect roughly:

```
==> multi-node cluster (gke)                5 nodes
==> namespace with PodSecurity enforcing
==> images                                  using published ghcr.io/... :latest
==> real workloads                          6 replicas Ready with httpGet probes
==> install                                 executor readiness=Ready
==> storage, on a named StorageClass        Bound on standard-rwo
==> the ReadWriteOnce claim keeps its two readers together
==> a plan against real state               plan with N steps
==> execute it                              run Succeeded
==> the drain was recorded as awaiting reclamation
==> GKE's own cluster autoscaler removes it
      observed as reclaimed after 11m — by a reclaimer we did not write
```

**Send me the output either way.** If it fails I would rather see the real
message than guess.

---

## If something goes wrong

**Check nothing is left running.** This is the only step that costs money if
skipped:

```bash
gcloud container clusters list      # should be empty
make cloud-e2e-clean                # if it is not
```

### Failures I would expect first

**Nothing to fix — the run says "using on-demand nodes".** That is the
preemptible quota being zero, which is the default on a new project. It costs a
few cents more and is otherwise identical.

**Genuinely out of quota.** If on-demand `CPUS` is also short, run smaller:

```bash
AGENTS=2 GCP_MACHINE=e2-small make cloud-e2e
```

**The node is never reclaimed, and it times out after 15 minutes.** Most likely
a `kube-system` pod with no PodDisruptionBudget is pinned to that node, and the
autoscaler is correctly refusing to remove it. The failure output lists the pods
still on the node so you can see which. This is a limitation of the test setup,
not of the product.

**`Spot` capacity unavailable in the zone.** Try another:

```bash
GCP_ZONE=us-east1-b make cloud-e2e
```

### Cleaning up entirely

When you are finished with the whole experiment:

```bash
gcloud projects delete YOUR_PROJECT_ID
```

Deleting the project is the only way to be certain nothing is left behind
anywhere in it.

---

[← Documentation index](README.md) · [Project README](../README.md)

## What a run costs, exactly

Stated so nobody has to guess, using list prices as of mid-2026 — verify
against [GCP's pricing page](https://cloud.google.com/compute/all-pricing)
before relying on them:

| Item | Rate | A full run (~45 min) |
|---|---|---|
| 5 × `e2-small` **Spot** nodes | ≈ $0.007/h each | **≈ $0.03** |
| 5 × `e2-small` on-demand (the quota fallback) | ≈ $0.017/h each | ≈ $0.07 |
| GKE control plane, one **zonal** cluster | covered by GKE's free tier credit for one cluster | $0 |
| Egress, images, API calls | pennies at this scale | ≈ $0 |

So a complete cloud test is **a few cents on Spot, under ten on-demand** —
provided teardown ran. The one real cost risk is not the run; it is a
leftover: a cluster that never tore down bills ~$3.50/day on-demand, and a
forgotten load balancer ~$0.60/day.

That is what `make gke-leftovers` is for: a **read-only** audit (every
command is a `list`) of clusters, instances, disks, load balancers, static
IPs and snapshots. Run it after any cloud session; an empty report is the
receipt. It deletes nothing, ever.

## The playground

`make gcp-play` stands up a throwaway GKE cluster with the full demo on top,
hands you the UI for twenty minutes, and then deletes everything it made.

- **A random scenario each run** — one of the demo chart's seven constraint
  scenarios, on a KWOK fabric whose size also varies (24–40 fake nodes,
  free); the three real `e2-medium` nodes underneath are the entire bill,
  a few cents for the window.
- **It never launches silently** — it prints the cluster, the scenario, the
  cost ceiling, and waits for a typed `yes`.
- **It always tears down** — the countdown ends in deletion; Ctrl-C ends in
  deletion; a failure mid-flight ends in deletion; and the run finishes with
  the `gke-leftovers` audit as the receipt. If a cluster somehow survives,
  `make gcp-play-clean` knows where it lives.
- **Your kubeconfig is left alone** — the current context is restored the
  moment credentials are fetched; the playground is addressed by `--context`
  throughout.
- The window is `PLAY_MINUTES` (default 20); the token it prints expires
  with the window.

It installs the **published** images (`ghcr.io` at the chart's appVersion),
so the UI you play with is the last released one — cut a release first if
you want the playground to show newer work.

## The second run

The first cloud test (M23) proved one thing: a reclaimer nobody here wrote
removed a drained node, observed and timed. Everything shipped since exists
to be pointed at a real cluster, and the second run is scoped to prove
exactly what k3d and KWOK structurally cannot:

| What | Why only GCP can prove it |
|---|---|
| **Converge against a real scheduler** | the monotonic rail's whole premise is scheduler divergence; kube-scheduler with real scoring is the divergence KWOK cannot produce. Envelope: `--max-nodes 2 --max-impact Green`, dry run first |
| **The observed overlay under real churn** | NotReady flickers, autoscaler removals, and cordons from outside the product |
| **The savings ledger on real machines** | capacity captured at drain time from real allocatable, summed after GKE's autoscaler actually removes the node |
| **`planConfirmedAt` under real latency** | the confirmation heartbeat has only ever run on localhost |
| **Preflight on real workloads** | blockers quoting real PDBs rather than fixtures |

Order of operations, human present throughout:

1. Publish the release images (outward-facing: needs an explicit go)
2. `make gke-setup` — quota and billing checks, Spot-first
3. `make cloud-e2e` — the existing harness, which restores your kubectl
   context immediately and tears down on exit
4. `dencer preflight`, `dencer converge --dry-run`, then the real envelope
5. Watch the ledger and the overlay while GKE's autoscaler does its part
6. Teardown verified, budgets checked, no identifiers written anywhere

Nothing in this section runs unattended. The first `gcloud` command waits
for the operator.
