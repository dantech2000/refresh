package cmdutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
)

// ErrStopped is the error of a watch that the user stopped with Ctrl+C. It
// is not a failure: to stop a watch is the normal way to end it, so the
// process exits 0 and prints nothing. It is the one exception to "an
// interrupt exits 1".
var ErrStopped = errors.New("stopped")

// minWatchInterval keeps a watch from calling AWS in a tight loop.
const minWatchInterval = time.Second

// AddWatchFlags adds -w/--watch and --watch-interval to cmd.
func AddWatchFlags(cmd *cobra.Command, watch *bool, interval *time.Duration) {
	cmd.Flags().BoolVarP(watch, "watch", "w", false, "Read again and redraw every --watch-interval, until Ctrl+C")
	cmd.Flags().DurationVar(interval, "watch-interval", 10*time.Second, "Time between two reads of --watch")
}

// ValidateWatch rejects a watch that cannot work: a watch redraws a table
// on a terminal. A machine format prints one document for a run, and plain
// output one table, so neither can be watched; a script runs the command
// in a loop.
func ValidateWatch(ios *IOStreams, format Format, watch bool, interval time.Duration) error {
	switch {
	case !watch:
		return nil
	case format != FormatTable:
		return fmt.Errorf("--watch cannot be used with -o %s: it redraws a table; run the command in a loop for a script", format)
	case !ios.OutTTY:
		return errors.New("--watch needs a terminal on stdout: it redraws a table; run the command in a loop for a script")
	case interval < minWatchInterval:
		return fmt.Errorf("invalid --watch-interval %s: must be at least %s", interval, minWatchInterval)
	}
	return nil
}

// clearScreen moves the cursor to the top left and clears the terminal.
const clearScreen = "\x1b[H\x1b[2J"

// Watch calls draw, and again after each interval, until ctx ends. It
// clears the screen before each call, so each read replaces the one
// before it. draw runs one read of the command and prints it; the
// --timeout budget applies to each read, because draw sets it.
//
// A read with a partial result (exit 4) printed what it has and named what
// it could not read, and the watch goes on: the next read may be whole.
// Any other error ends the watch. When ctx ends, the watch returns
// ErrStopped.
//
// after returns a channel that delivers after a duration; nil means
// time.After. A test passes its own.
func Watch(ctx context.Context, ios *IOStreams, interval time.Duration, after func(time.Duration) <-chan time.Time, draw func(context.Context) error) error {
	if after == nil {
		after = time.After
	}
	for {
		if _, err := io.WriteString(ios.Out, clearScreen); err != nil {
			return err
		}
		err := draw(ctx)
		if ctx.Err() != nil {
			return ErrStopped
		}
		if err != nil && ExitCode(err) != ExitIncomplete {
			return err
		}
		select {
		case <-ctx.Done():
			return ErrStopped
		case <-after(interval):
		}
	}
}
