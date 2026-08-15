#!/usr/bin/env bash
#
# Everything worth keeping from a playground run, collected before the cluster
# goes away — and a findings file to write into while it is still fresh.
#
#   ./hack/capture/postrun.sh            # into ./capture/<timestamp>/
#   OUT=/tmp/run ./hack/capture/postrun.sh
#
# The 2026-08-07 run produced twenty issues, and the ones that turned out to
# matter were not the ones anyone remembered afterwards. The cluster is
# self-destructing and cannot be re-asked, so this errs heavily towards taking
# too much: the expensive thing is the cluster, not the disk.
#
# Safe to run more than once, and safe to run late — every step tolerates the
# thing it is asking about having already gone.

set -uo pipefail

CTX="${CTX:-$(kubectl config current-context)}"
NS="${NS:-k8s-dencer}"
DEMO_NS="${DEMO_NS:-play}"
RELEASE="${RELEASE:-k8s-dencer}"
OUT="${OUT:-capture/$(date -u +%Y%m%dT%H%M%SZ)}"
PF_PORT="${PF_PORT:-18922}"

mkdir -p "$OUT"
bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
warn()  { printf '\033[33m%s\033[0m\n' "$*"; }

say() { bold "==> $1"; }
keep() { # keep <file> <command...>
  local f="$OUT/$1"; shift
  if "$@" > "$f" 2>&1; then green "  $f"; else warn "  $f (command failed; output kept)"; fi
}

say "cluster shape"
keep nodes.txt          kubectl --context "$CTX" get nodes -o wide
keep nodes.yaml         kubectl --context "$CTX" get nodes -o yaml
keep pods-all.txt       kubectl --context "$CTX" get pods -A -o wide
keep events.txt         kubectl --context "$CTX" get events -A --sort-by=.lastTimestamp

say "the product"
keep release.txt        helm --kube-context "$CTX" list -n "$NS"
keep values.yaml        helm --kube-context "$CTX" get values "$RELEASE" -n "$NS"
keep dencer-pods.txt    kubectl --context "$CTX" -n "$NS" get pods -o wide
keep dencer-desc.txt    kubectl --context "$CTX" -n "$NS" describe pods
for c in planner ui-backend executor; do
  keep "log-$c.txt"     kubectl --context "$CTX" -n "$NS" logs "deploy/${RELEASE}-${c}" --tail=4000
  # The previous container matters more than the current one when something
  # crashlooped: the current log starts after the interesting part.
  kubectl --context "$CTX" -n "$NS" logs "deploy/${RELEASE}-${c}" --previous --tail=2000 \
    > "$OUT/log-$c-previous.txt" 2>/dev/null && green "  $OUT/log-$c-previous.txt"
done

say "the workloads under test"
keep demo-pods.txt      kubectl --context "$CTX" -n "$DEMO_NS" get pods -o wide
keep demo-all.yaml      kubectl --context "$CTX" -n "$DEMO_NS" get all,pdb -o yaml

say "what the product says about itself"
kubectl --context "$CTX" -n "$NS" port-forward "svc/${RELEASE}-ui-backend" "${PF_PORT}:8080" >/dev/null 2>&1 &
PF=$!
trap 'kill $PF 2>/dev/null || true' EXIT
sleep 4
# An identity that is allowed to read.
#
# The ui-backend's own ServiceAccount is not: it serves the API, it does not
# consume it, so its token authenticates fine and is then refused by the
# authorizer. Using it produced eleven files of
# {"code":"forbidden","error":"… may not get plans.dencer.io"} that looked
# like a successful capture — the failure mode this whole script exists to
# avoid, since the cluster is gone by the time anyone reads them.
kubectl --context "$CTX" -n "$NS" create sa capture >/dev/null 2>&1 || true
kubectl --context "$CTX" create clusterrolebinding capture-dencer \
  --clusterrole="${RELEASE}-consolidation-operator" \
  --serviceaccount="${NS}:capture" >/dev/null 2>&1 || true
TOKEN="$(kubectl --context "$CTX" -n "$NS" create token capture --duration=30m 2>/dev/null || true)"

# Prove it before capturing eleven files with it.
probe="$(curl -s --max-time 15 -H "Authorization: Bearer ${TOKEN}" \
  "http://localhost:${PF_PORT}/api/v1/plans/latest" 2>/dev/null)"
case "$probe" in
  *forbidden*|*unauthenticated*|"")
    warn "  the capture identity cannot read the API — JSON below will be error bodies"
    warn "  ${probe:0:120}"
    ;;
  *) green "  capture identity authorised" ;;
esac

api() { curl -s --max-time 20 -H "Authorization: Bearer ${TOKEN}" "http://localhost:${PF_PORT}$1"; }
for ep in version plans/latest reclamations recommendations rightsizing resilience preflight windows runs stability history; do
  f="$OUT/api-$(echo "$ep" | tr '/' '-').json"
  if api "/api/v1/$ep" > "$f" 2>&1 && [[ -s "$f" ]]; then green "  $f"; else warn "  $f (empty or failed)"; fi
done
# The graph is per-plan and is the payload every lens draws from.
PID="$(python3 -c "
import json,sys
try:
    d=json.load(open('$OUT/api-plans-latest.json')); print((d.get('plan') or d).get('id',''))
except Exception: print('')
" 2>/dev/null)"
if [[ -n "$PID" ]]; then
  if api "/api/v1/plans/$PID/graph" > "$OUT/api-graph.json" 2>&1 && [[ -s "$OUT/api-graph.json" ]]; then
    green "  $OUT/api-graph.json"
  else
    warn "  $OUT/api-graph.json (empty)"
  fi
else
  # Every lens draws from the graph, so its absence is worth a line rather
  # than a gap someone notices a week later.
  warn "  no plan id in api-plans-latest.json — graph not captured"
fi
kill $PF 2>/dev/null || true
trap - EXIT

say "metrics"
# planner and executor expose metrics on their probe port; ui-backend serves
# them on the same port as the API, because it already has one. Using 8081 for
# all three quietly produced an empty file for ui-backend.
for c in planner ui-backend executor; do
  port=8081; [[ "$c" == "ui-backend" ]] && port=8080
  kubectl --context "$CTX" -n "$NS" port-forward "deploy/${RELEASE}-${c}" "18923:${port}" >/dev/null 2>&1 &
  MP=$!; sleep 3
  if curl -s --max-time 10 http://localhost:18923/metrics > "$OUT/metrics-$c.txt" 2>&1 && [[ -s "$OUT/metrics-$c.txt" ]]; then
    green "  $OUT/metrics-$c.txt"
  else
    warn "  $OUT/metrics-$c.txt (empty)"
  fi
  kill $MP 2>/dev/null || true
done

say "node overhead — what GKE's own daemons take"
# The figure that explained the whole 2026-08-07 failure: e2-medium is 2 vCPU
# but 940m allocatable, and system pods were taking 29-82% of that.
# It reads `kubectl get pods -A -o json` on stdin — passing --context to it
# gets a traceback, which is how this was found.
kubectl --context "$CTX" get pods -A -o json \
  | python3 "$(dirname "${BASH_SOURCE[0]}")/overhead.py" > "$OUT/overhead.txt" 2>&1 \
  && green "  $OUT/overhead.txt" || warn "  overhead.py failed (kept)"

say "findings, to fill in now rather than tomorrow"
cat > "$OUT/FINDINGS.md" <<'NOTES'
# Run findings

Written while the cluster still exists. Anything left for later is a memory,
and the 2026-08-07 run showed which of those survive: not the important ones.

## The P0 question — did it plan?

- steps in the first plan:
- nodes before / after:
- if zero steps: what did Cluster show, and what did preflight say?

## Was anything wrong?

One line each. Screen, what it said, what it should have said.

-

## Was anything surprising but correct?

These matter as much — they are usually documentation bugs, not code bugs.

-

## Numbers worth keeping

- allocatable per node:
- system overhead observed:
- plan: nodes before → after, reclaimable:
- reclamation: drained at → observed gone at, elapsed:
- money: what the ledger reported, and whether anything was unpriced:

## What I would change

Ordered by how much it would have helped during this run.

1.

## Follow-ups to file

- [ ]
NOTES
green "  $OUT/FINDINGS.md"

echo
bold "captured into $OUT"
echo "  Fill in FINDINGS.md before the cluster expires — it cannot be re-asked."
echo "  Teardown check afterwards: ./hack/capture/verify-teardown.sh"
