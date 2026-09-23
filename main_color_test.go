package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fatih/color"
	"github.com/pterm/pterm"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/ui"
)

func restoreColor(t *testing.T) {
	t.Helper()
	prev := color.NoColor
	t.Cleanup(func() {
		color.NoColor = prev
		pterm.EnableColor()
	})
}

// NO_COLOR follows no-color.org: any non-empty value disables color, and it
// must never make flag parsing fail (it used to be a ParseBool env source).
func TestNoColorEnvAnyNonEmptyValue(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"yes", true},
		{"1", true},
		{"false", true}, // any non-empty value counts
		{"", false},
	}
	for _, tt := range tests {
		t.Run("NO_COLOR="+tt.value, func(t *testing.T) {
			restoreColor(t)
			t.Setenv("NO_COLOR", tt.value)
			if got := colorDisabled([]string{"refresh", "version"}); got != tt.want {
				t.Fatalf("colorDisabled() = %v, want %v", got, tt.want)
			}

			color.NoColor = false
			var out, errOut bytes.Buffer
			if err := run(context.Background(), []string{"refresh", "version", "--no-update-check"}, &out, &errOut); err != nil {
				t.Fatalf("run version with NO_COLOR=%q: %v", tt.value, err)
			}
			if tt.want && !color.NoColor {
				t.Fatal("color should be disabled")
			}
		})
	}
}

func TestColorDisabledFlag(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"refresh", "--no-color", "--help"}, true},
		{[]string{"refresh", "cluster", "list", "-no-color"}, true},
		{[]string{"refresh", "--no-color=true", "version"}, true},
		{[]string{"refresh", "--no-color=false", "version"}, false},
		{[]string{"refresh", "version"}, false},
		{[]string{"refresh", "use", "--", "--no-color"}, false},
		{[]string{"refresh", "use", "no-color"}, false},
	}
	for _, tt := range tests {
		if got := colorDisabled(tt.args); got != tt.want {
			t.Errorf("colorDisabled(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

// Help is printed without running Before, so --no-color must take effect
// before app.Run.
func TestNoColorFlagAppliesToHelp(t *testing.T) {
	restoreColor(t)
	t.Setenv("NO_COLOR", "")
	color.NoColor = false
	var out, errOut bytes.Buffer
	if err := run(context.Background(), []string{"refresh", "--no-color", "--help"}, &out, &errOut); err != nil {
		t.Fatalf("run --help: %v", err)
	}
	if bytes.Contains(out.Bytes(), []byte("\x1b[")) {
		t.Fatalf("help output contains ANSI escapes with --no-color: %q", out.String())
	}
}

// urfave/cli prints ExitCoder messages to its package-level ErrWriter. run
// points it at the stderr writer it is given (ui.Stderr in main), so a
// colored cli.Exit message reaches a redirected stderr without escapes.
func TestExitCoderMessageToRedirectedStderrHasNoANSI(t *testing.T) {
	restoreColor(t)
	t.Setenv("NO_COLOR", "")
	origErrWriter, origExiter, origStderr := cli.ErrWriter, cli.OsExiter, os.Stderr
	t.Cleanup(func() { cli.ErrWriter, cli.OsExiter, os.Stderr = origErrWriter, origExiter, origStderr })

	path := filepath.Join(t.TempDir(), "stderr")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	os.Stderr = f // a file: not a terminal

	var out bytes.Buffer
	if err := run(context.Background(), []string{"refresh", "version", "--no-update-check"}, &out, ui.Stderr); err != nil {
		t.Fatalf("run version: %v", err)
	}
	cli.OsExiter = func(int) {}
	color.NoColor = false // a message colored by the stdout decision
	cli.HandleExitCoder(cli.Exit(color.RedString("Upgrade blocked"), 1))

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("Upgrade blocked")) || bytes.Contains(got, []byte("\x1b")) {
		t.Errorf("redirected stderr = %q, want the message without escape codes", got)
	}
}
