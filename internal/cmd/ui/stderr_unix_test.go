//go:build unix

package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// While the UI runs, a write to file descriptor 2, as a kubeconfig exec
// plugin makes, goes nowhere, and so does a write to os.Stderr. After the
// restore both reach the stderr of before again.
func TestDetachStderr_SilencesDescriptorTwo(t *testing.T) {
	// Descriptor 2 of the test points at a file, so the test can read
	// what reached it. The cleanup puts the real one back.
	original, err := unix.Dup(2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Dup2(original, 2)
		_ = unix.Close(original)
	})
	path := filepath.Join(t.TempDir(), "stderr")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := unix.Dup2(int(file.Fd()), 2); err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr

	restore, err := detachStderr()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = unix.Write(2, []byte("descriptor while detached\n"))
	_, _ = os.Stderr.WriteString("os.Stderr while detached\n")
	restore()
	if os.Stderr != stderr {
		t.Error("os.Stderr is not the one of before the detach")
	}
	_, _ = unix.Write(2, []byte("descriptor after restore\n"))
	_, _ = os.Stderr.WriteString("os.Stderr after restore\n")

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "while detached") {
		t.Errorf("a write reached stderr while it was detached:\n%s", got)
	}
	for _, want := range []string{"descriptor after restore", "os.Stderr after restore"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("stderr lacks %q after the restore:\n%s", want, got)
		}
	}
}
