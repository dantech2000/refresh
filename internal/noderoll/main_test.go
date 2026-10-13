package noderoll

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package when a test leaves a goroutine running: an
// observer that watches starts the goroutines of two informers, and Close
// must end them. No goroutine is ignored.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
