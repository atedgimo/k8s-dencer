#!/usr/bin/env bash
#
# A timed GCP playground: a real GKE cluster, REAL workloads shaped by a
# random scenario, the published k8s-dencer release installed — live for a
# fixed window so a human can play with the product, then destroyed.
#
#   ./hack/gcp-playground.sh            twenty minutes, then gone
#   PLAY_MINUTES=30 ./hack/gcp-playground.sh
#   PLAY_FABRIC=kwok ./hack/gcp-playground.sh   fake-node fabric instead
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
# Real nodes are the default because they are the honest demo: real pods
# with real probes, drains through the real eviction API, and any node you
# free is Google's own autoscaler's to remove — observed at ~11 minutes,
# inside the window. The KWOK variant survives behind PLAY_FABRIC=kwok for
# when a bigger, free fleet matters more than realness.

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"

PLAY_MINUTES="${PLAY_MINUTES:-20}"
PLAY_FABRIC="${PLAY_FABRIC:-real}"
CLUSTER="${CLUSTER:-dencer-play}"
CTX="gke-play"
GCP_ZONE="${GCP_ZONE:-us-central1-a}"
GCP_SPOT="${GCP_SPOT:-auto}"
NS="k8s-dencer"
DEMO_NS="dencer-demo"
KWOK_NS="kwok"
KWOK_CHART_VERSION="${KWOK_CHART_VERSION:-0.3.0}"
RELEASE="k8s-dencer"
UI_PORT="${UI_PORT:-8092}"
GHCR_TAG="${GHCR_TAG:-$(awk '/^appVersion:/ {gsub(/"/,"",$2); print $2}' "$REPO/charts/k8s-dencer/Chart.yaml")}"

# The machine profile is part of the draw: a wide fleet of shared-core
# smalls costs the same as a narrow fleet of mediums and reads differently
# in the plan. Both are honest; free GKE nodes do not exist — the free tier
# covers the zonal cluster fee (already used) and nothing that can be a
# node. Spot is the real discount, applied below when quota allows.
if [[ "$PLAY_FABRIC" == "real" ]]; then
  if [[ -z "${GCP_MACHINE:-}" ]]; then
    if (( RANDOM % 2 )); then GCP_MACHINE="e2-small";  REAL_NODES="${REAL_NODES:-8}"
    else                      GCP_MACHINE="e2-medium"; REAL_NODES="${REAL_NODES:-6}"
    fi
  fi
  REAL_NODES="${REAL_NODES:-6}"
else
  GCP_MACHINE="${GCP_MACHINE:-e2-medium}"
  REAL_NODES="${REAL_NODES:-3}"
fi

# On-demand cents/hour, for the consent line's ceiling. Spot, when granted,
# only makes the printed number an overestimate — the safe direction.
case "$GCP_MACHINE" in
  e2-small)  MACHINE_CENTS_H=1.7 ;;
  e2-medium) MACHINE_CENTS_H=3.4 ;;
  *)         MACHINE_CENTS_H=3.4 ;;
esac

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
red()   { printf '\033[31m%s\033[0m\n' "$*"; }
fail()  { red "ERROR: $*"; exit 1; }

# ------------------------------------------------------------- the draw
# One scenario per run, drawn at random so the playground is a different
# cluster to read each time. g-showcase stays KWOK-only: its heterogeneous
# fleet is built of fake node shapes real machines cannot fake.
if [[ "$PLAY_FABRIC" == "real" ]]; then
  SCENARIOS=(a-fragmented b-pdb-blocked c-topology-spread d-anti-affinity e-tainted-pool f-stateful)
else
  SCENARIOS=(a-fragmented b-pdb-blocked c-topology-spread d-anti-affinity e-tainted-pool f-stateful g-showcase)
fi
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
  # The context is only a third of the residue: get-credentials also wrote
  # cluster and user entries under the canonical GKE name, and leaving them
  # behind is how a kubeconfig fills with pointers to machines that no
  # longer exist — found on a real workstation, as a dangling current
  # context erroring at localhost:8080.
  kubectl config delete-context "$CTX" >/dev/null 2>&1 || true
  gke_name="gke_${project:-$(gcloud config get-value project 2>/dev/null)}_${GCP_ZONE}_${CLUSTER}"
  kubectl config delete-cluster "$gke_name" >/dev/null 2>&1 || true
  kubectl config delete-user "$gke_name" >/dev/null 2>&1 || true
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
est_cents="$(python3 -c "print(round($REAL_NODES * $MACHINE_CENTS_H * (($PLAY_MINUTES + 10) / 60), 1))")"
bold "==> the playground, before it exists"
echo "    project    ${project}"
echo "    cluster    ${CLUSTER} (${REAL_NODES}× ${GCP_MACHINE}, zone ${GCP_ZONE})"
if [[ "$PLAY_FABRIC" == "real" ]]; then
  echo "    scenario   ${SCENARIO}, as REAL workloads on the real nodes"
else
  echo "    scenario   ${SCENARIO} on a ${FABRIC_NODES}-node KWOK fabric (fake nodes, free)"
fi
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
  # Deleting the cluster orphans its PVC-backed disks: the CSI driver that
  # would release them dies with the control plane. Found live — three 1GB
  # pvc-* disks quietly billing across runs. GKE labels every dynamically
  # provisioned disk with its cluster name, so ours are addressable exactly.
  orphans="$(gcloud compute disks list \
    --filter="labels.goog-k8s-cluster-name=${CLUSTER} AND -users:*" \
    --format="value(name)" 2>/dev/null || true)"
  if [[ -n "$orphans" ]]; then
    echo "  deleting orphaned volume disk(s): $(echo "$orphans" | tr '\n' ' ')"
    # shellcheck disable=SC2086
    gcloud compute disks delete $orphans --zone "$GCP_ZONE" --quiet >/dev/null 2>&1 || true
  fi
  bold "==> anything billable left?"
  "$REPO/hack/gke-leftovers.sh" || true
  green "playground over"
}
trap teardown EXIT INT TERM

# --------------------------------------------------------------- cluster
# Spot when the region's preemptible quota can hold the fleet — the same
# quota-aware auto the e2e harness uses. A reclaimed Spot node mid-window is
# not a bug here; it is the observed overlay's favourite weather.
spot_flag=()
case "$GCP_SPOT" in
  auto)
    limit="$(gcloud compute regions describe "${GCP_ZONE%-*}" --format=json 2>/dev/null \
      | python3 -c 'import json,sys
try: print(int({x["metric"]: x["limit"] for x in json.load(sys.stdin).get("quotas",[])}.get("PREEMPTIBLE_CPUS",0)))
except Exception: print(0)')"
    if [[ "${limit:-0}" -ge $(( REAL_NODES * 2 )) ]]; then
      spot_flag=(--spot)
    fi
    ;;
  1|true|yes) spot_flag=(--spot) ;;
  *) ;;
esac

bold "==> GKE cluster (${REAL_NODES}× ${GCP_MACHINE}${spot_flag:+, Spot})"
gcloud container clusters create "$CLUSTER" \
  --zone "$GCP_ZONE" \
  --num-nodes "$REAL_NODES" \
  --machine-type "$GCP_MACHINE" \
  "${spot_flag[@]}" \
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

# ------------------------------------------------------- real workloads
# Restricted-PSS-compliant nginx with real readiness probes, sized so six
# e2-mediums end up fragmented: enough spread that the plan frees a node or
# two, enough headroom that every eviction has somewhere to land. The
# scenario adds the constraint that should change the ratings — the same
# grammar as the demo chart, spoken by real pods.
real_base() {
  cat <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: { name: play-web, namespace: ${DEMO_NS}, labels: { app: play-web } }
spec:
  replicas: 8
  selector: { matchLabels: { app: play-web } }
  template:
    metadata: { labels: { app: play-web } }
    spec:
      securityContext: { runAsNonRoot: true, seccompProfile: { type: RuntimeDefault } }
      containers:
        - name: web
          image: nginxinc/nginx-unprivileged:1.27-alpine
          ports: [{ containerPort: 8080 }]
          securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ["ALL"] } }
          resources: { requests: { cpu: 150m, memory: 64Mi }, limits: { cpu: 300m, memory: 128Mi } }
          readinessProbe: { httpGet: { path: /, port: 8080 }, initialDelaySeconds: 2, periodSeconds: 3 }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: play-cache, namespace: ${DEMO_NS}, labels: { app: play-cache } }
spec:
  replicas: 5
  selector: { matchLabels: { app: play-cache } }
  template:
    metadata: { labels: { app: play-cache } }
    spec:
      securityContext: { runAsNonRoot: true, seccompProfile: { type: RuntimeDefault } }
      containers:
        - name: web
          image: nginxinc/nginx-unprivileged:1.27-alpine
          ports: [{ containerPort: 8080 }]
          securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ["ALL"] } }
          resources: { requests: { cpu: 120m, memory: 64Mi }, limits: { cpu: 250m, memory: 128Mi } }
          readinessProbe: { httpGet: { path: /, port: 8080 }, initialDelaySeconds: 2, periodSeconds: 3 }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: play-filler, namespace: ${DEMO_NS}, labels: { app: play-filler } }
spec:
  replicas: 6
  selector: { matchLabels: { app: play-filler } }
  template:
    metadata: { labels: { app: play-filler } }
    spec:
      securityContext: { runAsNonRoot: true, seccompProfile: { type: RuntimeDefault } }
      containers:
        - name: web
          image: nginxinc/nginx-unprivileged:1.27-alpine
          ports: [{ containerPort: 8080 }]
          securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ["ALL"] } }
          resources: { requests: { cpu: 100m, memory: 48Mi }, limits: { cpu: 200m, memory: 96Mi } }
          readinessProbe: { httpGet: { path: /, port: 8080 }, initialDelaySeconds: 2, periodSeconds: 3 }
EOF
}

real_scenario() {
  case "$SCENARIO" in
    a-fragmented) ;; # the base alone: pure bin-packing headroom
    b-pdb-blocked) cat <<EOF
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: play-payments, namespace: ${DEMO_NS}, labels: { app: play-payments } }
spec:
  replicas: 2
  selector: { matchLabels: { app: play-payments } }
  template:
    metadata: { labels: { app: play-payments } }
    spec:
      securityContext: { runAsNonRoot: true, seccompProfile: { type: RuntimeDefault } }
      containers:
        - name: web
          image: nginxinc/nginx-unprivileged:1.27-alpine
          ports: [{ containerPort: 8080 }]
          securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ["ALL"] } }
          resources: { requests: { cpu: 150m, memory: 64Mi }, limits: { cpu: 300m, memory: 128Mi } }
          readinessProbe: { httpGet: { path: /, port: 8080 }, initialDelaySeconds: 2, periodSeconds: 3 }
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata: { name: play-payments, namespace: ${DEMO_NS} }
spec:
  minAvailable: 2
  selector: { matchLabels: { app: play-payments } }
EOF
      ;;
    c-topology-spread) cat <<EOF
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: play-checkout, namespace: ${DEMO_NS}, labels: { app: play-checkout } }
spec:
  replicas: 4
  selector: { matchLabels: { app: play-checkout } }
  template:
    metadata: { labels: { app: play-checkout } }
    spec:
      securityContext: { runAsNonRoot: true, seccompProfile: { type: RuntimeDefault } }
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: DoNotSchedule
          labelSelector: { matchLabels: { app: play-checkout } }
      containers:
        - name: web
          image: nginxinc/nginx-unprivileged:1.27-alpine
          ports: [{ containerPort: 8080 }]
          securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ["ALL"] } }
          resources: { requests: { cpu: 120m, memory: 64Mi }, limits: { cpu: 250m, memory: 128Mi } }
          readinessProbe: { httpGet: { path: /, port: 8080 }, initialDelaySeconds: 2, periodSeconds: 3 }
EOF
      ;;
    d-anti-affinity) cat <<EOF
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: play-broker, namespace: ${DEMO_NS}, labels: { app: play-broker } }
spec:
  replicas: 3
  selector: { matchLabels: { app: play-broker } }
  template:
    metadata: { labels: { app: play-broker } }
    spec:
      securityContext: { runAsNonRoot: true, seccompProfile: { type: RuntimeDefault } }
      affinity:
        podAntiAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            - topologyKey: kubernetes.io/hostname
              labelSelector: { matchLabels: { app: play-broker } }
      containers:
        - name: web
          image: nginxinc/nginx-unprivileged:1.27-alpine
          ports: [{ containerPort: 8080 }]
          securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ["ALL"] } }
          resources: { requests: { cpu: 120m, memory: 64Mi }, limits: { cpu: 250m, memory: 128Mi } }
          readinessProbe: { httpGet: { path: /, port: 8080 }, initialDelaySeconds: 2, periodSeconds: 3 }
EOF
      ;;
    e-tainted-pool) cat <<EOF
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: play-ledger, namespace: ${DEMO_NS}, labels: { app: play-ledger } }
spec:
  replicas: 2
  selector: { matchLabels: { app: play-ledger } }
  template:
    metadata: { labels: { app: play-ledger } }
    spec:
      securityContext: { runAsNonRoot: true, seccompProfile: { type: RuntimeDefault } }
      tolerations:
        - { key: dedicated, operator: Equal, value: play, effect: NoSchedule }
      nodeSelector: { dencer-play/dedicated: "true" }
      containers:
        - name: web
          image: nginxinc/nginx-unprivileged:1.27-alpine
          ports: [{ containerPort: 8080 }]
          securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ["ALL"] } }
          resources: { requests: { cpu: 120m, memory: 64Mi }, limits: { cpu: 250m, memory: 128Mi } }
          readinessProbe: { httpGet: { path: /, port: 8080 }, initialDelaySeconds: 2, periodSeconds: 3 }
EOF
      ;;
    f-stateful) cat <<EOF
---
apiVersion: apps/v1
kind: StatefulSet
metadata: { name: play-ledgerdb, namespace: ${DEMO_NS}, labels: { app: play-ledgerdb } }
spec:
  replicas: 2
  serviceName: play-ledgerdb
  selector: { matchLabels: { app: play-ledgerdb } }
  template:
    metadata: { labels: { app: play-ledgerdb } }
    spec:
      securityContext: { runAsNonRoot: true, seccompProfile: { type: RuntimeDefault } }
      containers:
        - name: web
          image: nginxinc/nginx-unprivileged:1.27-alpine
          ports: [{ containerPort: 8080 }]
          securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ["ALL"] } }
          resources: { requests: { cpu: 120m, memory: 64Mi }, limits: { cpu: 250m, memory: 128Mi } }
          readinessProbe: { httpGet: { path: /, port: 8080 }, initialDelaySeconds: 2, periodSeconds: 3 }
---
apiVersion: v1
kind: Service
metadata: { name: play-ledgerdb, namespace: ${DEMO_NS} }
spec:
  clusterIP: None
  selector: { app: play-ledgerdb }
  ports: [{ port: 8080 }]
---
apiVersion: v1
kind: Pod
metadata: { name: play-adhoc, namespace: ${DEMO_NS}, labels: { app: play-adhoc } }
spec:
  securityContext: { runAsNonRoot: true, seccompProfile: { type: RuntimeDefault } }
  containers:
    - name: web
      image: nginxinc/nginx-unprivileged:1.27-alpine
      ports: [{ containerPort: 8080 }]
      securityContext: { allowPrivilegeEscalation: false, capabilities: { drop: ["ALL"] } }
      resources: { requests: { cpu: 100m, memory: 48Mi }, limits: { cpu: 200m, memory: 96Mi } }
      readinessProbe: { httpGet: { path: /, port: 8080 }, initialDelaySeconds: 2, periodSeconds: 3 }
EOF
      ;;
  esac
}

install_real_fabric() {
  bold "==> real workloads, scenario ${SCENARIO}"
  kubectl --context "$CTX" create namespace "$DEMO_NS" >/dev/null
  # The same PodSecurity bar the e2e holds: if any playground pod violates
  # restricted, admission refuses it here rather than a reviewer noticing.
  kubectl --context "$CTX" label namespace "$DEMO_NS" \
    pod-security.kubernetes.io/enforce=restricted \
    pod-security.kubernetes.io/enforce-version=latest >/dev/null

  if [[ "$SCENARIO" == "e-tainted-pool" ]]; then
    # A real dedicated pool: one machine tainted and labelled, and a workload
    # that tolerates it. The plan has to reason about a node most pods
    # cannot land on.
    dedicated="$(kubectl --context "$CTX" get nodes -o jsonpath='{.items[0].metadata.name}')"
    kubectl --context "$CTX" taint node "$dedicated" dedicated=play:NoSchedule --overwrite >/dev/null
    kubectl --context "$CTX" label node "$dedicated" dencer-play/dedicated=true --overwrite >/dev/null
    echo "    tainted ${dedicated} as the dedicated pool"
  fi

  { real_base; real_scenario; } | kubectl --context "$CTX" apply -f - >/dev/null

  # Rollouts, not scheduling: real pods must pass their real probes before
  # the planner reads a cluster worth planning.
  for d in $(kubectl --context "$CTX" -n "$DEMO_NS" get deploy -o name); do
    kubectl --context "$CTX" -n "$DEMO_NS" rollout status "$d" --timeout=3m >/dev/null
  done
  if kubectl --context "$CTX" -n "$DEMO_NS" get statefulset play-ledgerdb >/dev/null 2>&1; then
    kubectl --context "$CTX" -n "$DEMO_NS" rollout status statefulset/play-ledgerdb --timeout=3m >/dev/null
  fi
  green "  workloads Ready on real nodes"
}

install_kwok_fabric() {
  bold "==> KWOK fabric + scenario ${SCENARIO} (${FABRIC_NODES} fake nodes)"
  helm repo add kwok https://kwok.sigs.k8s.io/charts/ >/dev/null 2>&1 || true
  helm repo update kwok >/dev/null 2>&1
  # Rendered and filtered rather than helm-installed: the kwok chart
  # hard-codes a FlowSchema referencing the cluster-critical "exempt"
  # priority level, and GKE's flowcontrol guardrail webhook denies exactly
  # that. helm template also skips namespace injection and the crds/
  # directory, hence -n and --include-crds — one live launch per lesson.
  kubectl --context "$CTX" create namespace "$KWOK_NS" --dry-run=client -o yaml \
    | kubectl --context "$CTX" apply -f - >/dev/null
  helm template kwok kwok/kwok --version "$KWOK_CHART_VERSION" \
    --namespace "$KWOK_NS" --include-crds -f "$REPO/demo/kwok-values.yaml" \
    | python3 -c 'import sys; print("\n---".join(d for d in sys.stdin.read().split("\n---") if "kind: FlowSchema" not in d))' \
    | kubectl --context "$CTX" -n "$KWOK_NS" apply -f - >/dev/null
  kubectl --context "$CTX" wait --for=condition=established crd/stages.kwok.x-k8s.io --timeout=60s >/dev/null
  kubectl --context "$CTX" -n "$KWOK_NS" rollout status deployment/kwok-controller --timeout=3m >/dev/null
  helm --kube-context "$CTX" upgrade --install kwok-stage-fast kwok/stage-fast --version "$KWOK_CHART_VERSION" \
    --namespace "$KWOK_NS" --wait --timeout 3m >/dev/null
  helm --kube-context "$CTX" upgrade --install dencer-demo "$REPO/demo/charts/dencer-demo" \
    --namespace "$DEMO_NS" --create-namespace \
    --set scenario="$SCENARIO" \
    --set nodes.count="$FABRIC_NODES" \
    --wait --timeout 3m >/dev/null
  green "  fabric up"
}

if [[ "$PLAY_FABRIC" == "real" ]]; then
  install_real_fabric
  # Real pods have real probes, so the executor keeps its honest default.
  READINESS_SET=()
  SAFETY_SET=(--set safety.maxNodesPerRun=3 --set safety.minReadyNodes=3)
else
  install_kwok_fabric
  # KWOK pods reach Running and never Ready; only this fabric weakens it.
  READINESS_SET=(--set executor.readiness=Running)
  SAFETY_SET=(--set safety.maxNodesPerRun=8 --set safety.minReadyNodes=5)
fi

# --------------------------------------------------------------- product
bold "==> k8s-dencer ${GHCR_TAG} (published images)"
helm --kube-context "$CTX" upgrade --install "$RELEASE" "$REPO/charts/k8s-dencer" \
  --namespace "$NS" --create-namespace \
  --set auth.enabled=true \
  --set persistence.enabled=true \
  --set-string persistence.storageClass=standard-rwo \
  --set executor.enabled=true \
  "${READINESS_SET[@]}" \
  "${SAFETY_SET[@]}" \
  --set planner.minNodeAge=30s \
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
  --wait --timeout 480s >/dev/null || {
    # A failure here must explain itself: the fourth live launch died at
    # exactly the old 300s wait with nothing but a pod list, and the error
    # scrolled away with the window. Wide pods plus the event tail is the
    # difference between a diagnosis and a mystery. The timeout is longer
    # too — first pulls of four images onto shared-core machines can
    # honestly take more than five minutes.
    kubectl --context "$CTX" -n "$NS" get pods -o wide || true
    echo "--- recent events ---"
    kubectl --context "$CTX" -n "$NS" get events --sort-by=.lastTimestamp 2>/dev/null | tail -15 || true
    fail "chart did not install (diagnostics above)"
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
if [[ "$PLAY_FABRIC" == "real" ]]; then
  echo "  scenario ${SCENARIO}, on real machines: every drain is a real eviction,"
  echo "  and a node you free is Google's autoscaler's to remove — ~11 minutes,"
  echo "  observed. Start it early if you want to watch the ledger move."
else
  echo "  scenario ${SCENARIO}: the fabric's fake nodes drain instantly; any REAL"
  echo "  node you drain is Google's autoscaler's to remove (~11 minutes)."
fi
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
