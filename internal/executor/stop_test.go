package executor

import (
	"os"
	"strings"
	"testing"
)

// The product offers one control, not two, and the reason is worth pinning
// down: a pod already evicted cannot be un-evicted, so "abort" and "pause"
// differ only in the urgency of a thing that can happen at exactly one place
// — the step boundary. Two buttons would imply an undo that does not exist.
//
// This asserts the boundary check exists on both execution paths. If someone
// later moves the check inside a step, they are claiming an ability to
// interrupt an eviction, and this should stop them long enough to reconsider.
func TestStopIsCheckedOnBothPaths(t *testing.T) {
	for _, f := range []string{"executor.go", "converge.go"} {
		if !fileContains(t, f, "e.stopRequested(ctx, run.ID)") {
			t.Errorf("%s does not check for a stop request at its step boundary", f)
		}
	}
}

func fileContains(t *testing.T, name, want string) bool {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return strings.Contains(string(b), want)
}
