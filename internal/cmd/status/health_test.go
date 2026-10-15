package status_test

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// The HEALTH column counts the issues of the control plane, of the
// nodegroups, and of the add-ons, and any issue needs attention: a
// DEGRADED add-on is not "ok".
func TestStatus_HealthCountsEveryPart(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(c *fleettest.Cluster)
		health string
		exit   int
	}{
		{name: "healthy", edit: func(*fleettest.Cluster) {}, health: "ok", exit: cmdutil.ExitOK},
		{
			name:   "a DEGRADED add-on with no issue listed",
			edit:   func(c *fleettest.Cluster) { c.Addons[0].Status = fleettest.StatusDegraded },
			health: "1 issue", exit: cmdutil.ExitNeedsAttention,
		},
		{
			name: "a DEGRADED add-on with its issues",
			edit: func(c *fleettest.Cluster) {
				c.Addons[0].Status = fleettest.StatusDegraded
				c.Addons[0].HealthIssues = []fleettest.Issue{{Code: "InsufficientNumberOfReplicas", Message: "0 of 2 replicas are ready"}}
			},
			health: "1 issue", exit: cmdutil.ExitNeedsAttention,
		},
		{
			name: "a nodegroup issue",
			edit: func(c *fleettest.Cluster) {
				c.Nodegroups[0].HealthIssues = []fleettest.Issue{{Code: "AsgInstanceLaunchFailures", Message: "no capacity"}}
			},
			health: "1 issue", exit: cmdutil.ExitNeedsAttention,
		},
		{
			name: "every part",
			edit: func(c *fleettest.Cluster) {
				c.HealthIssues = []fleettest.Issue{{Code: "Ec2SubnetInvalidConfiguration", Message: "a subnet is gone"}}
				c.Nodegroups[0].HealthIssues = []fleettest.Issue{{Code: "AsgInstanceLaunchFailures", Message: "no capacity"}}
				c.Addons[0].Status = fleettest.StatusDegraded
			},
			health: "3 issues", exit: cmdutil.ExitNeedsAttention,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			world := fleettest.New()
			c := current("prod", east)
			tt.edit(c)
			world.Edit(func(s *fleettest.State) {
				seed(s)
				s.Clusters = []*fleettest.Cluster{c}
			})
			e := cmdtest.NewFake(t, world, east)
			err := e.Run(cmdutiltest.Context(t), "status", "-o", "plain")
			if got := cmdutil.ExitCode(err); got != tt.exit {
				t.Errorf("exit code = %d (%v), want %d", got, err, tt.exit)
			}
			lines := strings.Split(strings.TrimSpace(e.Out.String()), "\n")
			if len(lines) != 2 {
				t.Fatalf("stdout = %q, want a header and one row", e.Out.String())
			}
			header, row := strings.Split(lines[0], "\t"), strings.Split(lines[1], "\t")
			for i, name := range header {
				if name == "HEALTH" && row[i] != tt.health {
					t.Errorf("HEALTH = %q, want %q", row[i], tt.health)
				}
			}
		})
	}
}
