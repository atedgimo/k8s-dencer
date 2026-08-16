package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type capture struct {
	events []Event
	err    error
	calls  atomic.Int32
}

func (c *capture) Send(_ context.Context, ev Event) error {
	c.calls.Add(1)
	c.events = append(c.events, ev)
	return c.err
}

func notifier(sink Sink) *Notifier {
	return &Notifier{Sink: sink, Cluster: "prod-eu"}
}

// The point of the whole package: a message when there is something to do,
// and silence otherwise.
//
// A notifier that fired every resync would send a message every thirty
// seconds saying the same thing, and the fifth one is worse than none —
// people build filters for it, and then the one that mattered is filtered
// too.
func TestOnlyTransitionsAreAnnounced(t *testing.T) {
	c := &capture{}
	n := notifier(c)

	// Nothing safe: no news.
	n.PlanStored("p1", 0, 4, 6, 6)
	n.Wait()
	if c.calls.Load() != 0 {
		t.Fatalf("a plan with nothing safe sent %d message(s)", c.calls.Load())
	}

	// Safe steps appear. This is the message.
	n.PlanStored("p2", 3, 8, 6, 3)
	n.Wait()
	if c.calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 when safe steps appear", c.calls.Load())
	}
	if c.events[0].Kind != KindActionable {
		t.Errorf("kind = %s, want %s", c.events[0].Kind, KindActionable)
	}

	// Still the same plan, still actionable. Nothing changed.
	n.PlanStored("p2", 3, 8, 6, 3)
	n.Wait()
	if c.calls.Load() != 1 {
		t.Errorf("re-storing the same actionable plan sent another message (%d total)",
			c.calls.Load())
	}
}

// A plan somebody is reviewing gets replaced, and they should be told: the
// executor will refuse the one on their screen.
func TestReplacingAnActionablePlanIsAnnounced(t *testing.T) {
	c := &capture{}
	n := notifier(c)

	n.PlanStored("p1", 2, 5, 6, 4)
	n.Wait()
	n.PlanStored("p2", 2, 5, 6, 4)
	n.Wait()

	if c.calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", c.calls.Load())
	}
	if c.events[1].Kind != KindSuperseded {
		t.Errorf("second kind = %s, want %s", c.events[1].Kind, KindSuperseded)
	}
	if !strings.Contains(c.events[1].Text, "p2") {
		t.Errorf("the superseded message does not name the new plan: %q", c.events[1].Text)
	}
}

// Nothing about the cluster's shape may leave it.
//
// A webhook URL is a bearer credential in a query string, and a chat channel
// is not an authorization boundary. The plan is behind authentication and
// stays there; what goes out is a count and a link that is useless without a
// token.
func TestTheEventCarriesNoClusterDetail(t *testing.T) {
	c := &capture{}
	n := notifier(c)
	n.BaseURL = "https://dencer.example.com"
	n.PlanStored("abc123", 3, 8, 6, 3)
	n.Wait()

	body, err := json.Marshal(c.events[0])
	if err != nil {
		t.Fatal(err)
	}
	// The fields that exist are the fields that were designed. A node name or
	// a workload name appearing here would mean somebody added a field
	// without thinking about who reads the channel.
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"kind": true, "planId": true, "at": true, "cluster": true,
		"safeSteps": true, "totalSteps": true, "nodesBefore": true,
		"nodesAfter": true, "link": true, "text": true,
	}
	for k := range got {
		if !allowed[k] {
			t.Errorf("event carries %q, which nobody decided to publish outside the cluster", k)
		}
	}
}

// A dead endpoint must cost a log line, not a planning cycle.
func TestASlowEndpointDoesNotBlockTheCaller(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(release)

	n := &Notifier{Sink: NewWebhook(srv.URL)}

	done := make(chan struct{})
	go func() {
		n.PlanStored("p1", 2, 4, 6, 4)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("PlanStored blocked on a slow endpoint; a planning cycle would have stalled")
	}
}

// An unset URL is off, not a request to nowhere.
func TestNoURLMeansNoSink(t *testing.T) {
	if w := NewWebhook(""); w != nil {
		t.Error("an empty URL produced a webhook")
	}
	if w := NewWebhook("   "); w != nil {
		t.Error("a blank URL produced a webhook")
	}
	// A nil Notifier and a nil Sink both have to be safe: the planner builds
	// one unconditionally and leaves it empty when unconfigured.
	var n *Notifier
	n.PlanStored("p1", 1, 1, 2, 1) // must not panic
	(&Notifier{}).PlanStored("p1", 1, 1, 2, 1)
}

func TestWebhookPostsTheEventAndRetriesOnce(t *testing.T) {
	var hits atomic.Int32
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body.Store(string(b))
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := NewWebhook(srv.URL)
	wh.Attempts = 2
	if err := wh.Send(context.Background(), Event{Kind: KindActionable, PlanID: "p1", SafeSteps: 2}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if hits.Load() != 2 {
		t.Errorf("hits = %d, want 2 — the first failed and should have been retried", hits.Load())
	}
	if s, _ := body.Load().(string); !strings.Contains(s, `"kind":"plan.actionable"`) {
		t.Errorf("posted body does not carry the event: %q", s)
	}
}

func TestAFailingWebhookReportsRatherThanPanics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	wh := NewWebhook(srv.URL)
	wh.Attempts = 1
	err := wh.Send(context.Background(), Event{Kind: KindActionable})
	if err == nil {
		t.Fatal("a 403 reported success")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error does not name the status: %v", err)
	}
}

// The sentence is what most people will actually read, so it has to say the
// one thing this product is careful about.
func TestTheSentenceSaysNothingWasExecuted(t *testing.T) {
	got := sentence(Event{Kind: KindActionable, SafeSteps: 3, NodesBefore: 6, NodesAfter: 3, Cluster: "prod-eu"})
	for _, want := range []string{"prod-eu", "3 steps", "6 nodes now, 3 after", "Nothing has been executed"} {
		if !strings.Contains(got, want) {
			t.Errorf("sentence %q is missing %q", got, want)
		}
	}
	if one := sentence(Event{Kind: KindActionable, SafeSteps: 1}); !strings.Contains(one, "1 step can") {
		t.Errorf("singular reads wrong: %q", one)
	}
}
