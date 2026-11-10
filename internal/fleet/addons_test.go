package fleet_test

import (
	"context"
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// The context ends after the first add-on was described and before the
// lookups of the newest versions start: the region has one request at a
// time, and the describe calls start first. The add-on that was read has
// no newest version, and says why: the lookup was not sent. Nothing is
// sent after the context ended.
func TestFetch_NewestVersionNotSent(t *testing.T) {
	world, srv, provider := newFleet(t, 1, east+"/prod")
	world.Edit(func(s *fleettest.State) {
		s.Clusters[0].Addons = []*fleettest.Addon{{Name: "coredns", Version: "v1.12.0-eksbuild.1"}, {Name: "vpc-cni", Version: "v1.20.0-eksbuild.1"}}
	})
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	srv.OnRequest = func(_ context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeAddon && r.Input["addonName"] == "vpc-cni" {
			cancel()
		}
	}
	snap, _ := (&fleet.Fetcher{Now: world.Now}).Fetch(ctx, provider.Clients(east), "prod", fleet.Need{LatestAddons: true})
	if len(snap.Addons) != 2 {
		t.Fatalf("add-ons = %+v, want coredns and vpc-cni", snap.Addons)
	}
	if a := snap.Addons[0]; a.Name != "coredns" || a.State != fleet.Collected || a.LatestState != fleet.NotAttempted || a.Latest != "" {
		t.Errorf("coredns = %s, latest %s %q; want Collected with the newest version NotAttempted", a.State, a.LatestState, a.Latest)
	}
	found := false
	for _, f := range snap.Failures {
		if f.Kind == apidoc.FailureAddon && f.Name == "coredns" {
			found = true
			if f.Reason != apidoc.ReasonNotAttempted || f.Cluster != "prod" || f.Region != east {
				t.Errorf("the failure of coredns = %+v; want NotAttempted in prod, %s", f, east)
			}
		}
	}
	if !found {
		t.Errorf("no failure says that the newest version of coredns was not read: %+v", snap.Failures)
	}
	if snap.Have.Addons != fleet.Partial {
		t.Errorf("Have.Addons = %s, want Partial", snap.Have.Addons)
	}
	for _, r := range srv.Requests() {
		if r.Operation == fleettest.EKSDescribeAddonVersions {
			t.Errorf("the newest version was looked up after the context ended: %+v", r)
		}
	}
}
