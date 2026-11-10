package fleet_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

func version(t *testing.T, s string) fleet.Version {
	t.Helper()
	v, err := fleet.ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The rollback window decides whether cluster rollback is offered: the
// newest successful in-place upgrade to the live version, less than seven
// days ago, with no rollback after it.
func TestRollbackWindowOf(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	upgrade := func(to string, ago time.Duration) fleet.Update {
		return fleet.Update{Type: "VersionUpdate", Status: "Successful", Version: version(t, to), CreatedAt: now.Add(-ago)}
	}
	rollback := func(to string, ago time.Duration) fleet.Update {
		return fleet.Update{Type: "VersionRollback", Status: "Successful", Version: version(t, to), CreatedAt: now.Add(-ago)}
	}
	failed := upgrade("1.33", day)
	failed.Status = "Failed"
	tests := []struct {
		name    string
		live    string
		updates []fleet.Update
		notRead bool // the updates
		want    *fleet.Rollback
	}{
		{
			name: "an upgrade a day ago", live: "1.33", updates: []fleet.Update{upgrade("1.33", day)},
			want: &fleet.Rollback{PreviousVersion: version(t, "1.32"), UpgradedAt: now.Add(-day), AvailableUntil: now.Add(6 * day)},
		},
		{
			name: "the newest of two upgrades to the version", live: "1.33", updates: []fleet.Update{upgrade("1.33", 10*day), upgrade("1.33", 2*day)},
			want: &fleet.Rollback{PreviousVersion: version(t, "1.32"), UpgradedAt: now.Add(-2 * day), AvailableUntil: now.Add(5 * day)},
		},
		{name: "the window ended", live: "1.33", updates: []fleet.Update{upgrade("1.33", 7*day)}},
		{name: "a rollback after the upgrade", live: "1.33", updates: []fleet.Update{upgrade("1.33", 2*day), rollback("1.33", day)}},
		{
			name: "a rollback before the upgrade", live: "1.33", updates: []fleet.Update{rollback("1.33", 3*day), upgrade("1.33", 2*day)},
			want: &fleet.Rollback{PreviousVersion: version(t, "1.32"), UpgradedAt: now.Add(-2 * day), AvailableUntil: now.Add(5 * day)},
		},
		{name: "an upgrade that failed", live: "1.33", updates: []fleet.Update{failed}},
		{name: "an upgrade to another version", live: "1.33", updates: []fleet.Update{upgrade("1.32", day)}},
		{name: "minor version 0", live: "2.0", updates: []fleet.Update{upgrade("2.0", day)}},
		{name: "no update", live: "1.33"},
		{name: "the updates were not read", live: "1.33", updates: []fleet.Update{upgrade("1.33", day)}, notRead: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := &fleet.Snapshot{Cluster: fleet.Cluster{Name: "prod", Version: version(t, tt.live)}, Updates: tt.updates}
			snap.Have.Cluster, snap.Have.Updates = fleet.Collected, fleet.Collected
			if tt.notRead {
				snap.Have.Updates = fleet.Failed
			}
			got, ok := fleet.RollbackWindowOf(snap, now)
			if ok != (tt.want != nil) {
				t.Fatalf("ok = %t with %+v, want %t", ok, got, tt.want != nil)
			}
			if tt.want != nil {
				if diff := cmp.Diff(*tt.want, got); diff != "" {
					t.Errorf("rollback (-want +got):\n%s", diff)
				}
			}
		})
	}
	t.Run("the cluster was not read", func(t *testing.T) {
		snap := &fleet.Snapshot{Cluster: fleet.Cluster{Name: "prod", Version: version(t, "1.33")}, Updates: []fleet.Update{upgrade("1.33", day)}}
		snap.Have.Cluster, snap.Have.Updates = fleet.Failed, fleet.Collected
		if _, ok := fleet.RollbackWindowOf(snap, now); ok {
			t.Error("a rollback window from a cluster that was not read")
		}
	})
}

// updatesWorld returns a fleet with the cluster prod on 1.33, upgraded a
// day ago, and two older updates. Each region has one request at a time.
func updatesWorld(t *testing.T) (*fleettest.World, *awsxtest.Server, *awsx.Clients) {
	t.Helper()
	world, srv, provider := newFleet(t, 1, east+"/prod")
	at := world.Now()
	world.Edit(func(s *fleettest.State) {
		s.Clusters[0].Updates = []*fleettest.Update{
			{ID: "u-1", Type: fleettest.TypeVersionUpdate, Params: []fleettest.UpdateParam{{Type: "Version", Value: "1.32"}}, CreatedAt: at.Add(-30 * 24 * time.Hour)},
			{ID: "u-2", Type: "LoggingUpdate", CreatedAt: at.Add(-2 * 24 * time.Hour)},
			{ID: "u-3", Type: fleettest.TypeVersionUpdate, Params: []fleettest.UpdateParam{{Type: "Version", Value: "1.33"}}, CreatedAt: at.Add(-24 * time.Hour)},
		}
	})
	return world, srv, provider.Clients(east)
}

// fetchUpdates reads the update history of prod.
func fetchUpdates(t *testing.T, world *fleettest.World, c *awsx.Clients) *fleet.Snapshot {
	t.Helper()
	snap, err := (&fleet.Fetcher{Now: world.Now}).Fetch(testContext(t), c, "prod", fleet.Need{Updates: true})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// The update history: every update is described, its version parsed, and
// the read is advisory: when it fails, the state and the error say so,
// and the snapshot has no failure.
func TestFetch_Updates(t *testing.T) {
	t.Run("read", func(t *testing.T) {
		world, _, c := updatesWorld(t)
		snap := fetchUpdates(t, world, c)
		if snap.Have.Updates != fleet.Collected || snap.UpdatesErr != nil {
			t.Fatalf("updates %s, %v; want Collected", snap.Have.Updates, snap.UpdatesErr)
		}
		var got []string
		for _, u := range snap.Updates {
			got = append(got, u.ID+" "+u.Type+" "+u.Status+" "+u.Version.String())
		}
		// EKS promises no order.
		slices.Sort(got)
		want := []string{"u-1 VersionUpdate Successful 1.32", "u-2 LoggingUpdate Successful ", "u-3 VersionUpdate Successful 1.33"}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("updates (-want +got):\n%s", diff)
		}
		window, ok := fleet.RollbackWindowOf(snap, world.Now())
		if !ok || window.PreviousVersion.String() != "1.32" {
			t.Errorf("rollback window = %+v, %t; want back to 1.32", window, ok)
		}
	})
	for _, op := range []fleettest.Operation{fleettest.EKSListUpdates, fleettest.EKSDescribeUpdate} {
		t.Run(string(op)+" is denied", func(t *testing.T) {
			world, _, c := updatesWorld(t)
			world.SetFault(op, fleettest.Fault{Deny: true})
			snap := fetchUpdates(t, world, c)
			if snap.Have.Updates != fleet.Failed || snap.UpdatesErr == nil || len(snap.Updates) != 0 {
				t.Errorf("updates %s, %v, %v; want Failed with the error", snap.Have.Updates, snap.UpdatesErr, snap.Updates)
			}
			if len(snap.Failures) != 0 {
				t.Errorf("failures %+v; the read is advisory", snap.Failures)
			}
		})
	}
	// The caller's context ends while the second update is described:
	// the read stops, and nothing more is described.
	t.Run("the context ends", func(t *testing.T) {
		world, srv, c := updatesWorld(t)
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		var mu sync.Mutex
		describes := 0
		srv.OnRequest = func(_ context.Context, r awsxtest.Request) {
			mu.Lock()
			defer mu.Unlock()
			if r.Operation == fleettest.EKSDescribeUpdate {
				if describes++; describes == 2 {
					cancel()
				}
			}
		}
		snap, _ := (&fleet.Fetcher{Now: world.Now}).Fetch(ctx, c, "prod", fleet.Need{Updates: true})
		mu.Lock()
		defer mu.Unlock()
		if describes != 2 {
			t.Errorf("%d updates described, want 2: none starts after the context ended", describes)
		}
		if snap.Have.Updates == fleet.Collected || !errors.Is(snap.UpdatesErr, context.Canceled) || len(snap.Updates) != 0 {
			t.Errorf("updates %s, %v, %v; want not Collected, with the error of the context", snap.Have.Updates, snap.UpdatesErr, snap.Updates)
		}
	})
}
