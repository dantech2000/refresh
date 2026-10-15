package change

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// An add-on update that someone else started and that settles ACTIVE
// below the target of the phase is followed by the update of the run
// (REF-114): the run does not take the attachment for its own change.
func TestEngine_AddonAttachedBelowTheTargetIsUpdated(t *testing.T) {
	world := engineWorld(nil)
	e := newEnv(t, world)
	// A change of the configuration of vpc-cni, at the version that it
	// has, runs when the phase of the add-ons begins.
	same := cni32
	hooks := Hooks{Checkpoint: func(_ context.Context, next StepRef) (Checkpoint, error) {
		if next.Phase.Kind == PhaseAddonsBefore && next.Kind == "" {
			if _, err := world.UpdateAddon(east, "prod", "vpc-cni", fleettest.AddonRequest{Version: &same}); err != nil {
				t.Errorf("starting the update: %v", err)
			}
		}
		return Checkpoint{}, nil
	}}
	var notices []string
	hooks.Event = func(ev Event) {
		if ev.Kind == Notice {
			notices = append(notices, ev.Text)
		}
	}
	res := mustRun(t, e, upgradeTo("1.33"), hooks)
	if res.Status != Succeeded || res.ExitCode() != exitcode.OK {
		t.Fatalf("status = %s, exit %d, err %v; want Succeeded", res.Status, res.ExitCode(), res.Err)
	}
	if diff := cmp.Diff(wholeUpgrade(), e.sent()); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
	if s := stepResultOf(t, res, StepAddon, "vpc-cni"); s.Attached || s.UpdateID == "" {
		t.Errorf("vpc-cni: attached %t, update %q; want the update of the run", s.Attached, s.UpdateID)
	}
	if !slices.ContainsFunc(notices, func(n string) bool {
		return strings.Contains(n, "add-on vpc-cni is UPDATING already")
	}) || !slices.ContainsFunc(notices, func(n string) bool { return strings.Contains(n, "does not meet the phase; updating it to "+cni33) }) {
		t.Errorf("notices = %q; want the attachment and the update that follows it", notices)
	}
	e.world.View(func(s *fleettest.State) {
		if got := s.Cluster(east, "prod").Addon("vpc-cni").Version; got != cni33 {
			t.Errorf("vpc-cni is at %s, want %s", got, cni33)
		}
	})
}

// A cluster that is FAILED or DELETING never becomes ACTIVE: the wait for
// the control plane ends at the first answer, and does not poll to the
// deadline.
func TestWaitSettled_ClusterThatWillNotBecomeActive(t *testing.T) {
	controlPlane := Resource{Target: prod, Kind: StepControlPlane}
	for _, status := range []fleettest.ResourceStatus{"FAILED", "DELETING"} {
		t.Run(string(status), func(t *testing.T) {
			e := newEnv(t, newWorld(func(c *fleettest.Cluster) { c.Status = status }))
			var rec recorder
			got, err := WaitSettled(testContext(t), e.deps.AWS.PollClients(east), controlPlane, clusterSettled, rec.options(e))
			if err == nil || !strings.Contains(err.Error(), "the cluster is "+string(status)+"; it will not become ACTIVE") {
				t.Fatalf("error = %v, want that the cluster will not become ACTIVE", err)
			}
			if got.Status != string(status) || len(rec.polls) != 1 || e.count(fleettest.EKSDescribeCluster) != 1 {
				t.Errorf("settled %+v after %d poll(s) and %d describe(s); want the status after one", got, len(rec.polls), e.count(fleettest.EKSDescribeCluster))
			}
			if got := e.world.Now().Sub(fleettest.New().Now()); got != 15*time.Second {
				t.Errorf("the wait took %s, want the one interval before the first poll", got)
			}
		})
	}
	t.Run("UPDATING is waited for", func(t *testing.T) {
		e := newEnv(t, newWorld(func(c *fleettest.Cluster) { c.Status = fleettest.StatusUpdating }))
		e.onRequest(fleettest.EKSDescribeCluster, func(n int) {
			if n == 3 {
				e.world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Status = fleettest.StatusActive })
			}
		})
		got, err := WaitSettled(testContext(t), e.deps.AWS.PollClients(east), controlPlane, clusterSettled, (&recorder{}).options(e))
		if err != nil || got.Status != "ACTIVE" {
			t.Errorf("settled %+v, error %v; want ACTIVE", got, err)
		}
	})
}

// A VPN drop or a laptop that sleeps fails the polls of a long wait with
// a network error and no API error. The wait polls through them. A denied
// poll ends the wait at once, for a control plane and a nodegroup as for
// an add-on.
func TestWait_NetworkErrorsAndDeniedPolls(t *testing.T) {
	resources := []struct {
		name string
		res  Resource
		op   fleettest.Operation
		done func(Settled) (bool, error)
		busy func(c *fleettest.Cluster, s fleettest.ResourceStatus)
	}{
		{"control plane", Resource{Target: prod, Kind: StepControlPlane}, fleettest.EKSDescribeCluster, clusterSettled,
			func(c *fleettest.Cluster, s fleettest.ResourceStatus) { c.Status = s }},
		{"nodegroup", Resource{Target: prod, Kind: StepNodegroup, Name: "web"}, fleettest.EKSDescribeNodegroup, nodegroupSettled,
			func(c *fleettest.Cluster, s fleettest.ResourceStatus) { c.Nodegroup("web").Status = s }},
	}
	for _, r := range resources {
		t.Run(r.name+": the connection drops", func(t *testing.T) {
			e := newEnv(t, newWorld(func(c *fleettest.Cluster) { r.busy(c, fleettest.StatusUpdating) }))
			e.world.SetFault(r.op, fleettest.Fault{Reset: true})
			// The network is back at the fourth poll, and the change has
			// ended by then.
			e.onRequest(r.op, func(n int) {
				if n == 4 {
					e.world.SetFault(r.op, fleettest.Fault{})
					e.world.Edit(func(s *fleettest.State) { r.busy(s.Cluster(east, "prod"), fleettest.StatusActive) })
				}
			})
			var rec recorder
			got, err := WaitSettled(testContext(t), e.deps.AWS.PollClients(east), r.res, r.done, rec.options(e))
			if err != nil || got.Status != "ACTIVE" {
				t.Fatalf("settled %+v, error %v; want ACTIVE after the polls that failed", got, err)
			}
			if diff := cmp.Diff([]string{"error", "error", "error", "ACTIVE"}, rec.statuses()); diff != "" {
				t.Errorf("polls (-want +got):\n%s", diff)
			}
		})
		t.Run(r.name+": a denied poll", func(t *testing.T) {
			e := newEnv(t, newWorld(func(c *fleettest.Cluster) { r.busy(c, fleettest.StatusUpdating) }))
			e.onRequest(r.op, func(n int) {
				if n == 2 {
					e.world.SetFault(r.op, fleettest.Fault{Deny: true})
				}
			})
			_, err := WaitSettled(testContext(t), e.deps.AWS.PollClients(east), r.res, r.done, (&recorder{}).options(e))
			var unobserved *UnobservedError
			if !errors.As(err, &unobserved) || unobserved.LastStatus != "UPDATING" {
				t.Fatalf("error = %v, want an *UnobservedError after UPDATING", err)
			}
			if n := e.count(r.op); n != 2 {
				t.Errorf("%d polls, want 2: the denial ends the wait", n)
			}
		})
	}
	t.Run("update: the connection drops", func(t *testing.T) {
		e := newEnv(t, newWorld(nil))
		id := startUpdate(t, e, fleettest.UpdateScript{After: 20 * time.Second})
		e.world.SetFault(fleettest.EKSDescribeUpdate, fleettest.Fault{Reset: true})
		e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
			if n == 4 {
				e.world.SetFault(fleettest.EKSDescribeUpdate, fleettest.Fault{})
			}
		})
		var rec recorder
		if err := WaitUpdate(testContext(t), e.deps.AWS.PollClients(east), cni, id, rec.options(e)); err != nil {
			t.Fatalf("error = %v, want none", err)
		}
		if diff := cmp.Diff([]string{"error", "error", "error", "Successful"}, rec.statuses()); diff != "" {
			t.Errorf("polls (-want +got):\n%s", diff)
		}
	})
}

// One update of a batch that cannot be polled does not stop the wait for
// the others, and the failure names that update alone.
func TestRoll_OneUnmonitoredUpdateDoesNotStopTheOthers(t *testing.T) {
	e := newEnv(t, rollWorld(nil))
	e.world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeUpdate, Nodegroup: "ng-a"}, fleettest.Fault{Deny: true})
	req := rollOf("ng-a", "ng-b")
	req.Policy.Skip = true
	res, err := Run(testContext(t), e.deps, req, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]StepStatus{"prod/ng-a": StepInProgress, "prod/ng-b": StepSucceeded}
	if diff := cmp.Diff(want, clusterStatuses(res)); diff != "" {
		t.Errorf("statuses (-want +got):\n%s", diff)
	}
	a, b := res.Clusters[0].Steps[0], res.Clusters[0].Steps[1]
	if a.Failure == nil || a.Failure.Reason != apidoc.ReasonNotMonitored || a.Failure.AWSErrorCode != "AccessDeniedException" ||
		a.Failure.Name != "ng-a" || a.Failure.UpdateID != a.UpdateID || a.UpdateID == "" || strings.Contains(a.Failure.Error, "ng-b") {
		t.Errorf("ng-a: update %q, failure %+v; want NotMonitored with the denial, for its own update alone", a.UpdateID, a.Failure)
	}
	if b.Failure != nil {
		t.Errorf("ng-b has the failure %+v, want none", b.Failure)
	}
	if res.Status != Incomplete || res.ExitCode() != exitcode.Incomplete {
		t.Errorf("status = %s, exit %d; want Incomplete, exit 4", res.Status, res.ExitCode())
	}
	e.world.View(func(s *fleettest.State) {
		if got := s.Cluster(east, "prod").Nodegroup("ng-b").Status; got != fleettest.StatusActive {
			t.Errorf("ng-b is %s; the wait ended before its roll did", got)
		}
	})
}

// A failed Auto Scaling launch is reported once, however many polls of
// the wait see it.
func TestRoll_ScalingFailureIsReportedOnce(t *testing.T) {
	world := rollWorld(func(s *fleettest.State, _ *fleettest.Cluster) {
		s.DefaultUpdate = fleettest.UpdateScript{After: 2 * time.Minute}
	})
	now := world.Now()
	world.Edit(func(s *fleettest.State) {
		s.ScalingActivities = append(s.ScalingActivities,
			fleettest.ScalingActivity{ID: "act-1", Group: "eks-prod-ng-a", Status: "Failed", Message: "no capacity", Start: now})
	})
	e := newEnv(t, world)
	// The watch reads the scaling activities once for each tick of the wait
	// for the update, and a tick it is too busy to take is dropped. Poll n
	// of the update is held until the watch has read n-1 times, so on a
	// loaded machine the update cannot end before the watch has seen the
	// failure twice. The first poll is never held.
	var (
		paceMu sync.Mutex
		paced  = sync.NewCond(&paceMu)
		reads  int
		polls  int
	)
	e.srv.OnRequest = func(ctx context.Context, r awsxtest.Request) {
		paceMu.Lock()
		defer paceMu.Unlock()
		switch r.Operation {
		case fleettest.AutoScalingDescribeScalingActivities:
			reads++
			paced.Broadcast()
		case fleettest.EKSDescribeUpdate:
			polls++
			stop := context.AfterFunc(ctx, func() {
				paceMu.Lock()
				defer paceMu.Unlock()
				paced.Broadcast()
			})
			defer stop()
			for reads < polls-1 && ctx.Err() == nil {
				paced.Wait()
			}
		}
	}
	req := rollOf("ng-a")
	req.Policy.Skip = true
	var (
		mu      sync.Mutex
		notices int
	)
	res, err := Run(testContext(t), e.deps, req, Hooks{Event: func(ev Event) {
		if ev.Kind == Notice && strings.Contains(ev.Text, "Auto Scaling group eks-prod-ng-a: no capacity") {
			mu.Lock()
			notices++
			mu.Unlock()
		}
	}})
	if err != nil || res.ExitCode() != exitcode.OK {
		t.Fatalf("run: %v, exit %d (%v)", err, res.ExitCode(), res.Err)
	}
	if reads := e.count(fleettest.AutoScalingDescribeScalingActivities); reads < 2 {
		t.Fatalf("%d reads of the scaling activities; the test needs the watch to see the failure twice", reads)
	}
	if notices != 1 {
		t.Errorf("%d notices of the failed launch, want 1", notices)
	}
}

// EKS reports an add-on update as Successful and the add-on is not at the
// target version: the step fails.
func TestRun_SuccessfulUpdateAtAnotherVersionFails(t *testing.T) {
	e := newEnv(t, newWorld(nil))
	// The first poll of the update is the handshake: the add-on is put
	// back to its old version while EKS still reports the update.
	e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
		if n == 1 {
			e.world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").Addon("vpc-cni").Version = cniOld })
		}
	})
	res, err := Run(testContext(t), e.deps, yes(request("vpc-cni")), Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	s := oneStep(t, res)
	if s.Status != StepFailed || s.Failure == nil || s.Failure.Reason != apidoc.ReasonUpdateFailed ||
		!strings.Contains(s.Failure.Error, "reported Successful, but the add-on is at "+cniOld+", not "+cniNew) {
		t.Errorf("step = %s, failure %+v; want Failed with the version that the add-on has", s.Status, s.Failure)
	}
	if res.Status != Failed || res.ExitCode() != exitcode.Failure {
		t.Errorf("status = %s, exit %d; want Failed, exit 1", res.Status, res.ExitCode())
	}
}

// A nodegroup that the plan of a roll could not describe is a failure,
// exit code 4. It is not a nodegroup that needs no roll.
func TestRoll_NodegroupThatCannotBeDescribed(t *testing.T) {
	denyB := func(e *env) {
		e.world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup, Nodegroup: "ng-b"}, fleettest.Fault{Deny: true})
	}
	t.Run("dry run", func(t *testing.T) {
		e := newEnv(t, rollWorld(nil))
		denyB(e)
		p, err := NewPlan(testContext(t), e.deps, rollOf())
		if err != nil {
			t.Fatal(err)
		}
		if p.Decision() != DecisionBlocked || p.ExitCode() != exitcode.Incomplete {
			t.Errorf("decision %s, exit %d; want Blocked, exit 4", p.Decision(), p.ExitCode())
		}
		failures := p.allFailures()
		if !slices.ContainsFunc(failures, func(f apidoc.Failure) bool {
			return f.Kind == apidoc.FailureNodegroup && f.Name == "ng-b" && f.Reason == apidoc.ReasonAccessDenied
		}) {
			t.Errorf("failures = %+v, want the denied read of ng-b", failures)
		}
		for _, s := range p.Clusters[0].Steps() {
			if s.Ref.Name == "ng-b" && s.Action != Blocked {
				t.Errorf("ng-b is planned as %s (%s); it was not read", s.Action, s.Reason)
			}
		}
		if !slices.ContainsFunc(p.Clusters[0].Blockers, func(b Blocker) bool {
			return b.Kind == BlockerUnread && b.Step != nil && b.Step.Name == "ng-b"
		}) {
			t.Errorf("blockers = %+v, want one that ng-b could not be read", p.Clusters[0].Blockers)
		}
	})
	t.Run("a real run", func(t *testing.T) {
		e := newEnv(t, rollWorld(nil))
		denyB(e)
		res, err := Run(testContext(t), e.deps, rollOf(), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != Refused || res.ExitCode() != exitcode.Incomplete || len(e.rolls()) != 0 {
			t.Errorf("status = %s, exit %d, rolls %v; want Refused, exit 4, and no roll", res.Status, res.ExitCode(), e.rolls())
		}
	})
}

// A fleet run in which the nodegroups of one cluster cannot be listed
// exits 4 with a failure for that cluster, as a dry run and as a run.
func TestFleet_OneClusterCannotBeRead(t *testing.T) {
	deny := func(e *env) {
		e.world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSListNodegroups, Cluster: "staging"}, fleettest.Fault{Deny: true})
	}
	hasFailure := func(failures []apidoc.Failure) bool {
		return slices.ContainsFunc(failures, func(f apidoc.Failure) bool {
			return f.Reason == apidoc.ReasonAccessDenied && (f.Name == "staging" || f.Cluster == "staging")
		})
	}
	t.Run("dry run", func(t *testing.T) {
		e := newEnv(t, fleetWorld(nil))
		deny(e)
		p, err := NewPlan(testContext(t), e.deps, fleetRoll())
		if err != nil {
			t.Fatal(err)
		}
		if p.ExitCode() != exitcode.Incomplete || !hasFailure(p.allFailures()) {
			t.Errorf("exit %d, failures %+v; want exit 4 and the denied read of staging", p.ExitCode(), p.allFailures())
		}
	})
	t.Run("a real run", func(t *testing.T) {
		e := newEnv(t, fleetWorld(nil))
		deny(e)
		res, err := Run(testContext(t), e.deps, fleetRoll(), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode() != exitcode.Incomplete {
			t.Errorf("status = %s, exit %d; want exit 4", res.Status, res.ExitCode())
		}
		for _, r := range e.rolls() {
			if r.Cluster == "staging" {
				t.Errorf("a roll in the cluster that could not be read: %+v", r)
			}
		}
	})
}

// A run that is interrupted while the drain check reads the budgets is
// interrupted. The read that the interrupt ended is not a budget that
// could not be read, which --force may pass: no roll follows it.
func TestEngine_InterruptDuringTheDrainCheckStopsDespiteForce(t *testing.T) {
	world := engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
		c.PDBs[0].MinAvailable, c.PDBs[0].DesiredHealthy, c.PDBs[0].DisruptionsAllowed = 2, 2, 0
	})
	e := newEnv(t, world)
	srv := e.withKube(t)
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	// The first read of the budgets after the add-ons is the one of the
	// gate before the nodegroups.
	srv.OnRequest = func(_ context.Context, r kubetest.Request) {
		if r.Operation == fleettest.KubeListPDBs && slices.Contains(e.sent(), sentDNS33) {
			cancel()
		}
	}
	req := upgradeTo("1.33")
	req.Policy.Force = true
	res, err := Run(ctx, e.deps, req, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != Interrupted || res.ExitCode() != exitcode.Failure {
		t.Errorf("status = %s, exit %d, err %v; want Interrupted, exit 1", res.Status, res.ExitCode(), res.Err)
	}
	if diff := cmp.Diff([]string{sentRefresh, sentCP33, sentCNI33, sentDNS33}, e.sent()); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
	for _, name := range []string{"ng-a", "ng-b"} {
		if s := stepResultOf(t, res, StepNodegroup, name); s.Status != StepNotAttempted {
			t.Errorf("%s = %s (%+v); want NotAttempted", name, s.Status, s.Failure)
		}
	}
}
