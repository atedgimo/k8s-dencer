package executor

import (
	"strings"
	"testing"
	"time"
)

// "3 pods move" is a count. How long the workload is degraded is the
// consequence, and two Green steps can differ by an order of magnitude in it
// while looking identical on screen.
func TestRecoverySummaryNamesTheSlowest(t *testing.T) {
	got := recoverySummary(map[string]time.Duration{
		"shop/web":      8 * time.Second,
		"shop/payments": 2*time.Minute + 10*time.Second,
		"shop/cache":    40 * time.Second,
	})
	if !strings.Contains(got, "shop/payments") {
		t.Errorf("summary does not name the slowest workload: %q", got)
	}
	if !strings.Contains(got, "2m10s") {
		t.Errorf("summary does not carry the slowest duration: %q", got)
	}
}

// One workload needs no comparison, so it reads as a plain duration.
func TestRecoverySummaryReadsPlainlyForOne(t *testing.T) {
	got := recoverySummary(map[string]time.Duration{"shop/web": 12 * time.Second})
	if got != ", in 12s" {
		t.Errorf("summary = %q, want \", in 12s\"", got)
	}
}

// Sub-second recoveries are the simulated fabric, or a pod that never really
// left. Reporting "0s" would be noise dressed as precision.
func TestRecoverySummaryDoesNotReportZero(t *testing.T) {
	got := recoverySummary(map[string]time.Duration{"shop/web": 120 * time.Millisecond})
	if strings.Contains(got, "0s") {
		t.Errorf("sub-second recovery reported as a measurement: %q", got)
	}
	if !strings.Contains(got, "within a second") {
		t.Errorf("summary = %q, want it to say the recovery was under a second", got)
	}
}

// Nothing observed means nothing claimed.
func TestRecoverySummaryEmpty(t *testing.T) {
	if got := recoverySummary(nil); got != "" {
		t.Errorf("summary = %q, want empty", got)
	}
}
