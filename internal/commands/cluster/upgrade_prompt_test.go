package cluster

import (
	"os"
	"path/filepath"
	"testing"
)

// The per-phase prompt follows the output contract: stderr, as "[y/N]".
// Stdout gets nothing, also in table mode.
func TestPromptPhase_WritesToStderr(t *testing.T) {
	dir := t.TempDir()
	open := func(name string) *os.File {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origIn, origOut, origErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = r, open("stdout"), open("stderr")
	t.Cleanup(func() { os.Stdin, os.Stdout, os.Stderr = origIn, origOut, origErr })
	_, _ = w.WriteString("y\n")
	_ = w.Close()

	if !promptPhase(t.Context(), "control plane 1.31 → 1.32") {
		t.Fatal("promptPhase = false, want true for y")
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := read("stdout"); got != "" {
		t.Errorf("stdout = %q, want nothing", got)
	}
	if got, want := read("stderr"), "\nProceed with control plane 1.31 → 1.32? [y/N]: "; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}
