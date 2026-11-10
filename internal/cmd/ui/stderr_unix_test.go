//go:build unix

package ui

import (
	"fmt"
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
	// In a program os.Stderr writes to descriptor 2, so detaching the
	// descriptor detaches it too; detachStderr does not reassign it. Under
	// go test -json the testing package points os.Stderr at os.Stdout, and
	// the test can check the descriptor only. (An os.NewFile(2) to stand in
	// for it would close descriptor 2 when the garbage collector finds it.)
	viaStderr := os.Stderr != os.Stdout
	stderr := os.Stderr
	wants := []string{"descriptor after restore"}

	restore, err := detachStderr()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = unix.Write(2, []byte("descriptor while detached\n"))
	if viaStderr {
		_, _ = os.Stderr.WriteString("os.Stderr while detached\n")
	}
	restore()
	if os.Stderr != stderr {
		t.Error("os.Stderr is not the one of before the detach")
	}
	_, _ = unix.Write(2, []byte("descriptor after restore\n"))
	if viaStderr {
		_, _ = os.Stderr.WriteString("os.Stderr after restore\n")
		wants = append(wants, "os.Stderr after restore")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "while detached") {
		t.Errorf("a write reached stderr while it was detached:\n%s", got)
	}
	for _, want := range wants {
		if !strings.Contains(string(got), want) {
			t.Errorf("stderr lacks %q after the restore:\n%s", want, got)
		}
	}
}

// Found in review: run restores once after the program and once in a
// defer. The first restore closes the saved descriptor; files and
// sockets opened before the second can take its number, and a second
// Dup2 of it pointed descriptor 2 at one of them. The restore runs once.
func TestDetachStderr_SecondRestoreDoesNothing(t *testing.T) {
	original, err := unix.Dup(2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Dup2(original, 2)
		_ = unix.Close(original)
	})
	dir := t.TempDir()
	stderrPath := filepath.Join(dir, "stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderrFile.Close() }()
	if err := unix.Dup2(int(stderrFile.Fd()), 2); err != nil {
		t.Fatal(err)
	}

	restore, err := detachStderr()
	if err != nil {
		t.Fatal(err)
	}
	restore()
	// Something else opens descriptors now: the sockets of a sweep, in
	// the real program. They take the lowest free numbers, the closed
	// saved descriptor among them.
	var others []string
	for i := range 3 {
		path := filepath.Join(dir, fmt.Sprintf("other-%d", i))
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		others = append(others, path)
	}
	restore() // the deferred call of run
	_, _ = unix.Write(2, []byte("an error message for the user\n"))

	for _, path := range others {
		if got, _ := os.ReadFile(path); len(got) > 0 {
			t.Errorf("after the second restore, descriptor 2 is %s, which holds %q", filepath.Base(path), got)
		}
	}
	if got, _ := os.ReadFile(stderrPath); !strings.Contains(string(got), "an error message for the user") {
		t.Errorf("stderr lacks the message after two restores: %q", got)
	}
}
