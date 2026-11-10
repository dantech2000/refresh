package ui

import (
	"testing"

	"go.uber.org/goleak"
)

// Program.Run of Bubble Tea returns without waiting for the commands that
// still run, and the model has a tick of one second in flight: its
// goroutine, and the one of the batch that waits for it, end when the
// timer fires. A command of the UI that does not end still shows: its own
// function is on top of its stack.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("charm.land/bubbletea/v2.Tick.func1"),
		goleak.IgnoreAnyFunction("charm.land/bubbletea/v2.(*Program).execBatchMsg"),
	)
}
