//go:build unix

package ui_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

const east = "us-east-1"

// stderrToFile points file descriptor 2 at a file for the rest of the
// test and returns its path. The cleanup puts the real one back.
func stderrToFile(t *testing.T) string {
	t.Helper()
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
	t.Cleanup(func() { _ = file.Close() })
	if err := unix.Dup2(int(file.Fd()), 2); err != nil {
		t.Fatal(err)
	}
	return path
}

// uiFake is the fake of a session: the cluster prod in the home region, a
// terminal on stdin and stdout, and stdin a pipe that the test types into.
func uiFake(t *testing.T) (*cmdtest.Fake, *io.PipeWriter) {
	t.Helper()
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		s.Clusters = append(s.Clusters, &fleettest.Cluster{Name: "prod", Region: east, Version: "1.33"})
	})
	e := cmdtest.NewFake(t, world, east)
	in, typed := io.Pipe()
	t.Cleanup(func() { _ = typed.Close() })
	ios := e.Factory.IOStreams
	ios.In, ios.InTTY, ios.OutTTY = in, true, true
	return e, typed
}

// typeOnce types keys into the session once, from a hook of the fake,
// when the first sweep describes the cluster: the backend runs, and the
// model waits for its next state. The write blocks until the program
// reads it, so it runs on a goroutine that the test waits for.
func typeOnce(t *testing.T, e *cmdtest.Fake, typed *io.PipeWriter, keys string) (wait func()) {
	t.Helper()
	var once sync.Once
	var wg sync.WaitGroup
	e.Server.OnAnswer = func(r awsxtest.Request, _ awsxtest.Answer) {
		if r.Operation == fleettest.EKSDescribeCluster {
			once.Do(func() {
				wg.Go(func() {
					if _, err := typed.Write([]byte(keys)); err != nil {
						t.Errorf("typing %q: %v", keys, err)
					}
				})
			})
		}
	}
	return wg.Wait
}

// The whole session, as main runs it, with no terminal: run starts the
// backend, detaches descriptor 2, runs the program, and on q puts
// descriptor 2 back, says nothing (no change was started), stops the
// backend, and waits for it. The goleak TestMain of the package checks
// that nothing of the session is left running.
func TestRun_QuitEndsTheSession(t *testing.T) {
	stderr := stderrToFile(t)
	e, typed := uiFake(t)
	wait := typeOnce(t, e, typed, "q")
	if err := e.Run(t.Context(), "ui"); err != nil {
		t.Fatalf("the session ended with %v, want nil", err)
	}
	wait()
	if e.Err.Len() != 0 {
		t.Errorf("stderr of the command = %q, want nothing", e.Err.String())
	}
	if e.Out.Len() == 0 {
		t.Error("the program drew nothing")
	}
	assertStderrBack(t, stderr)
}

// An interrupt ends the session context: run returns no error, and main
// says that the run was interrupted. The backend is stopped all the same.
func TestRun_InterruptEndsTheSession(t *testing.T) {
	stderr := stderrToFile(t)
	e, _ := uiFake(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var once sync.Once
	e.Server.OnAnswer = func(r awsxtest.Request, _ awsxtest.Answer) {
		if r.Operation == fleettest.EKSDescribeCluster {
			once.Do(cancel)
		}
	}
	if err := e.Run(ctx, "ui"); err != nil {
		t.Fatalf("the interrupted session ended with %v, want nil", err)
	}
	if e.Err.Len() != 0 {
		t.Errorf("stderr of the command = %q, want nothing", e.Err.String())
	}
	assertStderrBack(t, stderr)
}

// assertStderrBack checks that descriptor 2 reaches the stderr of before
// the session again.
func assertStderrBack(t *testing.T, path string) {
	t.Helper()
	_, _ = unix.Write(2, []byte("after the session\n"))
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "after the session") {
		t.Errorf("descriptor 2 is not the stderr of before the session: %q", got)
	}
}
