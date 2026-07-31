package cli_test

import (
	"strings"
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/cli"
	"github.com/atedgimo/k8s-dencer/internal/model"
)

func confirmPlan() *cli.PlanEnvelope {
	return &cli.PlanEnvelope{
		Plan: &model.Plan{
			ID: "abc123def456",
			Steps: []model.PlanStep{
				{SequenceNumber: 1, TargetNode: "node-a", Impact: model.ImpactGreen,
					Moves: []model.Move{{Pod: "p1"}, {Pod: "p2"}}},
				{SequenceNumber: 2, TargetNode: "node-b", Impact: model.ImpactRed,
					Moves: []model.Move{{Pod: "p3"}}},
				{SequenceNumber: 3, TargetNode: "node-c", Impact: model.ImpactGreen,
					Moves: []model.Move{{Pod: "p4"}}},
			},
		},
	}
}

// The consent semantics, exhaustively: only an explicit yes is a yes.
func TestConfirmOnlyExplicitYesConsents(t *testing.T) {
	cases := []struct {
		answer string
		want   bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{" yes \n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false},    // bare enter: the [y/N] default is N
		{"", false},      // EOF: closed stdin is not consent
		{"yep\n", false}, // almost is not yes
		{"y n\n", false}, // ambiguity is not yes
		{"continue\n", false},
	}
	for _, c := range cases {
		var out strings.Builder
		got, err := cli.Confirm(&out, strings.NewReader(c.answer), confirmPlan(), []int{1, 2})
		if err != nil {
			t.Errorf("answer %q: unexpected error %v", c.answer, err)
		}
		if got != c.want {
			t.Errorf("answer %q: consent = %v, want %v", c.answer, got, c.want)
		}
	}
}

// What the operator is shown must name the machines and count the evictions —
// the moment of consent is exactly when vagueness is most dangerous.
func TestConfirmShowsWhatWillActuallyHappen(t *testing.T) {
	var out strings.Builder
	if _, err := cli.Confirm(&out, strings.NewReader("n\n"), confirmPlan(), []int{1, 2}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	text := out.String()

	for _, must := range []string{
		"node-a", // the machines, named
		"node-b",
		"3 pod(s) will be evicted", // 2 moves + 1 move, steps 1 and 2 only
		"1 step(s) are Red",        // the Red warning
		"[y/N]",                    // the default is visible
	} {
		if !strings.Contains(text, must) {
			t.Errorf("confirmation prompt is missing %q; shown:\n%s", must, text)
		}
	}
	if strings.Contains(text, "node-c") {
		t.Error("confirmation lists a step that was not selected; the operator is consenting to the wrong thing")
	}
}
