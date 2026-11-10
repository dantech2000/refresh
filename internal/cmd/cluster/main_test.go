package cluster

import (
	"testing"

	"go.uber.org/goleak"
)

// describe reads the nodes beside the rest of the cluster on its own
// goroutine.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
