package nodegroup_test

import (
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Pods that cannot follow their node, such as a pod whose EBS volume
// lives in a zone that the new nodes are not in, stay Pending after the
// roll. No pre-flight check reads volumes or zones, so the gate lets the
// roll go; the check after the roll names the new Pending pods, exit 5.
// The fake cannot place a volume in a zone; RollScript.PendingPods leaves
// every evicted pod Pending, which is the outcome.
func TestUpdate_PodsLeftPending(t *testing.T) {
	world := updateWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Nodegroup("web").UpdateScripts = []fleettest.UpdateScript{{After: 30 * time.Second, Roll: fleettest.RollScript{PendingPods: true}}}
	})
	e := cmdtest.NewFake(t, world, east)
	e.UseWorldClock()
	withKube(t, e)
	err := e.Run(cmdutiltest.Context(t), "nodegroup", "update", "web", "-c", "prod", "--yes")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitVerifyFailed {
		t.Errorf("exit code = %d (%v), want 5", got, err)
	}
	out := e.Out.String() + e.Err.String()
	for _, want := range []string{"NewPendingPods", "newly Pending after the roll", "default/app-1-u1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output has no %q:\n%s", want, out)
		}
	}
}
