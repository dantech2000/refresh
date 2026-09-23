package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"
)

func newWatchTestCommand(t *testing.T, watch bool, interval time.Duration) *cli.Command {
	t.Helper()
	var captured *cli.Command
	cmd := &cli.Command{
		Name: "test",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "watch"},
			&cli.DurationFlag{Name: "watch-interval", Value: 10 * time.Second},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			captured = c
			return nil
		},
	}
	argv := []string{"test"}
	if watch {
		argv = append(argv, "--watch")
	}
	if interval > 0 {
		argv = append(argv, "--watch-interval="+interval.String())
	}
	if err := cmd.Run(context.Background(), argv); err != nil {
		t.Fatal(err)
	}
	return captured
}

func TestWatchRunsOnceWithoutFlag(t *testing.T) {
	runs := 0
	err := Watch(context.Background(), newWatchTestCommand(t, false, 0), func() error {
		runs++
		return nil
	})
	if err != nil || runs != 1 {
		t.Fatalf("Watch without --watch: runs=%d err=%v, want 1 run and nil", runs, err)
	}
}

func TestWatchPropagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	runs := 0
	err := Watch(context.Background(), newWatchTestCommand(t, true, time.Millisecond), func() error {
		runs++
		if runs == 3 {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Watch error = %v, want sentinel", err)
	}
	if runs != 3 {
		t.Fatalf("Watch should rerun until error: runs=%d, want 3", runs)
	}
}

// A partial result (exit 4) does not end --watch: the next poll may be
// complete. Any other error does.
func TestWatchKeepsPollingAfterIncomplete(t *testing.T) {
	sentinel := errors.New("boom")
	runs := 0
	err := Watch(context.Background(), newWatchTestCommand(t, true, time.Millisecond), func() error {
		runs++
		if runs < 3 {
			return cli.Exit("incomplete", ExitIncomplete)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) || runs != 3 {
		t.Fatalf("runs=%d err=%v, want 3 runs ending with the sentinel", runs, err)
	}
}

// --watch with -o json/yaml would print one document per interval (plus
// clear-screen codes on a terminal), breaking the one-document stdout
// contract. It must fail before fn runs; other formats still watch.
func TestWatchRejectsMachineFormats(t *testing.T) {
	for _, tc := range []struct {
		format  string
		wantErr bool
	}{
		{"json", true},
		{"YAML", true},
		{"table", false},
		{"plain", false},
	} {
		t.Run(tc.format, func(t *testing.T) {
			var captured *cli.Command
			root := &cli.Command{
				Name: "test",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "watch"},
					&cli.DurationFlag{Name: "watch-interval", Value: time.Millisecond},
					&cli.StringFlag{Name: "format", Aliases: []string{"o"}},
				},
				Action: func(_ context.Context, c *cli.Command) error { captured = c; return nil },
			}
			if err := root.Run(context.Background(), []string{"test", "--watch", "-o", tc.format}); err != nil {
				t.Fatal(err)
			}
			runs := 0
			err := Watch(context.Background(), captured, func() error {
				runs++
				return errors.New("stop")
			})
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "--watch cannot be combined") || runs != 0 {
					t.Fatalf("Watch -o %s: err=%v runs=%d, want rejection before any run", tc.format, err, runs)
				}
				return
			}
			if runs != 1 {
				t.Fatalf("Watch -o %s: runs=%d, want 1", tc.format, runs)
			}
		})
	}
}

func TestWatchNonInteractiveDoesNotClearScreen(t *testing.T) {
	// In tests stdout is not a TTY, so watchIsTerminal() is false and no
	// clear-screen codes are emitted; just verify the loop respects errors.
	old := watchIsTerminal
	watchIsTerminal = func() bool { return false }
	t.Cleanup(func() { watchIsTerminal = old })

	runs := 0
	_ = Watch(context.Background(), newWatchTestCommand(t, true, time.Millisecond), func() error {
		runs++
		return errors.New("stop")
	})
	if runs != 1 {
		t.Fatalf("runs = %d, want 1", runs)
	}
}
