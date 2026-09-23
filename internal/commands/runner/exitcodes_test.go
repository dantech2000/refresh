package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/urfave/cli/v3"
)

// Partial data after Ctrl+C is an interrupted run (exit 1); partial data
// after the --timeout deadline stays exit 4 (REF-165).
func TestUnlessInterrupted(t *testing.T) {
	partial := cli.Exit("incomplete", ExitIncomplete)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	deadline, cancelDeadline := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancelDeadline()
	<-deadline.Done()

	for _, tc := range []struct {
		name string
		ctx  context.Context
		err  error
		want int
	}{
		{"live, partial", t.Context(), partial, ExitIncomplete},
		{"live, ok", t.Context(), nil, ExitOK},
		{"interrupted, partial", canceled, partial, ExitError},
		{"interrupted, verdict", canceled, cli.Exit("stale", ExitNeedsAttention), ExitError},
		{"interrupted, ok", canceled, nil, ExitOK},
		{"deadline, partial", deadline, partial, ExitIncomplete},
		{"plain error", canceled, errors.New("boom"), ExitError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitCodeOf(UnlessInterrupted(tc.ctx, tc.err)); got != tc.want {
				t.Errorf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}
