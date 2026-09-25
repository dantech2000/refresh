package commands

import (
	"errors"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
)

// runUICommand calls the action directly: Run on a root command would pass
// the exit error to urfave's OsExiter and end the test binary.
func runUICommand(t *testing.T) error {
	t.Helper()
	return runUI(t.Context(), UICommand())
}

func TestUINeedsATerminalForTheLiveBackend(t *testing.T) {
	// go test runs without a terminal on stdin.
	t.Setenv(envDevSimulate, "")
	err := runUICommand(t)
	if err == nil || !strings.Contains(err.Error(), "needs an interactive terminal") {
		t.Fatalf("err = %v", err)
	}
	var ec cli.ExitCoder
	if !errors.As(err, &ec) || ec.ExitCode() != runner.ExitError {
		t.Fatalf("exit code = %v, want %d", ec, runner.ExitError)
	}
}

func TestUISimulatedNeedsATerminal(t *testing.T) {
	// go test runs without a terminal on stdin.
	t.Setenv(envDevSimulate, "1")
	err := runUICommand(t)
	if err == nil || !strings.Contains(err.Error(), "needs an interactive terminal") {
		t.Fatalf("err = %v", err)
	}
}

func TestUICommandIsHidden(t *testing.T) {
	if !UICommand().Hidden {
		t.Fatal("refresh ui must stay hidden while it is an experiment")
	}
}

func TestEnvFloat(t *testing.T) {
	t.Setenv(envDevSimSpeed, "")
	if v, err := envFloat(envDevSimSpeed, 8); err != nil || v != 8 {
		t.Fatalf("unset = %v, %v", v, err)
	}
	t.Setenv(envDevSimSpeed, "2.5")
	if v, err := envFloat(envDevSimSpeed, 8); err != nil || v != 2.5 {
		t.Fatalf("2.5 = %v, %v", v, err)
	}
	for _, bad := range []string{"fast", "0", "-1"} {
		t.Setenv(envDevSimSpeed, bad)
		if _, err := envFloat(envDevSimSpeed, 8); err == nil || !strings.Contains(err.Error(), envDevSimSpeed) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}
