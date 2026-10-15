//go:build unix

package parity

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// A reader that goes away, as "refresh ... | head -1" does, must not make
// the v2 binary hang or write garbage: the first write to the closed pipe
// ends the process, as it ends any Unix filter, and nothing is written to
// stderr in its place. Each command line runs against a fake AWS with a
// stdout whose read end is closed before the binary starts, so the first
// write fails whatever the timing.
func TestClosedStdoutEndsTheRun(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		for _, name := range []string{"prod", "staging"} {
			s.Clusters = append(s.Clusters, &fleettest.Cluster{
				Name: name, Region: Region, Version: "1.33",
				Nodegroups: []*fleettest.Nodegroup{{Name: "web", Version: "1.33"}},
			})
		}
	})
	srv := awsxtest.NewServer(world)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(environment(srv.URL, dir, empty, filepath.Join(dir, "no-kubeconfig")), "REFRESH_NO_UPDATE_CHECK=1")

	for _, args := range [][]string{
		{"version"},
		{"completion", "bash"},
		{"cluster", "list", "-r", Region},
		{"cluster", "list", "-r", Region, "-o", "json"},
		{"status", "-r", Region, "-o", "yaml"},
		{"nodegroup", "list", "-c", "prod", "-r", Region, "-o", "plain"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			// The guard of a hang. The run itself takes milliseconds.
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, v2Binary, args...) //nolint:gosec // the binary this package built
			cmd.Env = env
			cmd.Stdout = w
			var stderr strings.Builder
			cmd.Stderr = &stderr
			cmd.Stdin = strings.NewReader("")
			err = cmd.Run()
			_ = w.Close()
			if ctx.Err() != nil {
				t.Fatalf("the run did not end after its stdout closed: %v", ctx.Err())
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("the run succeeded or did not start (%v), want it ended by the closed pipe", err)
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok {
				t.Fatalf("no wait status in %v", exit)
			}
			// SIGPIPE is the death of a filter whose reader left. An exit
			// code of 1 with a message about the pipe is the other
			// acceptable end; anything on stderr must say so in one line.
			switch {
			case status.Signaled() && status.Signal() == syscall.SIGPIPE:
				if stderr.Len() != 0 {
					t.Errorf("stderr after SIGPIPE:\n%s", stderr.String())
				}
			case status.Exited() && status.ExitStatus() == 1:
				if !strings.Contains(stderr.String(), "broken pipe") || strings.Count(strings.TrimSpace(stderr.String()), "\n") > 0 {
					t.Errorf("exit 1 with stderr %q, want one line about the broken pipe", stderr.String())
				}
			default:
				t.Errorf("the run ended with %v and stderr %q, want SIGPIPE or exit 1", exit, stderr.String())
			}
		})
	}
}
