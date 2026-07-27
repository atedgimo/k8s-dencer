#!/usr/bin/env bash
#
# Chart portability gate.
#
# Renders every values profile and asserts the contract from the plan: the
# production profile must be free of any local-development artifact, the
# minimal profile must still be a valid install, and the SQLite single-writer
# guard must actually reject a multi-replica ui-backend.
#
# Run via `make lint`.

set -euo pipefail

CHART="${CHART:-charts/k8s-dencer}"
KUBECONFORM="${KUBECONFORM:-kubeconform}"
K8S_VERSION="${K8S_VERSION:-1.30.0}"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
bold()  { printf '\033[1m%s\033[0m\n' "$*"; }

fail() { red "FAIL: $*"; exit 1; }

render() {
  local profile="$1"
  if [[ "$profile" == "defaults" ]]; then
    helm template dencer "$CHART" --namespace k8s-dencer
  else
    helm template dencer "$CHART" --namespace k8s-dencer -f "$CHART/ci/${profile}-values.yaml"
  fi
}

bold "==> helm lint"
for profile in minimal production orbstack; do
  helm lint "$CHART" -f "$CHART/ci/${profile}-values.yaml" >/dev/null \
    || fail "helm lint failed for $profile"
done
helm lint "$CHART" >/dev/null || fail "helm lint failed for defaults"
green "  all profiles lint clean"

bold "==> render + kubeconform"
for profile in defaults minimal production orbstack; do
  # CRD-backed kinds (ServiceMonitor, kagent.dev/*) have no upstream schema, so
  # they are skipped rather than failing the run.
  render "$profile" | "$KUBECONFORM" \
    -strict \
    -summary \
    -ignore-missing-schemas \
    -kubernetes-version "$K8S_VERSION" \
    >/dev/null || fail "kubeconform rejected profile: $profile"
  green "  $profile validates"
done

bold "==> contract: production profile carries no local assumptions"
prod="$(render production)"
for forbidden in orbstack Orbstack OrbStack kwok KWOK hostPath 'type: LoadBalancer' 'imagePullPolicy: Always'; do
  if grep -qi -- "$forbidden" <<<"$prod"; then
    grep -in -- "$forbidden" <<<"$prod" | head -3
    fail "production profile contains forbidden token: $forbidden"
  fi
done
green "  no forbidden tokens"

bold "==> contract: production profile renders the expected objects"
for kind in "kind: Ingress" "kind: PersistentVolumeClaim" "kind: PodDisruptionBudget" "kind: NetworkPolicy" "kind: ServiceMonitor"; do
  grep -q -- "$kind" <<<"$prod" || fail "production profile is missing $kind"
done
grep -q "eks.amazonaws.com/role-arn" <<<"$prod" \
  || fail "production profile lost the ServiceAccount IRSA annotation"
green "  Ingress, PVC, PDB, NetworkPolicy, ServiceMonitor and IRSA annotation all present"

bold "==> contract: minimal profile is a valid restricted-PSS install"
minimal="$(render minimal)"
grep -q "runAsNonRoot: true" <<<"$minimal"          || fail "minimal profile lost runAsNonRoot"
grep -q "readOnlyRootFilesystem: true" <<<"$minimal" || fail "minimal profile lost readOnlyRootFilesystem"
grep -q "allowPrivilegeEscalation: false" <<<"$minimal" || fail "minimal profile lost allowPrivilegeEscalation"
green "  restricted security context intact with all optionals off"

bold "==> contract: eviction is granted only to the executor, only when enabled"
# Phase 1's assertion was "nobody, anywhere". Phase 2 narrows it rather than
# dropping it: the executor exists to evict, but every other component must
# still be provably unable to, and an install that did not ask for an executor
# must gain nothing.
# These profiles do not ask for an executor, so they must gain nothing. The
# orbstack profile deliberately does — it is the POC that exercises real
# drains — and is checked separately below.
for profile in defaults minimal production; do
  if render "$profile" | grep -q "pods/eviction"; then
    fail "$profile grants pods/eviction without enabling the executor"
  fi
done
green "  no eviction in any profile that did not ask for an executor"

# The one profile that opts in must still confine the grant.
orbstack_eviction="$(awk '/^kind: ClusterRole$/,/^---/' <<<"$(render orbstack)" \
  | awk '/^  name: /{n=$2} /pods\/eviction/{print n}' | sort -u)"
if [[ "$orbstack_eviction" != "dencer-k8s-dencer-executor" ]]; then
  fail "orbstack enables the executor but pods/eviction landed on: ${orbstack_eviction:-nothing}"
fi
green "  orbstack opts in, and only its executor role holds eviction"

with_executor="$(helm template dencer "$CHART" --namespace k8s-dencer \
  --set executor.enabled=true --set persistence.enabled=true)"

# The grant must live on the executor's ClusterRole and no other.
eviction_roles="$(awk '/^kind: ClusterRole$/,/^---/' <<<"$with_executor" \
  | awk '/^  name: /{n=$2} /pods\/eviction/{print n}' | sort -u)"
if [[ "$eviction_roles" != "dencer-k8s-dencer-executor" ]]; then
  fail "pods/eviction appears on unexpected ClusterRole(s): ${eviction_roles:-none}"
fi
green "  pods/eviction confined to the executor ClusterRole"

# The read role the planner and ui-backend share must never acquire a write
# verb on a cluster workload. It legitimately writes status on its own
# ConsolidationPolicy CRD, so the check is scoped to nodes and pods rather than
# banning every write verb outright.
read_role="$(awk '/name: dencer-k8s-dencer-read$/,/^---/' <<<"$with_executor")"
offending="$(awk '
  /^  - apiGroups:/       { resources = ""; }
  /^    resources:/       { collecting = 1; next }
  /^      - /             { if (collecting) { gsub(/^      - /, ""); resources = resources " " $0 } ; next }
  /^    verbs:/           {
      collecting = 0
      if (resources ~ /(^| )(nodes|pods|pods\/eviction)( |$)/ &&
          $0 ~ /(patch|update|delete|create)/) print "  " resources " ->" $0
  }
  /^    resources: \[/    { line = $0; sub(/^    resources: \[/, "", line); gsub(/[]"]/, "", line); resources = " " line }
' <<<"$read_role")"
if [[ -n "$offending" ]]; then
  fail "the shared read ClusterRole grants writes on cluster workloads:
$offending"
fi
green "  planner and ui-backend hold no write verb on nodes or pods"

# The executor is the one workload with eviction, so it must also be the one
# workload with no Service in front of it.
if grep -q "name: dencer-k8s-dencer-executor$" <<<"$(awk '/^kind: Service$/,/^---/' <<<"$with_executor")"; then
  fail "the executor has a Service; the component holding pods/eviction must not be reachable"
fi
green "  executor has no Service"

bold "==> contract: the executor cannot be enabled into an unsafe configuration"
if helm template dencer "$CHART" --set executor.enabled=true --set auth.enabled=false >/dev/null 2>&1; then
  fail "schema accepted an executor with authentication disabled"
fi
if helm template dencer "$CHART" --set executor.enabled=true --set persistence.enabled=false >/dev/null 2>&1; then
  fail "schema accepted an executor with no shared volume to claim work from"
fi
green "  rejected without auth, and without persistence"

bold "==> contract: no duplicate env keys in any container"
# kubeconform passes duplicate env names and so does helm template — a
# duplicate is valid YAML and a valid PodSpec on paper. The API server rejects
# it on server-side apply, so without this check the failure lands at deploy
# time on a build that linted clean.
for profile in defaults minimal production orbstack; do
  dupes="$(render "$profile" \
    | awk '
        /^[[:space:]]*- name: [A-Z_][A-Z0-9_]*$/ && in_env {
          gsub(/^[[:space:]]*- name: /, ""); print indent_key "/" $0; next
        }
        /^[[:space:]]*env:[[:space:]]*$/ { in_env = 1; next }
        /^[[:space:]]*(volumeMounts|ports|resources|args|command|livenessProbe|readinessProbe|startupProbe|securityContext):[[:space:]]*$/ { in_env = 0 }
        /^[[:space:]]*- name: / && !in_env { indent_key = $0; sub(/^[[:space:]]*- name: /, "", indent_key) }
      ' \
    | sort | uniq -d)"
  [[ -z "$dupes" ]] || fail "$profile declares duplicate env keys: $dupes"
done
green "  env keys unique in every container"

bold "==> contract: authentication is on unless a profile deliberately opts out"
for profile in defaults production orbstack; do
  render "$profile" | grep -q 'name: AUTH_ENABLED' \
    || fail "$profile does not configure AUTH_ENABLED at all"
  # The env var is rendered from .Values.auth.enabled; "false" here means the
  # profile shipped an unauthenticated API.
  if render "$profile" | grep -A1 'name: AUTH_ENABLED' | grep -q 'value: "false"'; then
    fail "$profile disables authentication"
  fi
done
green "  defaults, production and orbstack all authenticate"

bold "==> contract: ui-backend can delegate authn, and only ui-backend can"
delegator="$(render defaults | awk '/name: dencer-k8s-dencer-auth-delegator/,/^---/')"
grep -q 'system:auth-delegator' <<<"$delegator" \
  || fail "ui-backend is not bound to system:auth-delegator; TokenReview will 403"
grep -q 'name: dencer-k8s-dencer-ui-backend' <<<"$delegator" \
  || fail "the auth-delegator binding does not name the ui-backend ServiceAccount"
if grep -q 'name: dencer-k8s-dencer-planner' <<<"$delegator"; then
  fail "the planner is bound to system:auth-delegator; it never authenticates anyone"
fi
green "  system:auth-delegator scoped to ui-backend"

bold "==> contract: the operator ClusterRole ships unbound"
if render defaults | grep -q 'name: dencer-k8s-dencer-consolidation-operator' ; then
  # Present as a ClusterRole, but nothing may bind it: who is allowed to drain
  # nodes is the cluster owner's decision, not the chart's.
  bindings="$(render defaults | awk '/^kind: (Cluster)?RoleBinding/,/^---/' \
                | grep -c 'name: dencer-k8s-dencer-consolidation-operator' || true)"
  [[ "$bindings" == "0" ]] \
    || fail "the chart binds the consolidation-operator role to someone; it must ship unbound"
else
  fail "the consolidation-operator ClusterRole is missing"
fi
green "  shipped, unbound"

bold "==> contract: NetworkPolicy defends ui-backend by default"
render defaults | grep -q 'kind: NetworkPolicy' \
  || fail "defaults render no NetworkPolicy; ui-backend is reachable from any pod"
# Ingress-only on purpose: egress to the API server needs an ipBlock for the
# endpoint or a cluster-wide CIDR, neither of which is portable.
if render defaults | awk '/kind: NetworkPolicy/,/^---/' | grep -q '^\s*- Egress'; then
  fail "the NetworkPolicy restricts egress; that cannot be expressed portably"
fi
green "  ingress-only policy present by default"

bold "==> contract: OIDC cannot be half-configured"
if helm template dencer "$CHART" --set auth.oidc.enabled=true >/dev/null 2>&1; then
  fail "schema accepted auth.oidc.enabled=true with no issuerUrl or clientId"
fi
green "  rejected as expected"

bold "==> contract: SQLite guard rejects a second ui-backend replica"
if helm template dencer "$CHART" \
     --set database.type=sqlite \
     --set uiBackend.replicaCount=2 >/dev/null 2>&1; then
  fail "schema accepted uiBackend.replicaCount=2 with SQLite; it must be rejected"
fi
green "  rejected as expected"

bold "==> contract: chart packages without the ci/ fixtures"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
helm package "$CHART" -d "$tmp" >/dev/null
if tar -tzf "$tmp"/*.tgz | grep -q '/ci/'; then
  fail "packaged chart ships the ci/ lint fixtures"
fi
green "  ci/ excluded from the package"

green "
All chart contract checks passed."
