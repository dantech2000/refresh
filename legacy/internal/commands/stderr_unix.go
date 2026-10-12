//go:build unix

package commands

import (
	"os"

	"golang.org/x/sys/unix"
)

// detachStderrFD points file descriptor 2 at null and returns a copy of
// the original, for restoreStderrFD. Reassigning os.Stderr reaches only the
// code that reads it later: a child process (a kubeconfig exec plugin, and
// the tools it starts) or a handle taken earlier writes to descriptor 2
// itself, and a line there lands on top of the TUI.
func detachStderrFD(null *os.File) (int, error) {
	saved, err := unix.Dup(2)
	if err != nil {
		return -1, err
	}
	if err := unix.Dup2(int(null.Fd()), 2); err != nil {
		_ = unix.Close(saved)
		return -1, err
	}
	return saved, nil
}

// restoreStderrFD puts the descriptor detachStderrFD saved back on 2.
func restoreStderrFD(saved int) {
	if saved < 0 {
		return
	}
	_ = unix.Dup2(saved, 2)
	_ = unix.Close(saved)
}
