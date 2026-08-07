#!/usr/bin/env bash
# Snapshot the cluster and the product's own view of it, into a phase folder.
#
#   ./capture.sh <phase-name>
#
# Every call is timestamped, so the folder order is the story order and the
# gaps between them are real elapsed time — which is what lets us say "the
# autoscaler took N minutes" afterwards instead of guessing.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
RUN="${RUN_DIR:-$HERE/run}"
CTX="${KCTX:-gke-play}"
DENCER="${DENCER_BIN:-$HERE/../dencer}"
PHASE="${1:?usage: capture.sh <phase-name>}"

N=$(printf '%02d' "$(( $(find "$RUN" -maxdepth 1 -type d -name '[0-9][0-9]-*' 2>/dev/null | wc -l) + 1 ))")
OUT="$RUN/$N-$PHASE"
mkdir -p "$OUT"

k() { kubectl --context "$CTX" "$@"; }

date -u +"%Y-%m-%dT%H:%M:%SZ" > "$OUT/timestamp"

# --- the cluster as Kubernetes sees it ---------------------------------
k get nodes -o wide                       > "$OUT/nodes.txt"          2>&1
k get nodes -o json                       > "$OUT/nodes.json"         2>&1
k get pods -A -o wide                     > "$OUT/pods.txt"           2>&1
k get pods -A -o json                     > "$OUT/pods.json"          2>&1
k top nodes                               > "$OUT/top-nodes.txt"      2>&1
k get events -A --sort-by=.lastTimestamp 2>&1 | tail -40 > "$OUT/events.txt"

# The measurement we could not make last time, because the cluster was gone.
python3 "$HERE/overhead.py" < "$OUT/pods.json" > "$OUT/node-overhead.txt" 2>&1

# --- the cluster as the product sees it --------------------------------
if [ -x "$DENCER" ]; then
  "$DENCER" plan --context "$CTX"                         > "$OUT/dencer-plan.txt"    2>&1
  "$DENCER" plan -o json --context "$CTX"                  > "$OUT/dencer-plan.json"   2>&1
  "$DENCER" reclamations --context "$CTX"                  > "$OUT/reclamations.txt"   2>&1
fi

echo "==> $OUT"
grep -E 'system CPU requests per node' "$OUT/node-overhead.txt" 2>/dev/null || true
head -3 "$OUT/dencer-plan.txt" 2>/dev/null || true
