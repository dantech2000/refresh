//go:build windows

package commands

import "os"

// detachStderrFD is a no-op on Windows: there, only os.Stderr is
// reassigned (see detachStdio).
func detachStderrFD(*os.File) (int, error) { return -1, nil }

func restoreStderrFD(int) {}
