package ui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/health"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = original })

	fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

// ──────────────────────────────────────────────────────────────────────────────
// GetHealthStatusText
// ──────────────────────────────────────────────────────────────────────────────

func TestGetHealthStatusText(t *testing.T) {
	cases := []struct {
		status health.HealthStatus
		want   string
	}{
		{health.StatusPass, "PASS"},
		{health.StatusWarn, "WARN"},
		{health.StatusFail, "FAIL"},
		{"UNKNOWN_STATUS", "UNKNOWN"},
	}
	for _, c := range cases {
		got := GetHealthStatusText(c.status)
		// Use visibleLength-stripped comparison since the value may be ANSI-colored.
		if !strings.Contains(got, c.want) {
			t.Errorf("GetHealthStatusText(%q) = %q, want it to contain %q", c.status, got, c.want)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// GetDecisionText
// ──────────────────────────────────────────────────────────────────────────────

func TestGetDecisionText(t *testing.T) {
	cases := []struct {
		decision health.Decision
		want     string
	}{
		{health.DecisionProceed, "READY FOR UPDATE"},
		{health.DecisionWarn, "READY WITH WARNINGS"},
		{health.DecisionBlock, "CRITICAL ISSUES FOUND"},
		{"SOME_OTHER", "UNKNOWN"},
	}
	for _, c := range cases {
		if got := GetDecisionText(c.decision); got != c.want {
			t.Errorf("GetDecisionText(%q) = %q, want %q", c.decision, got, c.want)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// GetHealthDecisionColor
// ──────────────────────────────────────────────────────────────────────────────

func TestGetHealthDecisionColor_ReturnsCallable(t *testing.T) {
	decisions := []health.Decision{
		health.DecisionProceed,
		health.DecisionWarn,
		health.DecisionBlock,
		"UNKNOWN",
	}
	for _, d := range decisions {
		fn := GetHealthDecisionColor(d)
		if fn == nil {
			t.Errorf("GetHealthDecisionColor(%q) returned nil", d)
		}
		if out := fn("x"); out == "" {
			t.Errorf("GetHealthDecisionColor(%q) fn returned empty string", d)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// RenderProgressBar
// ──────────────────────────────────────────────────────────────────────────────

func TestRenderProgressBar_VisibleLength(t *testing.T) {
	// Bar is [ + 20 chars + ] = 22 visible chars.
	for _, status := range []health.HealthStatus{health.StatusPass, health.StatusWarn, health.StatusFail, "OTHER"} {
		bar := RenderProgressBar(50, status)
		if vl := visibleLength(bar); vl != 22 {
			t.Errorf("RenderProgressBar(%q) visible length = %d, want 22", status, vl)
		}
	}
}

func TestRenderProgressBar_FullScore(t *testing.T) {
	bar := RenderProgressBar(100, health.StatusPass)
	// All 20 inner chars should be filled (no empty char).
	// Strip ANSI and check no "▒" appears.
	if strings.Contains(bar, "▒") {
		t.Error("100% bar should have no empty blocks")
	}
}

func TestRenderProgressBar_ZeroScore(t *testing.T) {
	bar := RenderProgressBar(0, health.StatusFail)
	if strings.Contains(bar, "█") {
		t.Error("0% bar should have no filled blocks")
	}
}

func TestRenderProgressBar_HasBrackets(t *testing.T) {
	bar := RenderProgressBar(50, health.StatusPass)
	if !strings.Contains(bar, "[") || !strings.Contains(bar, "]") {
		t.Error("bar should contain opening and closing brackets")
	}
}

func TestDisplayHealthResults(t *testing.T) {
	output := captureStdout(t, func() {
		DisplayHealthResults(health.HealthSummary{
			Decision: health.DecisionWarn,
			Results: []health.HealthResult{
				{Name: "Nodes", Status: health.StatusPass, Score: 100},
				{Name: "Pods", Status: health.StatusWarn, Score: 50},
			},
			Warnings: []string{"warning"},
			Errors:   []string{"error"},
		})
	})

	for _, want := range []string{"Cluster Health Assessment", "Nodes", "Warnings", "Errors", "issues found"} {
		if !strings.Contains(output, want) {
			t.Fatalf("DisplayHealthResults output missing %q: %q", want, output)
		}
	}

	output = captureStdout(t, func() {
		DisplayHealthResults(health.HealthSummary{Decision: health.DecisionProceed})
	})
	if !strings.Contains(output, "Status:") {
		t.Fatalf("DisplayHealthResults no-issues output = %q", output)
	}
}

func TestPromptContinueWithWarnings(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"\n", true},
		{"y\n", true},
		{"yes\n", true},
		{"n\n", false},
	}

	for _, tt := range tests {
		t.Run(strings.TrimSpace(tt.input), func(t *testing.T) {
			original := os.Stdin
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stdin = r
			t.Cleanup(func() { os.Stdin = original })
			_, _ = w.WriteString(tt.input)
			_ = w.Close()

			if got := PromptContinueWithWarnings([]string{"warn"}); got != tt.want {
				t.Fatalf("PromptContinueWithWarnings() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPromptContinueWithWarningsReadError(t *testing.T) {
	original := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = original })
	_ = r.Close()
	_ = w.Close()

	if PromptContinueWithWarnings(nil) {
		t.Fatal("expected false on read error")
	}
}

func TestHealthDisplayHelpers(t *testing.T) {
	if NewHealthSpinner("checking") == nil {
		t.Fatal("NewHealthSpinner returned nil")
	}
	output := captureStdout(t, func() {
		DisplayHealthCheckStart("prod")
		DisplayHealthCheckComplete(health.DecisionWarn)
		DisplayHealthCheckComplete(health.DecisionBlock)
	})
	if !strings.Contains(output, "prod") || !strings.Contains(output, "--force") {
		t.Fatalf("health helper output = %q", output)
	}
	DisplayHealthCheckComplete(health.DecisionProceed)
}
