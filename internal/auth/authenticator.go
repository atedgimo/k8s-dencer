package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Authenticator resolves an HTTP request to an Identity.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (Identity, error)
}

// TokenReviewAuthenticator validates bearer tokens against the Kubernetes API
// server, falling back to a trusted proxy header when one is configured.
type TokenReviewAuthenticator struct {
	client kubernetes.Interface
	cfg    Config
	cache  *tokenCache
	now    func() time.Time
}

// NewAuthenticator builds an authenticator over a Kubernetes client.
func NewAuthenticator(client kubernetes.Interface, cfg Config) *TokenReviewAuthenticator {
	ttl := cfg.TokenCacheTTL
	if ttl <= 0 {
		ttl = DefaultTokenCacheTTL
	}
	return &TokenReviewAuthenticator{
		client: client,
		cfg:    cfg,
		cache:  newTokenCache(ttl),
		now:    time.Now,
	}
}

// Authenticate resolves the caller.
//
// A bearer token wins over a proxy header when both are present: the token is
// verifiable against the API server, whereas the header is merely asserted, and
// the stronger evidence should decide.
func (a *TokenReviewAuthenticator) Authenticate(ctx context.Context, r *http.Request) (Identity, error) {
	if token := bearerToken(r); token != "" {
		return a.authenticateToken(ctx, token)
	}
	if id, ok := a.proxyIdentity(r); ok {
		return id, nil
	}
	return Anonymous, ErrNoCredentials
}

func (a *TokenReviewAuthenticator) authenticateToken(ctx context.Context, token string) (Identity, error) {
	key := hashToken(token)
	if id, ok := a.cache.get(key, a.now()); ok {
		return id, nil
	}

	review, err := a.client.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{
			Token:     token,
			Audiences: a.cfg.Audiences,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		// The API server is unreachable or we lack permission to create
		// TokenReviews. Either way this is our fault, not the caller's, and it
		// must not be reported as a rejected token.
		return Anonymous, err
	}

	if !review.Status.Authenticated {
		reason := review.Status.Error
		if reason == "" {
			reason = "not authenticated"
		}
		return Anonymous, &InvalidTokenError{Reason: reason}
	}

	id := Identity{
		Username: review.Status.User.Username,
		UID:      review.Status.User.UID,
		Groups:   review.Status.User.Groups,
		Extra:    extraFromUserInfo(review.Status.User.Extra),
		Source:   SourceToken,
	}
	a.cache.put(key, id, a.now())
	return id, nil
}

// proxyIdentity reads an identity asserted by an upstream auth proxy.
//
// The headers are read only when TrustedProxy is enabled. While it is off they
// are not merely unused but actively ignored, so a client that sets
// X-Forwarded-User by hand is anonymous rather than an administrator.
func (a *TokenReviewAuthenticator) proxyIdentity(r *http.Request) (Identity, bool) {
	if !a.cfg.TrustedProxy.Enabled || a.cfg.TrustedProxy.UserHeader == "" {
		return Anonymous, false
	}
	user := strings.TrimSpace(r.Header.Get(a.cfg.TrustedProxy.UserHeader))
	if user == "" {
		return Anonymous, false
	}

	var groups []string
	if h := a.cfg.TrustedProxy.GroupsHeader; h != "" {
		for _, g := range strings.Split(r.Header.Get(h), ",") {
			if g = strings.TrimSpace(g); g != "" {
				groups = append(groups, g)
			}
		}
	}
	return Identity{Username: user, Groups: groups, Source: SourceProxy}, true
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	// RFC 7235 makes the scheme case-insensitive, and clients differ.
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(h[7:])
}

// hashToken keys the cache without holding the credential. Storing raw tokens
// in a long-lived map would turn a heap dump into a set of working credentials.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func extraFromUserInfo(in map[string]authenticationv1.ExtraValue) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = []string(v)
	}
	return out
}

// maxCachedTokens bounds the cache. Reached only under an unusual number of
// distinct tokens, at which point we stop caching rather than evict something
// arbitrary — degrading to the uncached path is always correct.
const maxCachedTokens = 1024

type cacheEntry struct {
	identity  Identity
	expiresAt time.Time
}

type tokenCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]cacheEntry
}

func newTokenCache(ttl time.Duration) *tokenCache {
	return &tokenCache{ttl: ttl, entries: make(map[string]cacheEntry)}
}

func (c *tokenCache) get(key string, now time.Time) (Identity, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return Identity{}, false
	}
	if !now.Before(e.expiresAt) {
		delete(c.entries, key)
		return Identity{}, false
	}
	return e.identity, true
}

func (c *tokenCache) put(key string, id Identity, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= maxCachedTokens {
		for k, e := range c.entries {
			if !now.Before(e.expiresAt) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= maxCachedTokens {
			return
		}
	}
	c.entries[key] = cacheEntry{identity: id, expiresAt: now.Add(c.ttl)}
}
