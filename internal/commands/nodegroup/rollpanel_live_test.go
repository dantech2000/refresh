package nodegroup

import (
	"context"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

// runLiveRollForUpdate must be best-effort and bounded: a nil client returns
// immediately, and a cluster that never converges is bounded by the timeout
// (never hangs the update). Output is swallowed via captureStdout.
func TestRunLiveRollForUpdate_DegradesAndBounds(t *testing.T) {
	_ = captureStdout(t, func() {
		// nil client → immediate, no panic.
		runLiveRollForUpdate(context.Background(), nil, "ng", time.Second, time.Second)

		// A fake cluster whose old node never gets replaced → bounded by timeout.
		client := fake.NewClientset(kn("ip-1", true, false))
		start := time.Now()
		runLiveRollForUpdate(context.Background(), client, "spot-burst", 40*time.Millisecond, 10*time.Millisecond)
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("runLiveRollForUpdate did not respect timeout bound: %v", elapsed)
		}
	})
}
