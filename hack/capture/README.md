# Capturing a cloud run

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
