package nodegroup

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// A roll that waits on Auto Scaling (the vCPU quota refuses the launch) is
// only "in progress" to EKS: the run says why.
func TestUpdate_ReportsFailedNodeLaunches(t *testing.T) {
	quota := "Could not launch On-Demand Instances. VcpuLimitExceeded - You have requested more vCPU capacity than your current vCPU limit of 8 allows."
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31", UpdateStatus: "InProgress", ScalingFailures: []string{quota}}))
	stdout, stderr, _ := runNodegroup(t, "update", "prod", "web", "--reroll", "--yes", "--skip-health-check", "--poll-interval", "5ms", "--wait-timeout", "500ms")
	want := "Auto Scaling could not launch a node for web: " + quota
	if n := strings.Count(stdout, want); n != 1 {
		t.Fatalf("the failure is reported %d time(s), want once\nstdout:\n%s\nstderr:\n%s", n, stdout, stderr)
	}
}
