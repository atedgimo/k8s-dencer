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

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(srcDir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// Files permitted to call fetch() directly.
//
//	api.ts   — owns the client, and attaches the bearer token
//	auth.ts  — fetches /authinfo, which is deliberately unauthenticated
var mayCallFetch = map[string]bool{
	"oidc.ts": true, // the library owns its own transport
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

// A plan must not be swapped out from under a selection.
//
// Step numbers are positional. The planner republishes every resync and again
// after any drain, so ticking "steps 4 through 9" and pausing to think is
// enough for the plan to change underneath — and the selection would survive
// as numbers while quietly coming to mean different nodes. That is the one
// path in this UI that could drain something the operator did not choose.
func TestPlanIsPinnedWhileThereIsSomethingToProtect(t *testing.T) {
	plan := read(t, "usePlan.ts")

	// The subscription must consult the hold before reloading, not after.
	if !regexp.MustCompile(`subscribePlans\([\s\S]{0,220}holdRef\.current`).MatchString(plan) {
		t.Error("usePlan reloads on a publish without checking whether the view is held")
	}
	if !strings.Contains(plan, "setSuperseded(true)") {
		t.Error("a withheld plan is dropped silently; the operator is never told a newer one exists")
	}

	app := read(t, "App.tsx")
	// Held while a selection exists or a run is in flight.
	if !regexp.MustCompile(`usePlan\(\s*checked\.size > 0 \|\| runActive\s*\)`).MatchString(app) {
		t.Error("App does not pin the plan while a selection or run is outstanding")
	}
}

// The Kubernetes credential is the ID token. An access token means nothing to
// the API server, and confusing the two is the classic way to make this
// integration fail with an opaque 401.
func TestOidcUsesTheIdTokenAsTheCredential(t *testing.T) {
	src := read(t, "oidc.ts")
	if !strings.Contains(src, "user?.id_token") {
		t.Error("oidc.ts does not take the ID token as the credential")
	}
	if regexp.MustCompile(`access_token`).MatchString(src) {
		t.Error("oidc.ts references an access token; only the ID token is a Kubernetes credential")
	}
	// The library defaults to localStorage; both stores must be overridden.
	for _, store := range []string{"userStore", "stateStore"} {
		if !regexp.MustCompile(store + `:\s*new WebStorageStateStore\(\{ store: window\.sessionStorage \}\)`).MatchString(src) {
			t.Errorf("%s is not pinned to sessionStorage; the default is localStorage", store)
		}
	}
}

// The plan's confirmation must be re-read, never taken once.
//
// This one shipped and reached a user. The verdict line read "confirmed 72
// minutes ago — the cluster may have moved on" on a cluster that had not moved
// at all: the store touches stored_at on every resync precisely so an
// unchanged plan reads as current, but a plan only re-publishes when its
// *content* changes. So the healthiest possible state — a steady cluster,
// planner confirming every 30s — was the one state guaranteed to trip the
// staleness warning. The signal meant the opposite of what it said.
//
// The fix is a poll, and a poll is one deleted line away from being a fetch
// again, with no compile error and no visible symptom until an hour in.
func TestPlanConfirmationIsPolledRatherThanFetchedOnce(t *testing.T) {
	app := read(t, "App.tsx")

	// The version fetch and a setInterval must live in the same effect. A
	// bare api.version() somewhere plus an unrelated interval elsewhere would
	// satisfy a naive substring check while leaving the value frozen.
	poll := regexp.MustCompile(`api\s*\n?\s*\.version\(\)[\s\S]{0,600}?setInterval\(`)
	if !poll.MatchString(app) {
		t.Error("App fetches the version once instead of polling it; " +
			"planConfirmedAt will freeze at page load and the plan will " +
			"appear to go stale while the planner is still confirming it")
	}

	// Ageing the displayed plan by another plan's confirmation would be worse
	// than not polling: while the view is pinned, the server's latest is a
	// different plan.
	if !strings.Contains(app, "latestPlanId === state.plan.plan.id") {
		t.Error("App does not check that the polled confirmation belongs to " +
			"the plan on screen; a pinned plan would be dated by the clock " +
			"of whatever superseded it")
	}

	// The backend half. Sending generatedAt here would restore the original
	// bug: it is the one timestamp that never moves for an unchanged plan.
	rest, err := os.ReadFile("../../internal/api/rest/rest.go")
	if err != nil {
		t.Fatalf("read rest.go: %v", err)
	}
	if !strings.Contains(string(rest), `resp["planConfirmedAt"] = latest.StoredAt`) {
		t.Error("the version response does not carry latest.StoredAt as " +
			"planConfirmedAt; nothing else refreshes on a steady cluster")
	}
}

// Observed node states must be drawn, not merely parsed.
//
// Twice now a fact about the real cluster made it into the client model and
// stopped there — `cordoned` (found by a user: "one node drained, how I know
// in UI?") and then `ready`, which sat in NodeInfo unrendered from the day it
// was parsed. The payload-parity guard cannot catch this class: it checks that
// a field is *read*, and layout.ts reading a field into a struct nothing draws
// satisfies it. These assertions pin the last hop, source-to-screen.
func TestObservedNodeStatesAreDrawnNotJustParsed(t *testing.T) {
	views := read(t, "components/FieldViews.tsx")

	// The observed vocabulary, complete. Losing a word here silently demotes
	// that state back to invisible.
	for _, word := range []string{`"reclaimed"`, `"NotReady"`, `"awaiting removal"`, `"cordoned"`} {
		if !strings.Contains(views, word) {
			t.Errorf("FieldViews no longer renders the observed state %s", word)
		}
	}

	// Observed beats predicted, structurally: stateWord must consult
	// observedWord before it considers the scrubber's "drained".
	if !regexp.MustCompile(`(?s)function stateWord[^}]*observedWord\(n\)[^}]*n\.drained`).MatchString(views) {
		t.Error("stateWord no longer prefers observed facts over the predicted " +
			"\"drained\"; a real cordon would vanish the moment the scrubber " +
			"passes the node's step")
	}

	// The specific field that was parsed and never drawn. NodeInfo.ready must
	// reach a draw state, not just a struct.
	field := read(t, "components/PackingField.tsx")
	if !strings.Contains(field, "notReady: !n.ready") {
		t.Error("PackingField no longer derives notReady from the model's " +
			"ready field; NotReady nodes are invisible again")
	}

	app := read(t, "App.tsx")

	// A rehearsal must never mark a node as actually drained. Dry runs emit
	// the same event stream on purpose (the UI renders both with one
	// component), so the observed overlay has to filter them or a dry run
	// paints the field with drains that never happened.
	if !regexp.MustCompile(`(?s)const observed = useMemo.{0,900}?!run\.state\.run\.dryRun`).MatchString(app) {
		t.Error("the observed overlay does not exclude dry runs; a rehearsal " +
			"would mark nodes as genuinely drained")
	}

	// The reclamation lists must reach the overlay as names, not just counts.
	// The counts-only version shipped and the first question was "which node?".
	if !strings.Contains(app, `{ reclaim: "awaiting" }`) {
		t.Error("App no longer maps awaiting reclamations onto named nodes; " +
			"the field cannot mark which node is dead weight")
	}
}
