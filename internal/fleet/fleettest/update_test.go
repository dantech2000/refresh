package fleettest_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// token is a client request token of a length EKS accepts.
const token = "0123456789abcdef0123456789abcdef-1"

func id(n int) string {
	return "00000000-0000-4000-8000-00000000000" + string(rune('0'+n))
}

// status returns the status of an update of prod.
func status(w *fleettest.World, updateID string) fleettest.UpdateStatus {
	var out fleettest.UpdateStatus
	w.View(func(s *fleettest.State) { out = s.Cluster(region, "prod").Update(updateID).Status })
	return out
}

func TestUpdateScripts(t *testing.T) {
	tests := []struct {
		name   string
		script fleettest.UpdateScript
		// advance is how far the clock moves before the checks.
		advance     time.Duration
		wantStatus  fleettest.UpdateStatus
		wantVersion string
		wantNG      fleettest.ResourceStatus
		wantErrors  []fleettest.UpdateError
	}{
		{name: "the zero script succeeds at once", wantStatus: fleettest.UpdateSuccessful, wantVersion: "1.32", wantNG: "ACTIVE"},
		{
			name: "in progress before its duration", script: fleettest.UpdateScript{After: time.Hour}, advance: time.Hour - time.Second,
			wantStatus: fleettest.UpdateInProgress, wantVersion: "1.31", wantNG: "UPDATING",
		},
		{
			name: "successful at its duration", script: fleettest.UpdateScript{After: time.Hour}, advance: time.Hour,
			wantStatus: fleettest.UpdateSuccessful, wantVersion: "1.32", wantNG: "ACTIVE",
		},
		{
			name: "failed leaves the nodegroup as it was", script: fleettest.UpdateScript{After: time.Hour, Result: fleettest.UpdateFailed}, advance: time.Hour,
			wantStatus: fleettest.UpdateFailed, wantVersion: "1.31", wantNG: "ACTIVE",
			wantErrors: []fleettest.UpdateError{{Code: "Unknown", Message: "the update did not complete"}},
		},
		{
			name:       "cancelled with scripted errors",
			script:     fleettest.UpdateScript{Result: fleettest.UpdateCancelled, Errors: []fleettest.UpdateError{{Code: "PodEvictionFailure", Message: "pdb"}}},
			wantStatus: fleettest.UpdateCancelled, wantVersion: "1.31", wantNG: "ACTIVE",
			wantErrors: []fleettest.UpdateError{{Code: "PodEvictionFailure", Message: "pdb"}},
		},
		{
			name: "never", script: fleettest.UpdateScript{Never: true}, advance: 1000 * time.Hour,
			wantStatus: fleettest.UpdateInProgress, wantVersion: "1.31", wantNG: "UPDATING",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld()
			w.Edit(func(s *fleettest.State) { s.DefaultUpdate = tt.script })
			started, err := w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: new("1.32")})
			if err != nil {
				t.Fatal(err)
			}
			want := fleettest.Update{
				ID: id(1), Type: fleettest.TypeVersionUpdate, Status: fleettest.UpdateInProgress, Nodegroup: "ng-a", CreatedAt: w.Now(),
				Params: []fleettest.UpdateParam{{Type: "Version", Value: "1.32"}, {Type: "ReleaseVersion", Value: "1.32.0-20260101"}},
			}
			if diff := cmp.Diff(want, started, cmpopts.IgnoreUnexported(fleettest.Update{})); diff != "" {
				t.Errorf("started update (-want +got):\n%s", diff)
			}

			w.Advance(tt.advance)
			w.View(func(s *fleettest.State) {
				c := s.Cluster(region, "prod")
				u, ng := c.Update(id(1)), c.Nodegroup("ng-a")
				if u.Status != tt.wantStatus || ng.Version != tt.wantVersion || ng.Status != tt.wantNG {
					t.Errorf("update %s, nodegroup %s %s; want %s, %s %s", u.Status, ng.Version, ng.Status, tt.wantStatus, tt.wantVersion, tt.wantNG)
				}
				if diff := cmp.Diff(tt.wantErrors, u.Errors); diff != "" {
					t.Errorf("update errors (-want +got):\n%s", diff)
				}
			})
		})
	}
}

func TestUpdateScriptsAreUsedInOrder(t *testing.T) {
	w := newWorld()
	w.Edit(func(s *fleettest.State) {
		s.Cluster(region, "prod").Addon("coredns").UpdateScripts = []fleettest.UpdateScript{{Result: fleettest.UpdateFailed}}
	})
	for i, want := range []fleettest.UpdateStatus{fleettest.UpdateFailed, fleettest.UpdateSuccessful} {
		u, err := w.UpdateAddon(region, "prod", "coredns", fleettest.AddonRequest{Version: new("v2")})
		if err != nil {
			t.Fatal(err)
		}
		if got := status(w, u.ID); got != want {
			t.Errorf("update %d = %s, want %s", i+1, got, want)
		}
	}
	w.View(func(s *fleettest.State) {
		if got := s.Cluster(region, "prod").Addon("coredns").Version; got != "v2" {
			t.Errorf("add-on version = %q, want v2", got)
		}
	})
}

func TestUpdatesFinishInDueOrder(t *testing.T) {
	w := newWorld()
	w.Edit(func(s *fleettest.State) {
		c := s.Cluster(region, "prod")
		c.UpdateScripts = []fleettest.UpdateScript{{After: 2 * time.Hour}}
		c.Addons[0].UpdateScripts = []fleettest.UpdateScript{{After: time.Hour}}
	})
	// The add-on first: once the control plane updates, EKS refuses it.
	addon, err := w.UpdateAddon(region, "prod", "coredns", fleettest.AddonRequest{Version: new("v2")})
	if err != nil {
		t.Fatal(err)
	}
	control, err := w.UpdateClusterVersion(region, "prod", fleettest.ClusterVersionRequest{Version: new("1.33")})
	if err != nil {
		t.Fatal(err)
	}
	if addon.ID != id(1) || control.ID != id(2) {
		t.Errorf("update IDs = %s, %s; want %s, %s", addon.ID, control.ID, id(1), id(2))
	}

	w.Advance(time.Hour)
	if c, a := status(w, control.ID), status(w, addon.ID); c != fleettest.UpdateInProgress || a != fleettest.UpdateSuccessful {
		t.Errorf("after one hour: control plane %s, add-on %s; want InProgress, Successful", c, a)
	}
	w.Advance(time.Hour)
	w.View(func(s *fleettest.State) {
		c := s.Cluster(region, "prod")
		if c.Version != "1.33" || c.Status != "ACTIVE" {
			t.Errorf("after two hours: cluster %s %s, want 1.33 ACTIVE", c.Version, c.Status)
		}
	})
}

func TestUpdateRequests(t *testing.T) {
	three := int32(3)
	tests := []struct {
		name  string
		start func(w *fleettest.World) (fleettest.Update, error)
		// wantErr is a pointer to the error type As must find, or nil.
		wantErr  any
		wantType fleettest.UpdateType
		// state reads what the update changed, and wantState is its value
		// once the update is done.
		state     func(c *fleettest.Cluster) string
		wantState string
	}{
		{
			name: "control plane upgrade",
			start: func(w *fleettest.World) (fleettest.Update, error) {
				return w.UpdateClusterVersion(region, "prod", fleettest.ClusterVersionRequest{Version: new("1.33"), Token: token})
			},
			wantType:  fleettest.TypeVersionUpdate,
			state:     func(c *fleettest.Cluster) string { return c.Version },
			wantState: "1.33",
		},
		{
			name: "control plane rollback",
			start: func(w *fleettest.World) (fleettest.Update, error) {
				return w.UpdateClusterVersion(region, "prod", fleettest.ClusterVersionRequest{Version: new("1.31")})
			},
			wantType: fleettest.TypeVersionRollback,
		},
		{
			name: "client request token of 32 characters",
			start: func(w *fleettest.World) (fleettest.Update, error) {
				return w.UpdateClusterVersion(region, "prod", fleettest.ClusterVersionRequest{Version: new("1.33"), Token: token[:32]})
			},
			wantErr: new(*fleettest.InvalidError),
		},
		{
			name: "scaling keeps the sizes the request leaves out",
			start: func(w *fleettest.World) (fleettest.Update, error) {
				return w.UpdateNodegroupConfig(region, "prod", "ng-a", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Desired: &three}})
			},
			wantType: fleettest.TypeConfigUpdate,
			state: func(c *fleettest.Cluster) string {
				ng := c.Nodegroup("ng-a")
				return fmt.Sprintf("%d/%d/%d", ng.Desired, ng.Min, ng.Max)
			},
			wantState: "3/1/3",
		},
		{
			name: "roll with no version keeps the nodegroup's",
			start: func(w *fleettest.World) (fleettest.Update, error) {
				return w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{ReleaseVersion: new("1.31.9-20260202")})
			},
			wantType: fleettest.TypeVersionUpdate,
			state: func(c *fleettest.Cluster) string {
				ng := c.Nodegroup("ng-a")
				return ng.Version + " " + ng.ReleaseVersion
			},
			wantState: "1.31 1.31.9-20260202",
		},
		{
			name: "unknown cluster",
			start: func(w *fleettest.World) (fleettest.Update, error) {
				return w.UpdateAddon(region, "nope", "coredns", fleettest.AddonRequest{})
			},
			wantErr: new(*fleettest.NotFoundError),
		},
		{
			name: "unknown nodegroup",
			start: func(w *fleettest.World) (fleettest.Update, error) {
				return w.UpdateNodegroupConfig(region, "prod", "nope", fleettest.NodegroupConfigRequest{})
			},
			wantErr: new(*fleettest.NotFoundError),
		},
		{
			name: "unknown add-on",
			start: func(w *fleettest.World) (fleettest.Update, error) {
				return w.UpdateAddon(region, "prod", "nope", fleettest.AddonRequest{})
			},
			wantErr: new(*fleettest.NotFoundError),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld()
			u, err := tt.start(w)
			if tt.wantErr != nil {
				if !errors.As(err, tt.wantErr) {
					t.Fatalf("err = %v, want %T", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if u.Type != tt.wantType {
				t.Errorf("update type = %s, want %s", u.Type, tt.wantType)
			}
			if tt.state != nil {
				var got string
				w.View(func(s *fleettest.State) { got = tt.state(s.Cluster(region, "prod")) })
				if got != tt.wantState {
					t.Errorf("after the update: %s, want %s", got, tt.wantState)
				}
			}
		})
	}
}

func TestUpdateTokenAndBusy(t *testing.T) {
	w := newWorld()
	w.Edit(func(s *fleettest.State) { s.DefaultUpdate = fleettest.UpdateScript{After: time.Hour} })
	roll := func(tok string) (fleettest.Update, error) {
		return w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: new("1.32"), Token: tok})
	}

	first, err := roll(token)
	if err != nil {
		t.Fatal(err)
	}
	again, err := roll(token)
	if err != nil || again.ID != first.ID {
		t.Errorf("the same token returned %q, %v; want the first update %q", again.ID, err, first.ID)
	}
	var inUse *fleettest.InUseError
	if _, err := roll(token + "x"); !errors.As(err, &inUse) || inUse.UpdateID != first.ID {
		t.Errorf("a second roll returned %v, want an InUseError for %s", err, first.ID)
	}
	// Another resource of the cluster is free.
	if _, err := w.UpdateAddon(region, "prod", "coredns", fleettest.AddonRequest{Version: new("v2"), Token: token}); err != nil {
		t.Errorf("an add-on update during the roll: %v", err)
	}

	w.Advance(time.Hour)
	// A late retry of the first call still reads as the call that started it.
	if late, err := roll(token); err != nil || late.ID != first.ID || late.Status != fleettest.UpdateInProgress {
		t.Errorf("a late retry returned %+v, %v", late, err)
	}
	if next, err := roll(token + "x"); err != nil || next.ID == first.ID {
		t.Errorf("a roll after the first ended returned %q, %v; want a new update", next.ID, err)
	}
}

// TestUpdateTokenScope checks that a token names one call: the same
// operation on the same resource with the same parameters.
func TestUpdateTokenScope(t *testing.T) {
	w := newWorld()
	w.Edit(func(s *fleettest.State) {
		c := s.Cluster(region, "prod")
		c.Nodegroups = append(c.Nodegroups, &fleettest.Nodegroup{Name: "ng-b", Version: "1.31"})
	})
	three, four := int32(3), int32(4)

	version, err := w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: new("1.32"), Token: token})
	if err != nil {
		t.Fatal(err)
	}
	// The same token on another operation of the same nodegroup, and on the
	// same operation of another nodegroup, starts new updates.
	config, err := w.UpdateNodegroupConfig(region, "prod", "ng-a", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Desired: &three}, Token: token})
	if err != nil || config.ID == version.ID || config.Type != fleettest.TypeConfigUpdate {
		t.Errorf("a config update with the token of a version update returned %+v, %v", config, err)
	}
	other, err := w.UpdateNodegroupVersion(region, "prod", "ng-b", fleettest.NodegroupVersionRequest{Version: new("1.32"), Token: token})
	if err != nil || other.ID == version.ID {
		t.Errorf("a roll of ng-b with the token of ng-a's roll returned %+v, %v", other, err)
	}

	var invalid *fleettest.InvalidError
	if _, err := w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: new("1.33"), Token: token}); !errors.As(err, &invalid) {
		t.Errorf("the token with another version returned %v, want an InvalidError", err)
	}
	if _, err := w.UpdateNodegroupConfig(region, "prod", "ng-a", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Desired: &four}, Token: token}); !errors.As(err, &invalid) {
		t.Errorf("the token with another size returned %v, want an InvalidError", err)
	}
	if again, err := w.UpdateNodegroupConfig(region, "prod", "ng-a", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Desired: &three}, Token: token}); err != nil || again.ID != config.ID {
		t.Errorf("the same config call again returned %+v, %v; want update %s", again, err, config.ID)
	}
	w.View(func(s *fleettest.State) {
		if n := len(s.Cluster(region, "prod").Updates); n != 3 {
			t.Errorf("the world has %d updates, want 3", n)
		}
	})
}

// TestUpdateReplayIgnoresLaterState replays a request that left its fields
// to the defaults, after the resource those defaults came from changed.
func TestUpdateReplayIgnoresLaterState(t *testing.T) {
	w := newWorld()
	values := "{}"
	tests := []struct {
		name string
		// first is the call to replay. change moves the resource on.
		first, change func() (fleettest.Update, error)
		// other is first with one field more, under the same token.
		other func() (fleettest.Update, error)
	}{
		{
			name: "nodegroup roll with no version",
			first: func() (fleettest.Update, error) {
				return w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{Token: token})
			},
			change: func() (fleettest.Update, error) {
				return w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: new("1.32")})
			},
			other: func() (fleettest.Update, error) {
				return w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: new("1.31"), Token: token})
			},
		},
		{
			name: "add-on update with no version",
			first: func() (fleettest.Update, error) {
				return w.UpdateAddon(region, "prod", "coredns", fleettest.AddonRequest{Token: token})
			},
			change: func() (fleettest.Update, error) {
				return w.UpdateAddon(region, "prod", "coredns", fleettest.AddonRequest{Version: new("v2")})
			},
			other: func() (fleettest.Update, error) {
				return w.UpdateAddon(region, "prod", "coredns", fleettest.AddonRequest{ConfigurationValues: &values, Token: token})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first, err := tt.first()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tt.change(); err != nil {
				t.Fatal(err)
			}
			again, err := tt.first()
			if err != nil {
				t.Fatalf("the replay: %v", err)
			}
			if diff := cmp.Diff(first, again, cmpopts.IgnoreUnexported(fleettest.Update{})); diff != "" {
				t.Errorf("the replay differs from the first answer (-first +replay):\n%s", diff)
			}
			// The field the first call left out now equals the default it
			// got then. Sending it is still another request.
			var invalid *fleettest.InvalidError
			if _, err := tt.other(); !errors.As(err, &invalid) {
				t.Errorf("the token with one more field returned %v, want an InvalidError", err)
			}
		})
	}
}

// TestUpdateTokenKeepsPresence checks that a field that was left out and
// one that was sent empty are different requests under one token, for every
// optional field of the four operations.
func TestUpdateTokenKeepsPresence(t *testing.T) {
	no := false
	zero, two := int32(0), int32(2)
	tests := []struct {
		name string
		// call sends the request, with the field left out or sent empty.
		call func(w *fleettest.World, empty bool) error
	}{
		{"nodegroup version", func(w *fleettest.World, empty bool) error {
			req := fleettest.NodegroupVersionRequest{Token: token}
			if empty {
				req.Version = new("")
			}
			_, err := w.UpdateNodegroupVersion(region, "prod", "ng-a", req)
			return err
		}},
		{"nodegroup release version", func(w *fleettest.World, empty bool) error {
			req := fleettest.NodegroupVersionRequest{Token: token}
			if empty {
				req.ReleaseVersion = new("")
			}
			_, err := w.UpdateNodegroupVersion(region, "prod", "ng-a", req)
			return err
		}},
		{"nodegroup force", func(w *fleettest.World, empty bool) error {
			req := fleettest.NodegroupVersionRequest{Token: token}
			if empty {
				req.Force = &no
			}
			_, err := w.UpdateNodegroupVersion(region, "prod", "ng-a", req)
			return err
		}},
		{"scaling config", func(w *fleettest.World, empty bool) error {
			req := fleettest.NodegroupConfigRequest{Token: token}
			if empty {
				req.Scaling = &fleettest.ScalingRequest{}
			}
			_, err := w.UpdateNodegroupConfig(region, "prod", "ng-a", req)
			return err
		}},
		{"desired size", func(w *fleettest.World, empty bool) error {
			req := fleettest.NodegroupConfigRequest{Token: token, Scaling: &fleettest.ScalingRequest{}}
			if empty {
				req.Scaling.Desired = &two
			}
			_, err := w.UpdateNodegroupConfig(region, "prod", "ng-a", req)
			return err
		}},
		{"add-on version", func(w *fleettest.World, empty bool) error {
			req := fleettest.AddonRequest{Token: token}
			if empty {
				req.Version = new("")
			}
			_, err := w.UpdateAddon(region, "prod", "coredns", req)
			return err
		}},
		{"add-on configuration", func(w *fleettest.World, empty bool) error {
			req := fleettest.AddonRequest{Token: token}
			if empty {
				req.ConfigurationValues = new("")
			}
			_, err := w.UpdateAddon(region, "prod", "coredns", req)
			return err
		}},
		{"cluster force", func(w *fleettest.World, empty bool) error {
			req := fleettest.ClusterVersionRequest{Version: new("1.33"), Token: token}
			if empty {
				req.Force = &no
			}
			_, err := w.UpdateClusterVersion(region, "prod", req)
			return err
		}},
		{"rollback config", func(w *fleettest.World, empty bool) error {
			req := fleettest.ClusterVersionRequest{Version: new("1.33"), Token: token}
			if empty {
				req.Rollback = &fleettest.RollbackRequest{}
			}
			_, err := w.UpdateClusterVersion(region, "prod", req)
			return err
		}},
		{"rollback timeout", func(w *fleettest.World, empty bool) error {
			req := fleettest.ClusterVersionRequest{Version: new("1.33"), Token: token, Rollback: &fleettest.RollbackRequest{}}
			if empty {
				req.Rollback.TimeoutMinutes = &zero
			}
			_, err := w.UpdateClusterVersion(region, "prod", req)
			return err
		}},
	}
	for _, tt := range tests {
		for _, firstEmpty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/first sent empty=%t", tt.name, firstEmpty), func(t *testing.T) {
				w := newWorld()
				if err := tt.call(w, firstEmpty); err != nil {
					t.Fatal(err)
				}
				if err := tt.call(w, firstEmpty); err != nil {
					t.Errorf("the same request again: %v", err)
				}
				var invalid *fleettest.InvalidError
				if err := tt.call(w, !firstEmpty); !errors.As(err, &invalid) {
					t.Errorf("the other form under the same token returned %v, want an InvalidError", err)
				}
			})
		}
	}

	var invalid *fleettest.InvalidError
	if _, err := newWorld().UpdateClusterVersion(region, "prod", fleettest.ClusterVersionRequest{}); !errors.As(err, &invalid) {
		t.Errorf("a cluster update with no version returned %v, want an InvalidError", err)
	}
}

// TestUpdateSharesNoMemory changes everything a caller still holds after it
// starts an update, and after the update ends. The world must not see it.
func TestUpdateSharesNoMemory(t *testing.T) {
	w := newWorld()
	script := fleettest.UpdateScript{
		After: time.Hour, Result: fleettest.UpdateFailed,
		Errors:         []fleettest.UpdateError{{Code: "NodeCreationFailure", Message: "m", ResourceIDs: []string{"i-1"}}},
		ResourceIssues: []fleettest.Issue{{Code: "NodeCreationFailure", ResourceIDs: []string{"i-1"}}},
	}
	w.Edit(func(s *fleettest.State) {
		s.DefaultUpdate = fleettest.UpdateScript{After: time.Hour}
		s.Cluster(region, "prod").Addon("coredns").UpdateScripts = []fleettest.UpdateScript{script}
	})

	desired := int32(3)
	scale, err := w.UpdateNodegroupConfig(region, "prod", "ng-a", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Desired: &desired}})
	if err != nil {
		t.Fatal(err)
	}
	addon, err := w.UpdateAddon(region, "prod", "coredns", fleettest.AddonRequest{Version: new("v2")})
	if err != nil {
		t.Fatal(err)
	}
	desired = 1
	scale.Params[0].Value = "1"
	script.Errors[0].ResourceIDs[0] = "changed"
	script.Errors[0].Code = "changed"
	script.ResourceIssues[0].ResourceIDs[0] = "changed"
	w.Advance(time.Hour)

	w.View(func(s *fleettest.State) {
		c := s.Cluster(region, "prod")
		if ng := c.Nodegroup("ng-a"); ng.Desired != 3 {
			t.Errorf("desired size = %d, want the 3 the request held when it was sent", ng.Desired)
		}
		if got := c.Update(scale.ID).Params[0].Value; got != "3" {
			t.Errorf("update parameter = %q, want 3", got)
		}
		wantErrors := []fleettest.UpdateError{{Code: "NodeCreationFailure", Message: "m", ResourceIDs: []string{"i-1"}}}
		if diff := cmp.Diff(wantErrors, c.Update(addon.ID).Errors); diff != "" {
			t.Errorf("update errors (-want +got):\n%s", diff)
		}
		wantIssues := []fleettest.Issue{{Code: "NodeCreationFailure", ResourceIDs: []string{"i-1"}}}
		if diff := cmp.Diff(wantIssues, c.Addon("coredns").HealthIssues); diff != "" {
			t.Errorf("add-on issues (-want +got):\n%s", diff)
		}
	})
}

// TestUpdateScriptSetsResource checks that a script decides the status and
// health the resource is left with, apart from the update's result.
func TestUpdateScriptSetsResource(t *testing.T) {
	issues := []fleettest.Issue{{Code: "NodeCreationFailure", Message: "nodes did not join"}}
	tests := []struct {
		name        string
		script      fleettest.UpdateScript
		wantStatus  fleettest.ResourceStatus
		wantVersion string
		wantIssues  []fleettest.Issue
		wantChanged bool
	}{
		{"success", fleettest.UpdateScript{}, fleettest.StatusActive, "v2", nil, true},
		{"failure restores the status", fleettest.UpdateScript{Result: fleettest.UpdateFailed}, fleettest.StatusCreateFailed, "v1", nil, false},
		{
			"failure with a final status",
			fleettest.UpdateScript{Result: fleettest.UpdateFailed, ResourceStatus: fleettest.StatusUpdateFailed, ResourceIssues: issues},
			fleettest.StatusUpdateFailed, "v1", issues, false,
		},
		{
			"success that leaves the resource degraded",
			fleettest.UpdateScript{ResourceStatus: fleettest.StatusDegraded, ResourceIssues: issues},
			fleettest.StatusDegraded, "v2", issues, true,
		},
		{
			"empty issues clear the health", fleettest.UpdateScript{ResourceIssues: []fleettest.Issue{}},
			fleettest.StatusActive, "v2", []fleettest.Issue{}, true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWorld()
			w.Edit(func(s *fleettest.State) {
				a := s.Cluster(region, "prod").Addon("coredns")
				a.Status, a.UpdateScripts = fleettest.StatusCreateFailed, []fleettest.UpdateScript{tt.script}
			})
			created := w.Now()
			w.Advance(time.Minute)
			if _, err := w.UpdateAddon(region, "prod", "coredns", fleettest.AddonRequest{Version: new("v2")}); err != nil {
				t.Fatal(err)
			}
			w.View(func(s *fleettest.State) {
				a := s.Cluster(region, "prod").Addon("coredns")
				if a.Status != tt.wantStatus || a.Version != tt.wantVersion {
					t.Errorf("add-on = %s %s, want %s %s", a.Version, a.Status, tt.wantVersion, tt.wantStatus)
				}
				if diff := cmp.Diff(tt.wantIssues, a.HealthIssues); diff != "" {
					t.Errorf("health issues (-want +got):\n%s", diff)
				}
				if got := a.ModifiedAt.After(created); got != tt.wantChanged || !a.CreatedAt.Equal(created) {
					t.Errorf("created %s, modified %s; want modified to move = %t", a.CreatedAt, a.ModifiedAt, tt.wantChanged)
				}
			})
		})
	}
}

// TestControlPlaneUpdateBlocksTheRest checks that a nodegroup or an add-on
// cannot start an update while the control plane updates, and can again
// when it is done. The reverse is allowed.
func TestControlPlaneUpdateBlocksTheRest(t *testing.T) {
	w := newWorld()
	w.Edit(func(s *fleettest.State) { s.DefaultUpdate = fleettest.UpdateScript{After: time.Hour} })
	three := int32(3)
	others := map[string]func() error{
		"nodegroup version": func() error {
			_, err := w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{})
			return err
		},
		"nodegroup config": func() error {
			_, err := w.UpdateNodegroupConfig(region, "prod", "ng-a", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Desired: &three}})
			return err
		},
		"add-on": func() error {
			_, err := w.UpdateAddon(region, "prod", "coredns", fleettest.AddonRequest{Version: new("v2")})
			return err
		},
	}

	control, err := w.UpdateClusterVersion(region, "prod", fleettest.ClusterVersionRequest{Version: new("1.33")})
	if err != nil {
		t.Fatal(err)
	}
	for name, start := range others {
		var inUse *fleettest.InUseError
		if err := start(); !errors.As(err, &inUse) || inUse.UpdateID != control.ID || inUse.Name != "prod" {
			t.Errorf("%s update while the control plane updates returned %v, want an InUseError for the cluster's update", name, err)
		}
	}
	w.Advance(time.Hour)
	if err := others["add-on"](); err != nil {
		t.Errorf("add-on update after the control plane is done: %v", err)
	}
	if _, err := w.UpdateClusterVersion(region, "prod", fleettest.ClusterVersionRequest{Version: new("1.34")}); err != nil {
		t.Errorf("control-plane update while an add-on updates: %v", err)
	}
	if got := w.Pending(); got != 2 {
		t.Errorf("Pending = %d with an add-on and a control-plane update in progress, want 2", got)
	}
	w.Advance(time.Hour)
	if got := w.Pending(); got != 0 {
		t.Errorf("Pending = %d after both ended, want 0", got)
	}
}

func TestInsightsRefresh(t *testing.T) {
	w := newWorld()
	w.Edit(func(s *fleettest.State) { s.InsightsRefresh = time.Minute })
	refresh := func() fleettest.RefreshStatus {
		var out fleettest.RefreshStatus
		w.View(func(s *fleettest.State) { out = s.Cluster(region, "prod").InsightsRefresh.Status })
		return out
	}

	if got := refresh(); got != "" {
		t.Errorf("before any refresh: %q, want none", got)
	}
	if err := w.StartInsightsRefresh(region, "prod"); err != nil {
		t.Fatal(err)
	}
	w.Advance(59 * time.Second)
	if got := refresh(); got != "IN_PROGRESS" {
		t.Errorf("after 59s: %q, want IN_PROGRESS", got)
	}
	w.Advance(time.Second)
	if got := refresh(); got != "COMPLETED" {
		t.Errorf("after 60s: %q, want COMPLETED", got)
	}
	var r fleettest.InsightsRefresh
	w.View(func(s *fleettest.State) { r = s.Cluster(region, "prod").InsightsRefresh })
	if got := r.EndedAt.Sub(r.StartedAt); got != time.Minute {
		t.Errorf("EndedAt - StartedAt = %s, want 1m0s", got)
	}
	var notFound *fleettest.NotFoundError
	if err := w.StartInsightsRefresh(region, "nope"); !errors.As(err, &notFound) {
		t.Errorf("unknown cluster: %v, want a NotFoundError", err)
	}
}

// TestConcurrentUse runs every kind of call at once, for the race detector.
func TestConcurrentUse(t *testing.T) {
	w := newWorld()
	ctx := testContext(t)
	w.SetFault(fleettest.EKSListClusters, fleettest.Fault{Throttle: 50})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 50 {
				_, _ = w.UpdateAddon(region, "prod", "coredns", fleettest.AddonRequest{Version: new("v2")})
				_, _ = w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{})
				_, _ = w.Enter(ctx, fleettest.Call{Operation: fleettest.EKSListClusters})
				w.Advance(time.Second)
				w.View(func(s *fleettest.State) { _ = len(s.Clusters[0].Updates) })
				w.Edit(func(s *fleettest.State) { s.Clusters[0].Tags = map[string]string{"n": string(rune('a' + i))} })
				_ = w.Now()
			}
		})
	}
	wg.Wait()

	var ids []string
	w.View(func(s *fleettest.State) {
		for _, u := range s.Clusters[0].Updates {
			ids = append(ids, u.ID)
		}
	})
	if len(ids) != 8*50*2 {
		t.Fatalf("started %d updates, want %d", len(ids), 8*50*2)
	}
	seen := map[string]bool{}
	for _, updateID := range ids {
		if seen[updateID] {
			t.Fatalf("update ID %s was given twice", updateID)
		}
		seen[updateID] = true
	}
}
