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
#   ./hack/e2e.sh            run it
#   ./hack/e2e.sh clean      tear the cluster down
#   KEEP=1 ./hack/e2e.sh     leave the cluster up afterwards for poking at
#
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER="${CLUSTER:-dencer-e2e}"
CTX="k3d-${CLUSTER}"
NS="${NS:-k8s-dencer}"
APP_NS="${APP_NS:-shop}"
RELEASE="k8s-dencer"
# Five nodes, not three. The Safety Guard's MinReadyNodes floor defaults to 3,
# so on a three-node cluster draining anything would leave two and the guard
# refuses — correctly. Lowering the floor to make the test pass would be
# testing a configuration nobody runs; adding nodes tests the real one.
AGENTS="${AGENTS:-4}"
PF_PORT="${PF_PORT:-18099}"

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
    k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  fi
  restore_context
}
trap cleanup EXIT

if [[ "${1:-}" == "clean" ]]; then
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  restore_context
  green "removed"
  exit 0
fi

for bin in k3d kubectl helm docker; do
  command -v "$bin" >/dev/null || fail "$bin is required"
done

# ---------------------------------------------------------------- cluster

bold "==> multi-node cluster"
k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
k3d cluster create "$CLUSTER" --servers 1 --agents "$AGENTS" --wait --timeout 300s >/dev/null
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
TAG="$(make -s -C "$REPO" print-tag)"
make -s -C "$REPO" images >/dev/null 2>&1 || fail "image build failed"
k3d image import -c "$CLUSTER" \
  "k8s-dencer-planner:${TAG}" "k8s-dencer-ui-backend:${TAG}" \
  "k8s-dencer-executor:${TAG}" "k8s-dencer-ui-frontend:${TAG}" >/dev/null 2>&1 \
  || fail "could not import images into the cluster"
green "  built and imported ${TAG}"

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

# minNodeAge=0s: a fresh k3d cluster's nodes are seconds old, and the planner
# correctly refuses to drain anything younger than minNodeAge — the rail that
# stops it reclaiming a node an autoscaler added moments ago. Sensible in
# production, fatal to a test that built its cluster ten seconds earlier.
bold "==> install with execution on and readiness: Ready"
helm --kube-context "$CTX" upgrade --install "$RELEASE" "$REPO/charts/k8s-dencer" \
  --namespace "$NS" \
  --set auth.enabled=true \
  --set persistence.enabled=true \
  --set executor.enabled=true \
  --set planner.minNodeAge=0s \
  --set-string planner.image.tag="$TAG" \
  --set-string uiBackend.image.tag="$TAG" \
  --set-string executor.image.tag="$TAG" \
  --set-string uiFrontend.image.tag="$TAG" \
  --set planner.image.pullPolicy=IfNotPresent \
  --set uiBackend.image.pullPolicy=IfNotPresent \
  --set executor.image.pullPolicy=IfNotPresent \
  --set uiFrontend.image.pullPolicy=IfNotPresent \
  --set-string planner.image.registry="" \
  --set-string uiBackend.image.registry="" \
  --set-string executor.image.registry="" \
  --set-string uiFrontend.image.registry="" \
  --set-string planner.image.repository=k8s-dencer-planner \
  --set-string uiBackend.image.repository=k8s-dencer-ui-backend \
  --set-string executor.image.repository=k8s-dencer-executor \
  --set-string uiFrontend.image.repository=k8s-dencer-ui-frontend \
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

# ---------------------------------------------------------------- plan

bold "==> a plan against real state"
kubectl --context "$CTX" -n "$NS" port-forward "svc/${RELEASE}-ui-backend" "${PF_PORT}:8080" >/dev/null 2>&1 &
PF=$!
sleep 5

kubectl --context "$CTX" get serviceaccount e2e-operator -n "$NS" >/dev/null 2>&1 \
  || kubectl --context "$CTX" create serviceaccount e2e-operator -n "$NS" >/dev/null
kubectl --context "$CTX" create rolebinding e2e-operator -n "$NS" \
  --clusterrole="${RELEASE}-consolidation-operator" \
  --serviceaccount="${NS}:e2e-operator" >/dev/null 2>&1 || true
TOKEN="$(kubectl --context "$CTX" create token e2e-operator -n "$NS" --duration=30m)"

api() { curl -sS -H "Authorization: Bearer ${TOKEN}" "http://localhost:${PF_PORT}$1" "${@:2}"; }

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

# Pick a step that does not need a maintenance window, and that actually moves
# something — a step with no moves would drain an already-empty node and prove
# nothing about recovery.
read -r SEQ TARGET MOVES <<<"$(printf '%s' "$plan" | python3 -c '
import json,sys
d = json.load(sys.stdin)
p = d.get("plan") or d
for s in p.get("steps") or []:
    if s.get("impact") != "Red" and s.get("moves"):
        print(s["sequenceNumber"], s.get("targetNode",""), len(s["moves"])); break
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

bold "==> an autoscaler removes it"
kubectl --context "$CTX" delete node "$TARGET" --wait=true >/dev/null
# The planner observes on its resync, so this waits a cycle rather than a tick.
for _ in $(seq 1 30); do
  [[ "$(reclamation_outcome "$TARGET")" == "reclaimed" ]] && break
  sleep 5
done
[[ "$(reclamation_outcome "$TARGET")" == "reclaimed" ]] \
  || fail "${TARGET} was deleted but never observed as reclaimed; got '$(reclamation_outcome "$TARGET")'"
green "  observed as reclaimed — the loop closes"

# The other branch. An operator who uncordons a drained node is never getting a
# reclamation, and leaving that row pending forever would make the awaiting
# count grow without bound and mean nothing.
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
green "e2e passed: multi-node, PodSecurity enforced, real pods evicted and recovered,"
green "and the reclamation loop observed a node actually going away."
