// Package auth authenticates and authorizes callers by delegating to
// Kubernetes.
//
// We deliberately own no credential store. A caller proves who they are with a
// token the API server already trusts, and "may this person drain a node?"
// becomes an ordinary RoleBinding that composes with whatever the cluster
// already uses — OIDC, IRSA, or plain ServiceAccount tokens.
//
// Three ways an identity can arrive, all converging on the same
// SubjectAccessReview so that authorization is one code path:
//
//  1. A bearer token, validated by TokenReview. When the API server is
//     configured with --oidc-issuer-url, an ID token from that issuer is
//     already a Kubernetes credential, so SSO costs us nothing but a redirect
//     flow in the browser.
//  2. A header from a trusted auth proxy, for clusters whose API server cannot
//     validate a third-party token (EKS with IAM auth, GKE with Google auth).
//     Off by default; see TrustedProxy.
//  3. Anonymous, permitted for reads only when explicitly configured.
package auth

import (
	"errors"
	"fmt"
	"time"
)

// Source records how an identity was established. It is carried into the audit
// trail because "who drained this node" is a different question depending on
// whether the answer came from a verified token or an asserted header.
type Source string

const (
	SourceToken     Source = "token"
	SourceProxy     Source = "proxy"
	SourceAnonymous Source = "anonymous"
)

// Identity is an authenticated caller.
type Identity struct {
	Username string              `json:"username"`
	UID      string              `json:"uid,omitempty"`
	Groups   []string            `json:"groups,omitempty"`
	Extra    map[string][]string `json:"extra,omitempty"`
	Source   Source              `json:"source"`
}

// Anonymous is the identity of an unauthenticated caller. It is only ever
// authorized when AllowAnonymousRead is set, and never for a mutation.
var Anonymous = Identity{Username: "system:anonymous", Source: SourceAnonymous}

// IsAnonymous reports whether no credential was presented.
func (i Identity) IsAnonymous() bool { return i.Source == SourceAnonymous }

// Resource names a permission to check.
//
// The group and resource strings need no CRD behind them: RBAC and
// SubjectAccessReview match on strings, so an admin can grant
// "create consolidations.dencer.io" against a resource that exists only as a
// name. metrics-server and the Prometheus adapter lean on the same property.
type Resource struct {
	Group    string
	Resource string
	Verb     string
}

// The two permissions k8s-dencer checks. Read and execute are separate verbs on
// separate resources so that "may look" and "may drain" can be granted to
// different people — which is the entire point of putting this behind RBAC.
var (
	// ReadPlans guards the plan, graph and constraint endpoints.
	ReadPlans = Resource{Group: "dencer.io", Resource: "plans", Verb: "get"}

	// ExecuteConsolidations guards every route that can change the cluster.
	// Nothing in Phase 1 uses it; it exists now so the executor lands in M10
	// with its gate already built and tested.
	ExecuteConsolidations = Resource{Group: "dencer.io", Resource: "consolidations", Verb: "create"}
)

// String renders a permission the way kubectl does, so a denial message can be
// pasted more or less straight into a Role.
func (r Resource) String() string {
	return fmt.Sprintf("%s %s.%s", r.Verb, r.Resource, r.Group)
}

// TrustedProxy configures identity assertion by an upstream auth proxy
// (oauth2-proxy, Ingress auth, a service mesh).
//
// Off by default, and the headers are ignored entirely while it is off — a
// request carrying them is treated as anonymous rather than trusted. When it is
// on, the headers are only as trustworthy as the network path, so the chart's
// NetworkPolicy is what actually makes the proxy unbypassable.
type TrustedProxy struct {
	Enabled bool
	// UserHeader carries the authenticated username, e.g. X-Forwarded-User.
	UserHeader string
	// GroupsHeader carries comma-separated groups, e.g. X-Forwarded-Groups.
	// Optional: an IdP that asserts no groups still yields a usable identity.
	GroupsHeader string
}

// Config is the authentication and authorization configuration.
type Config struct {
	// Enabled turns the whole mechanism on. When false every request runs as
	// Anonymous and every check passes. The chart's schema refuses to combine
	// that with an enabled executor.
	Enabled bool

	// AllowAnonymousRead permits unauthenticated GETs. It never applies to a
	// mutation, whatever it is set to.
	AllowAnonymousRead bool

	// Audiences restricts which token audiences are acceptable. Empty means the
	// API server's own audience, which is the right default: for an OIDC ID
	// token the audience is the client ID the API server was configured with,
	// so the check is already being made by the party best placed to make it.
	Audiences []string

	// Namespace scopes the SubjectAccessReview, so permission can be granted
	// with a RoleBinding in the release namespace rather than cluster-wide.
	Namespace string

	// OperatorRoleName is the ClusterRole the chart ships carrying the execute
	// permission. It appears in denial messages so a 403 can name the command
	// that fixes it.
	OperatorRoleName string

	// TokenCacheTTL caches positive TokenReview results. The UI polls, and a
	// round-trip to the API server per request is waste.
	//
	// Only authentication is cached, never authorization: a revoked RoleBinding
	// must stop working immediately, and a cached "allowed" that outlives the
	// grant is exactly the bug you cannot afford in the component that drains
	// nodes.
	TokenCacheTTL time.Duration

	// OIDC describes the issuer the UI should sign in against. It is published
	// verbatim to unauthenticated callers, so it must contain only public
	// values — an issuer URL and a public client ID, never a client secret.
	OIDC OIDCInfo

	TrustedProxy TrustedProxy
}

// OIDCInfo is the public half of an OIDC client configuration.
//
// There is no client secret because the UI is a browser application using
// Authorization Code with PKCE: a secret shipped to a browser is not a secret.
type OIDCInfo struct {
	Enabled   bool     `json:"enabled"`
	IssuerURL string   `json:"issuerUrl,omitempty"`
	ClientID  string   `json:"clientId,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
}

// Info tells an unauthenticated client how to authenticate.
//
// Served openly and deliberately: a caller cannot sign in without knowing the
// issuer, and every field here is public by construction. Publishing it from
// the API rather than the frontend's ConfigMap keeps the two from drifting.
type Info struct {
	Enabled       bool     `json:"enabled"`
	AnonymousRead bool     `json:"anonymousRead"`
	OIDC          OIDCInfo `json:"oidc"`
}

// Describe renders the public description of this configuration.
func (c Config) Describe() Info {
	return Info{Enabled: c.Enabled, AnonymousRead: c.AllowAnonymousRead, OIDC: c.OIDC}
}

// DefaultTokenCacheTTL is short enough that a revoked token stops working
// promptly and long enough to absorb the UI's polling.
const DefaultTokenCacheTTL = 30 * time.Second

// ErrNoCredentials means the caller presented nothing to authenticate with.
var ErrNoCredentials = errors.New("no credentials presented")

// InvalidTokenError means a token was presented and rejected. The reason is
// deliberately not surfaced to the caller — it can distinguish "expired" from
// "forged", which is more than an unauthenticated client should learn.
type InvalidTokenError struct{ Reason string }

func (e *InvalidTokenError) Error() string { return "invalid token: " + e.Reason }

// DeniedError is an authorization failure carrying enough detail for an admin
// to write the missing RoleBinding straight from the error text.
type DeniedError struct {
	Identity Identity
	Resource Resource
	Reason   string
}

func (e *DeniedError) Error() string {
	msg := fmt.Sprintf("user %q cannot %s", e.Identity.Username, e.Resource)
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}
