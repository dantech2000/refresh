package nodegroup

import (
	"context"
	"testing"
	"time"
)

// Fleet mode applies --timeout per cluster, not across the whole fleet;
// <= 0 means no per-cluster limit.
func TestFleetClusterContext(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	ctx, cancel := fleetClusterContext(parent, 20*time.Minute)
	dl, ok := ctx.Deadline()
	if !ok || time.Until(dl) < 19*time.Minute {
		t.Errorf("timeout 20m: deadline = %v (ok=%v), want ~20m out", dl, ok)
	}
	cancel()
	if parent.Err() != nil {
		t.Fatal("cancelling one cluster's context must not cancel the fleet")
	}

	ctx, cancel = fleetClusterContext(parent, 0)
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Error("timeout 0: want no deadline")
	}
	cancelParent()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("fleet cancellation (Ctrl+C) must reach the cluster context")
	}
}

// scale --wait must get --op-timeout on top of --timeout, not be capped by it.
func TestScaleSetupTimeout(t *testing.T) {
	cases := []struct {
		name     string
		api, op  time.Duration
		wait, hc bool
		want     time.Duration
	}{
		{"no wait uses --timeout", time.Minute, 5 * time.Minute, false, false, time.Minute},
		{"wait adds op-timeout", time.Minute, 5 * time.Minute, true, false, 6 * time.Minute},
		{"wait + health-check adds post-check", time.Minute, 5 * time.Minute, true, true, 7 * time.Minute},
		{"op-timeout 0 means no limit", time.Minute, 0, true, false, 0},
		{"timeout 0 means no limit", 0, 5 * time.Minute, true, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scaleSetupTimeout(tc.api, tc.op, tc.wait, tc.hc); got != tc.want {
				t.Errorf("scaleSetupTimeout = %v, want %v", got, tc.want)
			}
		})
	}
}
