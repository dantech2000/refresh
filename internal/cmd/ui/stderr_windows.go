//go:build windows

package ui

import (
	"fmt"
	"os"
)

// detachStderr points os.Stderr at the null device, which is what client-go
// hands a kubeconfig exec plugin, and returns the function that puts it
// back. Windows has no descriptor to redirect, so a program that writes to
// its own standard error handle can still write over the UI.
func detachStderr() (restore func(), err error) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", os.DevNull, err)
	}
	stderr := os.Stderr
	os.Stderr = null
	return func() {
		os.Stderr = stderr
		_ = null.Close()
	}, nil
}
