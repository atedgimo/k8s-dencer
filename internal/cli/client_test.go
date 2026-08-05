package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd/api"
)

func TestParseSteps(t *testing.T) {
	good := map[string][]int{
		"1":       {1},
		"1,3,5":   {1, 3, 5},
		"1-4":     {1, 2, 3, 4},
		"3,1-2,5": {1, 2, 3, 5},
		"2,2,2":   {2},
		" 1 , 3 ": {1, 3},
		"5-5":     {5},
	}
	for in, want := range good {
		got, err := ParseSteps(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%q = %v, want %v", in, got, want)
		}
	}

	// A range that counts backwards is a typo, not an empty selection. Silently
	// running nothing would look like success.
	for _, bad := range []string{"", "x", "4-2", "1-", "-3", ",,", "1,x"} {
		if got, err := ParseSteps(bad); err == nil {
			t.Errorf("%q should be rejected, got %v", bad, got)
		}
	}
}

// The plan and run endpoints wrap their payloads. Reading "steps" or "status"
// off the top level yields the zero value, which is indistinguishable from "no
// plan yet" and "still running" — a mistake that cost a debugging session in
// hack/e2e.sh before this client existed.
func TestClientUnwrapsTheEnvelopes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/plans/latest"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"plan": map[string]any{
					"id":    "abc123",
					"steps": []map[string]any{{"sequenceNumber": 1, "impact": "Green", "targetNode": "n1"}},
				},
				"strategy": "greedy",
				"ratings":  map[string]int{"Green": 1},
			})
		case strings.Contains(r.URL.Path, "/runs/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"run":    map[string]any{"id": "r1", "status": "Succeeded", "summary": "done"},
				"events": []map[string]any{{"action": "Cordon", "message": "cordoned n1"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{base: srv.URL, http: srv.Client(), closer: func() {}}
	ctx := context.Background()

	plan, err := c.Plan(ctx, "latest")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Plan.ID != "abc123" {
		t.Errorf("plan id = %q, want abc123", plan.Plan.ID)
	}
	if len(plan.Plan.Steps) != 1 {
		t.Errorf("steps = %d, want 1 — the envelope was not unwrapped", len(plan.Plan.Steps))
	}

	run, err := c.Run(ctx, "r1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Run.Status != store.RunSucceeded {
		t.Errorf("status = %q, want Succeeded — the envelope was not unwrapped", run.Run.Status)
	}
	if len(run.Events) != 1 {
		t.Errorf("events = %d, want 1", len(run.Events))
	}
}

// An error the user cannot act on is barely better than a stack trace. Each of
// these has a specific fix, and the message has to name it.
func TestErrorsNameTheFix(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   []string
	}{
		{http.StatusUnauthorized, `{"error":"authentication required"}`,
			[]string{"kubectl create token", "DENCER_TOKEN"}},
		{http.StatusForbidden, `{"error":"forbidden"}`,
			[]string{"create consolidations.dencer.io", "get plans.dencer.io"}},
		{http.StatusNotFound, `{"error":"pod x/y is not in plan p"}`,
			[]string{"pod x/y is not in plan p"}},
	}
	for _, tc := range cases {
		err := apiError(tc.status, []byte(tc.body))
		if err == nil {
			t.Fatalf("status %d produced no error", tc.status)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("status %d message does not mention %q:\n%s", tc.status, want, err)
			}
		}
	}
}

// Wait must not report a run finished while it is still going, and must not
// spin forever on a terminal one.
func TestWaitStopsOnTerminalStatusOnly(t *testing.T) {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		polls++
		status := "Running"
		events := []map[string]any{{"action": "Cordon", "message": "cordoned"}}
		if polls >= 2 {
			status = "Blocked"
			events = append(events, map[string]any{"action": "Guard", "rule": "PDBHeadroom", "message": "refused"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run":    map[string]any{"id": "r1", "status": status},
			"events": events,
		})
	}))
	defer srv.Close()

	c := &Client{base: srv.URL, http: srv.Client(), closer: func() {}}
	var seen []string
	final, err := c.Wait(context.Background(), "r1", func(ev store.RunEvent) {
		seen = append(seen, ev.Action)
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if final.Status != store.RunBlocked {
		t.Errorf("final status = %q, want Blocked", final.Status)
	}
	// Each event exactly once, however many times it was polled.
	if !reflect.DeepEqual(seen, []string{"Cordon", "Guard"}) {
		t.Errorf("events delivered = %v, want [Cordon Guard] once each", seen)
	}
}

// Colour is a presentation choice; the glyph and the word are the information.
// Stripping colour must never remove either, or piping the output would drop
// the risk rating entirely.
func TestRatingSurvivesWithoutColour(t *testing.T) {
	saved := colour
	defer func() { colour = saved; resetPainters() }()

	colour = false
	resetPainters()
	for _, want := range []string{"Green", "Yellow", "Red"} {
		got := impactMark(ratingFor(want))
		if !strings.Contains(got, want) {
			t.Errorf("uncoloured mark %q does not contain the word %q", got, want)
		}
		if strings.Contains(got, "\033[") {
			t.Errorf("uncoloured mark still carries escape codes: %q", got)
		}
		if !strings.ContainsAny(got, "●▲■") {
			t.Errorf("uncoloured mark %q has no glyph; colour would be the only carrier", got)
		}
	}
}

func ratingFor(s string) model.ImpactRating { return model.ImpactRating(s) }

// tokenViaTransport must capture a bearer token from client-go's exec-plugin
// transport without sending a request to the API server — the stub RoundTripper
// in client.go is the whole trick.
func TestTokenViaTransportCapturesExecPluginToken(t *testing.T) {
	cfg := &rest.Config{
		Host: "https://cluster.example",
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true,
		},
		ExecProvider: &api.ExecConfig{
			APIVersion:      "client.authentication.k8s.io/v1",
			Command:         "sh",
			Args:            []string{"-c", `printf '{"apiVersion":"client.authentication.k8s.io/v1","kind":"ExecCredential","status":{"token":"plugin-token-abc"}}'`},
			InteractiveMode: api.NeverExecInteractiveMode,
		},
	}

	got, err := tokenViaTransport(cfg)
	if err != nil {
		t.Fatalf("tokenViaTransport: %v", err)
	}
	if got != "plugin-token-abc" {
		t.Errorf("token = %q, want plugin-token-abc", got)
	}
}
