package runner

import (
	"errors"
	"flag"
	"testing"
	"time"

	"github.com/urfave/cli/v2"
)

func newWatchTestContext(t *testing.T, watch bool, interval time.Duration) *cli.Context {
	t.Helper()
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	set.Bool("watch", false, "")
	set.Duration("watch-interval", 10*time.Second, "")
	if watch {
		if err := set.Set("watch", "true"); err != nil {
			t.Fatal(err)
		}
	}
	if interval > 0 {
		if err := set.Set("watch-interval", interval.String()); err != nil {
			t.Fatal(err)
		}
	}
	return cli.NewContext(cli.NewApp(), set, nil)
}

func TestWatchRunsOnceWithoutFlag(t *testing.T) {
	runs := 0
	err := Watch(newWatchTestContext(t, false, 0), func() error {
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
	err := Watch(newWatchTestContext(t, true, time.Millisecond), func() error {
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
	_ = Watch(newWatchTestContext(t, true, time.Millisecond), func() error {
		runs++
		return errors.New("stop")
	})
	if runs != 1 {
		t.Fatalf("runs = %d, want 1", runs)
	}
}
