package runner

import (
	"context"
	"errors"
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
	err := Watch(newWatchTestCommand(t, false, 0), func() error {
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
	err := Watch(newWatchTestCommand(t, true, time.Millisecond), func() error {
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

func TestWatchNonInteractiveDoesNotClearScreen(t *testing.T) {
	// In tests stdout is not a TTY, so watchIsTerminal() is false and no
	// clear-screen codes are emitted; just verify the loop respects errors.
	old := watchIsTerminal
	watchIsTerminal = func() bool { return false }
	t.Cleanup(func() { watchIsTerminal = old })

	runs := 0
	_ = Watch(newWatchTestCommand(t, true, time.Millisecond), func() error {
		runs++
		return errors.New("stop")
	})
	if runs != 1 {
		t.Fatalf("runs = %d, want 1", runs)
	}
}
