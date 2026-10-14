package backend_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// firstSweep runs the first sweep of the clusters in us-east-1, and
// returns its state.
func firstSweep(t *testing.T, clusters ...*fleettest.Cluster) state.State {
	t.Helper()
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		seed(s)
		s.Clusters = append(s.Clusters, clusters...)
	})
	r := newRig(t, world, fleet.Regions{Names: []string{east}}, nil)
	return r.wait(t, 0, swept(1))
}

// clusterLines returns the lines of the feed that name a cluster with a
// status, such as a failure. A change that runs has lines of its own.
func clusterLines(st state.State, status view.Status) []string {
	var out []string
	for _, l := range st.Feed {
		if l.Cluster.Name != "" && l.Status == status {
			out = append(out, l.Cluster.Name+" "+l.Text)
		}
	}
	return out
}

func statusOf(t *testing.T, st state.State, name string) view.Status {
	t.Helper()
	for _, c := range st.Clusters {
		if c.Name == name {
			return c.Status
		}
	}
	t.Fatalf("no cluster %s in %+v", name, st.Clusters)
	return view.Unknown
}

// The first sweep of a big fleet writes one summary, and a line only for
// each cluster that failed: not one line per cluster, and none for a
// cluster that only needs attention.
func TestFirstSweepOfABigFleetSummarizes(t *testing.T) {
	var clusters []*fleettest.Cluster
	for i := range 12 {
		clusters = append(clusters, cluster(fmt.Sprintf("ok-%02d", i), east, "1.34"))
	}
	behind := cluster("behind", east, "1.34")
	behind.Nodegroups[0].Version = "1.33"
	broken := cluster("broken", east, "1.34")
	broken.Nodegroups[0].Status = fleettest.StatusDegraded
	st := firstSweep(t, append(clusters, behind, broken)...)

	if got, want := statusOf(t, st, "behind"), view.Warn; got != want {
		t.Errorf("behind = %v, want %v", got, want)
	}
	if got, want := statusOf(t, st, "broken"), view.Fail; got != want {
		t.Errorf("broken = %v, want %v", got, want)
	}
	feed := feedText(st)
	if !strings.Contains(feed, "14 clusters: 2 need attention, 0 changing") {
		t.Errorf("feed lacks the summary:\n%s", feed)
	}
	if lines := clusterLines(st, view.Fail); len(lines) != 1 || !strings.HasPrefix(lines[0], "broken ") || !strings.Contains(lines[0], "nodegroup ng DEGRADED") {
		t.Errorf("the failed lines about clusters = %q, want one, for broken, that says why", lines)
	}
}

// A cluster that is only changing is counted as changing, not as one that
// needs attention, and has no line of its own.
func TestFirstSweepKeepsChangingClusters(t *testing.T) {
	rolling := cluster("rolling", east, "1.34")
	rolling.Nodegroups[0].Status = fleettest.StatusUpdating
	st := firstSweep(t, cluster("ok", east, "1.34"), rolling)
	if got, want := statusOf(t, st, "rolling"), view.Progress; got != want {
		t.Errorf("rolling = %v, want %v", got, want)
	}
	if feed := feedText(st); !strings.Contains(feed, "2 clusters: 0 need attention, 1 changing") {
		t.Errorf("feed lacks the summary:\n%s", feed)
	}
	for _, status := range []view.Status{view.Fail, view.Warn} {
		if lines := clusterLines(st, status); len(lines) != 0 {
			t.Errorf("the %v lines about clusters = %q, want none", status, lines)
		}
	}
}

// A changing cluster that also has an error reports the error: the row
// has failed, and the feed says why.
func TestFirstSweepShowsTheErrorUnderAChange(t *testing.T) {
	// 1.31 is in extended support on the clock of the test.
	old := cluster("old", east, "1.31")
	old.Nodegroups[0].Status = fleettest.StatusUpdating
	st := firstSweep(t, cluster("ok", east, "1.34"), old)
	if got, want := statusOf(t, st, "old"), view.Fail; got != want {
		t.Errorf("old = %v, want %v", got, want)
	}
	if feed := feedText(st); !strings.Contains(feed, "2 clusters: 1 need attention, 0 changing") {
		t.Errorf("feed lacks the summary:\n%s", feed)
	}
	if lines := clusterLines(st, view.Fail); len(lines) != 1 || !strings.Contains(lines[0], "extended support") {
		t.Errorf("the failed lines about clusters = %q, want one for old that names its support", lines)
	}
}

// A nodegroup whose AMI could not be read is an unknown of its row, which
// is then not healthy: it does not show as current.
func TestSweepRowWithAnUnreadAMI(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		seed(s)
		c := cluster("prod", east, "1.34")
		c.Nodegroups[0].AutoScalingGroups = []string{"asg-ng"}
		s.Clusters = append(s.Clusters, c, cluster("dev", east, "1.34"))
		s.AutoScalingGroups = []fleettest.AutoScalingGroup{{Name: "asg-ng", InstanceIDs: []string{"i-ng"}}}
		s.Instances = []fleettest.Instance{{ID: "i-ng", ImageID: "ami-1"}}
	})
	world.SetFault(fleettest.SSMGetParameter, fleettest.Fault{Deny: true})
	r := newRig(t, world, fleet.Regions{Names: []string{east}}, nil)
	st := r.wait(t, 0, swept(1))
	for _, c := range st.Clusters {
		if c.Name != "prod" {
			continue
		}
		if c.Stale.Unknown != 1 || c.Status == view.Healthy || !c.Incomplete {
			t.Errorf("prod: unknown %d, status %v, incomplete %t; want 1 unknown, not healthy, and incomplete", c.Stale.Unknown, c.Status, c.Incomplete)
		}
		return
	}
	t.Fatalf("no prod in %+v", st.Clusters)
}
