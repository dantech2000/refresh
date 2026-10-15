//go:build unix

package ui

import (
	"fmt"
	"os"
	"runtime/debug"

	"golang.org/x/sys/unix"
)

// detachStderr points file descriptor 2 and os.Stderr at the null device
// and returns the function that puts them back. Reassigning os.Stderr
// reaches only the code that reads it later; a child process (a kubeconfig
// exec plugin and the tools it starts) and a handle taken earlier write to
// descriptor 2 itself, and a line there lands on top of the UI. A crash
// trace still reaches the terminal: it goes to the saved descriptor.
func detachStderr() (restore func(), err error) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", os.DevNull, err)
	}
	saved, err := unix.Dup(2)
	if err != nil {
		_ = null.Close()
		return nil, fmt.Errorf("saving stderr: %w", err)
	}
	if err := unix.Dup2(int(null.Fd()), 2); err != nil {
		_ = unix.Close(saved)
		_ = null.Close()
		return nil, fmt.Errorf("detaching stderr: %w", err)
	}
	terminal := os.NewFile(uintptr(saved), "stderr") //nolint:gosec // saved is a descriptor that Dup returned
	stderr := os.Stderr
	os.Stderr = null
	// A failed SetCrashOutput only means that a crash trace goes to the
	// null device, as it would without it.
	_ = debug.SetCrashOutput(terminal, debug.CrashOptions{})
	return func() {
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
		_ = unix.Dup2(saved, 2)
		os.Stderr = stderr
		_ = terminal.Close()
		_ = null.Close()
	}, nil
}
