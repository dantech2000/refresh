package nodegroup_test

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

// --quiet hides the report of the health gate, so on a terminal it must
// not go past a warning that the user did not see: the run asks, and the
// question names the warning. A no rolls nothing.
func TestUpdate_QuietOnATerminalAsksAboutWarnings(t *testing.T) {
	const question = "Proceed despite 1 warning (Kubernetes: no Kubernetes access"
	for _, tt := range []struct {
		answer string
		exit   int
		rolls  []string
	}{
		{"n\n", cmdutil.ExitFailure, nil},
		{"y\n", cmdutil.ExitOK, []string{"web"}},
	} {
		t.Run(strings.TrimSpace(tt.answer), func(t *testing.T) {
			e, _ := runUpdate(t, updateCase{noKube: true, args: []string{"web", "--quiet"}, answer: tt.answer, exit: tt.exit, rolls: tt.rolls})
			if n := strings.Count(e.Err.String(), question); n != 1 {
				t.Errorf("the question about the warning is on stderr %d times, want 1:\n%s", n, e.Err)
			}
		})
	}
	// Without a terminal nobody can be asked, and --quiet is no consent.
	t.Run("no terminal", func(t *testing.T) {
		runUpdate(t, updateCase{noKube: true, args: []string{"web", "--quiet"}, exit: cmdutil.ExitFailure})
	})
}

// A size beyond int32 is a usage error. It never wraps to another size,
// and nothing is sent.
func TestScale_SizeBeyondInt32(t *testing.T) {
	for _, flag := range []string{"--desired", "--min", "--max"} {
		for _, value := range []string{"2147483648", "4294967299", "-2147483649"} {
			t.Run(flag+" "+value, func(t *testing.T) {
				e := cmdtest.NewFake(t, scaleWorld(nil), east)
				err := e.Run(cmdutiltest.Context(t), "nodegroup", "scale", "web", "-c", "prod", "--yes", flag, value)
				if err == nil || !strings.HasPrefix(err.Error(), flag+" is too ") {
					t.Fatalf("error = %v, want that the value of %s is invalid", err, flag)
				}
				if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
					t.Errorf("exit code = %d, want 1", got)
				}
				if n := len(e.Server.Requests()); n != 0 {
					t.Errorf("%d requests, want none: %v", n, e.Operations())
				}
			})
		}
	}
}
