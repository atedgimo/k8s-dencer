#!/usr/bin/env bash
# Wait for the playground to delete itself, then audit for anything billable.
set -uo pipefail
for i in $(seq 1 90); do
  s=$(gcloud container clusters list --filter="name=dencer-play" --format='value(status)' 2>/dev/null)
  [ -z "$s" ] && break
  sleep 20
done
echo "cluster gone at $(date -u +%H:%M:%SZ)"
make gke-leftovers 2>&1 | tail -12
echo "--- kubeconfig residue for the playground (should be empty) ---"
kubectl config get-contexts -o name 2>/dev/null | grep -i play || echo "  no play contexts"
kubectl config get-clusters 2>/dev/null | grep -i play || echo "  no play clusters"
