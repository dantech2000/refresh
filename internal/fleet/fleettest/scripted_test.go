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

// TestScriptedReads_ClusterInEveryRegion checks the short form of a
// one-region test: a cluster with no Region answers in every region, and a
// script for it is read there.
func TestScriptedReads_ClusterInEveryRegion(t *testing.T) {
	t.Parallel()
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{{Name: "prod", Version: "1.32", Nodegroups: []*fleettest.Nodegroup{{Name: "ng-a", Version: "1.32"}}}}
	})
	w.ScriptStatus("us-east-1", "prod", "ng-a", fleettest.StatusUpdating)
	w.HideUpdates("us-east-1", "prod", 1)

	var status fleettest.ResourceStatus
	var hidden bool
	w.ViewReads(func(s *fleettest.State, r *fleettest.Reads) {
		c := s.Cluster("us-east-1", "prod")
		status = r.Status(c, "ng-a", c.Nodegroup("ng-a").Status)
		hidden = r.HidesUpdates(c)
	})
	if status != fleettest.StatusUpdating {
		t.Errorf("status read = %s, want the scripted UPDATING", status)
	}
	if !hidden {
		t.Error("ListUpdates did not hide the updates in progress")
	}
	if left := w.Unconsumed(); len(left) != 0 {
		t.Errorf("Unconsumed() = %q, want nothing", left)
	}
}

func TestUnconsumed(t *testing.T) {
	t.Parallel()
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{{Name: "prod", Version: "1.32", Region: "us-east-1"}}
	})
	w.ScriptStatus("us-east-1", "prod", "", fleettest.StatusUpdating, fleettest.StatusActive)
	w.HideUpdates("us-east-1", "prod", 2)
	w.SetFault(fleettest.EKSListClusters, fleettest.Fault{Throttle: 2, LoseResponse: 1})
	w.ViewReads(func(s *fleettest.State, r *fleettest.Reads) {
		c := s.Cluster("us-east-1", "prod")
		r.Status(c, "", c.Status)
		r.HidesUpdates(c)
	})
	if _, err := w.Enter(t.Context(), fleettest.Call{Operation: fleettest.EKSListClusters}); err == nil {
		t.Fatal("the first request was not throttled")
	}
	want := []string{
		"Fault eks:ListClusters: 1 lost responses not used",
		"HideUpdates us-east-1/prod: 1 ListUpdates answers not hidden",
		"ScriptStatus us-east-1/prod/: 1 statuses not read",
	}
	if diff := cmp.Diff(want, w.Unconsumed()); diff != "" {
		t.Errorf("Unconsumed (-want +got):\n%s", diff)
	}
}

func TestScriptStatus_NoClusterPanics(t *testing.T) {
	t.Parallel()
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{{Name: "prod", Version: "1.32", Region: "us-east-1"}}
	})
	for name, call := range map[string]func(){
		"ScriptStatus": func() { w.ScriptStatus("eu-west-1", "prod", "", fleettest.StatusUpdating) },
		"HideUpdates":  func() { w.HideUpdates("us-east-1", "other", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Errorf("%s for a cluster that does not answer did not panic", name)
				}
			}()
			call()
		})
	}
}
