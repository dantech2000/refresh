package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
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

// refresh ui is shown, and says it is experimental wherever it is listed
// (help, completion, the command reference).
func TestUICommandIsShownAsExperimental(t *testing.T) {
	c := UICommand()
	if c.Hidden || !strings.Contains(c.Usage, "experimental") {
		t.Fatalf("Hidden = %v, Usage = %q; want shown and labelled experimental", c.Hidden, c.Usage)
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

func TestDetachStdioHidesStderrAndStdinUntilRestore(t *testing.T) {
	in, errOut := os.Stdin, os.Stderr
	tty, restore, err := detachStdio()
	if err != nil {
		t.Fatal(err)
	}
	if tty.in != in {
		t.Fatal("the TUI did not keep the original stdin")
	}
	if os.Stderr == errOut || os.Stdin == in {
		t.Fatal("stderr or stdin still points at the terminal while the TUI runs")
	}
	if _, err := fmt.Fprintln(os.Stderr, "an exec plugin's traceback"); err != nil {
		t.Fatalf("writing to the detached stderr: %v", err)
	}
	restore()
	if os.Stderr != errOut || os.Stdin != in {
		t.Fatal("restore did not put stdin and stderr back")
	}
}

// Without -r or -A the UI sweeps the config region and the kubectl
// cluster's region, when that one is in the same partition.
func TestUIRegionsAddTheKubectlRegion(t *testing.T) {
	t.Setenv("REFRESH_EKS_REGIONS", "")
	for _, tc := range []struct {
		name, cfg, kubectl string
		args               []string
		want               []string
	}{
		{"no kubectl", "us-east-1", "", nil, []string{"us-east-1"}},
		{"same region", "us-east-1", "us-east-1", nil, []string{"us-east-1"}},
		{"other region", "us-east-1", "eu-west-1", nil, []string{"us-east-1", "eu-west-1"}},
		{"other partition", "us-east-1", "cn-north-1", nil, []string{"us-east-1"}},
		{"govcloud", "us-east-1", "us-gov-west-1", nil, []string{"us-east-1"}},
		{"opt-in region", "us-east-1", "eu-south-1", nil, []string{"us-east-1", "eu-south-1"}},
		{"china to china", "cn-north-1", "cn-northwest-1", nil, []string{"cn-north-1", "cn-northwest-1"}},
		{"explicit -r", "us-east-1", "eu-west-1", []string{"-r", "ap-south-1"}, []string{"ap-south-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			cmd := UICommand()
			cmd.Action = func(_ context.Context, cmd *cli.Command) error {
				got, _ = uiRegions(cmd, aws.Config{Region: tc.cfg}, tc.kubectl)
				return nil
			}
			if err := cmd.Run(t.Context(), append([]string{"ui"}, tc.args...)); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("regions = %v, want %v", got, tc.want)
			}
		})
	}
}
