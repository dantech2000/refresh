//go:build unix

package commands

import (
	"io"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// While the TUI runs, a write to file descriptor 2 itself (a child
// process, a handle taken before the TUI started) goes nowhere; after
// restore it reaches the terminal again.
func TestDetachStdioSilencesDescriptorTwo(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	real, err := unix.Dup(2)
	if err != nil {
		t.Fatal(err)
	}
	// The pipe stands in for the terminal.
	if err := unix.Dup2(int(w.Fd()), 2); err != nil {
		t.Fatal(err)
	}
	_, restore, err := detachStdio()
	if err == nil {
		_, _ = unix.Write(2, []byte("W1002 feature_gate.go] a plugin's line\n"))
		restore()
		_, _ = unix.Write(2, []byte("after\n"))
	}
	_ = unix.Dup2(real, 2)
	_ = unix.Close(real)
	_ = w.Close()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	if string(got) != "after\n" {
		t.Fatalf("the terminal got %q, want only the line after restore", got)
	}
}
