package ui

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
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

func TestPromptContinueWithWarnings(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Bare Enter declines: this prompt gates an update on a cluster with
		// health warnings, so the safe answer must be the default.
		{"\n", false},
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

			if got := PromptContinueWithWarnings(t.Context(), []string{"warn"}); got != tt.want {
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

	if PromptContinueWithWarnings(t.Context(), nil) {
		t.Fatal("expected false on read error")
	}
}

// The prompt follows the output contract: it goes to stderr as "[y/N]", and
// stdout gets nothing.
func TestPromptContinueWithWarningsWritesToStderr(t *testing.T) {
	readOut, readErr := fakeStreams(t, false, false)
	original := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = original })
	_, _ = w.WriteString("y\n")
	_ = w.Close()

	if !PromptContinueWithWarnings(t.Context(), []string{"warn"}) {
		t.Fatal("PromptContinueWithWarnings() = false, want true")
	}
	if out := readOut(); out != "" {
		t.Errorf("stdout = %q, want nothing", out)
	}
	want := "\n1 warning(s) reported above.\nProceed with update? [y/N]: "
	if got := readErr(); got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}
