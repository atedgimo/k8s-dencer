// Package notify tells someone a plan exists.
//
// Until now this product required a human to open a browser and look. That
// fits the premise — it produces the plan and stops — but the premise is
// about not *acting* without a human, not about being invisible. Telling
// someone a plan exists is not executing it.
//
// Three deliberate limits, because this is the first thing in the product
// that reaches outward:
//
//   - It sends transitions, never a heartbeat. A message every resync is a
//     message nobody reads, and the fifth one is worse than none.
//   - It sends an id, counts and a link — never the plan. The plan is behind
//     authentication and stays there; a webhook endpoint is a URL anyone who
//     has it can read, and a chat channel is not an authorization boundary.
//   - It cannot fail a planning cycle. Every send is off the cycle's
//     goroutine, bounded by a timeout, and a dead endpoint costs a log line.
//     A consolidation planner that stops planning because a chat server is
//     down would be a worse product than one that never notified at all.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Kind is what happened. Deliberately few: each one has to earn an
// interruption.
type Kind string

const (
	// KindActionable: safe steps exist where a moment ago there were none.
	// The one message worth waking a channel for — it means there is
	// something to approve.
	KindActionable Kind = "plan.actionable"

	// KindSuperseded: a plan that had safe steps has been replaced. Anyone
	// part-way through reviewing the old one is now looking at history, and
	// the executor will refuse it.
	KindSuperseded Kind = "plan.superseded"
)

// Event is everything that leaves the cluster.
//
// No node names, no workload names, no rationale. Someone who should not see
// the cluster's shape learns that a cluster somewhere has a plan with three
// safe steps, and that is all — the link is useless without a token.
type Event struct {
	Kind   Kind      `json:"kind"`
	PlanID string    `json:"planId"`
	At     time.Time `json:"at"`

	// Cluster is the operator's own label for this cluster, so a channel
	// receiving from several can tell them apart. Empty when unset.
	Cluster string `json:"cluster,omitempty"`

	SafeSteps  int `json:"safeSteps"`
	TotalSteps int `json:"totalSteps"`
	// NodesBefore and NodesAfter are the fleet, so "6 to 2" reads without
	// anyone having to open anything.
	NodesBefore int `json:"nodesBefore"`
	NodesAfter  int `json:"nodesAfter"`

	// Link points at the UI, when the operator has said where it is.
	Link string `json:"link,omitempty"`

	// Text is the same thing in a sentence. Chat webhooks that expect a
	// "text" field render it directly; everything else can ignore it.
	Text string `json:"text"`
}

// Sink delivers an event. Split out so the planner can be tested without a
// server, and so a future transport is a new type rather than a new branch.
type Sink interface {
	Send(ctx context.Context, ev Event) error
}

// Webhook posts JSON to an operator-supplied URL.
type Webhook struct {
	URL     string
	Client  *http.Client
	Timeout time.Duration
	// Attempts includes the first try. Two means one retry.
	Attempts int
}

// NewWebhook builds a Sink for url. An empty url yields nil, which every
// caller treats as "notifications are off" — the same shape as the pricing
// table, and for the same reason: unconfigured must never mean guessed.
func NewWebhook(url string) *Webhook {
	if strings.TrimSpace(url) == "" {
		return nil
	}
	return &Webhook{
		URL:      url,
		Client:   &http.Client{},
		Timeout:  5 * time.Second,
		Attempts: 2,
	}
}

func (w *Webhook) Send(ctx context.Context, ev Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}

	attempts := w.Attempts
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			// One short pause. This is not a delivery guarantee and should
			// not pretend to be one: the next transition will say the same
			// thing, and a queue that survives a planner restart is a
			// different product.
			select {
			case <-time.After(time.Duration(i) * time.Second):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		last = w.post(ctx, body)
		if last == nil {
			return nil
		}
	}
	return last
}

func (w *Webhook) post(ctx context.Context, body []byte) error {
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "k8s-dencer")

	client := w.Client
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}

// Notifier decides what is worth sending, and sends it without blocking.
//
// It holds one piece of state: whether the last plan it saw had anything safe
// to do. That is what makes "a Green step appeared" a transition rather than
// a repeated fact — and it is per-process, so a planner restart re-announces
// an actionable plan once. That is the right failure: a duplicate message is
// recoverable, a missed one is the thing this exists to prevent.
type Notifier struct {
	Sink    Sink
	Log     *slog.Logger
	Cluster string
	// BaseURL is where the UI is reachable, if the operator has said. Empty
	// means the message carries no link rather than a guessed one.
	BaseURL string

	mu       sync.Mutex
	wasSafe  bool
	lastPlan string
	inflight sync.WaitGroup
}

// PlanStored reports a newly stored plan. Safe safe steps of total.
//
// Called from the planning cycle, so it must return immediately: the send
// happens on its own goroutine with its own context, deliberately not the
// cycle's — a cycle that finishes and cancels its context mid-post would
// cancel the notification it just asked for.
func (n *Notifier) PlanStored(planID string, safe, total, nodesBefore, nodesAfter int) {
	if n == nil || n.Sink == nil {
		return
	}

	n.mu.Lock()
	wasSafe, prev := n.wasSafe, n.lastPlan
	n.wasSafe, n.lastPlan = safe > 0, planID
	n.mu.Unlock()

	var ev Event
	switch {
	case safe > 0 && !wasSafe:
		ev = Event{Kind: KindActionable}
	case wasSafe && prev != "" && prev != planID:
		// The plan somebody may be reviewing is no longer the current one.
		ev = Event{Kind: KindSuperseded}
	default:
		// Still actionable, still not actionable, or the same plan again.
		// Nothing changed that anyone needs to be interrupted for.
		return
	}

	ev.PlanID = planID
	ev.At = time.Now().UTC()
	ev.Cluster = n.Cluster
	ev.SafeSteps = safe
	ev.TotalSteps = total
	ev.NodesBefore = nodesBefore
	ev.NodesAfter = nodesAfter
	ev.Link = n.BaseURL
	ev.Text = sentence(ev)

	n.inflight.Add(1)
	go func() {
		defer n.inflight.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := n.Sink.Send(ctx, ev); err != nil && n.Log != nil {
			// A warning, never an error: nothing about the cluster is wrong.
			n.Log.Warn("notification not delivered", "kind", ev.Kind, "error", err)
		}
	}()
}

// Wait blocks until in-flight sends finish. For tests and shutdown; the
// planning cycle never calls it.
func (n *Notifier) Wait() {
	if n == nil {
		return
	}
	n.inflight.Wait()
}

func sentence(ev Event) string {
	where := ""
	if ev.Cluster != "" {
		where = " on " + ev.Cluster
	}
	switch ev.Kind {
	case KindActionable:
		s := fmt.Sprintf("k8s-dencer%s: %d step", where, ev.SafeSteps)
		if ev.SafeSteps != 1 {
			s += "s"
		}
		s += " can be run safely"
		if ev.NodesBefore > 0 && ev.NodesAfter > 0 {
			s += fmt.Sprintf(" — %d nodes now, %d after", ev.NodesBefore, ev.NodesAfter)
		}
		return s + ". Nothing has been executed."
	case KindSuperseded:
		return fmt.Sprintf("k8s-dencer%s: the plan you were reviewing has been replaced by %s.",
			where, ev.PlanID)
	}
	return ""
}
