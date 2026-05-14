package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	awsClient "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/types"
)

func TestMatchingNodegroups(t *testing.T) {
	nodegroups := []string{"web-prod", "web-staging", "api-prod", "api-staging"}

	tests := []struct {
		name     string
		pattern  string
		expected []string
	}{
		{
			name:     "empty pattern returns all",
			pattern:  "",
			expected: []string{"web-prod", "web-staging", "api-prod", "api-staging"},
		},
		{
			name:     "pattern matches multiple",
			pattern:  "web",
			expected: []string{"web-prod", "web-staging"},
		},
		{
			name:     "pattern matches single",
			pattern:  "web-prod",
			expected: []string{"web-prod"},
		},
		{
			name:     "pattern matches none",
			pattern:  "database",
			expected: []string{},
		},
		{
			name:     "pattern matches prod",
			pattern:  "prod",
			expected: []string{"web-prod", "api-prod"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := awsClient.MatchingNodegroups(nodegroups, tt.pattern)
			if len(result) != len(tt.expected) {
				t.Errorf("matchingNodegroups() = %v, want %v", result, tt.expected)
				return
			}

			for i, expected := range tt.expected {
				if result[i] != expected {
					t.Errorf("matchingNodegroups() = %v, want %v", result, tt.expected)
					return
				}
			}
		})
	}
}

func TestMatchingClusters(t *testing.T) {
	clusters := []string{"prod-cluster", "staging-cluster", "dev-cluster"}

	tests := []struct {
		name     string
		pattern  string
		expected []string
	}{
		{
			name:     "empty pattern returns all",
			pattern:  "",
			expected: []string{"prod-cluster", "staging-cluster", "dev-cluster"},
		},
		{
			name:     "pattern matches multiple",
			pattern:  "cluster",
			expected: []string{"prod-cluster", "staging-cluster", "dev-cluster"},
		},
		{
			name:     "pattern matches single",
			pattern:  "prod",
			expected: []string{"prod-cluster"},
		},
		{
			name:     "pattern matches none",
			pattern:  "test",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := awsClient.MatchingClusters(clusters, tt.pattern)
			if len(result) != len(tt.expected) {
				t.Errorf("matchingClusters() = %v, want %v", result, tt.expected)
				return
			}

			for i, expected := range tt.expected {
				if result[i] != expected {
					t.Errorf("matchingClusters() = %v, want %v", result, tt.expected)
					return
				}
			}
		})
	}
}

func TestAMIStatusString(t *testing.T) {
	tests := []struct {
		name     string
		status   types.AMIStatus
		expected string
	}{
		{
			name:     "AMILatest shows Latest",
			status:   types.AMILatest,
			expected: "Latest",
		},
		{
			name:     "AMIOutdated shows Outdated",
			status:   types.AMIOutdated,
			expected: "Outdated",
		},
		{
			name:     "AMIUpdating shows Updating",
			status:   types.AMIUpdating,
			expected: "Updating",
		},
		{
			name:     "AMIUnknown shows Unknown",
			status:   types.AMIUnknown,
			expected: "Unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// PlainString() returns uncolored text; String() adds ANSI codes tested separately.
			if got := tt.status.PlainString(); got != tt.expected {
				t.Errorf("AMIStatus.PlainString() = %q, want %q", got, tt.expected)
			}
			// String() must also contain the same text (with possible color wrapping).
			if !containsText(tt.status.String(), tt.expected) {
				t.Errorf("AMIStatus.String() does not contain %q", tt.expected)
			}
		})
	}
}

func TestDryRunActionString(t *testing.T) {
	tests := []struct {
		name      string
		action    types.DryRunAction
		plainWant string
	}{
		{"ActionUpdate", types.ActionUpdate, "UPDATE"},
		{"ActionSkipUpdating", types.ActionSkipUpdating, "SKIP"},
		{"ActionSkipLatest", types.ActionSkipLatest, "SKIP"},
		{"ActionForceUpdate", types.ActionForceUpdate, "FORCE UPDATE"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.action.PlainString(); !strings.Contains(got, tt.plainWant) {
				t.Errorf("DryRunAction.PlainString() = %q, want to contain %q", got, tt.plainWant)
			}
			if !containsText(tt.action.String(), tt.plainWant) {
				t.Errorf("DryRunAction.String() does not contain %q", tt.plainWant)
			}
		})
	}
}

func TestColoredHelpPrinter(t *testing.T) {
	var out bytes.Buffer

	coloredHelpPrinter(&out, `NAME:
   {{.Name}}
COMMANDS:
   cluster, c  manage clusters
   1invalid    should not be colored as command
`, map[string]string{"Name": "refresh"})

	got := out.String()
	if !strings.Contains(got, "refresh") {
		t.Fatalf("coloredHelpPrinter: missing app name 'refresh' in output: %q", got)
	}
	if !strings.Contains(got, "cluster, c") {
		t.Fatalf("coloredHelpPrinter: missing command 'cluster, c' in output: %q", got)
	}
	if !strings.Contains(got, "manage clusters") {
		t.Fatalf("coloredHelpPrinter: missing command description in output: %q", got)
	}
	// Section header should be present.
	if !strings.Contains(got, "NAME") || !strings.Contains(got, "COMMANDS") {
		t.Fatalf("coloredHelpPrinter: missing section headers in output: %q", got)
	}
}

func TestMainHelpPath(t *testing.T) {
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })

	os.Args = []string{"refresh", "--help"}
	main()
}

func TestNewAppAndRun(t *testing.T) {
	app := newApp()
	if app.Name != "refresh" {
		t.Fatalf("app name = %q", app.Name)
	}
	if len(app.Commands) == 0 || len(app.Flags) != 2 {
		t.Fatalf("unexpected app shape: commands=%d flags=%d", len(app.Commands), len(app.Flags))
	}

	var out, errOut bytes.Buffer
	if err := run([]string{"refresh", "version"}, &out, &errOut); err != nil {
		t.Fatalf("run version: %v", err)
	}
	// Note: VersionCommand uses fmt.Printf which writes to the real os.Stdout, not
	// app.Writer. We therefore only verify the command returns no error here.

	out.Reset()
	if err := run([]string{"refresh", "--timeout", "not-a-duration", "version"}, &out, &errOut); err == nil {
		t.Fatal("expected invalid flag error")
	}
}

func TestMainErrorPath(t *testing.T) {
	oldArgs := os.Args
	oldExit := exitProcess
	t.Cleanup(func() {
		os.Args = oldArgs
		exitProcess = oldExit
	})

	os.Args = []string{"refresh", "--timeout", "not-a-duration", "version"}
	exitProcess = func(code int) { panic(fmt.Sprintf("exit:%d", code)) }

	defer func() {
		r := recover()
		if r != "exit:1" {
			t.Fatalf("recover = %v, want exit:1", r)
		}
	}()
	main()
}

// containsText strips ANSI escape sequences then checks whether coloredText
// contains expectedText as a substring.
func containsText(coloredText, expectedText string) bool {
	// Strip ANSI escape sequences (ESC [ ... m) before comparing.
	var b strings.Builder
	s := coloredText
	for len(s) > 0 {
		idx := strings.IndexByte(s, '\033')
		if idx < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:idx])
		s = s[idx+1:]
		// Skip past the CSI sequence which ends at 'm'.
		if len(s) > 0 && s[0] == '[' {
			end := strings.IndexByte(s, 'm')
			if end >= 0 {
				s = s[end+1:]
			}
		}
	}
	return strings.Contains(b.String(), expectedText)
}
