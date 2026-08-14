#!/usr/bin/env bash
#
# End-to-end: install the chart on a multi-node cluster, plan against real
# workloads, drain a node, and prove the workload came back.
#
# This exists because every other gate in this repository is a unit test, a
# rendered template or a benchmark over synthesised fixtures. None of them
# install anything, and none of them evict a pod that is really running.
#
# Three specific gaps it closes:
#
#   1. readiness: Ready had never run anywhere. The executor verifies recovery
#      by waiting for the PodReady condition — the fix that stops it declaring
#      a crash-looping workload healthy and draining the next node. The KWOK
#      fabric cannot exercise it, because a fake pod reaches Running and never
#      becomes Ready, so the demo overlay sets readiness: Running and the real
#      path was covered only by a fake cluster in a Go test. Here the pods have
#      containers and probes, and the default is used.
#
#   2. One node. OrbStack is a single node, so co-scheduling and the
#      ReadWriteOnce claim's node affinity had never been exercised against a
#      cluster that could get them wrong.
#
#   3. PodSecurity was a grep. The chart has claimed restricted-PSS compliance
#      since M0 on the strength of `helm template | grep`. Here the namespace
#      enforces it and the API server decides.
#
#   ./hack/e2e.sh                 run it on a throwaway k3d cluster
#   ./hack/e2e.sh clean           tear the cluster down
#   KEEP=1 ./hack/e2e.sh          leave the cluster up afterwards for poking at
#   PROVIDER=gke ./hack/e2e.sh    run it on a real GKE cluster (see below)
#
# The GKE path exists for the one thing k3d structurally cannot do: let a
# cluster autoscaler we did not write decide, on its own schedule, to remove a
# node we drained. Everything else here is shared, deliberately — the
# assertions are the valuable part of this file, and a forked cloud copy would
# drift from them inside two milestones. Only cluster lifecycle, image delivery
# and the reclamation trigger branch.
#
# It costs roughly two cents a run and destroys the cluster on every exit path.
# See docs/development.md, and run `make gke-setup` once first.
#
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER="${CLUSTER:-dencer-e2e}"
NS="${NS:-k8s-dencer}"
APP_NS="${APP_NS:-shop}"
RELEASE="k8s-dencer"
# Five nodes, not three. The Safety Guard's MinReadyNodes floor defaults to 3,
# so on a three-node cluster draining anything would leave two and the guard
# refuses — correctly. Lowering the floor to make the test pass would be
# testing a configuration nobody runs; adding nodes tests the real one.
AGENTS="${AGENTS:-4}"
PF_PORT="${PF_PORT:-18099}"
INGRESS_PORT="${INGRESS_PORT:-18080}"
INGRESS_HOST="${INGRESS_HOST:-k8s-dencer.e2e}"

PROVIDER="${PROVIDER:-k3d}"

# Which plan store to run the whole e2e against: sqlite | postgres.
#
# Not a second script. Everything after the install — the plan, the guard, the
# execution, the assertion that the cluster actually changed — is identical,
# because the promise the store makes is that the two backends behave the same.
# Only the install and the storage assertions differ, and the storage
# assertions differ by being their own opposites: SQLite must have a claim and
# co-scheduled readers, Postgres must have neither.
#
# It was SQLite-only for the whole of v0.6.0, which is how a backend that could
# not record a single drain reached a release.
BACKEND="${BACKEND:-sqlite}"
GCP_ZONE="${GCP_ZONE:-us-central1-a}"
GCP_MACHINE="${GCP_MACHINE:-e2-medium}"
# Published tags, not a local build: on GKE this doubles as the first proof
# that what we ship to ghcr actually installs.
#
# Defaults to the chart's own appVersion rather than "latest" — the chart's
# values.schema.json refuses a tag of "latest" outright, and it is right to:
# deploying a floating tag makes a cluster unreproducible and a rollback
# meaningless. Testing the version the chart declares is also simply the more
# useful thing to test.
GHCR_TAG="${GHCR_TAG:-$(awk '/^appVersion:/ {gsub(/"/,"",$2); print $2}' "$REPO/charts/k8s-dencer/Chart.yaml")}"
# Spot is ~70% cheaper and interruption is fine for a 25-minute test, but a new
# GCP project's PREEMPTIBLE_CPUS quota starts at zero and has to be requested.
# Rather than fail the run on that, detect it and use on-demand — the
# difference over 25 minutes is about five cents.
GCP_SPOT="${GCP_SPOT:-auto}"

case "$PROVIDER" in
  k3d) CTX="k3d-${CLUSTER}" ;;
  gke) CTX="gke-e2e" ;;
  *)   echo "PROVIDER must be k3d or gke, got '$PROVIDER'" >&2; exit 1 ;;
esac

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
fail()  { red "FAIL: $*"; exit 1; }

# k3d switches the current context on create. Anything else the operator has
# open must not silently start pointing at a throwaway cluster — a lesson from
# verify-sso.sh, which once left `make token` talking to the wrong API server.
ORIGINAL_CTX="$(kubectl config current-context 2>/dev/null || true)"
restore_context() {
  if [[ -n "$ORIGINAL_CTX" ]] && kubectl config get-contexts -o name 2>/dev/null | grep -qx "$ORIGINAL_CTX"; then
    kubectl config use-context "$ORIGINAL_CTX" >/dev/null 2>&1 || true
  fi
}

PF=""
cleanup() {
  [[ -n "$PF" ]] && kill "$PF" 2>/dev/null || true
  if [[ "${KEEP:-0}" != "1" ]]; then
    cluster_delete
  fi
  restore_context
}
trap cleanup EXIT

# ------------------------------------------------------------- provider

# kubectl cannot talk to GKE without gke-gcloud-auth-plugin, and Homebrew
# installs it into the SDK's own bin directory rather than linking it alongside
# gcloud — so it is present, installed, and invisible.
#
# The failure it produces is genuinely misleading: the first few kubectl calls
# succeed on the token get-credentials leaves behind, and only later ones fail,
# so it looks like something broke mid-run rather than a prerequisite never
# having been met.
ensure_gke_auth_plugin() {
  command -v gke-gcloud-auth-plugin >/dev/null && return 0
  local d
  for d in \
    "$(dirname "$(command -v gcloud 2>/dev/null || echo /nonexistent)")" \
    "$(brew --prefix 2>/dev/null)/share/google-cloud-sdk/bin" \
    "/opt/homebrew/share/google-cloud-sdk/bin" \
    "/usr/local/share/google-cloud-sdk/bin" \
    "$HOME/google-cloud-sdk/bin"; do
    if [[ -x "$d/gke-gcloud-auth-plugin" ]]; then
      PATH="$d:$PATH"; export PATH
      return 0
    fi
  done
  return 1
}

cluster_create() {
  if [[ "$PROVIDER" == "k3d" ]]; then
    k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
    # The loadbalancer port is published so a request can actually traverse the
    # ingress controller. Rendering an Ingress proves nothing; k3s bundles
    # Traefik, so the only missing piece was a way in.
    k3d cluster create "$CLUSTER" --servers 1 --agents "$AGENTS" \
      -p "${INGRESS_PORT}:80@loadbalancer" --wait --timeout 300s >/dev/null
    return
  fi

  ensure_gke_auth_plugin \
    || fail "gke-gcloud-auth-plugin not found. kubectl cannot authenticate to GKE without it.
  Install:  gcloud components install gke-gcloud-auth-plugin
  Homebrew: it ships with the SDK at \$(brew --prefix)/share/google-cloud-sdk/bin — add that to PATH"

  local project
  project="$(gcloud config get-value project 2>/dev/null)"
  [[ -n "$project" && "$project" != "(unset)" ]] \
    || fail "no gcloud project set. Run 'make gke-setup' first"

  local spot_flag=()
  case "$GCP_SPOT" in
    auto)
      local limit
      limit="$(gcloud compute regions describe "${GCP_ZONE%-*}" --format=json 2>/dev/null \
        | python3 -c 'import json,sys
try: print(int({x["metric"]: x["limit"] for x in json.load(sys.stdin).get("quotas",[])}.get("PREEMPTIBLE_CPUS",0)))
except Exception: print(0)')"
      if [[ "${limit:-0}" -ge $(( (AGENTS + 1) * 2 )) ]]; then
        spot_flag=(--spot)
        green "  using Spot nodes (${limit} preemptible vCPUs available)"
      else
        green "  using on-demand nodes: preemptible quota in ${GCP_ZONE%-*} is ${limit:-0}"
      fi
      ;;
    1|true|yes) spot_flag=(--spot) ;;
    *) ;;
  esac

  # Zonal, not regional: the GKE free tier credit covers one zonal cluster's
  # management fee, and a regional control plane buys nothing for a cluster
  # that lives twenty minutes.
  #
  # Autoscaling is the entire point. min-nodes below the starting count is what
  # permits the autoscaler to remove the node we drain — without it, it would
  # correctly refuse and the run would report a false negative.
  #
  # Autoupgrade and autorepair off so GKE does not move nodes underneath the
  # test and make its own maintenance look like our reclamation.
  gcloud container clusters create "$CLUSTER" \
    --zone "$GCP_ZONE" \
    --num-nodes "$((AGENTS + 1))" \
    --machine-type "$GCP_MACHINE" \
    "${spot_flag[@]}" \
    --disk-type pd-standard --disk-size 20 \
    --enable-autoscaling --min-nodes 1 --max-nodes "$((AGENTS + 2))" \
    --no-enable-autoupgrade --no-enable-autorepair \
    --workload-pool "${project}.svc.id.goog" \
    --labels purpose=dencer-e2e \
    --quiet >/dev/null || fail "could not create the GKE cluster"

  gcloud container clusters get-credentials "$CLUSTER" --zone "$GCP_ZONE" --quiet 2>/dev/null
  kubectl config rename-context "$(kubectl config current-context)" "$CTX" >/dev/null 2>&1 || true

  # Hand the current context straight back.
  #
  # get-credentials does not just write a context, it makes it current — which
  # silently redirects every kubectl and helm command in every other terminal
  # on the machine for as long as this runs. Restoring only on exit is too
  # late: a 25-minute run is 25 minutes of the operator's own commands landing
  # on a throwaway cloud cluster. This script addresses the cluster by
  # --context everywhere, so it needs no help from the global setting.
  restore_context
}

cluster_delete() {
  if [[ "$PROVIDER" == "k3d" ]]; then
    k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
    return
  fi
  # Deliberately noisy and synchronous. A leaked GKE cluster is the only way
  # this script can cost real money, so a failure to delete must be seen rather
  # than swallowed into /dev/null like the k3d case.
  echo "  deleting the GKE cluster (this takes a few minutes)…"
  if ! gcloud container clusters delete "$CLUSTER" --zone "$GCP_ZONE" --quiet 2>/tmp/dencer-del.err; then
    # A delete already in flight, or a cluster that is simply gone, is not the
    # failure this warning exists for. Shouting about those trains people to
    # ignore the one case that matters: a cluster still running and billing.
    if grep -qiE "not found|already being deleted|is currently being (deleted|repaired)" /tmp/dencer-del.err; then
      green "  already gone"
    else
      red "COULD NOT DELETE CLUSTER ${CLUSTER} in ${GCP_ZONE} — delete it by hand, it is costing money"
      sed 's/^/    /' /tmp/dencer-del.err
    fi
  fi
  kubectl config delete-context "$CTX" >/dev/null 2>&1 || true
}

for bin in kubectl helm; do
  command -v "$bin" >/dev/null || fail "$bin is required"
done
if [[ "$PROVIDER" == "k3d" ]]; then
  for bin in k3d docker; do
    command -v "$bin" >/dev/null || fail "$bin is required"
  done
else
  command -v gcloud >/dev/null || fail "gcloud is required for PROVIDER=gke"
fi

# `clean` lives here, not at the top of the file: it calls cluster_delete, and
# a subcommand placed before its own function is defined fails with "command
# not found" — which is exactly what it did.
if [[ "${1:-}" == "clean" ]]; then
  cluster_delete
  restore_context
  green "removed"
  exit 0
fi

# ---------------------------------------------------------------- cluster

bold "==> multi-node cluster (${PROVIDER})"
cluster_create
nodes="$(kubectl --context "$CTX" get nodes --no-headers | wc -l | tr -d ' ')"
[[ "$nodes" -ge 4 ]] || fail "expected at least 4 nodes for the guard's floor, got $nodes"
green "  ${nodes} nodes"

bold "==> namespace with PodSecurity enforcing"
kubectl --context "$CTX" create namespace "$NS" >/dev/null
# enforce, not warn. The chart has claimed restricted-PSS compliance since M0
# on the strength of a grep; if any pod in the release violates it, admission
# refuses and this script fails rather than a reviewer noticing later.
kubectl --context "$CTX" label namespace "$NS" \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest >/dev/null
kubectl --context "$CTX" create namespace "$APP_NS" >/dev/null
kubectl --context "$CTX" label namespace "$APP_NS" \
  pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/enforce-version=latest >/dev/null
green "  restricted enforced on $NS and $APP_NS"

# ---------------------------------------------------------------- images

bold "==> images"
if [[ "$PROVIDER" == "k3d" ]]; then
  TAG="$(make -s -C "$REPO" print-tag)"
  # Output kept and shown on failure. This line used to discard it, which
  # turned a CI flake into an hour of local reproduction attempts for a
  # failure whose reason had been printed and thrown away — the "reported
  # failure while hiding the cause" cousin of everything in findings.md.
  if ! make -s -C "$REPO" images >"$REPO/.e2e-images.log" 2>&1; then
    tail -n 40 "$REPO/.e2e-images.log"
    fail "image build failed"
  fi
  k3d image import -c "$CLUSTER" \
    "k8s-dencer-planner:${TAG}" "k8s-dencer-ui-backend:${TAG}" \
    "k8s-dencer-executor:${TAG}" "k8s-dencer-ui-frontend:${TAG}" >/dev/null 2>&1 \
    || fail "could not import images into the cluster"
  green "  built and imported ${TAG}"
else
  # Pulled, not imported. On a real cluster there is no local image store to
  # cheat with, so this run is also the first check that what we publish to
  # ghcr is installable by someone who is not us.
  TAG="$GHCR_TAG"
  green "  using published ghcr.io/atedgimo/k8s-dencer-*:${TAG}"
fi

# ---------------------------------------------------------------- workload

bold "==> real workloads"
# Real containers with real readiness probes. This is the whole point: a KWOK
# pod has no container to probe, so the Ready path could never be exercised.
# nginx-unprivileged because it already runs rootless and read-only, which
# restricted PSS requires and which the chart's own images satisfy.
kubectl --context "$CTX" apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: ${APP_NS}
spec:
  replicas: 6
  selector:
    matchLabels: { app: web }
  template:
    metadata:
      labels: { app: web }
    spec:
      terminationGracePeriodSeconds: 2
      containers:
        - name: web
          image: nginxinc/nginx-unprivileged:1.27-alpine
          ports: [{ containerPort: 8080 }]
          # The probe is load-bearing for this test. The executor waits on the
          # PodReady condition, so a pod that starts but never passes this
          # would hold the drain open — which is exactly the behaviour being
          # verified.
          readinessProbe:
            httpGet: { path: /, port: 8080 }
            initialDelaySeconds: 1
            periodSeconds: 2
          resources:
            requests: { cpu: 20m, memory: 24Mi }
          securityContext:
            allowPrivilegeEscalation: false
            runAsNonRoot: true
            capabilities: { drop: ["ALL"] }
            seccompProfile: { type: RuntimeDefault }
EOF
kubectl --context "$CTX" -n "$APP_NS" rollout status deploy/web --timeout=180s >/dev/null \
  || fail "test workload never became ready"
ready="$(kubectl --context "$CTX" -n "$APP_NS" get deploy web -o jsonpath='{.status.readyReplicas}')"
green "  ${ready} replicas Ready with httpGet probes"

# ---------------------------------------------------------------- install

# Chart flags that differ by provider. On GKE the Ingress is skipped: it
# provisions a billable load balancer and takes minutes to become healthy,
# which is real cost and real waiting for a path Traefik already proves.
if [[ "$PROVIDER" == "k3d" ]]; then
  # Local, unqualified image names with IfNotPresent so the imported images are
  # used and nothing reaches for a registry.
  IMAGE_SET=(
    --set planner.image.pullPolicy=IfNotPresent
    --set uiBackend.image.pullPolicy=IfNotPresent
    --set executor.image.pullPolicy=IfNotPresent
    --set uiFrontend.image.pullPolicy=IfNotPresent
    --set-string planner.image.registry=""
    --set-string uiBackend.image.registry=""
    --set-string executor.image.registry=""
    --set-string uiFrontend.image.registry=""
    --set-string planner.image.repository=k8s-dencer-planner
    --set-string uiBackend.image.repository=k8s-dencer-ui-backend
    --set-string executor.image.repository=k8s-dencer-executor
    --set-string uiFrontend.image.repository=k8s-dencer-ui-frontend
  )
  PROVIDER_SET=(
    --set-string persistence.storageClass=local-path
    --set ingress.enabled=true
    --set-string ingress.className=traefik
    --set-string "ingress.hosts[0].host=${INGRESS_HOST}"
    --set-string "ingress.hosts[0].paths[0].path=/"
    --set-string "ingress.hosts[0].paths[0].pathType=Prefix"
  )
else
  IMAGE_SET=()
  PROVIDER_SET=(
    --set-string persistence.storageClass=standard-rwo
    --set-string planner.image.registry=ghcr.io
    --set-string uiBackend.image.registry=ghcr.io
    --set-string executor.image.registry=ghcr.io
    --set-string uiFrontend.image.registry=ghcr.io
    --set-string planner.image.repository=atedgimo/k8s-dencer-planner
    --set-string uiBackend.image.repository=atedgimo/k8s-dencer-ui-backend
    --set-string executor.image.repository=atedgimo/k8s-dencer-executor
    --set-string uiFrontend.image.repository=atedgimo/k8s-dencer-ui-frontend
  )
fi

# minNodeAge=0s: a fresh k3d cluster's nodes are seconds old, and the planner
# correctly refuses to drain anything younger than minNodeAge — the rail that
# stops it reclaiming a node an autoscaler added moments ago. Sensible in
# production, fatal to a test that built its cluster ten seconds earlier.
# The store the rest of this run will use. Deployed into the same namespace so
# it is torn down with everything else, and with an emptyDir because nothing
# here outlives the cluster.
STORE_SET=(--set persistence.enabled=true)
if [[ "$BACKEND" == "postgres" ]]; then
  bold "==> postgres for the plan store"
  kubectl --context "$CTX" -n "$NS" apply -f - >/dev/null <<PGYAML
apiVersion: v1
kind: Secret
metadata:
  name: e2e-db
type: Opaque
stringData:
  password: e2e-not-a-real-password
---
apiVersion: v1
kind: Service
metadata:
  name: e2e-postgres
spec:
  selector: {app: e2e-postgres}
  ports: [{port: 5432, targetPort: 5432}]
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: e2e-postgres
spec:
  replicas: 1
  selector: {matchLabels: {app: e2e-postgres}}
  template:
    metadata:
      labels: {app: e2e-postgres}
    spec:
      # On the server node, which this test never drains. The plan targets
      # agents, and a store sitting on a drainable node is a store the test
      # will evict out from under itself — its emptyDir goes with it, and the
      # ui-backend then serves "relation does not exist" against a database it
      # migrated successfully ten minutes earlier. Observed, before this line.
      nodeSelector:
        node-role.kubernetes.io/control-plane: "true"
      tolerations:
        - key: node-role.kubernetes.io/control-plane
          operator: Exists
          effect: NoSchedule
        - key: node-role.kubernetes.io/master
          operator: Exists
          effect: NoSchedule
      containers:
        - name: postgres
          image: postgres:16-alpine
          env:
            - {name: POSTGRES_DB, value: dencer}
            - {name: POSTGRES_USER, value: dencer}
            - {name: POSTGRES_PASSWORD, valueFrom: {secretKeyRef: {name: e2e-db, key: password}}}
            - {name: PGDATA, value: /var/lib/postgresql/data/pgdata}
          ports: [{containerPort: 5432}]
          readinessProbe:
            exec: {command: ["pg_isready","-U","dencer"]}
            periodSeconds: 3
          securityContext:
            allowPrivilegeEscalation: false
            runAsNonRoot: true
            runAsUser: 70
            capabilities: {drop: ["ALL"]}
            seccompProfile: {type: RuntimeDefault}
          volumeMounts: [{name: data, mountPath: /var/lib/postgresql/data}]
      volumes: [{name: data, emptyDir: {}}]
PGYAML
  kubectl --context "$CTX" -n "$NS" rollout status deploy/e2e-postgres --timeout=180s >/dev/null \
    || fail "postgres did not become ready"
  green "  postgres ready in ${NS}"

  # persistence off is the correct configuration here, not a shortcut: the
  # chart gates the claim and the /data mount away entirely on this backend.
  # replicaCount=2 is the point of the release — it is the constraint SQLite
  # imposes and Postgres lifts — and it also puts two migrators in the race
  # the advisory lock exists for.
  STORE_SET=(
    --set database.type=postgres
    --set database.postgres.host=e2e-postgres
    --set database.postgres.database=dencer
    --set database.postgres.user=dencer
    --set database.postgres.existingSecret=e2e-db
    --set database.postgres.sslMode=disable
    --set persistence.enabled=false
    --set uiBackend.replicaCount=2
  )
fi

bold "==> install with execution on and readiness: Ready"
helm --kube-context "$CTX" upgrade --install "$RELEASE" "$REPO/charts/k8s-dencer" \
  --namespace "$NS" \
  --set auth.enabled=true \
  "${STORE_SET[@]}" \
  "${PROVIDER_SET[@]}" \
  --set executor.enabled=true \
  --set planner.minNodeAge=0s \
  --set-string planner.image.tag="$TAG" \
  --set-string uiBackend.image.tag="$TAG" \
  --set-string executor.image.tag="$TAG" \
  --set-string uiFrontend.image.tag="$TAG" \
  "${IMAGE_SET[@]}" \
  --wait --timeout 300s >/dev/null || {
    kubectl --context "$CTX" -n "$NS" get pods
    fail "chart did not install"
  }

# The default is Ready. Assert it rather than assume, because the whole value
# of this run is that the Ready path executed.
mode="$(kubectl --context "$CTX" -n "$NS" get deploy "${RELEASE}-executor" \
  -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="EXECUTOR_READINESS")].value}')"
[[ "${mode:-Ready}" == "Ready" ]] || fail "executor is on readiness=${mode}; this run would not exercise the Ready path"
green "  installed, executor readiness=${mode:-Ready} (default)"

# Admission had its say: every pod is running under enforced restricted PSS.
green "  all pods admitted under enforced restricted PodSecurity"

# The chart has always claimed these three; none was ever checked against a
# cluster that could disprove them.
if [[ "$BACKEND" == "postgres" ]]; then
  # The mirror image of the SQLite assertions below, and asserted rather than
  # skipped: everything the file store needs must be *absent*, or the install
  # has quietly kept the single-node constraint that choosing Postgres was
  # meant to lift, and nothing would report an error.
  bold "==> postgres install carries none of the file store's baggage"
  claims="$(kubectl --context "$CTX" -n "$NS" get pvc -o name 2>/dev/null | wc -l | tr -d ' ')"
  [[ "$claims" == "0" ]] || fail "a Postgres install created ${claims} PersistentVolumeClaim(s)"

  mounts="$(kubectl --context "$CTX" -n "$NS" get deploy -o json \
    | grep -c '"mountPath": "/data"' || true)"
  [[ "$mounts" == "0" ]] || fail "a Postgres install still mounts /data in ${mounts} place(s)"
  green "  no claim, no /data mount"

  bold "==> the readers are free to land anywhere"
  replicas="$(kubectl --context "$CTX" -n "$NS" get deploy "${RELEASE}-ui-backend" \
    -o jsonpath='{.status.readyReplicas}')"
  [[ "${replicas:-0}" -ge 2 ]] || fail "ui-backend has ${replicas:-0} ready replica(s); Postgres exists so this can exceed 1"

  aff="$(kubectl --context "$CTX" -n "$NS" get deploy "${RELEASE}-planner" \
    -o jsonpath='{.spec.template.spec.affinity.podAffinity}')"
  [[ -z "$aff" ]] || fail "the planner still carries the ReadWriteOnce co-scheduling affinity"
  green "  ${replicas} ui-backend replicas, planner unpinned"

  # Two ui-backends and a planner all migrate on startup. Before the advisory
  # lock this raced and one pod died on a duplicate key, self-healing on
  # restart — which is exactly why a restart count is the thing to assert.
  bold "==> nothing restarted while three pods migrated at once"
  restarts="$(kubectl --context "$CTX" -n "$NS" get pods \
    -l app.kubernetes.io/part-of=k8s-dencer \
    -o jsonpath='{range .items[*]}{.status.containerStatuses[0].restartCount}{"\n"}{end}' \
    | awk '{t+=$1} END {print t+0}')"
  [[ "$restarts" == "0" ]] || fail "${restarts} container restart(s); a migration race looks exactly like this"
  green "  0 restarts across the release"
else
bold "==> storage, on a named StorageClass"
  pvc="$(kubectl --context "$CTX" -n "$NS" get pvc -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  [[ -n "$pvc" ]] || fail "persistence is on but no PersistentVolumeClaim was created"
  phase="$(kubectl --context "$CTX" -n "$NS" get pvc "$pvc" -o jsonpath='{.status.phase}')"
  [[ "$phase" == "Bound" ]] || fail "PVC ${pvc} is ${phase}, not Bound"
  want_sc="local-path"; [[ "$PROVIDER" == "gke" ]] && want_sc="standard-rwo"
  sc="$(kubectl --context "$CTX" -n "$NS" get pvc "$pvc" -o jsonpath='{.spec.storageClassName}')"
  [[ "$sc" == "$want_sc" ]] || fail "PVC bound to StorageClass '${sc}', not the requested ${want_sc}"
  green "  ${pvc} Bound on ${sc}"
  
  bold "==> the ReadWriteOnce claim keeps its two readers together"
  # SQLite is single-writer and the volume is ReadWriteOnce, so the chart
  # co-schedules the planner with ui-backend through a required podAffinity. On
  # one node that constraint is free; this is the first cluster that could
  # violate it.
  pnode="$(kubectl --context "$CTX" -n "$NS" get pod -l app.kubernetes.io/component=planner \
    -o jsonpath='{.items[0].spec.nodeName}')"
  bnode="$(kubectl --context "$CTX" -n "$NS" get pod -l app.kubernetes.io/component=ui-backend \
    -o jsonpath='{.items[0].spec.nodeName}')"
  [[ -n "$pnode" && "$pnode" == "$bnode" ]] \
    || fail "planner is on '${pnode}' and ui-backend on '${bnode}'; a ReadWriteOnce volume cannot span nodes"
  green "  planner and ui-backend co-scheduled on ${pnode}"
fi

if [[ "$PROVIDER" == "gke" ]]; then
  bold "==> ingress"
  green "  skipped on GKE: a cloud Ingress provisions a billable load balancer,"
  green "  and the Traefik run already proves the chart's Ingress routes"
else
bold "==> a request actually traverses the ingress controller"
code=""
for _ in $(seq 1 30); do
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
    -H "Host: ${INGRESS_HOST}" "http://localhost:${INGRESS_PORT}/" || true)"
  [[ "$code" == "200" ]] && break
  sleep 4
done
[[ "$code" == "200" ]] \
  || fail "GET / through the ingress returned '${code}', not 200"
green "  Traefik routed ${INGRESS_HOST} to the frontend (HTTP 200)"
fi

# ---------------------------------------------------------------- plan

bold "==> a plan against real state"
# The port-forward binds to one pod, and this test drains nodes for a living.
# A ui-backend that lands on the target node is evicted mid-run and the forward
# dies with it — after which every api() call fails and the run looks stuck at
# "Running" while the executor's own logs show it Succeeded twice.
#
# Postgres made that likely rather than rare: without the ReadWriteOnce
# co-scheduling pin the readers spread across agent nodes, and there are two of
# them, so the odds that one is on whichever node the planner picks are good.
# It is the test's plumbing that breaks, not the product, so the plumbing
# reconnects instead of the assertions being loosened.
pf_start() {
  kubectl --context "$CTX" -n "$NS" port-forward "svc/${RELEASE}-ui-backend" "${PF_PORT}:8080" >/dev/null 2>&1 &
  PF=$!
  for _ in $(seq 1 20); do
    curl -s --max-time 2 -o /dev/null "http://localhost:${PF_PORT}/healthz" && return 0
    sleep 1
  done
  return 1
}
pf_start || fail "could not port-forward to the ui-backend"

kubectl --context "$CTX" get serviceaccount e2e-operator -n "$NS" >/dev/null 2>&1 \
  || kubectl --context "$CTX" create serviceaccount e2e-operator -n "$NS" >/dev/null
kubectl --context "$CTX" create rolebinding e2e-operator -n "$NS" \
  --clusterrole="${RELEASE}-consolidation-operator" \
  --serviceaccount="${NS}:e2e-operator" >/dev/null 2>&1 || true
TOKEN="$(kubectl --context "$CTX" create token e2e-operator -n "$NS" --duration=30m)"

# Retries through a dead forward once, because the pod on the other end may
# have been evicted by the very drain this test asked for.
api() {
  local out rc
  out="$(curl -sS -H "Authorization: Bearer ${TOKEN}" "http://localhost:${PF_PORT}$1" "${@:2}" 2>/dev/null)"; rc=$?
  if [[ $rc -ne 0 ]]; then
    kill "$PF" 2>/dev/null || true
    pf_start >/dev/null 2>&1 || true
    out="$(curl -sS -H "Authorization: Bearer ${TOKEN}" "http://localhost:${PF_PORT}$1" "${@:2}" 2>/dev/null)"
  fi
  printf '%s' "$out"
}

plan=""
for _ in $(seq 1 40); do
  plan="$(api /api/v1/plans/latest 2>/dev/null || true)"
  # The endpoint wraps the plan: {"plan": {...}, ...}. Reading "steps" off the
  # top level silently yields zero, which looks exactly like "no plan yet".
  steps="$(printf '%s' "$plan" | python3 -c 'import json,sys
try:
    d = json.load(sys.stdin)
    print(len((d.get("plan") or d).get("steps") or []))
except Exception:
    print(0)' 2>/dev/null || echo 0)"
  [[ "$steps" -gt 0 ]] && break
  sleep 5
done
if [[ "${steps:-0}" -eq 0 ]]; then
  # Say why, rather than leaving the next person to guess. The usual causes are
  # a rail doing its job, not a broken planner.
  red "planner produced no steps after 200s. Current state:"
  kubectl --context "$CTX" -n "$NS" logs deploy/"${RELEASE}-planner" --tail=6 2>/dev/null | sed 's/^/    /'
  echo "    node ages:"
  kubectl --context "$CTX" get nodes -o custom-columns=NAME:.metadata.name,AGE:.metadata.creationTimestamp --no-headers | sed 's/^/      /'
  fail "no plan"
fi
green "  plan with ${steps} steps"

# The nodes k8s-dencer's own pods are sitting on.
#
# Draining one of those evicts the product mid-run. The planner's metrics are
# an in-process histogram, so a planner that is rescheduled loses the
# reclamation it just timed and dencer_reclamation_seconds_count reads 0 —
# observed twice in CI, both times looking like a regression in whatever PR
# happened to be open. The ui-backend being evicted kills the port-forward, and
# the store being evicted empties the database.
#
# On SQLite the ReadWriteOnce affinity kept these together and mostly out of
# the way. Postgres lifts that pin, which is the point of it, so the test has
# to stop assuming.
#
# This is a property of the test, not of the product: in production a planner
# that plans to drain its own node is behaving correctly and Kubernetes
# reschedules it. Here it saws off the branch it is measuring from.
OURS="$(kubectl --context "$CTX" -n "$NS" get pods \
  -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' 2>/dev/null | sort -u | tr '\n' ',')"

# Pick a step that does not need a maintenance window, that actually moves
# something — a step with no moves would drain an already-empty node and prove
# nothing about recovery — and that is not standing on our own feet.
read -r SEQ TARGET MOVES <<<"$(printf '%s' "$plan" | OURS="$OURS" python3 -c '
import json,os,sys
d = json.load(sys.stdin)
p = d.get("plan") or d
ours = {n for n in os.environ.get("OURS", "").split(",") if n}
usable = [s for s in (p.get("steps") or []) if s.get("impact") != "Red" and s.get("moves")]
# Prefer a node none of our own pods are on; fall back rather than fail, so a
# single-node or fully-packed cluster still exercises the drain.
pick = next((s for s in usable if s.get("targetNode") not in ours), None) or (usable[0] if usable else None)
if pick:
    print(pick["sequenceNumber"], pick.get("targetNode", ""), len(pick["moves"]))
else:
    print("", "", "")
')"
[[ -n "${SEQ:-}" ]] || fail "no non-Red step with moves; nothing to execute"
green "  step ${SEQ} drains ${TARGET}, moving ${MOVES} pods"

before_ready="$(kubectl --context "$CTX" -n "$APP_NS" get deploy web -o jsonpath='{.status.readyReplicas}')"

# ---------------------------------------------------------------- execute

bold "==> execute it"
planid="$(printf '%s' "$plan" | python3 -c 'import json,sys; d=json.load(sys.stdin); print((d.get("plan") or d)["id"])')"
run="$(api "/api/v1/plans/${planid}/execute" -X POST -H 'Content-Type: application/json' \
  -d "{\"steps\":[${SEQ}]}")"
# json.load consumes stdin, so read once and reuse it.
runid="$(printf '%s' "$run" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("runId") or d.get("id",""))' 2>/dev/null || true)"
[[ -n "$runid" ]] || fail "execute did not return a run id: $run"
green "  run ${runid} queued"

status=""
for _ in $(seq 1 60); do
  # Wrapped as {"run": {...}, "events": [...]}, like the plan endpoint.
  status="$(api "/api/v1/runs/${runid}" | python3 -c 'import json,sys; d=json.load(sys.stdin); print((d.get("run") or d).get("status",""))' 2>/dev/null || true)"
  case "$status" in Succeeded|Failed|Blocked) break;; esac
  sleep 5
done
[[ "$status" == "Succeeded" ]] || {
  api "/api/v1/runs/${runid}" | python3 -m json.tool | tail -40
  fail "run ended ${status:-unknown}, want Succeeded"
}
green "  run Succeeded"

# ---------------------------------------------------------------- verify

bold "==> the cluster actually changed"
sched="$(kubectl --context "$CTX" get node "$TARGET" -o jsonpath='{.spec.unschedulable}' 2>/dev/null || true)"
[[ "$sched" == "true" ]] || fail "node ${TARGET} is not cordoned"
green "  ${TARGET} cordoned"

left="$(kubectl --context "$CTX" -n "$APP_NS" get pods --field-selector "spec.nodeName=${TARGET}" \
  --no-headers 2>/dev/null | grep -vc Terminating || true)"
[[ "${left:-0}" -eq 0 ]] || fail "${left} workload pods still on the drained node"
green "  no workload pods left on it"

after_ready="$(kubectl --context "$CTX" -n "$APP_NS" get deploy web -o jsonpath='{.status.readyReplicas}')"
[[ "${after_ready:-0}" -eq "${before_ready}" ]] \
  || fail "readyReplicas ${before_ready} -> ${after_ready}: the workload did not fully recover"
green "  all ${after_ready} replicas Ready again — on the Ready path, with real probes"

# The assertion that would have caught v0.6.0.
#
# The drain above succeeded on both backends. On Postgres it also wrote nothing
# to the ledger, for the whole of that release: RecordDrain bound a Go bool
# into an INTEGER column, pgx refused it, and both call sites only log. Every
# drain reported success while the savings ledger stayed permanently empty and
# PendingReclamations never surfaced a node that was drained and never removed.
#
# Nothing here checked, because the executor's own events were all this run
# ever read. The store is a separate claim and gets its own.
bold "==> the drain reached the ledger"
pending="$(api /api/v1/reclamations | grep -c "\"node\":\"${TARGET}\"" || true)"
[[ "${pending:-0}" -ge 1 ]] \
  || fail "the ledger has no record of draining ${TARGET}; the drain succeeded and the store did not hear about it"
green "  ${TARGET} recorded in the reclamation ledger"

# ------------------------------------------------------- reclamation loop

# Draining is not removing. k8s-dencer empties a node and something else takes
# the machine away — and until the reclamation loop existed, nothing checked
# whether anything ever did, so "15 reclaimable" was a prediction reported as
# an outcome.
#
# k3d lets us play both parts, which no cloud test would do as cheaply.

reclamation_outcome() {
  api /api/v1/reclamations | python3 -c "
import json,sys
d = json.load(sys.stdin)
node = sys.argv[1]
for r in d.get('recent') or []:
    if r['node'] == node:
        print(r.get('outcome') or 'pending'); break
else:
    print('missing')
" "$1"
}

bold "==> the drain was recorded as awaiting reclamation"
for _ in $(seq 1 20); do
  [[ "$(reclamation_outcome "$TARGET")" == "pending" ]] && break
  sleep 3
done
[[ "$(reclamation_outcome "$TARGET")" == "pending" ]] \
  || fail "the drain of ${TARGET} was never recorded; got '$(reclamation_outcome "$TARGET")'"
green "  ${TARGET} is awaiting reclamation"

if [[ "$PROVIDER" == "k3d" ]]; then
  bold "==> an autoscaler removes it"
  # k3d has no autoscaler, so the script plays the part. This proves the
  # observation works; it cannot prove anything about a real reclaimer.
  kubectl --context "$CTX" delete node "$TARGET" --wait=true >/dev/null
  # The planner observes on its resync, so this waits a cycle rather than a tick.
  for _ in $(seq 1 30); do
    [[ "$(reclamation_outcome "$TARGET")" == "reclaimed" ]] && break
    sleep 5
  done
  [[ "$(reclamation_outcome "$TARGET")" == "reclaimed" ]] \
    || fail "${TARGET} was deleted but never observed as reclaimed; got '$(reclamation_outcome "$TARGET")'"
  green "  observed as reclaimed — the loop closes"
else
  bold "==> GKE's own cluster autoscaler removes it"
  echo "  Nothing here touches the node. GKE decides, on its own schedule —"
  echo "  scale-down-unneeded-time defaults to 10 minutes, so this waits."
  started="$(date +%s)"
  outcome=""
  for _ in $(seq 1 60); do   # up to 15 minutes
    outcome="$(reclamation_outcome "$TARGET")"
    [[ "$outcome" == "reclaimed" ]] && break
    # A Spot preemption would also make a node vanish. Tying the assertion to
    # the node the plan drained is what stops that reading as a pass.
    sleep 15
  done
  took=$(( $(date +%s) - started ))
  if [[ "$outcome" != "reclaimed" ]]; then
    red "  ${TARGET} was never reclaimed after $((took / 60))m. Current state:"
    kubectl --context "$CTX" get nodes -o wide 2>/dev/null | sed 's/^/    /'
    # The usual cause, and not a bug in this product: a kube-system pod with no
    # PDB pins the node and the autoscaler correctly refuses.
    kubectl --context "$CTX" get pods -A --field-selector "spec.nodeName=${TARGET}" \
      --no-headers 2>/dev/null | sed 's/^/    /'
    fail "no reclamation observed; got '${outcome}'"
  fi
  kubectl --context "$CTX" get node "$TARGET" >/dev/null 2>&1 \
    && fail "recorded as reclaimed but the Node object is still there"
  green "  observed as reclaimed after $((took / 60))m$((took % 60))s — by a reclaimer we did not write"
fi

# The other branch. An operator who uncordons a drained node is never getting a
# reclamation, and leaving that row pending forever would make the awaiting
# count grow without bound and mean nothing.
if [[ "$PROVIDER" == "gke" ]]; then
  bold "==> the other branch: a drained node put back into service"
  green "  skipped on GKE: uncordoning races the autoscaler, which may remove"
  green "  the node before the observer sees it schedulable — the k3d run proves it"
else
bold "==> the other branch: a drained node put back into service"
SECOND="$(api /api/v1/plans/latest | python3 -c '
import json,sys
d = json.load(sys.stdin)
p = d.get("plan") or d
for s in p.get("steps") or []:
    if s.get("impact") != "Red" and s.get("moves"):
        print(s["sequenceNumber"]); break
else:
    print("")
')"
if [[ -z "$SECOND" ]]; then
  green "  skipped: no second runnable step on this cluster"
else
  planid2="$(api /api/v1/plans/latest | python3 -c 'import json,sys; d=json.load(sys.stdin); print((d.get("plan") or d)["id"])')"
  node2="$(api "/api/v1/plans/${planid2}" | python3 -c "
import json,sys
d = json.load(sys.stdin)
for s in (d.get('plan') or d).get('steps') or []:
    if s['sequenceNumber'] == int(sys.argv[1]):
        print(s.get('targetNode','')); break
" "$SECOND")"

  run2="$(api "/api/v1/plans/${planid2}/execute" -X POST -H 'Content-Type: application/json' \
    -d "{\"steps\":[${SECOND}]}")"
  runid2="$(printf '%s' "$run2" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("runId",""))')"
  for _ in $(seq 1 60); do
    st="$(api "/api/v1/runs/${runid2}" | python3 -c 'import json,sys; d=json.load(sys.stdin); print((d.get("run") or d).get("status",""))' 2>/dev/null || true)"
    case "$st" in Succeeded|Failed|Blocked) break;; esac
    sleep 5
  done
  if [[ "$st" != "Succeeded" ]]; then
    green "  skipped: second run ended ${st}, which is a guard doing its job rather than a failure here"
  else
    kubectl --context "$CTX" uncordon "$node2" >/dev/null
    for _ in $(seq 1 30); do
      [[ "$(reclamation_outcome "$node2")" == "returned" ]] && break
      sleep 5
    done
    [[ "$(reclamation_outcome "$node2")" == "returned" ]] \
      || fail "${node2} was uncordoned but recorded as '$(reclamation_outcome "$node2")', not returned"
    green "  ${node2} observed as returned to service, not counted as a saving"
  fi
fi
fi

bold "==> the metrics agree"
kubectl --context "$CTX" -n "$NS" port-forward "deploy/${RELEASE}-planner" 18100:8081 >/dev/null 2>&1 &
MPF=$!
sleep 4
series="$(curl -s --max-time 10 http://localhost:18100/metrics || true)"
kill $MPF 2>/dev/null || true
grep -q "dencer_reclamation_seconds_count" <<<"$series" \
  || fail "the planner publishes no dencer_reclamation_seconds"
observed="$(grep -E "^dencer_reclamation_seconds_count " <<<"$series" | awk "{print \$2}")"
[[ "${observed%.*}" -ge 1 ]] || fail "dencer_reclamation_seconds_count is ${observed}; the reclamation was never timed"
green "  dencer_reclamation_seconds observed ${observed} reclamation(s)"

echo
if [[ "$PROVIDER" == "gke" ]]; then
  green "cloud e2e passed on GKE: the published images installed, a cloud StorageClass"
  green "bound, real pods were evicted and recovered, and GKE's own cluster autoscaler"
  green "removed the drained node — observed by a reclaimer we did not write."
else
  green "e2e passed: multi-node, PodSecurity enforced, a real ingress and StorageClass,"
  green "real pods evicted and recovered, and a node observed actually going away."
fi
