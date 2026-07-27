// Package window decides whether a maintenance window is open.
//
// This is the object that finally makes Red steps executable. Architecture doc
// §9 confines them to "an approved maintenance window", and with no window
// defined the safe reading of that is "never" — which is what Phase 2 shipped.
//
// Every failure mode here resolves to CLOSED. An unparseable schedule, an
// unknown timezone, a suspended window, a clock that has moved: none of them
// can open a window, and each says why. That asymmetry is deliberate — the cost
// of a window wrongly shut is an operator waiting; the cost of one wrongly open
// is an unattended drain of something that should not have been touched.
package window

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/atedgimo/k8s-dencer/api/v1alpha1"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

// parser accepts standard five-field cron, and nothing else.
//
// Descriptors like @every are excluded on purpose: "@every 1h" as a maintenance
// window means "always", which is not a window and is far too easy to type by
// accident.
var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// State is the evaluated condition of one window.
type State struct {
	Name string
	// Open reports whether the window is currently admitting work.
	Open bool
	// AllowsRed is true only when the window is open and permits Red steps.
	AllowsRed bool
	// ClosesAt is when the current opening ends.
	ClosesAt time.Time
	// NextOpen is when a closed window opens next; zero if it never will.
	NextOpen time.Time
	// Reason explains the state in words an operator can act on.
	Reason string
	// Selector limits the window to matching nodes; nil means all.
	Selector map[string]string
	// MaxNodes caps drains across the opening; zero means no window-level cap.
	MaxNodes int32
}

// Evaluate reports the state of a single window at instant now.
func Evaluate(mw v1alpha1.MaintenanceWindow, now time.Time) State {
	s := State{
		Name:     mw.Name,
		Selector: mw.Spec.NodeSelector,
		MaxNodes: mw.Spec.MaxNodes,
	}

	if mw.Spec.Suspend {
		s.Reason = "suspended"
		return s
	}

	// Checked before LoadLocation, which returns UTC for "" without an error.
	// Relying on it would make an omitted timezone silently mean UTC — the
	// exact fail-open this field exists to prevent. The CRD's MinLength=1 stops
	// most of these at the API server; this stops the rest.
	if mw.Spec.TimeZone == "" {
		s.Reason = "timeZone \"\" is not a known IANA zone, so the window cannot open"
		return s
	}

	loc, err := time.LoadLocation(mw.Spec.TimeZone)
	if err != nil {
		// A window whose timezone cannot be resolved is not "probably UTC".
		// Guessing would open it at the wrong hour, which is the failure this
		// field exists to prevent.
		s.Reason = fmt.Sprintf("timeZone %q is not a known IANA zone, so the window cannot open",
			mw.Spec.TimeZone)
		return s
	}

	schedule, err := parser.Parse(mw.Spec.Schedule)
	if err != nil {
		s.Reason = fmt.Sprintf("schedule %q is not a valid cron expression (%v), so the window cannot open",
			mw.Spec.Schedule, err)
		return s
	}

	duration, err := time.ParseDuration(orDefault(mw.Spec.Duration, "1h"))
	if err != nil || duration <= 0 {
		s.Reason = fmt.Sprintf("duration %q is not a positive interval, so the window cannot open",
			mw.Spec.Duration)
		return s
	}

	local := now.In(loc)

	// Cron gives the next firing, never the last, so the most recent opening is
	// found by winding back one duration and asking again. Anything that fired
	// earlier than that has already closed by definition.
	prev := schedule.Next(local.Add(-duration))
	if !prev.After(local) {
		closes := prev.Add(duration)
		if local.Before(closes) {
			s.Open = true
			s.AllowsRed = mw.Spec.AllowRed
			s.ClosesAt = closes
			s.Reason = fmt.Sprintf("open until %s", closes.Format(time.RFC3339))
			return s
		}
	}

	next := schedule.Next(local)
	s.NextOpen = next
	s.Reason = fmt.Sprintf("closed; next opens %s", next.Format(time.RFC3339))
	return s
}

// Set is the evaluated state of every window in the cluster.
type Set struct {
	states []State
	now    time.Time
}

// EvaluateAll evaluates a list of windows.
func EvaluateAll(windows []v1alpha1.MaintenanceWindow, now time.Time) *Set {
	out := &Set{now: now, states: make([]State, 0, len(windows))}
	for _, mw := range windows {
		out.states = append(out.states, Evaluate(mw, now))
	}
	return out
}

// States exposes every evaluated window, for status reporting and the UI.
func (s *Set) States() []State { return s.states }

// Any reports whether at least one window is open, ignoring node selectors.
func (s *Set) Any() bool {
	for _, st := range s.states {
		if st.Open {
			return true
		}
	}
	return false
}

// Covering returns the open windows that apply to a node.
//
// A window with no selector covers every node. With a selector it covers only
// nodes carrying those labels, so a permissive window scoped to a batch pool
// cannot accidentally authorise work on the rest of the cluster.
func (s *Set) Covering(node model.Node) []State {
	var out []State
	for _, st := range s.states {
		if !st.Open {
			continue
		}
		if matches(st.Selector, node.Labels) {
			out = append(out, st)
		}
	}
	return out
}

// AllowsRedOn reports whether some open window permits a Red step on this node,
// and explains the answer either way.
//
// Explaining the refusal matters as much as making it: "no window is open" and
// "a window is open but does not permit Red" call for completely different
// responses, and an operator staring at a blocked step needs to know which.
func (s *Set) AllowsRedOn(node model.Node) (bool, string) {
	covering := s.Covering(node)
	if len(covering) == 0 {
		if len(s.states) == 0 {
			return false, "no maintenance window is defined in this cluster"
		}
		if !s.Any() {
			return false, fmt.Sprintf("no maintenance window is open (%s)", s.summary())
		}
		return false, fmt.Sprintf("no open maintenance window covers node %s", node.Name)
	}

	for _, st := range covering {
		if st.AllowsRed {
			return true, fmt.Sprintf("maintenance window %s is open until %s and permits Red steps",
				st.Name, st.ClosesAt.Format(time.RFC3339))
		}
	}
	return false, fmt.Sprintf(
		"maintenance window %s is open but does not permit Red steps; set spec.allowRed to change that",
		covering[0].Name)
}

func (s *Set) summary() string {
	if len(s.states) == 1 {
		return s.states[0].Reason
	}
	return fmt.Sprintf("%d windows defined, none currently open", len(s.states))
}

func matches(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
