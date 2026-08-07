#!/usr/bin/env bash
# Watch until the autoscaler actually removes the cordoned nodes.
set -uo pipefail
start=$(date -u +%s)
echo "watching for node removal; started $(date -u +%H:%M:%SZ) with $(kubectl --context gke-play get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ') nodes"
for i in $(seq 1 60); do
  n=$(kubectl --context gke-play get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')
  if [ "$n" -lt 6 ]; then
    echo "NODES REMOVED: now $n at $(date -u +%H:%M:%SZ) after $(( $(date -u +%s) - start ))s"
    kubectl --context gke-play get nodes --no-headers 2>/dev/null | awk '{print $1, $2}'
    exit 0
  fi
  sleep 20
done
echo "still 6 nodes after $(( $(date -u +%s) - start ))s"
