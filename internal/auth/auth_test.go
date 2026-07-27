package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/atedgimo/k8s-dencer/internal/auth"
)

// user is the identity a valid token resolves to throughout these tests.
const validToken = "a-valid-token"

var testUser = authenticationv1.UserInfo{
	Username: "alice@example.com",
	UID:      "uid-1",
	Groups:   []string{"system:authenticated", "oidc:platform-sre"},
}

// clusterOpts configures the fake API server's answers.
type clusterOpts struct {
	authenticated map[string]authenticationv1.UserInfo // token -> user
	allow         bool
	denyReason    string
	tokenErr      error
	sarErr        error
	tokenReviews  *atomic.Int32
}

func newCluster(t *testing.T, opts clusterOpts) *fake.Clientset {
	t.Helper()
	cs := fake.NewSimpleClientset()

	cs.PrependReactor("create", "tokenreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		if opts.tokenReviews != nil {
			opts.tokenReviews.Add(1)
		}
		if opts.tokenErr != nil {
			return true, nil, opts.tokenErr
		}
		tr := a.(ktesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		user, ok := opts.authenticated[tr.Spec.Token]
		if !ok {
			tr.Status = authenticationv1.TokenReviewStatus{
				Authenticated: false,
				Error:         "token lookup failed",
			}
			return true, tr, nil
		}
		tr.Status = authenticationv1.TokenReviewStatus{Authenticated: true, User: user}
		return true, tr, nil
	})

	cs.PrependReactor("create", "subjectaccessreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		if opts.sarErr != nil {
			return true, nil, opts.sarErr
		}
		sar := a.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		sar.Status = authorizationv1.SubjectAccessReviewStatus{
			Allowed: opts.allow,
			Reason:  opts.denyReason,
		}
		return true, sar, nil
	})

	return cs
}

func guarded(t *testing.T, cs *fake.Clientset, cfg auth.Config, res auth.Resource) http.Handler {
	t.Helper()
	if cfg.Namespace == "" {
		cfg.Namespace = "k8s-dencer"
	}
	m := auth.NewMiddleware(
		auth.NewAuthenticator(cs, cfg),
		auth.NewAuthorizer(cs, cfg.Namespace),
		cfg,
		slog.New(slog.DiscardHandler),
	)
	return m.Require(res, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.IdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(id)
	}))
}

func do(h http.Handler, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/plans/latest", nil)
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func withToken(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func enabled() auth.Config {
	return auth.Config{Enabled: true, Namespace: "k8s-dencer"}
}

// The single most important test in this package. An operator who can reach
// ui-backend directly must not be able to promote themselves by setting the
// header an auth proxy would have set. While trustedProxy is off the header is
// ignored outright, not merely unused.
func TestForgedProxyHeaderIsIgnoredWhenTrustedProxyDisabled(t *testing.T) {
	cfg := enabled()
	cfg.TrustedProxy = auth.TrustedProxy{
		Enabled:      false,
		UserHeader:   "X-Forwarded-User",
		GroupsHeader: "X-Forwarded-Groups",
	}
	cs := newCluster(t, clusterOpts{allow: true})

	rec := do(guarded(t, cs, cfg, auth.ExecuteConsolidations), func(r *http.Request) {
		r.Header.Set("X-Forwarded-User", "attacker")
		r.Header.Set("X-Forwarded-Groups", "system:masters")
	})

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged header was honoured: got %d, want 401\n%s", rec.Code, rec.Body)
	}
	// Even the SubjectAccessReview must not have been attempted — a cluster
	// that happens to bind system:masters would otherwise allow the request.
	for _, a := range cs.Actions() {
		if a.GetResource().Resource == "subjectaccessreviews" {
			t.Error("a forged header reached the authorizer; it must never get that far")
		}
	}
}

func TestTrustedProxyHeaderIsHonouredWhenEnabled(t *testing.T) {
	cfg := enabled()
	cfg.TrustedProxy = auth.TrustedProxy{
		Enabled:      true,
		UserHeader:   "X-Forwarded-User",
		GroupsHeader: "X-Forwarded-Groups",
	}
	cs := newCluster(t, clusterOpts{allow: true})

	rec := do(guarded(t, cs, cfg, auth.ExecuteConsolidations), func(r *http.Request) {
		r.Header.Set("X-Forwarded-User", "bob@example.com")
		r.Header.Set("X-Forwarded-Groups", "platform-sre, oncall")
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200\n%s", rec.Code, rec.Body)
	}
	var id auth.Identity
	if err := json.Unmarshal(rec.Body.Bytes(), &id); err != nil {
		t.Fatal(err)
	}
	if id.Username != "bob@example.com" || id.Source != auth.SourceProxy {
		t.Errorf("identity wrong: %+v", id)
	}
	// Groups drive the RoleBinding, so the split has to be right.
	if len(id.Groups) != 2 || id.Groups[0] != "platform-sre" || id.Groups[1] != "oncall" {
		t.Errorf("groups mis-parsed: %q", id.Groups)
	}
}

// Both credentials present: the verifiable one must win over the asserted one.
func TestBearerTokenBeatsProxyHeader(t *testing.T) {
	cfg := enabled()
	cfg.TrustedProxy = auth.TrustedProxy{Enabled: true, UserHeader: "X-Forwarded-User"}
	cs := newCluster(t, clusterOpts{
		authenticated: map[string]authenticationv1.UserInfo{validToken: testUser},
		allow:         true,
	})

	rec := do(guarded(t, cs, cfg, auth.ReadPlans), func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+validToken)
		r.Header.Set("X-Forwarded-User", "someone-else")
	})

	var id auth.Identity
	_ = json.Unmarshal(rec.Body.Bytes(), &id)
	if id.Username != testUser.Username || id.Source != auth.SourceToken {
		t.Errorf("token should have won, got %+v", id)
	}
}

func TestValidTokenCarriesOIDCGroupsThrough(t *testing.T) {
	cs := newCluster(t, clusterOpts{
		authenticated: map[string]authenticationv1.UserInfo{validToken: testUser},
		allow:         true,
	})

	rec := do(guarded(t, cs, enabled(), auth.ExecuteConsolidations), withToken(validToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200\n%s", rec.Code, rec.Body)
	}

	// The group claim is the whole point of OIDC passthrough: permission is
	// granted to oidc:platform-sre, not to a list of usernames.
	var sarGroups []string
	for _, a := range cs.Actions() {
		if c, ok := a.(ktesting.CreateAction); ok && a.GetResource().Resource == "subjectaccessreviews" {
			sarGroups = c.GetObject().(*authorizationv1.SubjectAccessReview).Spec.Groups
		}
	}
	if len(sarGroups) != 2 || sarGroups[1] != "oidc:platform-sre" {
		t.Errorf("IdP groups did not reach the SubjectAccessReview: %q", sarGroups)
	}
}

func TestSubjectAccessReviewAsksForTheDocumentedVerbs(t *testing.T) {
	for _, tc := range []struct {
		name             string
		res              auth.Resource
		wantVerb, wantRe string
	}{
		{"read", auth.ReadPlans, "get", "plans"},
		{"execute", auth.ExecuteConsolidations, "create", "consolidations"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := newCluster(t, clusterOpts{
				authenticated: map[string]authenticationv1.UserInfo{validToken: testUser},
				allow:         true,
			})
			do(guarded(t, cs, enabled(), tc.res), withToken(validToken))

			var spec *authorizationv1.ResourceAttributes
			for _, a := range cs.Actions() {
				if c, ok := a.(ktesting.CreateAction); ok && a.GetResource().Resource == "subjectaccessreviews" {
					spec = c.GetObject().(*authorizationv1.SubjectAccessReview).Spec.ResourceAttributes
				}
			}
			if spec == nil {
				t.Fatal("no SubjectAccessReview was created")
			}
			if spec.Verb != tc.wantVerb || spec.Resource != tc.wantRe || spec.Group != "dencer.io" {
				t.Errorf("wrong permission checked: %+v", spec)
			}
			// Namespaced, so access can be granted with a RoleBinding rather
			// than requiring a cluster-wide grant.
			if spec.Namespace != "k8s-dencer" {
				t.Errorf("SAR should be namespaced, got %q", spec.Namespace)
			}
		})
	}
}

// A 403 has to be actionable. An operator reading it should learn the exact
// verb they lack and the command that grants it.
func TestForbiddenNamesThePermissionAndHowToGrantIt(t *testing.T) {
	cfg := enabled()
	cfg.OperatorRoleName = "dencer-consolidation-operator"
	cs := newCluster(t, clusterOpts{
		authenticated: map[string]authenticationv1.UserInfo{validToken: testUser},
		allow:         false,
		denyReason:    "no RoleBinding found",
	})

	rec := do(guarded(t, cs, cfg, auth.ExecuteConsolidations), withToken(validToken))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}

	var body struct {
		Code      string            `json:"code"`
		User      string            `json:"user"`
		Required  map[string]string `json:"required"`
		GrantWith string            `json:"grantWith"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "forbidden" || body.User != testUser.Username {
		t.Errorf("unexpected body: %+v", body)
	}
	if body.Required["verb"] != "create" || body.Required["resource"] != "consolidations" {
		t.Errorf("denial does not name the permission: %+v", body.Required)
	}
	for _, want := range []string{"kubectl create rolebinding", "dencer-consolidation-operator", testUser.Username} {
		if !strings.Contains(body.GrantWith, want) {
			t.Errorf("grant hint missing %q: %s", want, body.GrantWith)
		}
	}
}

func TestMissingCredentialsAreUnauthorizedWithAChallenge(t *testing.T) {
	cs := newCluster(t, clusterOpts{allow: true})
	rec := do(guarded(t, cs, enabled(), auth.ReadPlans), nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Error("401 must carry a Bearer challenge so a client knows what to present")
	}
}

func TestAnonymousReadIsOptInAndNeverCoversExecute(t *testing.T) {
	cfg := enabled()
	cfg.AllowAnonymousRead = true
	cs := newCluster(t, clusterOpts{allow: true})

	if got := do(guarded(t, cs, cfg, auth.ReadPlans), nil).Code; got != http.StatusOK {
		t.Errorf("anonymous read: got %d, want 200", got)
	}
	// The critical half: allowing anonymous reads must not leak into the
	// permission that drains nodes.
	if got := do(guarded(t, cs, cfg, auth.ExecuteConsolidations), nil).Code; got != http.StatusUnauthorized {
		t.Errorf("anonymous execute: got %d, want 401", got)
	}
}

// An expired token must not be quietly downgraded to anonymous when anonymous
// reads are permitted, or a UI would show stale data with no sign the session
// had lapsed.
func TestRejectedTokenIs401EvenWhenAnonymousReadIsAllowed(t *testing.T) {
	cfg := enabled()
	cfg.AllowAnonymousRead = true
	cs := newCluster(t, clusterOpts{allow: true})

	rec := do(guarded(t, cs, cfg, auth.ReadPlans), withToken("expired"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	// The reason a token failed is more than an unauthenticated caller should
	// learn; "expired" versus "forged" is a probing oracle.
	if strings.Contains(rec.Body.String(), "token lookup failed") {
		t.Errorf("internal rejection reason leaked: %s", rec.Body)
	}
}

// An unreachable API server is our failure, not the caller's. Reporting it as
// 401 or 403 would send an operator hunting for a RoleBinding they already have.
func TestApiServerFailuresFailClosedAs500(t *testing.T) {
	t.Run("tokenreview", func(t *testing.T) {
		cs := newCluster(t, clusterOpts{tokenErr: errors.New("connection refused")})
		rec := do(guarded(t, cs, enabled(), auth.ReadPlans), withToken(validToken))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("got %d, want 500", rec.Code)
		}
	})
	t.Run("subjectaccessreview", func(t *testing.T) {
		cs := newCluster(t, clusterOpts{
			authenticated: map[string]authenticationv1.UserInfo{validToken: testUser},
			sarErr:        errors.New("connection refused"),
		})
		rec := do(guarded(t, cs, enabled(), auth.ReadPlans), withToken(validToken))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("got %d, want 500", rec.Code)
		}
	})
}

func TestDisabledAuthIsTransparent(t *testing.T) {
	cs := newCluster(t, clusterOpts{allow: false})
	rec := do(guarded(t, cs, auth.Config{Enabled: false}, auth.ExecuteConsolidations), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 when auth is disabled", rec.Code)
	}
	if len(cs.Actions()) != 0 {
		t.Error("disabled auth should not talk to the API server at all")
	}
}

// The UI polls. Re-validating the same token against the API server on every
// request would put avoidable load on the control plane.
func TestPositiveTokenReviewsAreCached(t *testing.T) {
	var reviews atomic.Int32
	cs := newCluster(t, clusterOpts{
		authenticated: map[string]authenticationv1.UserInfo{validToken: testUser},
		allow:         true,
		tokenReviews:  &reviews,
	})
	h := guarded(t, cs, enabled(), auth.ReadPlans)

	for range 5 {
		if got := do(h, withToken(validToken)).Code; got != http.StatusOK {
			t.Fatalf("got %d, want 200", got)
		}
	}
	if n := reviews.Load(); n != 1 {
		t.Errorf("TokenReview ran %d times, want 1 (the rest served from cache)", n)
	}

	// Authorization, by contrast, must never be cached: a revoked RoleBinding
	// has to stop working immediately.
	var sars int
	for _, a := range cs.Actions() {
		if a.GetResource().Resource == "subjectaccessreviews" {
			sars++
		}
	}
	if sars != 5 {
		t.Errorf("SubjectAccessReview ran %d times, want 5 — authorization must not be cached", sars)
	}
}

// A rejected token is not cached, so revoking access takes effect at once and a
// caller cannot pin a negative result.
func TestRejectedTokensAreNotCached(t *testing.T) {
	var reviews atomic.Int32
	cs := newCluster(t, clusterOpts{allow: true, tokenReviews: &reviews})
	h := guarded(t, cs, enabled(), auth.ReadPlans)

	do(h, withToken("nope"))
	do(h, withToken("nope"))

	if n := reviews.Load(); n != 2 {
		t.Errorf("TokenReview ran %d times, want 2", n)
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	cs := newCluster(t, clusterOpts{
		authenticated: map[string]authenticationv1.UserInfo{validToken: testUser},
		allow:         true,
	})
	rec := do(guarded(t, cs, enabled(), auth.ReadPlans), func(r *http.Request) {
		r.Header.Set("Authorization", "bearer "+validToken)
	})
	if rec.Code != http.StatusOK {
		t.Errorf("lowercase scheme rejected: %d", rec.Code)
	}
}

// system:anonymous is a real Kubernetes user that a cluster could in principle
// bind to a role. We refuse before asking, so a careless binding elsewhere
// cannot hand an unauthenticated caller the ability to drain nodes.
func TestAnonymousIsRefusedWithoutConsultingRBAC(t *testing.T) {
	cs := newCluster(t, clusterOpts{allow: true})
	err := auth.NewAuthorizer(cs, "k8s-dencer").
		Authorize(context.Background(), auth.Anonymous, auth.ExecuteConsolidations)

	var denied *auth.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("anonymous was authorized: %v", err)
	}
	if len(cs.Actions()) != 0 {
		t.Error("anonymous reached the API server; it must be refused locally")
	}
}
