package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func authenticatingClient(reviews *int) *fake.Clientset {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "tokenreviews", func(a ktesting.Action) (bool, runtime.Object, error) {
		*reviews++
		tr := a.(ktesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
		tr.Status = authenticationv1.TokenReviewStatus{
			Authenticated: true,
			User:          authenticationv1.UserInfo{Username: "alice@example.com"},
		}
		return true, tr, nil
	})
	return cs
}

func request(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// The cache outlives any single request, so it must never hold a credential a
// heap dump could recover. Keys are SHA-256 digests, and this test fails if
// anyone later "simplifies" that to the token itself.
func TestCacheNeverStoresTheRawToken(t *testing.T) {
	const secret = "super-secret-token-value"
	reviews := 0
	a := NewAuthenticator(authenticatingClient(&reviews), Config{Enabled: true})

	if _, err := a.Authenticate(context.Background(), request(secret)); err != nil {
		t.Fatal(err)
	}

	a.cache.mu.Lock()
	defer a.cache.mu.Unlock()
	if len(a.cache.entries) != 1 {
		t.Fatalf("expected one cached entry, got %d", len(a.cache.entries))
	}
	for key, entry := range a.cache.entries {
		if strings.Contains(key, secret) {
			t.Error("the raw token is being used as a cache key")
		}
		if len(key) != 64 {
			t.Errorf("cache key is not a SHA-256 digest: %q", key)
		}
		if strings.Contains(entry.identity.Username, secret) {
			t.Error("the raw token leaked into the cached identity")
		}
	}
}

func TestCachedIdentityExpires(t *testing.T) {
	reviews := 0
	a := NewAuthenticator(authenticatingClient(&reviews), Config{Enabled: true, TokenCacheTTL: time.Minute})

	clock := time.Now()
	a.now = func() time.Time { return clock }

	ctx := context.Background()
	if _, err := a.Authenticate(ctx, request("t")); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(59 * time.Second)
	if _, err := a.Authenticate(ctx, request("t")); err != nil {
		t.Fatal(err)
	}
	if reviews != 1 {
		t.Fatalf("within the TTL: TokenReview ran %d times, want 1", reviews)
	}

	// Past the TTL the token must be re-validated, so a revocation takes
	// effect rather than being masked for the lifetime of the process.
	clock = clock.Add(2 * time.Second)
	if _, err := a.Authenticate(ctx, request("t")); err != nil {
		t.Fatal(err)
	}
	if reviews != 2 {
		t.Errorf("after the TTL: TokenReview ran %d times, want 2", reviews)
	}
}

func TestCacheIsBounded(t *testing.T) {
	reviews := 0
	a := NewAuthenticator(authenticatingClient(&reviews), Config{Enabled: true})

	for i := range maxCachedTokens + 50 {
		if _, err := a.Authenticate(context.Background(), request(string(rune(i))+"-token")); err != nil {
			t.Fatal(err)
		}
	}

	a.cache.mu.Lock()
	defer a.cache.mu.Unlock()
	if len(a.cache.entries) > maxCachedTokens {
		t.Errorf("cache grew to %d entries, past the %d cap", len(a.cache.entries), maxCachedTokens)
	}
}
