package healthview

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
)

func sampleSummary() health.HealthSummary {
	return health.HealthSummary{
		Decision: health.DecisionWarn,
		Results: []health.HealthResult{
			{Name: "Node Health", Status: health.StatusPass, Score: 100},
			{Name: "Cluster Capacity", Status: health.StatusWarn, Score: 60},
			{Name: "Control Plane", Status: health.StatusFail, Score: 10},
			{Name: "Critical Workloads", Status: health.StatusPass, Skipped: true, Message: "Kubernetes client not available"},
		},
		Warnings: []string{"capacity is tight"},
		Errors:   []string{"control plane unhealthy"},
	}
}

// lineWith returns the first line of lines that contains s.
func lineWith(lines []string, s string) string {
	for _, l := range lines {
		if strings.Contains(l, s) {
			return l
		}
	}
	return ""
}

func TestReportLines_Tokens(t *testing.T) {
	lines := ReportLines(render.New(render.ColorNone, true), sampleSummary())
	for name, want := range map[string]string{
		"Node Health":        "● PASS",
		"Cluster Capacity":   "▲ WARN",
		"Control Plane":      "✗ FAIL",
		"Critical Workloads": "• SKIP",
	} {
		if l := lineWith(lines, name); !strings.Contains(l, want) {
			t.Errorf("%s row = %q, want %q", name, l, want)
		}
	}
	// A skipped check was not measured: it never reads as PASS.
	if l := lineWith(lines, "Critical Workloads"); strings.Contains(l, "PASS") || strings.Contains(l, "█") {
		t.Errorf("skipped row = %q, want an empty bar and no PASS", l)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"▸ CLUSTER HEALTH ASSESSMENT",
		"Status: ▲ READY WITH WARNINGS (2 issues found)",
		"Warnings:\n  ▲ capacity is tight",
		"Errors:\n  ✗ control plane unhealthy",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("report missing %q:\n%s", want, joined)
		}
	}
}

// Without Unicode the report falls back to ASCII glyphs and bars, and
// without color it carries no escape codes: color is additive.
func TestReportLines_ASCIIFallback(t *testing.T) {
	lines := ReportLines(render.New(render.ColorNone, false), sampleSummary())
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"> CLUSTER HEALTH ASSESSMENT", "[OK] PASS", "[!] WARN", "[X] FAIL", "- SKIP", "[####################]", "Status: [!] READY WITH WARNINGS"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ASCII report missing %q:\n%s", want, joined)
		}
	}
	if strings.ContainsAny(joined, "●▲✗•█░▕▏▸\x1b") {
		t.Errorf("ASCII report has Unicode glyphs or ANSI:\n%s", joined)
	}
}

func TestReportLines_Colored(t *testing.T) {
	joined := strings.Join(ReportLines(render.New(render.ColorTrue, true), sampleSummary()), "\n")
	if !strings.Contains(joined, "\x1b[38;2;") {
		t.Fatalf("truecolor report has no color:\n%q", joined)
	}
}

func TestVerdictLines(t *testing.T) {
	th := render.New(render.ColorNone, true)
	pass := strings.Join(VerdictLines(th, health.DecisionProceed, false), "\n")
	if !strings.Contains(pass, "● All health checks passed. Proceeding with the update.") {
		t.Errorf("proceed verdict = %q", pass)
	}
	// --health-only stops after the checks: the verdict must not say the
	// update is going ahead.
	only := strings.Join(VerdictLines(th, health.DecisionProceed, true), "\n")
	if !strings.Contains(only, "● All health checks passed.") || strings.Contains(only, "Proceeding") {
		t.Errorf("--health-only verdict = %q", only)
	}
	block := strings.Join(VerdictLines(th, health.DecisionBlock, false), "\n")
	for _, want := range []string{"✗ Critical health issues detected", "--skip-health-check"} {
		if !strings.Contains(block, want) {
			t.Errorf("block verdict missing %q: %q", want, block)
		}
	}
	if got := VerdictLines(th, health.DecisionWarn, false); got != nil {
		t.Errorf("warn verdict = %q, want none (the prompt decides)", got)
	}
}

func TestStartLines(t *testing.T) {
	got := strings.Join(StartLines(render.New(render.ColorNone, true), "prod"), "\n")
	if !strings.Contains(got, "▸ PRE-FLIGHT HEALTH CHECKS  prod") || strings.Contains(got, "update-ami") {
		t.Errorf("start = %q", got)
	}
}

// The writers theme for the writer they get: a buffer is not a terminal, so
// no escape codes reach it.
func TestWritersPlainForNonTerminal(t *testing.T) {
	var buf bytes.Buffer
	WriteStart(&buf, "prod")
	WriteReport(&buf, sampleSummary())
	WriteVerdict(&buf, health.DecisionBlock, false)
	if strings.Contains(buf.String(), "\x1b") {
		t.Fatalf("output to a buffer has ANSI escapes: %q", buf.String())
	}
	for _, want := range []string{"prod", "CLUSTER HEALTH ASSESSMENT", "Critical health issues detected"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestCheckRows(t *testing.T) {
	rows := CheckRows(render.New(render.ColorNone, true), sampleSummary().Results)
	if l := lineWith(rows, "Critical Workloads"); !strings.HasPrefix(l, "    • ") {
		t.Errorf("skipped check row = %q, want the neutral glyph", l)
	}
	if l := lineWith(rows, "Control Plane"); !strings.HasPrefix(l, "    ✗ ") {
		t.Errorf("failed check row = %q", l)
	}
	if CheckRows(render.New(render.ColorNone, true), nil) != nil {
		t.Error("no results should render no rows")
	}
}
