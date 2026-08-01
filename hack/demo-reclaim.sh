#!/usr/bin/env bash
# The demo fabric's stand-in autoscaler.
#
# On a real cloud, the platform's autoscaler removes nodes k8s-dencer has
# drained (GKE did it in 11m9s, observed). The KWOK fabric has no autoscaler,
# so the demo's story used to stop in the middle: drained nodes sat "awaiting
# removal" forever and the savings ledger read zero. This script plays the
# missing role — it deletes fake nodes that are drained, which is exactly
# what the real autoscaler does, clearly labelled as the fabric acting.
#
# Structural safety: it refuses any node not labelled type=kwok, so it cannot
# touch a real node no matter what it is asked.
#
#   hack/demo-reclaim.sh          one pass
#   hack/demo-reclaim.sh --watch  keep going (Ctrl-C to stop) — run this in a
#                                 second terminal during a demo
set -euo pipefail

bold() { printf '\033[1m%s\033[0m\n' "$*"; }

pass() {
  # Drained means: cordoned, and holding nothing that can move. Mirror the
  # tracker's definition: only kwok-labelled, unschedulable nodes whose pods
  # are all DaemonSet-owned or terminating.
  local nodes
  nodes="$(kubectl get nodes -l type=kwok \
    -o jsonpath='{range .items[?(@.spec.unschedulable==true)]}{.metadata.name}{"\n"}{end}')"
  [ -z "$nodes" ] && return 0

  while IFS= read -r node; do
    [ -z "$node" ] && continue
    # Any non-DaemonSet, non-terminating pod means the drain is not finished.
    local blockers
    blockers="$(kubectl get pods --all-namespaces \
      --field-selector spec.nodeName="$node" -o json 2>/dev/null |
      python3 -c '
import json,sys
d=json.load(sys.stdin)
n=0
for p in d.get("items",[]):
    if p.get("metadata",{}).get("deletionTimestamp"): continue
    owners=[o.get("kind") for o in p.get("metadata",{}).get("ownerReferences",[])]
    if "DaemonSet" in owners: continue
    n+=1
print(n)')"
    if [ "$blockers" = "0" ]; then
      bold "fabric-autoscaler: removing drained node $node"
      kubectl delete node "$node" --wait=false
    fi
  done <<< "$nodes"
}

if [ "${1:-}" = "--watch" ]; then
  bold "fabric-autoscaler: watching for drained kwok nodes (Ctrl-C to stop)"
  while true; do pass; sleep 20; done
else
  pass
fi
