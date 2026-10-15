package fleettest_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

func TestScriptedReads(t *testing.T) {
	t.Parallel()
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{{Name: "prod", Version: "1.32", Region: "us-east-1", Nodegroups: []*fleettest.Nodegroup{{Name: "ng-a", Version: "1.32"}}}}
	})
	w.ScriptStatus("us-east-1", "prod", "ng-a", fleettest.StatusUpdating)
	w.ScriptStatus("us-east-1", "prod", "ng-a", fleettest.StatusDegraded)
	w.HideUpdates("us-east-1", "prod", 2)

	var statuses []fleettest.ResourceStatus
	var hidden []bool
	w.ViewReads(func(s *fleettest.State, r *fleettest.Reads) {
		c := s.Cluster("us-east-1", "prod")
		for range 3 {
			statuses = append(statuses, r.Status(c, "ng-a", c.Nodegroup("ng-a").Status))
			hidden = append(hidden, r.HidesUpdates(c))
		}
		// Another resource has no script.
		statuses = append(statuses, r.Status(c, "", c.Status))
	})
	before := w.Generation()
	at := w.ViewAt(func(*fleettest.State) {})
	want := []fleettest.ResourceStatus{fleettest.StatusUpdating, fleettest.StatusDegraded, fleettest.StatusActive, fleettest.StatusActive}
	if diff := cmp.Diff(want, statuses); diff != "" {
		t.Errorf("statuses read (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]bool{true, true, false}, hidden); diff != "" {
		t.Errorf("lists that hide the updates in progress (-want +got):\n%s", diff)
	}
	if at != before {
		t.Errorf("ViewAt returned generation %d, and the world was at %d", at, before)
	}
}
