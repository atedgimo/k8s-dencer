package window_test

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/atedgimo/k8s-dencer/api/v1alpha1"
	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/window"
)

func mw(name string, mutate ...func(*v1alpha1.MaintenanceWindow)) v1alpha1.MaintenanceWindow {
	w := v1alpha1.MaintenanceWindow{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.MaintenanceWindowSpec{
			// Sundays at 02:00 London, for four hours.
			Schedule: "0 2 * * 0",
			Duration: "4h",
			TimeZone: "Europe/London",
			AllowRed: true,
		},
	}
	for _, m := range mutate {
		m(&w)
	}
	return w
}

// at builds an instant in the window's own zone, so the tests read the way an
// operator thinks about them.
func at(t *testing.T, s string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	ts, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func node(name string, labels map[string]string) model.Node {
	return model.Node{Name: name, Labels: labels, Ready: true}
}

// 2026-08-02 is a Sunday.
func TestWindowIsOpenOnlyInsideItsInterval(t *testing.T) {
	for _, tc := range []struct {
		when string
		open bool
	}{
		{"2026-08-02 01:59", false}, // a minute early
		{"2026-08-02 02:00", true},  // the moment it opens
		{"2026-08-02 05:59", true},  // a minute before it closes
		{"2026-08-02 06:00", false}, // exactly four hours later, closed
		{"2026-08-02 06:01", false},
		{"2026-08-03 03:00", false}, // Monday
	} {
		t.Run(tc.when, func(t *testing.T) {
			s := window.Evaluate(mw("weekly"), at(t, tc.when))
			if s.Open != tc.open {
				t.Errorf("open = %v, want %v (%s)", s.Open, tc.open, s.Reason)
			}
		})
	}
}

// Every malformed window is closed, and says why. Guessing at intent here would
// mean opening a window at an hour nobody chose.
func TestMalformedWindowsFailClosedAndExplain(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*v1alpha1.MaintenanceWindow)
		says   string
	}{
		{"unknown timezone", func(w *v1alpha1.MaintenanceWindow) {
			w.Spec.TimeZone = "Mars/Olympus"
		}, "not a known IANA zone"},
		{"empty timezone", func(w *v1alpha1.MaintenanceWindow) {
			w.Spec.TimeZone = ""
		}, "not a known IANA zone"},
		{"nonsense schedule", func(w *v1alpha1.MaintenanceWindow) {
			w.Spec.Schedule = "every other tuesday"
		}, "not a valid cron expression"},
		{"zero duration", func(w *v1alpha1.MaintenanceWindow) {
			w.Spec.Duration = "0h"
		}, "not a positive interval"},
		{"suspended", func(w *v1alpha1.MaintenanceWindow) {
			w.Spec.Suspend = true
		}, "suspended"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Evaluated at a moment the window would otherwise be wide open.
			s := window.Evaluate(mw("w", tc.mutate), at(t, "2026-08-02 03:00"))
			if s.Open {
				t.Fatalf("a %s window opened; every failure must resolve to closed", tc.name)
			}
			if !strings.Contains(s.Reason, tc.says) {
				t.Errorf("reason %q does not mention %q", s.Reason, tc.says)
			}
		})
	}
}

// "@every 1h" as a maintenance window means "always", which is not a window.
func TestCronDescriptorsAreRejected(t *testing.T) {
	for _, sched := range []string{"@every 1h", "@daily", "@hourly", "@reboot"} {
		s := window.Evaluate(
			mw("w", func(w *v1alpha1.MaintenanceWindow) { w.Spec.Schedule = sched }),
			at(t, "2026-08-02 03:00"))
		if s.Open {
			t.Errorf("descriptor %q opened a window", sched)
		}
	}
}

// The timezone is load-bearing: the same instant is inside the window in London
// and outside it in Tokyo.
func TestTheZoneDecidesWhetherItIsOpen(t *testing.T) {
	// 02:30 London on Sunday.
	instant := at(t, "2026-08-02 02:30")

	london := window.Evaluate(mw("london"), instant)
	if !london.Open {
		t.Errorf("London window should be open: %s", london.Reason)
	}

	tokyo := window.Evaluate(
		mw("tokyo", func(w *v1alpha1.MaintenanceWindow) { w.Spec.TimeZone = "Asia/Tokyo" }),
		instant)
	if tokyo.Open {
		t.Error("the same instant is 10:30 in Tokyo and must not be inside a 02:00 window")
	}
}

// Creating a window must not by itself unlock the most dangerous class of step.
func TestOpenWindowDoesNotPermitRedUnlessAsked(t *testing.T) {
	closed := window.EvaluateAll(
		[]v1alpha1.MaintenanceWindow{mw("w", func(w *v1alpha1.MaintenanceWindow) { w.Spec.AllowRed = false })},
		at(t, "2026-08-02 03:00"))

	ok, why := closed.AllowsRedOn(node("n1", nil))
	if ok {
		t.Error("a window with allowRed=false permitted a Red step")
	}
	// The distinction matters: "nothing is open" and "open but not for Red"
	// call for completely different responses.
	if !strings.Contains(why, "does not permit Red") {
		t.Errorf("reason should distinguish this from no window at all: %q", why)
	}

	open := window.EvaluateAll([]v1alpha1.MaintenanceWindow{mw("w")}, at(t, "2026-08-02 03:00"))
	if ok, why := open.AllowsRedOn(node("n1", nil)); !ok {
		t.Errorf("allowRed=true should permit: %s", why)
	}
}

func TestNoWindowsAtAllIsExplainedDistinctly(t *testing.T) {
	empty := window.EvaluateAll(nil, at(t, "2026-08-02 03:00"))
	ok, why := empty.AllowsRedOn(node("n1", nil))
	if ok {
		t.Fatal("an empty cluster permitted a Red step")
	}
	if !strings.Contains(why, "no maintenance window is defined") {
		t.Errorf("reason %q should say none is defined, not that none is open", why)
	}
}

// A permissive window scoped to one pool must not authorise the rest of the
// cluster.
func TestSelectorLimitsWhichNodesAWindowCovers(t *testing.T) {
	scoped := window.EvaluateAll([]v1alpha1.MaintenanceWindow{
		mw("batch", func(w *v1alpha1.MaintenanceWindow) {
			w.Spec.NodeSelector = map[string]string{"pool": "batch"}
		}),
	}, at(t, "2026-08-02 03:00"))

	if ok, _ := scoped.AllowsRedOn(node("b1", map[string]string{"pool": "batch"})); !ok {
		t.Error("a node in the selected pool should be covered")
	}
	ok, why := scoped.AllowsRedOn(node("w1", map[string]string{"pool": "web"}))
	if ok {
		t.Error("a node outside the pool was covered by a scoped window")
	}
	if !strings.Contains(why, "covers node w1") {
		t.Errorf("reason should name the uncovered node: %q", why)
	}
}

// With several windows, the permissive one wins for the nodes it covers and
// only those.
func TestMostPermissiveCoveringWindowWins(t *testing.T) {
	set := window.EvaluateAll([]v1alpha1.MaintenanceWindow{
		mw("strict", func(w *v1alpha1.MaintenanceWindow) { w.Spec.AllowRed = false }),
		mw("batch", func(w *v1alpha1.MaintenanceWindow) {
			w.Spec.AllowRed = true
			w.Spec.NodeSelector = map[string]string{"pool": "batch"}
		}),
	}, at(t, "2026-08-02 03:00"))

	if ok, why := set.AllowsRedOn(node("b1", map[string]string{"pool": "batch"})); !ok {
		t.Errorf("batch node should be permitted by the batch window: %s", why)
	}
	if ok, _ := set.AllowsRedOn(node("w1", map[string]string{"pool": "web"})); ok {
		t.Error("a web node was permitted by a batch-scoped window")
	}
}

func TestClosedWindowReportsWhenItOpensNext(t *testing.T) {
	s := window.Evaluate(mw("weekly"), at(t, "2026-08-03 12:00")) // Monday
	if s.Open {
		t.Fatal("should be closed on a Monday")
	}
	if s.NextOpen.IsZero() {
		t.Fatal("no next opening reported")
	}
	if s.NextOpen.Weekday() != time.Sunday || s.NextOpen.Hour() != 2 {
		t.Errorf("next opening is %s, want the following Sunday at 02:00", s.NextOpen)
	}
	if !strings.Contains(s.Reason, "next opens") {
		t.Errorf("reason should tell the operator when to come back: %q", s.Reason)
	}
}

// Across a DST boundary the window must still open at 02:00 local, not drift by
// an hour. 2026-10-25 is when the UK leaves BST.
func TestWindowTracksLocalTimeAcrossDST(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/London")
	// 02:30 local on the Sunday the clocks change.
	instant := time.Date(2026, 10, 25, 2, 30, 0, 0, loc)
	s := window.Evaluate(mw("weekly"), instant)
	if !s.Open {
		t.Errorf("window should be open at 02:30 local on a DST-change Sunday: %s", s.Reason)
	}
}
