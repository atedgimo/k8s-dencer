#!/usr/bin/env bash
#
# The second terminal. Everything around a playground run except the launch.
#
#   Terminal 1:  PLAY_MINUTES=45 SCENARIO=a-fragmented GCP_MACHINE=e2-medium \
#                REAL_NODES=6 make gcp-play
#   Terminal 2:  ./hack/capture/run.sh
#
# The launch stays in terminal 1 on purpose. It prints what it will create and
# what that costs and waits for a typed yes, and a wrapper that answered that
# for you would be a wrapper that spends your money without asking. This does
# the parts that are safe to automate: waiting, connecting, reminding, and —
# the one that actually matters — capturing before the cluster self-destructs.
#
# Safe to run late, safe to re-run, and safe to Ctrl-C: the only thing it
# creates is a port-forward and a read-only ServiceAccount.

set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HERE="$REPO/hack/capture"
CTX="${CTX:-}"
NS="${NS:-k8s-dencer}"
DEMO_NS="${DEMO_NS:-play}"
RELEASE="${RELEASE:-k8s-dencer}"
UI_PORT="${UI_PORT:-8090}"

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
warn()  { printf '\033[33m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }
rule()  { printf '\033[2m%s\033[0m\n' "────────────────────────────────────────────────────────────"; }

# ------------------------------------------------------- wait, then context
# wait-up.sh is self-contained: it waits for the cluster, fetches credentials,
# names the context gke-play, and puts your previous context back. So it has
# to run before there is a context to talk to — guessing one first would find
# whatever you were last pointed at, which on a good day is OrbStack and on a
# bad day is production.
bold "==> waiting for the cluster and the release"
if [[ -x "$HERE/wait-up.sh" && -z "$CTX" ]]; then
  "$HERE/wait-up.sh" || warn "  wait-up reported a problem; continuing so you can look"
  CTX="${CTX:-gke-play}"
fi
if [[ -z "$CTX" ]]; then
  CTX="$(kubectl config current-context 2>/dev/null || true)"
fi
[[ -n "$CTX" ]] || { red "no kubectl context found"; exit 1; }
kubectl --context "$CTX" get ns "$NS" >/dev/null 2>&1 \
  || { red "context $CTX has no $NS namespace — is this the playground?"; exit 1; }
bold "==> context: $CTX"

# ------------------------------------------------------------------- access
bold "==> opening the UI"
pkill -f "port-forward.*${RELEASE}-ui-frontend" 2>/dev/null || true
sleep 1
kubectl --context "$CTX" -n "$NS" port-forward --address 127.0.0.1 \
  "svc/${RELEASE}-ui-frontend" "${UI_PORT}:80" >/tmp/dencer-ui-pf.log 2>&1 &
PF=$!
cleanup() { kill $PF 2>/dev/null || true; }
trap cleanup EXIT
sleep 4

# An identity that can read AND execute, because you are going to drain
# something. The components' own ServiceAccounts cannot: they serve the API,
# they do not consume it.
kubectl --context "$CTX" -n "$NS" create sa player >/dev/null 2>&1 || true
kubectl --context "$CTX" create clusterrolebinding player-dencer \
  --clusterrole="${RELEASE}-consolidation-operator" \
  --serviceaccount="${NS}:player" >/dev/null 2>&1 || true
TOKEN="$(kubectl --context "$CTX" -n "$NS" create token player --duration=8h 2>/dev/null || true)"

code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 8 "http://localhost:${UI_PORT}/" || echo 000)"
if [[ "$code" == "200" ]]; then green "  UI is up"; else warn "  UI returned $code — it may still be starting"; fi

rule
bold "  http://localhost:${UI_PORT}"
echo
echo "  Paste this into the sign-in box:"
echo
echo "$TOKEN"
rule
echo
bold "  CLI against the same cluster:"
echo "    export DENCER_TOKEN='<the token above>'"
echo "    dencer plan --context $CTX -n $NS --release $RELEASE"
echo
bold "  Open beside the browser:"
echo "    $HERE/UI-CHECKLIST.md      what to look at, and what right looks like"
echo "    $HERE/CHECKLIST.md         the eight questions this run exists to answer"
echo
warn "  Item 1 is the stop condition: if Review shows no steps on a cluster"
warn "  with half-empty nodes, capture and stop — everything else is downstream."
echo

# ------------------------------------------------------------------ the run
bold "==> first look"
if [[ -n "$TOKEN" ]]; then
  steps="$(curl -s --max-time 15 -H "Authorization: Bearer $TOKEN" \
    "http://localhost:${UI_PORT}/api/v1/plans/latest" 2>/dev/null \
    | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin); p=d.get('plan') or d
    print(len(p.get('steps') or []), p.get('nodesBefore'), p.get('nodesAfter'))
except Exception: print('? ? ?')" 2>/dev/null)"
  read -r n before after <<<"$steps"
  if [[ "$n" == "0" ]]; then
    red "  THE PLAN HAS NO STEPS (${before} nodes). This is the P0 question failing."
    red "  Capture now:  $HERE/postrun.sh"
  elif [[ "$n" == "?" ]]; then
    warn "  could not read a plan yet — give the planner a resync and refresh the UI"
  else
    green "  plan has $n step(s): $before nodes now, $after after"
  fi
fi
echo

bold "==> play"
echo "  Take as long as you like. When you are done — and BEFORE the cluster"
echo "  expires — press Enter here and everything gets captured."
echo
read -r -p "  Press Enter to capture, or Ctrl-C to skip: " _ || true

# --------------------------------------------------------------- the keeping
echo
OUT="${OUT:-$REPO/capture/$(date -u +%Y%m%dT%H%M%SZ)}"
CTX="$CTX" NS="$NS" DEMO_NS="$DEMO_NS" RELEASE="$RELEASE" OUT="$OUT" "$HERE/postrun.sh"

echo
rule
bold "  Fill in $OUT/FINDINGS.md now, while the cluster still exists."
echo "  After it self-destructs:  $HERE/verify-teardown.sh"
rule
