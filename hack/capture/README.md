# Capturing a cloud run

## The run, in two terminals

Everything you need. Copy these.

```bash
# TERMINAL 1 — launches. Prints what it creates and what it costs, then waits
# for you to type: yes
PLAY_MINUTES=45 SCENARIO=a-fragmented GCP_MACHINE=e2-medium REAL_NODES=6 make gcp-play
```

```bash
# TERMINAL 2 — everything else. Waits for the cluster, opens the UI, prints a
# token, tells you the first plan's step count, then waits while you play.
# Press Enter when done and it captures 33 files.
./hack/capture/run.sh
```

If terminal 2 cannot find the cluster, the context it wants is `gke-play`:

```bash
CTX=gke-play ./hack/capture/run.sh
```

Optional, only after checklist items 1-3 pass and only if time remains:

```bash
./hack/capture/postgres-phase.sh      # convert the same cluster to Postgres
```

After the cluster self-destructs:

```bash
./hack/capture/verify-teardown.sh     # confirm nothing is still billing
```

Every script also runs standalone, so nothing depends on `run.sh` working:
`wait-up.sh`, `capture.sh <phase>`, `watch-reclaim.sh`, `postrun.sh`.

**Read beside the browser:** `UI-CHECKLIST.md` (what each screen should say)
and `CHECKLIST.md` (the eight questions). Item 1 is the stop condition — no
steps on a cluster with spare nodes means the 2026-08-07 P0 is back, and
nothing downstream is worth measuring.

**Cost:** roughly 15-20¢ for six `e2-medium` nodes over 45 minutes, less on
spot. The Postgres phase adds fractions of a cent.

Built during the 2026-08-07 GKE run, when the interesting findings turned out
to be things nobody had thought to look at. A metered cluster is a bad place
to be writing tooling, so it lives here now.

Everything writes into a numbered, timestamped folder per phase, so the folder
order is the story order and the gaps between them are real elapsed time.

```bash
# 1. Launch (yours to run — it prompts for consent and spends money)
PLAY_MINUTES=45 SCENARIO=a-fragmented GCP_MACHINE=e2-medium REAL_NODES=6 make gcp-play

# 2. Wire up kubectl and wait for the product, in another terminal
./hack/capture/wait-up.sh

# 3. Snapshot at each phase
RUN_DIR=/tmp/gcprun ./hack/capture/capture.sh baseline
RUN_DIR=/tmp/gcprun ./hack/capture/capture.sh after-converge

# 4. UI stills
DENCER_URL=http://localhost:8092 DENCER_TOKEN="$(kubectl --context gke-play \
  -n k8s-dencer create token dencer-operator --duration=30m)" \
  SHOT_DIR=/tmp/gcprun/ui node ui/e2e-demo/shots.mjs

# 5. Confirm nothing is left billing
./hack/capture/verify-teardown.sh
```

`overhead.py` is the one worth knowing about: it sums CPU and memory requests
per node, split system against workload. It is how the "GKE daemons take 29%
to 82% of a node before your workload is scheduled" figure was measured, and
it is what `test/fixtures/gke-managed.yaml` was built from.

`watch-reclaim.sh` waits for the cloud's own autoscaler to remove nodes, which
on the first run took 64 seconds from marking to gone.
