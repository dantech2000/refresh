package change

import (
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// No test of this package may start a timer of the real clock: a
	// budget gets its timer from Deps.AfterFunc, or from the test.
	newTimer = func(time.Duration, func()) (stop func() bool) {
		panic("a test started a real timer: set Deps.AfterFunc, or the afterFunc of the budget")
	}
	goleak.VerifyTestMain(m)
}
