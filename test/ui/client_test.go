// Package ui_test guards invariants of the frontend that only bite at runtime.
//
// These are the failures a typecheck cannot see and a human reviewer skims
// past: a request that forgot its credentials still compiles, still looks
// correct, and 401s only once authentication is switched on — by which point
// the page half-works and the cause is three milestones old.
package ui_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const srcDir = "../../ui/src"

// Files permitted to call fetch() directly.
//
//	api.ts   — owns the client, and attaches the bearer token
//	auth.ts  — fetches /authinfo, which is deliberately unauthenticated
var mayCallFetch = map[string]bool{
	"api.ts":  true,
	"auth.ts": true,
}

// Every request must go through the shared client, so every request carries
// the token.
//
// This exists because it already happened: useConstraints.ts kept a bare
// fetch() when M9 added authentication, so the plan loaded and only the pod
// inspector said "Unauthorized". A whole page failing is obvious; one panel
// failing looks like a backend bug and cost real time to find.
func TestEveryRequestGoesThroughTheAuthenticatedClient(t *testing.T) {
	// Matches fetch( but not authHeaders-bearing helpers or the word in prose.
	call := regexp.MustCompile(`(^|[^.\w])fetch\s*\(`)

	err := filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".ts" && ext != ".tsx" {
			return nil
		}
		if mayCallFetch[filepath.Base(path)] {
			return nil
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			// Skip comments; several files mention fetch in prose.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			if call.MatchString(line) {
				rel, _ := filepath.Rel(srcDir, path)
				t.Errorf("%s:%d calls fetch() directly, so the request carries no "+
					"bearer token and will 401 wherever authentication is on.\n"+
					"    Add a method to the api object in api.ts instead.\n    %s",
					rel, i+1, trimmed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The token must never leave sessionStorage for localStorage. localStorage
// outlives the browsing session and is readable by every script on the origin
// for as long as it sits there — not the right home for a credential that can
// drain nodes.
func TestTokenIsNotPersistedBeyondTheSession(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(srcDir, "auth.ts"))
	if err != nil {
		t.Fatal(err)
	}
	// Property access, not the bare word: auth.ts names localStorage in a
	// comment explaining why it does not use it, and a test that fails on its
	// own rationale would only teach people to delete the rationale.
	if regexp.MustCompile(`localStorage\s*\.`).Match(body) {
		t.Error("auth.ts touches localStorage; the bearer token is scoped to the tab on purpose")
	}
	if !regexp.MustCompile(`sessionStorage\s*\.`).Match(body) {
		t.Error("auth.ts no longer uses sessionStorage; where is the token being kept?")
	}
}
