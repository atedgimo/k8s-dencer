# Authentication and authorization

k8s-dencer holds no credential store of its own. Every identity decision is delegated to the Kubernetes API server, and every permission decision to cluster RBAC. This page covers how, and how to verify it yourself.

## Authentication

k8s-dencer owns **no credential store**. A caller presents a token,
[ui-backend](../internal/auth/authenticator.go) validates it with a `TokenReview`,
and permission is decided by a
[`SubjectAccessReview`](../internal/auth/authorizer.go). So "who may read the plan"
and "who may drain a node" are ordinary RoleBindings that compose with whatever
your cluster already uses, audit flows through the API server, and there is no
user database to breach.

On by default (`auth.enabled`). Two unbound ClusterRoles ship with the chart —
bind whichever you need:

| ClusterRole | Grants |
|---|---|
| `<release>-viewer` | `get plans.dencer.io` |
| `<release>-consolidation-operator` | the above, plus `create consolidations.dencer.io` |

Neither resource is a CRD, and neither ever will be: RBAC and
SubjectAccessReview match on strings, so a permission can be granted against a
resource name that has no API behind it. metrics-server relies on the same
property.

### Three ways to authenticate

All three converge on the same `SubjectAccessReview` — authentication is
pluggable, authorization is one code path.

**1. OIDC single sign-on** — the recommended deployment. Verified end to end
against a real Dex by [`hack/verify-sso.sh`](../hack/verify-sso.sh); see
[Verifying SSO](#verifying-sso). When your API server
runs with `--oidc-issuer-url`, an ID token from that issuer **is already a
Kubernetes credential**, so `TokenReview` validates it for us and returns the
user's IdP groups. SSO therefore costs a redirect flow in the browser and
nothing on the server: no session store, no user database, no client secret.

```yaml
auth:
  oidc:
    enabled: true
    issuerUrl: https://login.example.com/realms/platform   # the SAME issuer the API server trusts
    clientId: k8s-dencer                                   # public client, Authorization Code + PKCE
```

```bash
kubectl create rolebinding dencer-operators -n k8s-dencer \
  --clusterrole=k8s-dencer-consolidation-operator --group=oidc:platform-sre
```

Unavailable on clusters whose API server cannot validate a third-party token —
EKS with IAM auth, GKE with Google auth. Use an auth proxy there.

**2. Auth proxy** — oauth2-proxy, Ingress auth or a service mesh terminates SSO
and asserts the identity in a header. We skip `TokenReview` and go straight to
`SubjectAccessReview`, which accepts `spec.user` and `spec.groups` directly, so
we can ask "may *this* person do this" without ever holding their token.

Off by default, and **while it is off the headers are ignored, not merely
unused** — a request carrying `X-Forwarded-User` is anonymous, not an
administrator. Turning it on means anything that can reach ui-backend can claim
to be anyone, so `networkPolicy.enabled` is what makes the proxy unbypassable.

**Signing in.** Where an issuer is configured the UI runs the redirect flow
itself; `oidc-client-ts` is loaded on demand, so an install using a pasted
token never downloads it. The credential taken from the flow is the **ID
token** — an access token means nothing to the Kubernetes API server, and
confusing the two is the classic way to make this fail with an opaque 401. The
redirect lands on `/oidc/callback`, which the SPA fallback serves; register that
URI with your issuer.

**3. Bearer token** — for the POC and CI.

```bash
make token     # mints one and prints it; paste into the UI
```

### Notes worth knowing

- **Token expiry cannot break a run.** From M10, authorization happens once at
  enqueue and the executor then works under its own ServiceAccount, so a
  15-minute ID token can authorize a 40-minute consolidation. The authenticated
  username and groups are recorded on the run and on every audit event.
- **A 403 tells you how to fix it** — it names the exact verb, group and
  resource, and quotes the `kubectl create rolebinding` that grants them.
- **Authentication is cached for 30s; authorization never is.** A revoked
  RoleBinding must stop working immediately.
- **`/api/v1/authinfo` is served openly.** A client cannot sign in without
  knowing where to sign in, and the answer is an issuer URL and a public client
  ID. [A test](../internal/api/rest/guarded_test.go) fails if any *other* route is
  registered without a guard — which is what will stop an execute endpoint
  shipping unauthenticated in M10.
- **Do not assume your NetworkPolicy is enforced.** A CNI that does not
  implement NetworkPolicy accepts the object and silently ignores it — there is
  no warning and no event. k3s's default CNI is one of these, so on the OrbStack
  POC cluster the policy provides *zero* protection: a pod in `default` reaches
  ui-backend with a 200. Verify on your own cluster before relying on it, and
  note that `auth.trustedProxy` is only safe where it *is* enforced.

  ```bash
  kubectl run probe --rm -i --restart=Never -n default --image=curlimages/curl:8.11.1 \
    --command -- curl -s -o /dev/null -w '%{http_code}\n' \
    http://<release>-ui-backend.<ns>.svc:8080/api/v1/authinfo   # 000 = enforced, 200 = not
  ```

- **The MCP surface can be token-guarded, and on an unenforced CNI it should
  be.** Verified against Kagent 0.9.12: `RemoteMCPServer.headersFrom` presents a
  bearer token and all four tools are discovered through it. Off by default only
  because the secret is yours to create and rotate — the chart will not mint a
  non-expiring ServiceAccount token on your behalf.

  ```bash
  kubectl create serviceaccount dencer-agent -n k8s-dencer
  kubectl create rolebinding dencer-agent -n k8s-dencer \
    --clusterrole=k8s-dencer-viewer --serviceaccount=k8s-dencer:dencer-agent
  kubectl create secret generic dencer-mcp-token -n kagent \
    --from-literal=authorization="Bearer $(kubectl create token dencer-agent -n k8s-dencer --duration=720h)"
  helm upgrade ... --set auth.mcpRequireToken=true \
    --set kagent.authSecret.name=dencer-mcp-token
  ```

  The value must be the whole header — Kagent substitutes it verbatim. The
  surface is read-only either way, and a test fails if a fifth tool appears.
- **The plan is pinned while you have work in progress.** The planner
  republishes every resync and again after any drain. Step numbers are
  positional, so a plan swapped underneath a ticked selection would leave it
  meaning different nodes — the one path here that could drain something you did
  not choose. While a selection or run is outstanding the view holds, and a
  newer plan waits behind a banner.

---

---

## Verifying SSO

```bash
make images && ./hack/verify-sso.sh              # Dex, ~2 minutes
make images && IDP=keycloak ./hack/verify-sso.sh # Keycloak, ~4 minutes
./hack/verify-sso.sh clean                       # tear it down
```

**Use Keycloak if you care about groups.** Dex's local connector cannot emit a
groups claim alongside static passwords, so that path can only bind by
username. Keycloak emits real groups, so it verifies what operators actually
want — access granted to an IdP *group* rather than a list of people:

```
groups: ["platform-sre"]
kubectl create rolebinding … --group=oidc:platform-sre   -> 200
```

It also guards a trap that costs an afternoon in production. Keycloak's Group
Membership mapper defaults to **full group path**, emitting `/platform-sre`
with a leading slash — so every RoleBinding would need `oidc:/platform-sre`,
which reads as a typo. The realm sets `full.path=false` and the script fails if
a leading slash ever appears.

Builds a throwaway k3d cluster whose API server trusts a local Dex, installs the
chart, and walks the authorization matrix with a real IdP token:

| | |
|---|---|
| no token | `401` |
| forged token | `401` |
| valid Dex token, no RBAC | `403` naming the verb |
| after one RoleBinding | `200` |

**Why k3d and not the OrbStack cluster.** OrbStack runs k3s with the API server
embedded and no way to pass `--oidc-issuer-url` — no config file, no CLI flag,
and the k8s VM is not reachable as a machine. A throwaway cluster is the only
way to test this locally, and it has the side benefit of exercising the chart on
a second, differently-provisioned cluster (k3s 1.35 rather than 1.34), which is
the first real test of the platform-agnostic claim the chart has made since M0.

**Closed gap.** The Dex path could only verify a username identity. `IDP=keycloak`
closes it: the group claim is asserted in the token, and the RoleBinding is made
against the group rather than the person.

---

## Credentials for the CLI

The [CLI](cli.md) authenticates exactly as the UI does — it sends a bearer
token and the backend resolves it with `TokenReview`. What differs is where the
token comes from.

**With OIDC single sign-on**, the ID token already in your kubeconfig *is* a
Kubernetes credential, and the CLI uses it directly. Nothing to mint.

**With a client-certificate kubeconfig** — the default on k3d, kind and
OrbStack — there is no token at all. A certificate proves who you are to the
API server and means nothing to a `TokenReview` call, so one has to be minted:

```bash
export DENCER_TOKEN="$(kubectl create token dencer-operator -n k8s-dencer)"
```

The CLI detects this case and prints that command rather than letting the
server answer "unauthenticated", which would send you looking in the wrong
place.

Whatever the source, the token is only ever an identity. What it may *do* is
decided by `SubjectAccessReview` against your cluster's RBAC: `get
plans.dencer.io` to read, `create consolidations.dencer.io` to execute. A 403
from the CLI names the missing verb.

---

[← Documentation index](README.md) · [Project README](../README.md)