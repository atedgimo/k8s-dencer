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

bold "==> contract: no eviction permission anywhere (Phase 1 is read-only)"
for profile in defaults minimal production orbstack; do
  if render "$profile" | grep -q "pods/eviction"; then
    fail "$profile grants pods/eviction; Phase 1 must be read-only"
  fi
done
green "  no profile grants pods/eviction"

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
