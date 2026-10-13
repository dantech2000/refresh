package change

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

var cni = Resource{Target: prod, Kind: StepAddon, Name: "vpc-cni"}

// startUpdate starts an update of vpc-cni in the world with the script,
// and returns its ID.
func startUpdate(t *testing.T, e *env, script fleettest.UpdateScript) string {
	t.Helper()
	e.world.Edit(func(s *fleettest.State) {
		s.Cluster(east, "prod").Addon("vpc-cni").UpdateScripts = []fleettest.UpdateScript{script}
	})
	version := cniNew
	u, err := e.world.UpdateAddon(east, "prod", "vpc-cni", fleettest.AddonRequest{Version: &version})
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// recorder collects the progress of a wait.
type recorder struct{ polls []UpdateProgress }

func (r *recorder) options(e *env) WaitOptions {
	return WaitOptions{Interval: 15 * time.Second, After: e.deps.After, Progress: func(p UpdateProgress) { r.polls = append(r.polls, p) }}
}

// statuses returns the status of each poll, or its error.
func (r *recorder) statuses() []string {
	out := make([]string, len(r.polls))
	for i, p := range r.polls {
		out[i] = p.Status
		if p.PollError != "" {
			out[i] = "error"
		}
	}
	return out
}

// Each terminal status of an update, and the first poll after one
// interval.
func TestWaitUpdate_TerminalStatuses(t *testing.T) {
	tests := []struct {
		name      string
		script    fleettest.UpdateScript
		wantPolls []string
		check     func(t *testing.T, err error)
	}{
		{
			name: "Successful", script: fleettest.UpdateScript{After: 40 * time.Second},
			wantPolls: []string{"InProgress", "InProgress", "Successful"},
			check: func(t *testing.T, err error) {
				t.Helper()
				if err != nil {
					t.Errorf("error = %v, want none", err)
				}
			},
		},
		{
			name: "Failed", wantPolls: []string{"Failed"},
			script: fleettest.UpdateScript{Result: fleettest.UpdateFailed, Errors: []fleettest.UpdateError{
				{Code: "ConfigurationConflict", Message: "Conflicts found", ResourceIDs: []string{"kube-system/aws-node"}},
			}},
			check: func(t *testing.T, err error) {
				t.Helper()
				var ue *UpdateError
				if !errors.As(err, &ue) {
					t.Fatalf("error = %v, want an *UpdateError", err)
				}
				want := []UpdateIssue{{Code: "ConfigurationConflict", Message: "Conflicts found", ResourceIDs: []string{"kube-system/aws-node"}}}
				if diff := cmp.Diff(want, ue.Issues); diff != "" || ue.Status != "Failed" || ue.Cancelled() {
					t.Errorf("status %s, cancelled %v, issues (-want +got):\n%s", ue.Status, ue.Cancelled(), diff)
				}
				if !strings.Contains(err.Error(), "ended Failed: ConfigurationConflict: Conflicts found (kube-system/aws-node)") {
					t.Errorf("message = %q", err)
				}
			},
		},
		{
			name: "Cancelled", script: fleettest.UpdateScript{After: 15 * time.Second, Result: fleettest.UpdateCancelled},
			wantPolls: []string{"Cancelled"},
			check: func(t *testing.T, err error) {
				t.Helper()
				var ue *UpdateError
				if !errors.As(err, &ue) || !ue.Cancelled() {
					t.Errorf("error = %v, want a cancelled *UpdateError", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, newWorld(nil))
			id := startUpdate(t, e, tt.script)
			start, before := e.world.Now(), len(e.srv.Requests())
			var rec recorder
			err := WaitUpdate(testContext(t), e.deps.AWS.PollClients(east), cni, id, rec.options(e))
			tt.check(t, err)
			if diff := cmp.Diff(tt.wantPolls, rec.statuses()); diff != "" {
				t.Errorf("polls (-want +got):\n%s", diff)
			}
			if last := rec.polls[len(rec.polls)-1]; !last.Final {
				t.Error("the last poll is not marked final")
			}
			// One pause before each poll: the first poll is not at once.
			if got, want := e.world.Now().Sub(start), time.Duration(len(tt.wantPolls))*15*time.Second; got != want {
				t.Errorf("the wait took %s on the clock, want %s", got, want)
			}
			for _, op := range e.operations(before) {
				if op != "eks:DescribeUpdate" {
					t.Errorf("the wait called %s; it may call DescribeUpdate only", op)
				}
			}
		})
	}
}

// A fresh update ID can answer not-found for a moment. That is transient
// before the update was seen, and permanent after.
func TestWaitUpdate_NotFound(t *testing.T) {
	t.Run("before the first sighting", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		id := startUpdate(t, e, fleettest.UpdateScript{After: 30 * time.Second})
		e.world.SetFault(fleettest.EKSDescribeUpdate, fleettest.Fault{Code: "ResourceNotFoundException"})
		e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
			if n == 3 {
				e.world.SetFault(fleettest.EKSDescribeUpdate, fleettest.Fault{})
			}
		})
		var rec recorder
		if err := WaitUpdate(testContext(t), e.deps.AWS.PollClients(east), cni, id, rec.options(e)); err != nil {
			t.Fatalf("error = %v, want none", err)
		}
		if diff := cmp.Diff([]string{"error", "error", "Successful"}, rec.statuses()); diff != "" {
			t.Errorf("polls (-want +got):\n%s", diff)
		}
	})
	t.Run("after the update was seen", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		id := startUpdate(t, e, fleettest.UpdateScript{Never: true})
		e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
			if n == 2 {
				e.world.SetFault(fleettest.EKSDescribeUpdate, fleettest.Fault{Code: "ResourceNotFoundException"})
			}
		})
		err := WaitUpdate(testContext(t), e.deps.AWS.PollClients(east), cni, id, (&recorder{}).options(e))
		var unobserved *UnobservedError
		if !errors.As(err, &unobserved) || unobserved.LastStatus != "InProgress" {
			t.Fatalf("error = %v, want an *UnobservedError after InProgress", err)
		}
	})
}

// A throttled poll is one attempt, and the wait polls again.
func TestWaitUpdate_ThrottledPoll(t *testing.T) {
	e := newEnv(t, newWorld(nil))
	id := startUpdate(t, e, fleettest.UpdateScript{After: 20 * time.Second})
	e.world.SetFault(fleettest.EKSDescribeUpdate, fleettest.Fault{Throttle: 2})
	var rec recorder
	if err := WaitUpdate(testContext(t), e.deps.AWS.PollClients(east), cni, id, rec.options(e)); err != nil {
		t.Fatalf("error = %v, want none", err)
	}
	if diff := cmp.Diff([]string{"error", "error", "Successful"}, rec.statuses()); diff != "" {
		t.Errorf("polls (-want +got):\n%s", diff)
	}
	if n := e.count(fleettest.EKSDescribeUpdate); n != 3 {
		t.Errorf("%d DescribeUpdate requests, want 3: the poll clients make one attempt", n)
	}
	if rec.polls[0].PollError == "" {
		t.Error("the throttled poll has no error text")
	}
}

// An access denial in the middle of a wait ends it: another poll will not
// fix it, and the update may still be running.
func TestWaitUpdate_AccessDenied(t *testing.T) {
	e := newEnv(t, newWorld(nil))
	id := startUpdate(t, e, fleettest.UpdateScript{Never: true})
	e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
		if n == 3 {
			e.world.SetFault(fleettest.EKSDescribeUpdate, fleettest.Fault{Deny: true})
		}
	})
	err := WaitUpdate(testContext(t), e.deps.AWS.PollClients(east), cni, id, (&recorder{}).options(e))
	var unobserved *UnobservedError
	if !errors.As(err, &unobserved) {
		t.Fatalf("error = %v, want an *UnobservedError", err)
	}
	if unobserved.UpdateID != id || unobserved.LastStatus != "InProgress" || unobserved.Resource != cni {
		t.Errorf("error = %+v", unobserved)
	}
	if !strings.Contains(err.Error(), "it may still be running") {
		t.Errorf("message = %q", err)
	}
	if n := e.count(fleettest.EKSDescribeUpdate); n != 3 {
		t.Errorf("%d polls, want 3", n)
	}
}

// An answer with no update in it is polled through.
func TestWaitUpdate_EmptyAnswer(t *testing.T) {
	e := newEnv(t, newWorld(nil))
	id := startUpdate(t, e, fleettest.UpdateScript{After: 30 * time.Second})
	empty := 2
	deps := e.intercept(t, func(r *http.Request) bool {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/updates/") && empty > 0 {
			empty--
			return true
		}
		return false
	})
	var rec recorder
	if err := WaitUpdate(testContext(t), deps.AWS.PollClients(east), cni, id, rec.options(e)); err != nil {
		t.Fatalf("error = %v, want none", err)
	}
	if diff := cmp.Diff([]string{"error", "error", "Successful"}, rec.statuses()); diff != "" {
		t.Errorf("polls (-want +got):\n%s", diff)
	}
}

// A wait with no update ID cannot follow anything.
func TestWaitUpdate_NoUpdateID(t *testing.T) {
	e := newEnv(t, newWorld(nil))
	err := WaitUpdate(testContext(t), e.deps.AWS.PollClients(east), cni, "", (&recorder{}).options(e))
	var unobserved *UnobservedError
	if !errors.As(err, &unobserved) {
		t.Fatalf("error = %v, want an *UnobservedError", err)
	}
	if n := len(e.srv.Requests()); n != 0 {
		t.Errorf("%d requests, want none", n)
	}
}

// A context that ends, ends the wait with its error and the last status.
// The caller tells a deadline from an interrupt by the cause.
func TestWaitUpdate_ContextEnds(t *testing.T) {
	deadline := errors.New("the budget ran out")
	for _, tt := range []struct {
		name  string
		cause error
	}{{"an interrupt", nil}, {"a deadline", deadline}} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, newWorld(nil))
			id := startUpdate(t, e, fleettest.UpdateScript{Never: true})
			ctx, cancel := context.WithCancelCause(testContext(t))
			defer cancel(nil)
			// The second poll has answered when the third pause starts.
			opts := (&recorder{}).options(e)
			pauses := 0
			opts.After = func(d time.Duration) <-chan time.Time {
				if pauses++; pauses == 3 {
					cancel(tt.cause)
				}
				return e.deps.After(d)
			}
			err := WaitUpdate(ctx, e.deps.AWS.PollClients(east), cni, id, opts)
			if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "(last status InProgress)") {
				t.Fatalf("error = %v, want the error of the context with the last status", err)
			}
			if got := errors.Is(context.Cause(ctx), deadline); got != (tt.cause != nil) {
				t.Errorf("the cause says deadline = %v", got)
			}
			if n := e.count(fleettest.EKSDescribeUpdate); n != 2 {
				t.Errorf("%d polls, want 2: none after the context ended", n)
			}
		})
	}
}

// WaitSettled hands every answer to done, and returns the state that done
// accepted.
func TestWaitSettled_Addon(t *testing.T) {
	t.Run("an update that ends ACTIVE", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		startUpdate(t, e, fleettest.UpdateScript{After: 40 * time.Second})
		before := len(e.srv.Requests())
		var rec recorder
		got, err := WaitSettled(testContext(t), e.deps.AWS.PollClients(east), cni, addonSettled, rec.options(e))
		if err != nil {
			t.Fatalf("error = %v", err)
		}
		if diff := cmp.Diff(Settled{Status: "ACTIVE", Version: cniNew}, got); diff != "" {
			t.Errorf("settled (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff([]string{"UPDATING", "UPDATING", "ACTIVE"}, rec.statuses()); diff != "" {
			t.Errorf("polls (-want +got):\n%s", diff)
		}
		for _, op := range e.operations(before) {
			if op != "eks:DescribeAddon" {
				t.Errorf("the wait called %s; it may call DescribeAddon only", op)
			}
		}
	})
	for _, status := range []fleettest.ResourceStatus{fleettest.StatusDegraded, fleettest.StatusUpdateFailed, fleettest.StatusCreateFailed, "DELETE_FAILED"} {
		t.Run("an update that ends "+string(status), func(t *testing.T) {
			e := newEnv(t, newWorld(nil))
			startUpdate(t, e, fleettest.UpdateScript{After: 15 * time.Second, Result: fleettest.UpdateFailed, ResourceStatus: status})
			got, err := WaitSettled(testContext(t), e.deps.AWS.PollClients(east), cni, addonSettled, (&recorder{}).options(e))
			if err == nil || err.Error() != "the add-on update failed: status "+string(status) {
				t.Fatalf("error = %v", err)
			}
			if got.Status != string(status) || got.Version != cniOld {
				t.Errorf("settled = %+v, want %s at %s", got, status, cniOld)
			}
		})
	}
	t.Run("a poll that is throttled, and one that is denied", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		startUpdate(t, e, fleettest.UpdateScript{Never: true})
		e.world.SetFault(fleettest.EKSDescribeAddon, fleettest.Fault{Throttle: 1})
		e.onRequest(fleettest.EKSDescribeAddon, func(n int) {
			if n == 3 {
				e.world.SetFault(fleettest.EKSDescribeAddon, fleettest.Fault{Deny: true})
			}
		})
		var rec recorder
		_, err := WaitSettled(testContext(t), e.deps.AWS.PollClients(east), cni, addonSettled, rec.options(e))
		var unobserved *UnobservedError
		if !errors.As(err, &unobserved) || unobserved.LastStatus != "UPDATING" {
			t.Fatalf("error = %v, want an *UnobservedError after UPDATING", err)
		}
		if diff := cmp.Diff([]string{"error", "UPDATING"}, rec.statuses()); diff != "" {
			t.Errorf("polls (-want +got):\n%s", diff)
		}
	})
	t.Run("an add-on that does not exist", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		gone := Resource{Target: prod, Kind: StepAddon, Name: "gone"}
		_, err := WaitSettled(testContext(t), e.deps.AWS.PollClients(east), gone, addonSettled, (&recorder{}).options(e))
		var unobserved *UnobservedError
		if !errors.As(err, &unobserved) {
			t.Fatalf("error = %v, want an *UnobservedError", err)
		}
	})
	t.Run("a context that ends", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		startUpdate(t, e, fleettest.UpdateScript{Never: true})
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		e.onRequest(fleettest.EKSDescribeAddon, func(n int) {
			if n == 2 {
				cancel()
			}
		})
		_, err := WaitSettled(ctx, e.deps.AWS.PollClients(east), cni, addonSettled, (&recorder{}).options(e))
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "waiting for add-on vpc-cni (last status UPDATING)") {
			t.Fatalf("error = %v", err)
		}
	})
}

// WaitSettled reads a nodegroup with its sizes, and a cluster.
func TestWaitSettled_NodegroupAndCluster(t *testing.T) {
	e := newEnv(t, newWorld(func(c *fleettest.Cluster) {
		c.Nodegroups[0].Desired, c.Nodegroups[0].Min, c.Nodegroups[0].Max = 4, 2, 6
	}))
	poll := e.deps.AWS.PollClients(east)
	first := func(Settled) (bool, error) { return true, nil }

	web := Resource{Target: prod, Kind: StepNodegroup, Name: "web"}
	got, err := WaitSettled(testContext(t), poll, web, first, (&recorder{}).options(e))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(Settled{Status: "ACTIVE", Version: "1.33", Sizes: Sizes{Desired: 4, Min: 2, Max: 6}}, got); diff != "" {
		t.Errorf("nodegroup (-want +got):\n%s", diff)
	}

	// done sees every answer, also one of a status that still changes,
	// and its error ends the wait.
	e.world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Status = fleettest.StatusUpdating })
	var seen []string
	stop := errors.New("stop")
	_, err = WaitSettled(testContext(t), poll, Resource{Target: prod, Kind: StepControlPlane}, func(s Settled) (bool, error) {
		seen = append(seen, s.Status+" "+s.Version)
		if len(seen) == 2 {
			return false, stop
		}
		return false, nil
	}, (&recorder{}).options(e))
	if !errors.Is(err, stop) {
		t.Fatalf("error = %v, want the error of done", err)
	}
	if diff := cmp.Diff([]string{"UPDATING 1.33", "UPDATING 1.33"}, seen); diff != "" {
		t.Errorf("answers (-want +got):\n%s", diff)
	}
	for _, r := range []Resource{web, cni, {Target: prod, Kind: StepControlPlane}, {Target: prod, Kind: StepScale, Name: "web"}} {
		if r.String() == "" {
			t.Errorf("%+v has no name", r)
		}
	}
}

// The default interval and the default clock: a wait with no options
// uses time.After, which a context that has ended beats.
func TestWaitOptionsDefaults(t *testing.T) {
	if got := (WaitOptions{}).interval(); got != DefaultPollInterval {
		t.Errorf("interval = %s, want %s", got, DefaultPollInterval)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if (WaitOptions{}).pause(ctx) {
		t.Error("pause went on under a context that has ended")
	}
}
