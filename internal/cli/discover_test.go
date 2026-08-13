package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/model"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func backendSvc(name, ns, instance string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "k8s-dencer",
				"app.kubernetes.io/component": "ui-backend",
				"app.kubernetes.io/instance":  instance,
			},
		},
	}
}

// The CLI used to rebuild the Service name as `<release>-ui-backend`, which is
// correct for exactly one release name — k8s-dencer, the one in every doc. The
// chart derives it from the fullname helper, so `helm install dencer` produces
// dencer-k8s-dencer-ui-backend and the CLI could not find its own backend.
//
// Worse, the error told the user to pass --release, which they had already
// passed correctly; the value that actually worked was the fullname, which is
// not a release name at all.
func TestBackendIsFoundWhateverTheReleaseIsCalled(t *testing.T) {
	for _, tc := range []struct {
		name    string
		svc     string
		release string
	}{
		{"conventional release", "k8s-dencer-ui-backend", "k8s-dencer"},
		{"release without the chart name", "dencer-k8s-dencer-ui-backend", "dencer"},
		{"release nothing like the chart", "platform-tools-k8s-dencer-ui-backend", "platform-tools"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewClientset(backendSvc(tc.svc, "ns", tc.release))
			got, err := findBackendService(context.Background(), cs, "ns", tc.release)
			if err != nil {
				t.Fatalf("could not find the backend: %v", err)
			}
			if got.Name != tc.svc {
				t.Errorf("found %q, want %q", got.Name, tc.svc)
			}
		})
	}
}

// Two installs in one namespace is unusual but legal, and picking either at
// random would send commands to the wrong cluster's plan.
func TestTwoInstallsAreDistinguishedByRelease(t *testing.T) {
	cs := fake.NewClientset(
		backendSvc("a-k8s-dencer-ui-backend", "ns", "a"),
		backendSvc("b-k8s-dencer-ui-backend", "ns", "b"),
	)
	got, err := findBackendService(context.Background(), cs, "ns", "b")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.Name != "b-k8s-dencer-ui-backend" {
		t.Errorf("found %q, want the release that was asked for", got.Name)
	}
}

// And if the release named does not match either, say so rather than guessing.
func TestAmbiguousInstallsAreReportedNotGuessed(t *testing.T) {
	cs := fake.NewClientset(
		backendSvc("a-k8s-dencer-ui-backend", "ns", "a"),
		backendSvc("b-k8s-dencer-ui-backend", "ns", "b"),
	)
	_, err := findBackendService(context.Background(), cs, "ns", "neither")
	if err == nil {
		t.Fatal("two candidate backends were silently narrowed to one")
	}
	for _, want := range []string{"a-k8s-dencer-ui-backend", "b-k8s-dencer-ui-backend", "--release"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q so the user can choose: %v", want, err)
		}
	}
}

// An install predating these labels is still reachable by its old name.
func TestUnlabelledInstallStillResolves(t *testing.T) {
	cs := fake.NewClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "k8s-dencer-ui-backend", Namespace: "ns"},
	})
	got, err := findBackendService(context.Background(), cs, "ns", "k8s-dencer")
	if err != nil {
		t.Fatalf("legacy install unreachable: %v", err)
	}
	if got.Name != "k8s-dencer-ui-backend" {
		t.Errorf("found %q", got.Name)
	}
}

// Nothing installed is a different message from the wrong release, and it must
// not repeat advice the user has already followed.
func TestMissingBackendSaysWhatItLookedFor(t *testing.T) {
	cs := fake.NewClientset()
	_, err := findBackendService(context.Background(), cs, "ns", "k8s-dencer")
	if err == nil {
		t.Fatal("expected an error with no backend installed")
	}
	if !strings.Contains(err.Error(), "app.kubernetes.io/component=ui-backend") {
		t.Errorf("error does not say what it looked for: %v", err)
	}
}

// A plan ID is a content hash, so a stable cluster produces the same plan
// every cycle and GeneratedAt stays at the moment those steps were first
// computed. Reporting that as the plan's age tells an operator nobody has
// planned since yesterday, while the planner is re-confirming it every thirty
// seconds — and while the UI, reading planConfirmedAt, says "confirmed just
// now" about the same plan.
//
// Observed on a real cluster: "plan 51e99333b1a5  20h33m54s old" seconds after
// the planner logged that exact plan ID.
func TestFreshnessSeparatesConfirmedFromUnchanged(t *testing.T) {
	now := time.Now()

	t.Run("stable cluster reports both", func(t *testing.T) {
		env := &PlanEnvelope{
			Plan:     &model.Plan{GeneratedAt: now.Add(-20 * time.Hour)},
			StoredAt: now.Add(-9 * time.Second),
		}
		got := freshness(env)
		if !strings.Contains(got, "confirmed") {
			t.Errorf("a plan re-confirmed 9s ago reads as stale: %q", got)
		}
		if !strings.Contains(got, "unchanged for 20h") {
			t.Errorf("how long the plan has been stable is worth keeping: %q", got)
		}
	})

	t.Run("changing cluster says it once", func(t *testing.T) {
		env := &PlanEnvelope{
			Plan:     &model.Plan{GeneratedAt: now.Add(-12 * time.Second)},
			StoredAt: now.Add(-12 * time.Second),
		}
		got := freshness(env)
		if strings.Contains(got, "confirmed") {
			t.Errorf("a freshly computed plan should not need two clauses: %q", got)
		}
		if !strings.Contains(got, "old") {
			t.Errorf("want the plain form: %q", got)
		}
	})

	t.Run("no stored time falls back rather than claiming 1970", func(t *testing.T) {
		env := &PlanEnvelope{Plan: &model.Plan{GeneratedAt: now.Add(-time.Minute)}}
		got := freshness(env)
		if strings.Contains(got, "confirmed") {
			t.Errorf("with no confirmation time there is nothing to report: %q", got)
		}
	})
}
