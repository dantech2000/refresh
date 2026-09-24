package cluster

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// A --wait-timeout that runs out while the control-plane update is in
// progress reports a timeout that names --wait-timeout, not an interrupt,
// exits 1, and keeps the resume guidance.
func TestUpgrade_WaitTimeoutNamesFlag(t *testing.T) {
	for _, format := range []string{"table", "json"} {
		t.Run(format, func(t *testing.T) {
			world := upgradeWorld()
			world.UpdateStatus = "InProgress" // the control-plane update never finishes
			fakeaws.New(t, world)

			_, stderr, err := runCluster(t, "upgrade", "prod", "--to", "1.32", "--yes",
				"--poll-interval", "5ms", "--wait-timeout", "1s", "-o", format)
			if err == nil {
				t.Fatalf("upgrade succeeded, want a timeout\nstderr:\n%s", stderr)
			}
			if code := runner.ExitCodeOf(err); code != runner.ExitError {
				t.Errorf("exit code = %d, want %d", code, runner.ExitError)
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "timed out during control plane") || strings.Contains(msg, "interrupted") {
				t.Errorf("err = %q, want a timeout during the control-plane phase, not an interrupt", msg)
			}
			for _, want := range []string{"(increase --wait-timeout to allow more time)", "rerun the same command to resume"} {
				if !strings.Contains(msg, want) {
					t.Errorf("err = %q, want it to contain %q", msg, want)
				}
			}
			if strings.Contains(msg, "increase --timeout") {
				t.Errorf("err = %q names --timeout; the run deadline is --wait-timeout", msg)
			}
		})
	}
}
