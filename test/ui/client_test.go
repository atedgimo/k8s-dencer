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
	// Held while a TOUCHED selection exists or a run is in flight. The
	// redesign checks safe steps by default, so a pristine selection is not
	// the operator's — pinning on it would freeze every plan forever and the
	// freshness dot would lie by construction. Only intent pins.
	if !regexp.MustCompile(`usePlan\(\(touched\.current && checked\.size > 0\) \|\| runActive\)`).MatchString(app) {
		t.Error("App does not pin the plan while a touched selection or run is outstanding")
	}
	if !strings.Contains(app, "touched.current = true") {
		t.Error("toggling a step no longer marks the selection as the operator's; " +
			"either every plan pins (stale forever) or none does (selections silently remap)")
	}
	// Safe steps are checked by default — the primary button must mean
	// something before anyone clicks a box.
	if !regexp.MustCompile(`setChecked\(new Set\(greenSteps\.map`).MatchString(app) {
		t.Error("the default selection (safe steps checked) is gone")
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
	// The version fetch and a setInterval must live in the same effect. A
	// bare api.version() somewhere plus an unrelated interval elsewhere would
	// satisfy a naive substring check while leaving the value frozen.
	ver := read(t, "useVersion.ts")
	poll := regexp.MustCompile(`api\s*\n?\s*\.version\(\)[\s\S]{0,600}?setInterval\(`)
	if !poll.MatchString(ver) {
		t.Error("useVersion fetches the version once instead of polling it; " +
			"planConfirmedAt will freeze at page load and the plan will " +
			"appear to go stale while the planner is still confirming it")
	}

	// And App must actually use the polling hook — extracting it and then
	// fetching directly in App would satisfy the check above while the page
	// froze anyway.
	app := read(t, "App.tsx")
	if !strings.Contains(app, "useVersion()") {
		t.Error("App does not use the useVersion poll; the confirmation freezes at load")
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
// in UI?") and then `ready`, which sat unrendered from the day it was parsed.
// The payload-parity guard cannot catch this class: it checks that a field is
// *read*, and a hook reading a field into a struct nothing draws satisfies
// it. These assertions pin the last hop, source-to-screen — now onto the
// Cluster lenses, which replaced the packing field.
func TestObservedNodeStatesAreDrawnNotJustParsed(t *testing.T) {
	page := read(t, "components/cluster/ClusterPage.tsx")

	// The observed vocabulary, complete. Losing a word here silently demotes
	// that state back to invisible. "reclaimed" is drawn by omission — the
	// node leaves the grid — which is still a rendering decision this file
	// must make explicitly.
	for _, word := range []string{`"reclaimed"`, "NotReady", "awaiting removal", "cordoned"} {
		if !strings.Contains(page, word) {
			t.Errorf("ClusterPage no longer renders the observed state %q", word)
		}
	}

	obs := read(t, "useObserved.ts")

	// A rehearsal must never mark a node as actually drained. Dry runs emit
	// the same event stream on purpose (the UI renders both with one
	// component), so the observed overlay has to filter them or a dry run
	// paints the lenses with drains that never happened.
	if !regexp.MustCompile(`(?s)return useMemo.{0,900}?!runState\.run\.dryRun`).MatchString(obs) {
		t.Error("the observed overlay does not exclude dry runs; a rehearsal " +
			"would mark nodes as genuinely drained")
	}

	// The reclamation lists must reach the overlay as names, not just counts.
	// The counts-only version shipped and the first question was "which node?".
	if !strings.Contains(obs, `{ reclaim: "awaiting" }`) {
		t.Error("useObserved no longer maps awaiting reclamations onto named " +
			"nodes; the lenses cannot mark which node is dead weight")
	}

	// And the overlay must actually be mounted: derived in a hook nothing
	// calls is the parsed-but-never-drawn bug with an extra step.
	app := read(t, "App.tsx")
	if !strings.Contains(app, "useObserved(") || !strings.Contains(app, "observed={observed.nodes}") {
		t.Error("App does not wire the observed overlay into the Cluster lenses")
	}

	// The evicted-pod half of the overlay: a run's Evict events must reach
	// the Rack lens as ghosts, or in-flight pods go back to being drawn at
	// destinations the scheduler never confirmed.
	if !strings.Contains(app, "evictedPods={observed.evictedPods}") {
		t.Error("App does not pass the evicted-pod set to the lenses")
	}
	if !strings.Contains(page, "is-evicted") {
		t.Error("the Rack lens no longer ghosts evicted pods; their drawn " +
			"position is a lie until the next snapshot")
	}
}

// Every app-level poll must re-read when a token arrives.
//
// The app mounts before anyone has signed in, so a hook's first fetch is
// unauthenticated. It 401s, the catch swallows it — correctly, because a
// transient failure must not blank the screen — and the hook then waits out
// its interval before trying again.
//
// The cost was measured on a real install: for a full minute after sign-in,
// Recommendations said "No findings against the current cluster" while the
// cluster had 34. Not a spinner. A sentence, and a false one. The rail badge
// was empty for the same minute, and the ledger overlays with it.
//
// usePlan had always subscribed to onTokenChange, which is why the plan
// appeared immediately and hid the problem — one hook getting it right made
// the other three look like they had.
func TestPollingHooksReReadWhenTheTokenArrives(t *testing.T) {
	// The hooks App mounts before authentication exists. A hook that only
	// runs when its screen is opened is not in this list: by then the token
	// is already held.
	for _, name := range []string{
		"useRecommendations.ts",
		"useReclamations.ts",
		"useVersion.ts",
	} {
		src := read(t, name)
		if !strings.Contains(src, "onTokenChange") {
			t.Errorf("%s polls but does not re-read on sign-in; its screen will show "+
				"a confident zero until the interval fires", name)
		}
		// Subscribing without unsubscribing leaks a listener per mount, and
		// the listener holds a stale setState.
		if strings.Contains(src, "onTokenChange") && !strings.Contains(src, "stopAuth()") {
			t.Errorf("%s subscribes to onTokenChange but never unsubscribes", name)
		}
	}
}
