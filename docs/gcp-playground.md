# The GCP playground — commands

A timed, self-destructing GKE cluster with real workloads shaped by a random
scenario, for playing with k8s-dencer. Everything below assumes `make
gke-setup` has run once (project, quota, billing alert).

## Launch

```bash
make gcp-play                     # ~20 minutes, then the cluster deletes itself
PLAY_MINUTES=30 make gcp-play     # a longer window
PLAY_FABRIC=kwok make gcp-play    # the free 24–40 fake-node fabric instead
```

The script prints the plan (machines, scenario, cost ceiling) and waits for a
typed `yes` before creating anything. When it finishes — countdown, Ctrl-C or
failure alike — it deletes the cluster, sweeps the orphaned volume disks, and
prints the leftovers audit as the receipt.

It prints the UI URL (`http://localhost:8092`) and a sign-in token that
expires with the window.

## Connect with kubectl (from any other terminal, while it runs)

```bash
# 1. Fetch credentials for the playground cluster
gcloud container clusters get-credentials dencer-play --zone us-central1-a

# 2. Give the context a short name (optional, once)
kubectl config rename-context gke_dencer-e2e_us-central1-a_dencer-play gke-play

# 3. Hand your default context back — get-credentials hijacks it
kubectl config use-context orbstack

# 4. Talk to the playground explicitly
kubectl --context gke-play get nodes
kubectl --context gke-play get pods -n dencer-demo     # the scenario workloads
kubectl --context gke-play get pods -n k8s-dencer      # the product
kubectl --context gke-play get events -A --watch       # good television during a drain
```

Notes: the cluster is zonal — `--zone`, not `--region` — and named
`dencer-play`. Step 3 is the habit that keeps every other terminal pointed at
your own cluster instead of the throwaway.

## Overrides

```bash
GCP_MACHINE=e2-medium REAL_NODES=10 make gcp-play   # pin the fleet instead of the draw
GCP_SPOT=0 make gcp-play                            # force on-demand
SCENARIO=b-pdb-blocked make gcp-play                # pin the scenario
UI_PORT=9000 make gcp-play                          # if 8092 is taken
```

## If something is left behind

```bash
make gcp-play-clean      # deletes a stray dencer-play cluster
make gke-leftovers       # read-only audit of anything billable in the project
```
