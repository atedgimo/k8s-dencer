#!/usr/bin/env bash
#
# A timed GCP playground: a real GKE cluster, the KWOK demo fabric with a
# RANDOM scenario on top, the published k8s-dencer release installed — live
# for a fixed window so a human can play with the product, then destroyed.
#
#   ./hack/gcp-playground.sh            twenty minutes, then gone
#   PLAY_MINUTES=30 ./hack/gcp-playground.sh
#   ./hack/gcp-playground.sh clean      delete a playground left behind
#
# Design rules, in order of importance:
#   1. It always tears down. The countdown ends in deletion; Ctrl-C ends in
#      deletion; a mid-flight failure ends in deletion. The only way to keep
#      the cluster is to kill -9 the script, and even then `clean` and
#      hack/gke-leftovers.sh know where to look.
#   2. It never launches silently. It prints what it will create and what
#      that costs, and waits for a typed yes.
#   3. It leaves your kubeconfig alone. The current context is restored the
#      moment credentials are fetched; every command here is --context'd.
#
# The scenario is drawn at random from the demo chart's set, and the fabric
# size varies run to run, so each playground is a different cluster to read.
# KWOK nodes are free; the bill is the handful of real nodes underneath.

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"

PLAY_MINUTES="${PLAY_MINUTES:-20}"
CLUSTER="${CLUSTER:-dencer-play}"
CTX="gke-play"
GCP_ZONE="${GCP_ZONE:-us-central1-a}"
GCP_MACHINE="${GCP_MACHINE:-e2-medium}"
REAL_NODES="${REAL_NODES:-3}"
NS="k8s-dencer"
DEMO_NS="dencer-demo"
KWOK_NS="kwok"
KWOK_CHART_VERSION="${KWOK_CHART_VERSION:-0.3.0}"
RELEASE="k8s-dencer"
UI_PORT="${UI_PORT:-8092}"
GHCR_TAG="${GHCR_TAG:-$(awk '/^appVersion:/ {gsub(/"/,"",$2); print $2}' "$REPO/charts/k8s-dencer/Chart.yaml")}"

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }
fail()  { red "ERROR: $*"; exit 1; }

# ------------------------------------------------------------- the draw
# One scenario per run, drawn at random so the playground is a different
# cluster to read each time. The fabric size varies too — enough nodes that
# the plan has something to say, few enough that the control plane is bored.
SCENARIOS=(a-fragmented b-pdb-blocked c-topology-spread d-anti-affinity e-tainted-pool f-stateful g-showcase)
SCENARIO="${SCENARIO:-${SCENARIOS[$((RANDOM % ${#SCENARIOS[@]}))]}}"
FABRIC_NODES="${FABRIC_NODES:-$((24 + RANDOM % 17))}"

# ------------------------------------------------------- context hygiene
ORIGINAL_CTX="$(kubectl config current-context 2>/dev/null || true)"
restore_context() {
  if [[ -n "$ORIGINAL_CTX" ]]; then
    kubectl config use-context "$ORIGINAL_CTX" >/dev/null 2>&1 || true
  fi
}

cluster_delete() {
  echo "  deleting the GKE cluster (this takes a few minutes)…"
  if ! gcloud container clusters delete "$CLUSTER" --zone "$GCP_ZONE" --quiet 2>/tmp/dencer-play-del.err; then
    if grep -qiE "not found|already being deleted|is currently being (deleted|repaired)" /tmp/dencer-play-del.err; then
      green "  already gone"
    else
      red "COULD NOT DELETE CLUSTER ${CLUSTER} in ${GCP_ZONE} — delete it by hand, it is costing money:"
      red "  gcloud container clusters delete ${CLUSTER} --zone ${GCP_ZONE}"
      sed 's/^/    /' /tmp/dencer-play-del.err
    fi
  fi
  kubectl config delete-context "$CTX" >/dev/null 2>&1 || true
}

if [[ "${1:-}" == "clean" ]]; then
  cluster_delete
  restore_context
  green "removed"
  exit 0
fi

# ------------------------------------------------------------- preflight
for bin in gcloud kubectl helm python3; do
  command -v "$bin" >/dev/null || fail "$bin is required"
done
project="$(gcloud config get-value project 2>/dev/null)"
[[ -n "$project" && "$project" != "(unset)" ]] || fail "no gcloud project set. Run 'make gke-setup' first"

# ---------------------------------------------------------- the consent
# Rough ceiling, stated before anything exists: N e2-medium on-demand nodes
# at ~\$0.034/h each, for the window plus ~10 minutes of create/delete.
est_cents="$(python3 -c "print(round($REAL_NODES * 3.4 * (($PLAY_MINUTES + 10) / 60), 1))")"
bold "==> the playground, before it exists"
echo "    project    ${project}"
echo "    cluster    ${CLUSTER} (${REAL_NODES}× ${GCP_MACHINE}, zone ${GCP_ZONE})"
echo "    scenario   ${SCENARIO} on a ${FABRIC_NODES}-node KWOK fabric (fake nodes, free)"
echo "    images     ghcr.io/atedgimo/k8s-dencer-*:${GHCR_TAG}"
echo "    window     ${PLAY_MINUTES} minutes, then the cluster is deleted"
echo "    cost       roughly ${est_cents}¢ if the teardown runs; the teardown always runs"
echo
printf 'Type yes to create billable GCP resources: '
read -r answer
[[ "$answer" == "yes" ]] || { echo "nothing created"; exit 0; }

# From here on, the cluster may exist: every exit path must try to delete it.
PORT_FORWARD_PID=""
teardown() {
  trap - EXIT INT TERM
  echo
  bold "==> teardown"
  [[ -n "$PORT_FORWARD_PID" ]] && kill "$PORT_FORWARD_PID" >/dev/null 2>&1 || true
  cluster_delete
  restore_context
  bold "==> anything billable left?"
  "$REPO/hack/gke-leftovers.sh" || true
  green "playground over"
}
trap teardown EXIT INT TERM

# --------------------------------------------------------------- cluster
bold "==> GKE cluster (${REAL_NODES}× ${GCP_MACHINE})"
gcloud container clusters create "$CLUSTER" \
  --zone "$GCP_ZONE" \
  --num-nodes "$REAL_NODES" \
  --machine-type "$GCP_MACHINE" \
  --disk-type pd-standard --disk-size 20 \
  --enable-autoscaling --min-nodes 1 --max-nodes "$((REAL_NODES + 1))" \
  --no-enable-autoupgrade --no-enable-autorepair \
  --labels purpose=dencer-playground \
  --quiet >/dev/null || fail "could not create the GKE cluster"

gcloud container clusters get-credentials "$CLUSTER" --zone "$GCP_ZONE" --quiet 2>/dev/null
kubectl config rename-context "$(kubectl config current-context)" "$CTX" >/dev/null 2>&1 || true
# get-credentials makes the new context current, which silently redirects
# every other terminal on the machine. Hand it straight back.
restore_context
green "  up"

# ---------------------------------------------------------------- fabric
bold "==> KWOK fabric + scenario ${SCENARIO} (${FABRIC_NODES} fake nodes)"
helm repo add kwok https://kwok.sigs.k8s.io/charts/ >/dev/null 2>&1 || true
helm repo update kwok >/dev/null 2>&1
helm --kube-context "$CTX" upgrade --install kwok kwok/kwok --version "$KWOK_CHART_VERSION" \
  --namespace "$KWOK_NS" --create-namespace \
  -f "$REPO/demo/kwok-values.yaml" --wait --timeout 3m >/dev/null
helm --kube-context "$CTX" upgrade --install kwok-stage-fast kwok/stage-fast --version "$KWOK_CHART_VERSION" \
  --namespace "$KWOK_NS" --wait --timeout 3m >/dev/null
helm --kube-context "$CTX" upgrade --install dencer-demo "$REPO/demo/charts/dencer-demo" \
  --namespace "$DEMO_NS" --create-namespace \
  --set scenario="$SCENARIO" \
  --set nodes.count="$FABRIC_NODES" \
  --wait --timeout 3m >/dev/null
green "  fabric up"

# --------------------------------------------------------------- product
bold "==> k8s-dencer ${GHCR_TAG} (published images)"
helm --kube-context "$CTX" upgrade --install "$RELEASE" "$REPO/charts/k8s-dencer" \
  --namespace "$NS" --create-namespace \
  --set auth.enabled=true \
  --set persistence.enabled=true \
  --set-string persistence.storageClass=standard-rwo \
  --set executor.enabled=true \
  --set executor.readiness=Running \
  --set planner.minNodeAge=30s \
  --set safety.maxNodesPerRun=8 \
  --set safety.minReadyNodes=5 \
  --set-string planner.image.registry=ghcr.io \
  --set-string uiBackend.image.registry=ghcr.io \
  --set-string executor.image.registry=ghcr.io \
  --set-string uiFrontend.image.registry=ghcr.io \
  --set-string planner.image.repository=atedgimo/k8s-dencer-planner \
  --set-string uiBackend.image.repository=atedgimo/k8s-dencer-ui-backend \
  --set-string executor.image.repository=atedgimo/k8s-dencer-executor \
  --set-string uiFrontend.image.repository=atedgimo/k8s-dencer-ui-frontend \
  --set-string planner.image.tag="$GHCR_TAG" \
  --set-string uiBackend.image.tag="$GHCR_TAG" \
  --set-string executor.image.tag="$GHCR_TAG" \
  --set-string uiFrontend.image.tag="$GHCR_TAG" \
  --wait --timeout 300s >/dev/null || {
    kubectl --context "$CTX" -n "$NS" get pods
    fail "chart did not install"
  }
green "  installed"

# ---------------------------------------------------------------- access
bold "==> access"
kubectl --context "$CTX" get serviceaccount dencer-operator -n "$NS" >/dev/null 2>&1 \
  || kubectl --context "$CTX" create serviceaccount dencer-operator -n "$NS" >/dev/null
kubectl --context "$CTX" get rolebinding dencer-operator -n "$NS" >/dev/null 2>&1 \
  || kubectl --context "$CTX" create rolebinding dencer-operator -n "$NS" \
      --clusterrole="${RELEASE}-consolidation-operator" \
      --serviceaccount="${NS}:dencer-operator" >/dev/null
TOKEN="$(kubectl --context "$CTX" create token dencer-operator -n "$NS" --duration="${PLAY_MINUTES}m")"

kubectl --context "$CTX" -n "$NS" port-forward "svc/${RELEASE}-ui-frontend" "${UI_PORT}:80" >/dev/null 2>&1 &
PORT_FORWARD_PID=$!
sleep 2

echo
green  "  UI      http://localhost:${UI_PORT}"
echo   "  token   (valid ${PLAY_MINUTES}m — paste into the sign-in field)"
echo   "  ${TOKEN}"
echo
echo   "  scenario ${SCENARIO}: watch the plan explain it. Drain something safe;"
echo   "  the fabric's fake nodes drain instantly, and any REAL node you drain"
echo   "  is Google's autoscaler's to remove (~11 minutes, observed)."
echo

# ------------------------------------------------------------- the clock
bold "==> ${PLAY_MINUTES} minutes on the clock"
deadline=$(( $(date +%s) + PLAY_MINUTES * 60 ))
announced=""
while true; do
  left=$(( deadline - $(date +%s) ))
  (( left <= 0 )) && break
  # Announce crossings, not exact seconds — a 5s sleep never lands on :00.
  for mark in 600 300 60; do
    if (( left <= mark )) && [[ "$announced" != *"$mark"* ]]; then
      announced="$announced $mark"
      echo "    $(( mark / 60 )) minute(s) left"
    fi
  done
  sleep 5
done
echo "    time is up"
# teardown runs via the EXIT trap
