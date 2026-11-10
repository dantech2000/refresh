//go:build unix

package ui

import (
	"fmt"
	"os"
	"runtime/debug"
	"sync"

	"golang.org/x/sys/unix"
)

// detachStderr points file descriptor 2 at the null device and returns the
// function that puts it back. Descriptor 2 is where os.Stderr writes, and
// where a child process (a kubeconfig exec plugin and the tools it starts)
// and a handle taken earlier write; a line there lands on top of the UI.
// os.Stderr itself is not reassigned: Bubble Tea reads it on a goroutine
// that it does not wait for, to print a panic. A crash that nothing
// recovers still reaches the terminal: its trace goes to the saved
// descriptor.
//
// The restore runs once, however often it is called. The first call
// closes the saved descriptor; a second Dup2 of its number would point
// descriptor 2 at whatever file or socket reused it.
func detachStderr() (restore func(), err error) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", os.DevNull, err)
	}
	defer func() { _ = null.Close() }() // descriptor 2 keeps the device open
	saved, err := unix.Dup(2)
	if err != nil {
		return nil, fmt.Errorf("saving stderr: %w", err)
	}
	if err := unix.Dup2(int(null.Fd()), 2); err != nil {
		_ = unix.Close(saved)
		return nil, fmt.Errorf("detaching stderr: %w", err)
	}
	terminal := os.NewFile(uintptr(saved), "stderr") //nolint:gosec // saved is a descriptor that Dup returned
	// A failed SetCrashOutput only means that a crash trace goes to the
	// null device, as it would without it.
	_ = debug.SetCrashOutput(terminal, debug.CrashOptions{})
	return sync.OnceFunc(func() {
		_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
		_ = unix.Dup2(saved, 2)
		_ = terminal.Close()
	}), nil
}
