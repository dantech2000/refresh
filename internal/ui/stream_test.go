package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"
)

// fakeStreams makes stdout and stderr temp files and fakes which of them is a
// TTY. It restores os.Stdout, os.Stderr, the seam and fatih's global after
// the test. It returns readers for what each stream received.
func fakeStreams(t *testing.T, stdoutTTY, stderrTTY bool) (readOut, readErr func() string) {
	t.Helper()
	dir := t.TempDir()
	outF, err := os.Create(dir + "/stdout")
	if err != nil {
		t.Fatal(err)
	}
	errF, err := os.Create(dir + "/stderr")
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr, origSeam, origNoColor := os.Stdout, os.Stderr, isTerminalFd, color.NoColor
	origOff := colorOff.Load()
	SetColorDisabled(false)
	os.Stdout, os.Stderr = outF, errF
	isTerminalFd = func(fd uintptr) bool {
		switch fd {
		case outF.Fd():
			return stdoutTTY
		case errF.Fd():
			return stderrTTY
		}
		return false
	}
	t.Cleanup(func() {
		os.Stdout, os.Stderr, isTerminalFd, color.NoColor = origOut, origErr, origSeam, origNoColor
		SetColorDisabled(origOff)
		_ = outF.Close()
		_ = errF.Close()
	})
	read := func(name string) func() string {
		return func() string {
			b, err := os.ReadFile(dir + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
	}
	return read("stdout"), read("stderr")
}

// Each stream decides color on its own; NO_COLOR turns it off on both.
func TestStreamColorPerStream(t *testing.T) {
	tests := []struct {
		stdoutTTY, stderrTTY, noColor bool
		wantOut, wantErr              bool
	}{
		{stdoutTTY: true, stderrTTY: true, wantOut: true, wantErr: true},
		{stdoutTTY: true, stderrTTY: false, wantOut: true, wantErr: false},
		{stdoutTTY: false, stderrTTY: true, wantOut: false, wantErr: true},
		{stdoutTTY: false, stderrTTY: false},
		{stdoutTTY: true, stderrTTY: true, noColor: true},
		{stdoutTTY: true, stderrTTY: false, noColor: true},
		{stdoutTTY: false, stderrTTY: true, noColor: true},
		{stdoutTTY: false, stderrTTY: false, noColor: true},
	}
	for _, tt := range tests {
		name := fmt.Sprintf("stdoutTTY=%v/stderrTTY=%v/NO_COLOR=%v", tt.stdoutTTY, tt.stderrTTY, tt.noColor)
		t.Run(name, func(t *testing.T) {
			t.Setenv("TERM", "xterm-256color")
			if tt.noColor {
				t.Setenv("NO_COLOR", "1")
			} else {
				t.Setenv("NO_COLOR", "")
			}
			readOut, readErr := fakeStreams(t, tt.stdoutTTY, tt.stderrTTY)
			InitColor()

			// Stdout: fatih's global color, as every stdout view uses it.
			_, _ = fmt.Fprintln(os.Stdout, color.RedString("out"))
			// Stderr: a fatih color picked for stderr, plus a line that was
			// colored by the stdout decision (color.XString) and a raw escape.
			_, _ = StderrColor(color.FgYellow).Fprintf(Stderr, "warn\n")
			_, _ = fmt.Fprintln(Stderr, "\x1b[31mraw\x1b[0m")

			if got := strings.Contains(readOut(), "\x1b["); got != tt.wantOut {
				t.Errorf("stdout has ANSI = %v, want %v: %q", got, tt.wantOut, readOut())
			}
			errOut := readErr()
			if got := strings.Contains(errOut, "\x1b["); got != tt.wantErr {
				t.Errorf("stderr has ANSI = %v, want %v: %q", got, tt.wantErr, errOut)
			}
			if !strings.Contains(errOut, "warn") || !strings.Contains(errOut, "raw") {
				t.Errorf("stderr lost text: %q", errOut)
			}
			if got := StreamColor(os.Stdout); got != tt.wantOut {
				t.Errorf("StreamColor(stdout) = %v, want %v", got, tt.wantOut)
			}
			if got := StreamColor(Stderr); got != tt.wantErr {
				t.Errorf("StreamColor(Stderr) = %v, want %v", got, tt.wantErr)
			}
		})
	}
}

// A warning colored for a TTY stdout must reach a redirected stderr without
// escape codes.
func TestStderrWarningHasNoANSIWhenNotTTY(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	_, readErr := fakeStreams(t, true, false)
	InitColor()
	color.NoColor = false

	_, _ = color.New(color.FgYellow).Fprintf(Stderr, "Warning: %s\n", "skipping checks")
	_, _ = fmt.Fprintln(Stderr, color.RedString("Error: boom"))
	_, _ = ColorFor(Stderr, color.FgRed).Fprintln(Stderr, "refused")

	got := readErr()
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("stderr has escape codes: %q", got)
	}
	want := "Warning: skipping checks\nError: boom\nrefused\n"
	if got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

// --no-color and TERM=dumb disable color on a TTY stderr too.
func TestStderrColorDisabledEverywhere(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"no-color flag", func(t *testing.T) {
			t.Helper()
			SetColorDisabled(true)
			t.Cleanup(func() { SetColorDisabled(false) })
		}},
		{"TERM=dumb", func(t *testing.T) {
			t.Helper()
			t.Setenv("TERM", "dumb")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("TERM", "xterm-256color")
			_, readErr := fakeStreams(t, true, true)
			tc.setup(t)
			_, _ = StderrColor(color.FgYellow).Fprintln(Stderr, "warn")
			if got := readErr(); strings.ContainsRune(got, 0x1b) {
				t.Fatalf("stderr has escape codes: %q", got)
			}
		})
	}
}

// An escape sequence split across writes is still stripped.
func TestStderrStripsSplitEscape(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	_, readErr := fakeStreams(t, false, false)
	_, _ = Stderr.Write([]byte("a\x1b[3"))
	_, _ = Stderr.Write([]byte("1mb\x1b[0m\n"))
	if got := readErr(); got != "ab\n" {
		t.Fatalf("stderr = %q, want %q", got, "ab\n")
	}
}

// On a terminal with color off, only SGR color codes go: cursor control such
// as the spinner's erase-line must survive.
func TestStderrKeepsCursorControlOnTTYWithoutColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	_, readErr := fakeStreams(t, false, true)
	_, _ = Stderr.Write([]byte("\r\x1b[Kframe \x1b[31mred\x1b[0m\x1b[3"))
	_, _ = Stderr.Write([]byte("2mgreen\x1b[0m\n"))
	if got, want := readErr(), "\r\x1b[Kframe redgreen\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

// When stderr is not a terminal every escape goes, cursor control included.
func TestStderrDropsAllEscapesWhenNotTTY(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	_, readErr := fakeStreams(t, true, false)
	_, _ = Stderr.Write([]byte("\r\x1b[Kframe \x1b[31mred\x1b[0m\n"))
	if got, want := readErr(), "\rframe red\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

// NO_COLOR on a terminal keeps an animated spinner, uncolored.
func TestSpinnerAnimatesWithoutColorOnTTY(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm-256color")
	_, readErr := fakeStreams(t, false, true)
	s := NewFunSpinner([]string{"working"})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if !s.animated {
		t.Fatal("spinner should animate on a TTY with NO_COLOR")
	}
	s.Stop()
	got := readErr()
	if !strings.Contains(got, "\x1b[K") || !strings.Contains(got, "working") {
		t.Fatalf("spinner frame missing erase-line or text: %q", got)
	}
	if rest := strings.ReplaceAll(got, "\x1b[K", ""); strings.ContainsRune(rest, 0x1b) {
		t.Fatalf("spinner wrote color codes with NO_COLOR: %q", got)
	}
}

// TERM=dumb cannot erase a line, so no spinner frames are written.
func TestSpinnerOffOnDumbTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	_, readErr := fakeStreams(t, true, true)
	s := NewFunSpinner([]string{"working"})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if s.animated {
		t.Fatal("spinner must not animate with TERM=dumb")
	}
	s.Stop()
	s.Success("done")
	if got := readErr(); got != "" {
		t.Fatalf("spinner wrote with TERM=dumb: %q", got)
	}
}
