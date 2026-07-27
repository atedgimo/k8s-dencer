package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

type contextKey struct{}

// WithIdentity stores an identity on a context.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// IdentityFrom recovers the authenticated caller. Handlers use it to attribute
// an action in the audit trail; it reports false when auth is disabled.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

// Middleware guards handlers with a required permission.
type Middleware struct {
	authn Authenticator
	authz Authorizer
	cfg   Config
	log   *slog.Logger
}

// NewMiddleware wires an authenticator and authorizer into HTTP middleware.
func NewMiddleware(authn Authenticator, authz Authorizer, cfg Config, log *slog.Logger) *Middleware {
	return &Middleware{authn: authn, authz: authz, cfg: cfg, log: log}
}

// Require wraps next so it runs only for callers holding res.
//
// When authentication is disabled the wrapper is transparent. That is a
// deployment choice for a private demo cluster, and the chart's schema refuses
// to combine it with an enabled executor — an unauthenticated route that can
// drain nodes should not be reachable by editing one value.
func (m *Middleware) Require(res Resource, next http.Handler) http.Handler {
	if !m.cfg.Enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := m.authn.Authenticate(r.Context(), r)

		switch {
		case err == nil:
			// Authenticated; fall through to the authorization check.

		case errors.Is(err, ErrNoCredentials):
			// Nothing was presented. Anonymous reads are permitted only when
			// configured, and never for a mutation whatever the setting says.
			if !(m.cfg.AllowAnonymousRead && res == ReadPlans) {
				m.unauthorized(w, "authentication required")
				return
			}
			// Setting allowAnonymousRead *is* the grant, so there is no
			// RoleBinding to consult. Asking anyway would always fail:
			// system:anonymous is refused before RBAC is even reached.
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
			return

		default:
			var invalid *InvalidTokenError
			if errors.As(err, &invalid) {
				// A credential was presented and rejected. Report that even
				// when anonymous reads are allowed: silently downgrading an
				// expired token to anonymous would leave a UI showing stale
				// data with no clue that its session had lapsed.
				m.log.Debug("token rejected", "reason", invalid.Reason)
				m.unauthorized(w, "invalid or expired credentials")
				return
			}
			// TokenReview itself failed. Fail closed.
			m.log.Error("authentication unavailable", "error", err)
			writeAuthError(w, http.StatusInternalServerError, "unavailable",
				"could not verify credentials", nil)
			return
		}

		if err := m.authz.Authorize(r.Context(), id, res); err != nil {
			var denied *DeniedError
			if errors.As(err, &denied) {
				m.forbidden(w, denied)
				return
			}
			m.log.Error("authorization unavailable", "error", err, "user", id.Username)
			writeAuthError(w, http.StatusInternalServerError, "unavailable",
				"could not check permissions", nil)
			return
		}

		next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
	})
}

// RequireFunc is Require for a HandlerFunc.
func (m *Middleware) RequireFunc(res Resource, next http.HandlerFunc) http.Handler {
	return m.Require(res, next)
}

func (m *Middleware) unauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="k8s-dencer"`)
	writeAuthError(w, http.StatusUnauthorized, "unauthenticated", msg, nil)
}

// forbidden answers with the exact permission that was missing, and a command
// that grants it. An operator who has just been refused should not have to read
// our source to find out which verb to ask their admin for.
func (m *Middleware) forbidden(w http.ResponseWriter, denied *DeniedError) {
	detail := map[string]any{
		"user":     denied.Identity.Username,
		"groups":   denied.Identity.Groups,
		"required": map[string]string{"verb": denied.Resource.Verb, "group": denied.Resource.Group, "resource": denied.Resource.Resource},
	}
	if denied.Reason != "" {
		detail["reason"] = denied.Reason
	}
	if hint := m.grantHint(denied); hint != "" {
		detail["grantWith"] = hint
	}
	writeAuthError(w, http.StatusForbidden, "forbidden",
		fmt.Sprintf("user %q may not %s", denied.Identity.Username, denied.Resource), detail)
}

func (m *Middleware) grantHint(denied *DeniedError) string {
	role := m.cfg.OperatorRoleName
	if role == "" || denied.Identity.IsAnonymous() {
		return ""
	}
	ns := m.cfg.Namespace
	if ns == "" {
		return fmt.Sprintf("kubectl create clusterrolebinding dencer-operator --clusterrole=%s --user=%q",
			role, denied.Identity.Username)
	}
	return fmt.Sprintf("kubectl create rolebinding dencer-operator -n %s --clusterrole=%s --user=%q",
		ns, role, denied.Identity.Username)
}

func writeAuthError(w http.ResponseWriter, status int, code, msg string, detail map[string]any) {
	body := map[string]any{"error": msg, "code": code}
	for k, v := range detail {
		body[k] = v
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
