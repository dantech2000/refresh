package nodegroup

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/health"
)

// captureStdout runs fn and returns everything written to os.Stdout and to
// fatih/color's writer while it ran. The color library snapshots os.Stdout
// at package init so we must override color.Output too.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	originalStdout := os.Stdout
	originalColorOutput := color.Output
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	color.Output = w
	t.Cleanup(func() {
		os.Stdout = originalStdout
		color.Output = originalColorOutput
	})
	fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

// Regression: --health-only is an explicit request for the health verdict.
// The success banner must print even when --quiet is also set; --quiet only
// suppresses noise around an actual update, not the result the user asked for.
func TestApplyHealthDecision_HealthOnlyPrintsBannerEvenWhenQuiet(t *testing.T) {
	flags := updateAMIFlags{healthOnly: true, quiet: true}
	summary := health.HealthSummary{Decision: health.DecisionProceed}

	var done bool
	var err error
	out := captureStdout(t, func() {
		done, err = applyHealthDecision(summary, flags)
	})

	if !done {
		t.Error("expected done=true on health-only path")
	}
	if err != nil {
		t.Errorf("expected nil error on PASS, got %v", err)
	}
	if !strings.Contains(out, "PASS") {
		t.Errorf("expected PASS banner in output, got %q", out)
	}
}

// Quiet alone (no --health-only) must suppress the banner during the regular
// update flow — the previous step's spinner success line already told the
// user the checks ran.
func TestApplyHealthDecision_QuietSuppressesBannerOnRegularUpdate(t *testing.T) {
	flags := updateAMIFlags{healthOnly: false, quiet: true}
	summary := health.HealthSummary{Decision: health.DecisionProceed}

	var done bool
	out := captureStdout(t, func() {
		done, _ = applyHealthDecision(summary, flags)
	})

	if done {
		t.Error("expected done=false so update can proceed")
	}
	if strings.Contains(out, "PASS") {
		t.Errorf("quiet mode should not print PASS banner, got %q", out)
	}
}

// Non-quiet PROCEED always prints the banner.
func TestApplyHealthDecision_NonQuietProceedPrintsBanner(t *testing.T) {
	flags := updateAMIFlags{healthOnly: false, quiet: false}
	summary := health.HealthSummary{Decision: health.DecisionProceed}

	out := captureStdout(t, func() {
		_, _ = applyHealthDecision(summary, flags)
	})

	if !strings.Contains(out, "PASS") {
		t.Errorf("expected PASS banner, got %q", out)
	}
}

// BLOCK always prints the fail banner and returns an error.
func TestApplyHealthDecision_BlockReturnsErrorAndPrintsBanner(t *testing.T) {
	flags := updateAMIFlags{quiet: true}
	summary := health.HealthSummary{Decision: health.DecisionBlock}

	var done bool
	var err error
	out := captureStdout(t, func() {
		done, err = applyHealthDecision(summary, flags)
	})

	if !done {
		t.Error("expected done=true on BLOCK")
	}
	if err == nil {
		t.Error("expected error on BLOCK")
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("expected FAIL banner, got %q", out)
	}
}

// WARN under --health-only short-circuits without prompting the user.
func TestApplyHealthDecision_WarnHealthOnlyDoesNotPrompt(t *testing.T) {
	flags := updateAMIFlags{healthOnly: true, quiet: true}
	summary := health.HealthSummary{Decision: health.DecisionWarn, Warnings: []string{"something"}}

	// captureStdout also redirects os.Stdout; the prompt would block on stdin if invoked.
	out := captureStdout(t, func() {
		done, err := applyHealthDecision(summary, flags)
		if !done {
			t.Error("expected done=true on WARN+healthOnly")
		}
		if err != nil {
			t.Errorf("expected nil error on WARN+healthOnly, got %v", err)
		}
	})
	_ = out
}
