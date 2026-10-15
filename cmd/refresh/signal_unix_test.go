//go:build unix

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// envSignalHelper makes TestSignalHelperProcess run its body.
const envSignalHelper = "REFRESH_TEST_SIGNAL_HELPER"

func TestSignalCancelsContext(t *testing.T) {
	tests := []struct {
		name   string
		signal syscall.Signal
	}{
		{name: "interrupt", signal: syscall.SIGINT},
		{name: "terminate", signal: syscall.SIGTERM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, stop := notifyContext(context.Background())
			defer stop()
			if err := ctx.Err(); err != nil {
				t.Fatalf("context is done before the signal: %v", err)
			}

			if err := syscall.Kill(os.Getpid(), tt.signal); err != nil {
				t.Fatalf("sending %v: %v", tt.signal, err)
			}

			// The handler is in place, so the signal cancels the context.
			// If it did not, the test would stop at its timeout.
			<-ctx.Done()
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Errorf("context error = %v, want context.Canceled", ctx.Err())
			}
		})
	}
}

func TestStopReleasesContext(*testing.T) {
	ctx, stop := notifyContext(context.Background())
	stop()
	<-ctx.Done()
}

// TestSignalHelperProcess is not a test. It is the body of the process that
// TestSecondSignalKills starts. It reports each step on stdout and then
// blocks, as a command would that ignores its context.
func TestSignalHelperProcess(*testing.T) {
	if os.Getenv(envSignalHelper) == "" {
		return
	}
	ctx, stop := notifyContext(context.Background())
	defer stop()
	fmt.Println("ready")
	<-ctx.Done()
	fmt.Println("cancelled")
	// Block until the parent closes stdin. The second signal must end the
	// process before that.
	_, _ = bufio.NewReader(os.Stdin).ReadByte()
	fmt.Println("stdin closed")
}

// TestSecondSignalKills checks that after the first signal cancelled the
// context, a second one ends a process that did not stop.
func TestSecondSignalKills(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSignalHelperProcess$")
	cmd.Env = append(os.Environ(), envSignalHelper+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	// Closing stdin unblocks the helper if the second signal did not end it.
	defer func() { _ = stdin.Close() }()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the helper process: %v", err)
	}
	lines := bufio.NewScanner(stdout)
	expect := func(want string) {
		t.Helper()
		if !lines.Scan() || lines.Text() != want {
			_ = cmd.Process.Kill()
			t.Fatalf("helper printed %q (error %v), want %q", lines.Text(), lines.Err(), want)
		}
	}

	expect("ready")
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("first signal: %v", err)
	}
	expect("cancelled")
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("second signal: %v", err)
	}

	if lines.Scan() {
		t.Errorf("helper printed %q after the second signal, want nothing", lines.Text())
	}
	var exitErr *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exitErr) {
		t.Fatalf("helper ended with %v, want a death by signal", err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGINT {
		t.Errorf("helper ended with %v, want a death by SIGINT", exitErr)
	}
}
