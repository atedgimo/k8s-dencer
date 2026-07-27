#!/usr/bin/env bash
#
# Proves tier-1 single sign-on end to end against a real OIDC issuer.
#
# The claim under test is the one the whole auth design rests on: when the API
# server trusts an issuer, an ID token from that issuer is ALREADY a Kubernetes
# credential — so k8s-dencer needs no session store, no user database, and no
# verification logic of its own.
#
# That cannot be tested on OrbStack, whose k3s runs the API server embedded with
# no way to pass --oidc-issuer-url. So this builds a throwaway k3d cluster that
# trusts a local Dex, which has the side benefit of exercising the chart on a
# second, differently-provisioned cluster — the platform-agnostic claim the
# chart has made since M0.
#
# The browser redirect is deliberately not driven here. Dex's password grant
# yields the same ID token by a different route, and what needs proving is that
# the token is accepted, not that oidc-client-ts can perform a redirect.
#
#   ./hack/verify-sso.sh          run it
#   ./hack/verify-sso.sh clean    tear down the cluster and Dex
#
set -euo pipefail

CLUSTER="${CLUSTER:-dencer-sso}"
CTX="k3d-${CLUSTER}"
DEX_CONTAINER="dencer-dex"
DEX_PORT=5556
ISSUER="https://host.k3d.internal:${DEX_PORT}/dex"
CLIENT_ID="k8s-dencer"
USER_EMAIL="alice@example.com"
USER_PASS="dencer"
WORK="${WORK:-$(mktemp -d)}"
# Absolute, because the certificate step cds into $WORK and everything after it
# would otherwise resolve chart paths and git against the temp directory.
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
fail()  { red "FAIL: $*"; exit 1; }

teardown() {
  k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
  docker rm -f "$DEX_CONTAINER" >/dev/null 2>&1 || true
  green "torn down"
}

if [[ "${1:-}" == "clean" ]]; then teardown; exit 0; fi

for tool in k3d docker openssl htpasswd helm kubectl; do
  command -v "$tool" >/dev/null || fail "$tool is required"
done

bold "==> certificates"
mkdir -p "$WORK/certs" && cd "$WORK/certs"
openssl req -x509 -newkey rsa:2048 -nodes -keyout ca.key -out ca.crt -days 2 \
  -subj "/CN=dencer-sso-ca" 2>/dev/null
openssl req -newkey rsa:2048 -nodes -keyout dex.key -out dex.csr \
  -subj "/CN=host.k3d.internal" 2>/dev/null
# host.k3d.internal is the name k3d injects into every node, so it is how the
# API server container reaches a service on the host.
printf 'subjectAltName=DNS:host.k3d.internal,DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n' > san.cnf
openssl x509 -req -in dex.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out dex.crt -days 2 -extfile san.cnf 2>/dev/null
green "  CA and server certificate for host.k3d.internal"

bold "==> dex"
HASH="$(htpasswd -bnBC 10 "" "$USER_PASS" | tr -d ':\n' | sed 's/^\$2y/\$2a/')"
cat > "$WORK/dex.yaml" <<EOF
issuer: ${ISSUER}
storage: {type: memory}
web:
  https: 0.0.0.0:${DEX_PORT}
  tlsCert: /certs/dex.crt
  tlsKey: /certs/dex.key
oauth2:
  passwordConnector: local
staticClients:
  - id: ${CLIENT_ID}
    name: k8s-dencer
    public: true
    redirectURIs: ["http://localhost:8090/oidc/callback"]
enablePasswordDB: true
staticPasswords:
  - email: ${USER_EMAIL}
    hash: "${HASH}"
    username: alice
    userID: alice-uid
EOF
docker rm -f "$DEX_CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$DEX_CONTAINER" -p "${DEX_PORT}:${DEX_PORT}" \
  -v "$WORK/dex.yaml:/etc/dex/config.yaml:ro" -v "$WORK/certs:/certs:ro" \
  ghcr.io/dexidp/dex:v2.41.1 dex serve /etc/dex/config.yaml >/dev/null
for _ in $(seq 1 30); do
  curl -sk --resolve "host.k3d.internal:${DEX_PORT}:127.0.0.1" \
    "${ISSUER}/.well-known/openid-configuration" >/dev/null 2>&1 && break
  sleep 1
done
green "  serving discovery at ${ISSUER}"

bold "==> k3d cluster trusting that issuer"
k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
k3d cluster create "$CLUSTER" --servers 1 --agents 0 --wait --timeout 180s \
  --volume "$WORK/certs/ca.crt:/etc/ssl/dex-ca.crt@server:*" \
  --k3s-arg "--kube-apiserver-arg=--oidc-issuer-url=${ISSUER}@server:*" \
  --k3s-arg "--kube-apiserver-arg=--oidc-client-id=${CLIENT_ID}@server:*" \
  --k3s-arg "--kube-apiserver-arg=--oidc-ca-file=/etc/ssl/dex-ca.crt@server:*" \
  --k3s-arg "--kube-apiserver-arg=--oidc-username-claim=email@server:*" \
  --k3s-arg "--kube-apiserver-arg=--oidc-groups-claim=groups@server:*" \
  --k3s-arg "--kube-apiserver-arg=--oidc-username-prefix=oidc:@server:*" \
  --k3s-arg "--kube-apiserver-arg=--oidc-groups-prefix=oidc:@server:*" >/dev/null 2>&1
green "  $CTX up"

bold "==> an ID token from dex"
TOKEN="$(curl -sk --resolve "host.k3d.internal:${DEX_PORT}:127.0.0.1" \
  -X POST "${ISSUER}/token" \
  -d grant_type=password -d "client_id=${CLIENT_ID}" \
  -d "username=${USER_EMAIL}" -d "password=${USER_PASS}" \
  -d "scope=openid profile email groups" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin).get("id_token",""))')"
[[ -n "$TOKEN" ]] || fail "dex issued no ID token"
green "  issued"

bold "==> the API server accepts it as a credential"
SERVER="$(kubectl config view -o jsonpath="{.clusters[?(@.name==\"$CTX\")].cluster.server}")"
# k3d writes 0.0.0.0 as the host, which is a bind address rather than a
# destination; curl will not reliably connect to it.
SERVER="${SERVER/0.0.0.0/127.0.0.1}"

# The API server fetches the issuer's JWKS lazily, so the first request after
# startup can land before OIDC is ready.
WHO=""
for _ in $(seq 1 20); do
  WHO="$(curl -sk -X POST "$SERVER/apis/authentication.k8s.io/v1/selfsubjectreviews" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"apiVersion":"authentication.k8s.io/v1","kind":"SelfSubjectReview"}' \
    | python3 -c 'import json,sys
d = json.load(sys.stdin)
s = d.get("status")
print(s.get("userInfo", {}).get("username", "") if isinstance(s, dict) else "")' 2>/dev/null)"
  [[ -n "$WHO" ]] && break
  sleep 2
done
[[ "$WHO" == "oidc:${USER_EMAIL}" ]] || fail "expected oidc:${USER_EMAIL}, got '${WHO:-<rejected>}'"
green "  authenticated as $WHO — no Kubernetes credential involved"

bold "==> k8s-dencer with that issuer"
TAG="$(git -C "$REPO" rev-parse --short HEAD)"
for c in planner ui-backend ui-frontend; do
  docker image inspect "k8s-dencer-$c:$TAG" >/dev/null 2>&1 \
    || fail "image k8s-dencer-$c:$TAG missing; run 'make images' first"
done
k3d image import -c "$CLUSTER" \
  "k8s-dencer-planner:$TAG" "k8s-dencer-ui-backend:$TAG" "k8s-dencer-ui-frontend:$TAG" >/dev/null 2>&1
helm --kube-context "$CTX" upgrade --install k8s-dencer "$REPO/charts/k8s-dencer" \
  -n k8s-dencer --create-namespace \
  --set planner.image.registry="" --set planner.image.repository=k8s-dencer-planner \
  --set uiBackend.image.registry="" --set uiBackend.image.repository=k8s-dencer-ui-backend \
  --set uiFrontend.image.registry="" --set uiFrontend.image.repository=k8s-dencer-ui-frontend \
  --set planner.image.tag="$TAG" --set uiBackend.image.tag="$TAG" --set uiFrontend.image.tag="$TAG" \
  --set planner.image.pullPolicy=IfNotPresent --set uiBackend.image.pullPolicy=IfNotPresent \
  --set uiFrontend.image.pullPolicy=IfNotPresent \
  --set auth.oidc.enabled=true --set "auth.oidc.issuerUrl=${ISSUER}" \
  --set "auth.oidc.clientId=${CLIENT_ID}" \
  --set persistence.enabled=false --set uiFrontend.podDisruptionBudget.enabled=false \
  --set kagent.enabled=false --wait --timeout 4m >/dev/null
green "  installed on a second, differently-provisioned cluster"

kubectl --context "$CTX" port-forward -n k8s-dencer svc/k8s-dencer-ui-backend 8092:8080 >/dev/null 2>&1 &
PF=$!
trap 'kill $PF 2>/dev/null || true' EXIT
sleep 4

bold "==> the authorization matrix, with a real IdP token"
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

[[ "$(code localhost:8092/api/v1/version)" == "401" ]] \
  || fail "no token should be 401"
green "  no token                -> 401"

[[ "$(code -H 'Authorization: Bearer not.a.token' localhost:8092/api/v1/version)" == "401" ]] \
  || fail "a forged token should be 401"
green "  forged token            -> 401"

[[ "$(code -H "Authorization: Bearer $TOKEN" localhost:8092/api/v1/version)" == "403" ]] \
  || fail "a valid token without RBAC should be 403"
green "  valid token, no RBAC    -> 403"

# Granting is one RoleBinding against the OIDC identity — the point of the
# whole design. The 403 above prints this command itself.
kubectl --context "$CTX" create rolebinding dencer-operator -n k8s-dencer \
  --clusterrole=k8s-dencer-consolidation-operator --user="oidc:${USER_EMAIL}" >/dev/null
sleep 2

[[ "$(code -H "Authorization: Bearer $TOKEN" localhost:8092/api/v1/version)" == "200" ]] \
  || fail "after the RoleBinding the same token should be 200"
green "  after one RoleBinding   -> 200"

# 403 would mean authorization refused; 501 means it passed and there is simply
# no executor deployed here.
EXEC="$(code -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"steps":[1]}' localhost:8092/api/v1/plans/latest/execute)"
[[ "$EXEC" != "403" ]] || fail "the operator role did not carry the execute permission"
green "  execute permission      -> $EXEC (not 403)"

echo
green "Tier-1 single sign-on verified: an ID token from the cluster's own issuer
is a Kubernetes credential, and access is one RoleBinding against the OIDC
identity. Run '$0 clean' to tear the cluster and dex down."
