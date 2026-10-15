package change

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// fleetWorld returns the roll world with a second cluster, staging.
func fleetWorld(edit func(s *fleettest.State, prod, staging *fleettest.Cluster)) *fleettest.World {
	return rollWorld(func(s *fleettest.State, prod *fleettest.Cluster) {
		staging := addRollCluster(s, "staging")
		if edit != nil {
			edit(s, prod, staging)
		}
	})
}

// fleetRoll returns a roll of every cluster of the region, with --yes and
// no gate. The filter keeps ng-a, so each cluster has one roll.
func fleetRoll() RollRequest {
	return RollRequest{
		Common: Common{Policy: health.Policy{Yes: true, Skip: true}, Limits: Limits{Poll: 15 * time.Second}, SkipVerify: true},
		Select: &Selector{Text: "-f name=ng-a", Match: func(n fleet.Nodegroup) bool { return n.Name == "ng-a" }},
		Fleet:  &FleetScope{Regions: fleet.Regions{Names: []string{east}}},
	}
}

// A fleet plan has one plan per cluster, in name order, and reads no
// cluster from the request.
func TestFleet_Plan(t *testing.T) {
	e := newEnv(t, fleetWorld(nil))
	p, err := NewPlan(testContext(t), e.deps, fleetRoll())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := range p.Clusters {
		for _, s := range p.Clusters[i].Steps() {
			got = append(got, p.Clusters[i].Cluster+"/"+s.Ref.Name+"="+string(s.Action))
		}
	}
	if diff := cmp.Diff([]string{"prod/ng-a=Submit", "staging/ng-a=Submit"}, got); diff != "" {
		t.Errorf("steps (-want +got):\n%s", diff)
	}
	if len(e.mutations()) != 0 {
		t.Errorf("mutations = %v", e.mutations())
	}
	bad := fleetRoll()
	bad.Cluster = "prod"
	if _, err := NewPlan(testContext(t), e.deps, bad); err == nil {
		t.Error("no error for a fleet roll that names a cluster")
	}
}

// No region that answers is an error, and not a fleet with no cluster.
func TestFleet_NoRegionAnswers(t *testing.T) {
	world := fleetWorld(nil)
	world.SetFault(fleettest.EKSListClusters, fleettest.Fault{Deny: true})
	e := newEnv(t, world)
	res, err := Run(testContext(t), e.deps, fleetRoll(), Hooks{})
	var discovery *DiscoveryError
	if !errors.As(err, &discovery) || len(discovery.Sweep.Failures) != 1 || res.ExitCode() != exitcode.Failure {
		t.Errorf("error = %v, exit %d; want a DiscoveryError with the failure of the region, exit 1", err, res.ExitCode())
	}
}

// Safety rule 8, first part. The failure of one cluster does not stop the
// next one, and neither does a cluster that the plan blocks.
func TestFleet_OneClusterDoesNotStopTheNext(t *testing.T) {
	tests := []struct {
		name string
		seed func(s *fleettest.State, prod, staging *fleettest.Cluster)
		edit func(r *RollRequest)
		prod StepStatus
		exit int
	}{
		{name: "the roll fails", prod: StepFailed, exit: exitcode.Incomplete,
			seed: func(_ *fleettest.State, prod, _ *fleettest.Cluster) {
				prod.Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{{After: 15 * time.Second, Result: fleettest.UpdateFailed}}
			}},
		{name: "the wait times out", prod: StepInProgress, exit: exitcode.Incomplete,
			seed: func(_ *fleettest.State, prod, _ *fleettest.Cluster) {
				prod.Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{{Never: true}}
			},
			edit: func(r *RollRequest) { r.Limits.Wait = time.Minute }},
		{name: "the target is not ACTIVE", prod: StepBlocked, exit: exitcode.Blocked,
			seed: func(_ *fleettest.State, prod, _ *fleettest.Cluster) {
				prod.Nodegroup("ng-a").Status = fleettest.StatusDegraded
			}},
		{name: "the cluster is busy", prod: StepBlocked, exit: exitcode.Blocked,
			seed: func(_ *fleettest.State, prod, _ *fleettest.Cluster) { prod.Status = fleettest.StatusUpdating }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, fleetWorld(tt.seed))
			req := fleetRoll()
			if tt.edit != nil {
				tt.edit(&req)
			}
			res, err := Run(testContext(t), e.deps, req, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]StepStatus{"prod/ng-a": tt.prod, "staging/ng-a": StepSucceeded}
			if diff := cmp.Diff(want, clusterStatuses(res)); diff != "" {
				t.Errorf("statuses (-want +got):\n%s", diff)
			}
			if res.ExitCode() != tt.exit {
				t.Errorf("exit = %d, want %d (%v)", res.ExitCode(), tt.exit, res.Err)
			}
			rolls := e.rolls()
			if len(rolls) == 0 || rolls[len(rolls)-1].Cluster != "staging" {
				t.Errorf("rolls = %v, want the roll of staging last", rolls)
			}
		})
	}
}

// Safety rule 8, second part. An interrupt stops the fleet: the steps of
// the later clusters are NotAttempted, and nothing is sent for them. A
// stop from the checkpoint does the same. A stopped run is no success: it
// exits 1, and a higher code of the steps before the stop still wins.
func TestFleet_InterruptAndStop(t *testing.T) {
	t.Run("interrupt", func(t *testing.T) {
		e := newEnv(t, fleetWorld(func(s *fleettest.State, _, _ *fleettest.Cluster) {
			s.DefaultUpdate = fleettest.UpdateScript{Never: true}
		}))
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
			if n == 2 {
				cancel()
			}
		})
		res, err := Run(ctx, e.deps, fleetRoll(), Hooks{})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]StepStatus{"prod/ng-a": StepInProgress, "staging/ng-a": StepNotAttempted}
		if diff := cmp.Diff(want, clusterStatuses(res)); diff != "" {
			t.Errorf("statuses (-want +got):\n%s", diff)
		}
		if res.ExitCode() != exitcode.Failure || res.Status != Interrupted {
			t.Errorf("status = %s, exit %d, want Interrupted, exit 1", res.Status, res.ExitCode())
		}
		if diff := cmp.Diff([]sentRoll{{"prod", "ng-a", "1.32", false}}, e.rolls()); diff != "" {
			t.Errorf("rolls (-want +got):\n%s", diff)
		}
	})
	t.Run("stop at the checkpoint", func(t *testing.T) {
		e := newEnv(t, fleetWorld(nil))
		var seen []string
		hooks := Hooks{Checkpoint: func(_ context.Context, next StepRef) (Checkpoint, error) {
			seen = append(seen, next.Phase.Target.Cluster)
			return Checkpoint{Stop: next.Phase.Target.Cluster == "staging"}, nil
		}}
		res, err := Run(testContext(t), e.deps, fleetRoll(), hooks)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]StepStatus{"prod/ng-a": StepSucceeded, "staging/ng-a": StepNotAttempted}
		if diff := cmp.Diff(want, clusterStatuses(res)); diff != "" {
			t.Errorf("statuses (-want +got):\n%s", diff)
		}
		// The hook runs before every cluster, the first included.
		if diff := cmp.Diff([]string{"prod", "staging"}, seen); diff != "" {
			t.Errorf("checkpoints (-want +got):\n%s", diff)
		}
		if res.Status != Stopped || res.ExitCode() != exitcode.Failure || len(e.rolls()) != 1 {
			t.Errorf("status = %s, exit %d, rolls %v; want Stopped, exit 1, one roll", res.Status, res.ExitCode(), e.rolls())
		}
		if res.Err == nil || !strings.Contains(res.Err.Error(), "stopped before cluster staging") {
			t.Errorf("message = %v, want it to say where the run stopped", res.Err)
		}
	})
	t.Run("stop at the checkpoint after a blocked cluster", func(t *testing.T) {
		e := newEnv(t, fleetWorld(func(_ *fleettest.State, prod, _ *fleettest.Cluster) {
			prod.Nodegroup("ng-a").Status = fleettest.StatusDegraded
		}))
		res, err := Run(testContext(t), e.deps, fleetRoll(), Hooks{Checkpoint: func(_ context.Context, next StepRef) (Checkpoint, error) {
			return Checkpoint{Stop: next.Phase.Target.Cluster == "staging"}, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]StepStatus{"prod/ng-a": StepBlocked, "staging/ng-a": StepNotAttempted}
		if diff := cmp.Diff(want, clusterStatuses(res)); diff != "" {
			t.Errorf("statuses (-want +got):\n%s", diff)
		}
		// The higher code of the step wins, and its label with it.
		if res.Status != RunBlocked || res.ExitCode() != exitcode.Blocked || len(e.rolls()) != 0 {
			t.Errorf("status = %s, exit %d, rolls %v; want Blocked, exit 3, no roll", res.Status, res.ExitCode(), e.rolls())
		}
	})
	t.Run("a checkpoint that fails", func(t *testing.T) {
		e := newEnv(t, fleetWorld(nil))
		broken := errors.New("the pause broke")
		res, err := Run(testContext(t), e.deps, fleetRoll(), Hooks{Checkpoint: func(_ context.Context, next StepRef) (Checkpoint, error) {
			if next.Phase.Target.Cluster == "staging" {
				return Checkpoint{}, broken
			}
			return Checkpoint{}, nil
		}})
		if !errors.Is(err, broken) || res.Status != Failed || res.ExitCode() != exitcode.Failure || len(e.rolls()) != 1 {
			t.Errorf("err = %v, status %s, exit %d, rolls %v", err, res.Status, res.ExitCode(), e.rolls())
		}
	})
}

// An interrupt between two clusters ends the fleet at the turn of the next
// one, before anything is done for it: no checkpoint, no request of any
// kind, and its steps are NotAttempted. That holds also for a cluster that
// the plan blocked, whose turn reads nothing: its rows are not Blocked,
// and the run exits 1 and not 3.
func TestFleet_InterruptBetweenClusters(t *testing.T) {
	tests := []struct {
		name string
		seed func(s *fleettest.State, prod, staging *fleettest.Cluster)
	}{
		{name: "the next cluster would run"},
		{name: "the next cluster is blocked", seed: func(_ *fleettest.State, _, staging *fleettest.Cluster) {
			staging.Nodegroup("ng-a").Status = fleettest.StatusDegraded
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, fleetWorld(tt.seed))
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			sent, checkpoints := -1, 0
			hooks := Hooks{
				// The roll of prod has ended, and its turn is over.
				Event: func(ev Event) {
					if ev.Kind == PhaseEnded && ev.Phase.Target.Cluster == "prod" {
						sent = len(e.srv.Requests())
						cancel()
					}
				},
				Checkpoint: func(context.Context, StepRef) (Checkpoint, error) {
					checkpoints++
					return Checkpoint{}, nil
				},
			}
			res, err := Run(ctx, e.deps, fleetRoll(), hooks)
			if err != nil {
				t.Fatal(err)
			}
			if sent < 0 {
				t.Fatal("the roll of prod did not end")
			}
			if after := e.operations(sent); len(after) != 0 {
				t.Errorf("requests after the interrupt: %v, want none", after)
			}
			// One checkpoint, before prod. None follows the interrupt.
			if checkpoints != 1 {
				t.Errorf("%d checkpoint(s), want the one before prod and none after the interrupt", checkpoints)
			}
			want := map[string]StepStatus{"prod/ng-a": StepSucceeded, "staging/ng-a": StepNotAttempted}
			if diff := cmp.Diff(want, clusterStatuses(res)); diff != "" {
				t.Errorf("statuses (-want +got):\n%s", diff)
			}
			if res.ExitCode() != exitcode.Failure || res.Status != Interrupted {
				t.Errorf("status = %s, exit %d, want Interrupted, exit 1", res.Status, res.ExitCode())
			}
			if diff := cmp.Diff([]sentRoll{{"prod", "ng-a", "1.32", false}}, e.rolls()); diff != "" {
				t.Errorf("rolls (-want +got):\n%s", diff)
			}
		})
	}
}

// newRelease makes the SSM parameter that the plan read name another AMI,
// as when AWS publishes a release.
func newRelease(e *env) {
	var path string
	for _, r := range e.srv.Requests() {
		if r.Operation == fleettest.SSMGetParameter {
			path = fmt.Sprint(r.Input["Name"])
		}
	}
	e.world.Edit(func(s *fleettest.State) {
		s.Parameters = append(s.Parameters, fleettest.Parameter{Name: path, Value: "ami-newer"})
	})
}

// Safety rule 8, third part. Each cluster is planned again at its turn,
// and a plan that changed since the confirmation follows the table of
// Diff: a refusal blocks that cluster, a difference that needs an answer
// is accepted by --yes or asked about, and less work is a notice.
func TestFleet_PlanChangedAfterConfirmation(t *testing.T) {
	stagingA := func(s *fleettest.State) *fleettest.Nodegroup { return s.Cluster(east, "staging").Nodegroup("ng-a") }
	tests := []struct {
		name string
		// change runs while the roll of prod is waited for.
		change  func(e *env)
		yes     bool
		answers []bool
		// nobody leaves the run with no way to ask.
		nobody  bool
		staging StepStatus
		exit    int
		// asked is the number of questions, and say a text in the last
		// question, the notices, or the failure of staging.
		asked int
		say   string
	}{
		{name: "another Kubernetes version refuses", yes: true, staging: StepBlocked, exit: exitcode.Blocked, say: "now goes to 1.31, not 1.32",
			change: func(e *env) { e.world.Edit(func(s *fleettest.State) { stagingA(s).Version = "1.31" }) }},
		{name: "a new AMI, with --yes", yes: true, staging: StepSucceeded, say: "staging: nodegroup ng-a now gets the AMI ami-newer, not ami-latest132",
			change: newRelease},
		{name: "a new AMI, asked and accepted", answers: []bool{true, true}, asked: 2, staging: StepSucceeded, say: "staging: The plan changed",
			change: newRelease},
		{name: "a new AMI, asked and declined", answers: []bool{true, false}, asked: 2, staging: StepNotAttempted, exit: exitcode.Failure, say: "staging: The plan changed",
			change: newRelease},
		{name: "a new AMI, nobody to ask", nobody: true, staging: StepBlocked, exit: exitcode.Blocked, say: "nobody can be asked",
			change: newRelease},
		{name: "the target is now changing", yes: true, staging: StepBusy, exit: exitcode.Incomplete, say: "staging: nodegroup ng-a is now changing elsewhere",
			change: func(e *env) {
				three := int32(3)
				if _, err := e.world.UpdateNodegroupConfig(east, "staging", "ng-a", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Desired: &three}}); err != nil {
					e.t.Errorf("scaling ng-a of staging: %v", err)
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, fleetWorld(nil))
			e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
				if n == 1 {
					tt.change(e)
				}
			})
			req := fleetRoll()
			req.Policy.Yes = tt.yes
			s := &script{t: t, answers: tt.answers}
			hooks := s.hooks()
			if tt.nobody {
				// The plan was reviewed, so the start needs no answer.
				reviewed, err := NewPlan(testContext(t), e.deps, req)
				if err != nil {
					t.Fatal(err)
				}
				req.Expect, hooks.Ask = reviewed, nil
			}
			res, err := Run(testContext(t), e.deps, req, hooks)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]StepStatus{"prod/ng-a": StepSucceeded, "staging/ng-a": tt.staging}
			if diff := cmp.Diff(want, clusterStatuses(res)); diff != "" {
				t.Errorf("statuses (-want +got):\n%s", diff)
			}
			if res.ExitCode() != tt.exit {
				t.Errorf("exit = %d, want %d (%v)", res.ExitCode(), tt.exit, res.Err)
			}
			if len(s.asked) != tt.asked {
				t.Errorf("%d questions, want %d: %+v", len(s.asked), tt.asked, s.asked)
			}
			said := strings.Join(s.events, "\n")
			for _, q := range s.asked {
				said += "\n" + q.Text
			}
			if f := res.Clusters[1].Steps[0].Failure; f != nil {
				said += "\n" + f.Error
			}
			if !strings.Contains(said, tt.say) {
				t.Errorf("nothing says %q:\n%s", tt.say, said)
			}
			wantRolls := 1
			if tt.staging == StepSucceeded {
				wantRolls = 2
			}
			if got := e.rolls(); len(got) != wantRolls {
				t.Errorf("rolls = %v, want %d", got, wantRolls)
			}
		})
	}
}

// One question confirms the whole fleet, and it names every cluster.
func TestFleet_OneConfirmation(t *testing.T) {
	e := newEnv(t, fleetWorld(func(_ *fleettest.State, prod, _ *fleettest.Cluster) {
		prod.Nodegroup("ng-a").Status = fleettest.StatusDegraded
	}))
	req := fleetRoll()
	req.Policy.Yes = false
	s := &script{t: t, answers: []bool{true}}
	res, err := Run(testContext(t), e.deps, req, s.hooks())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.asked) != 1 {
		t.Fatalf("%d questions, want 1: %+v", len(s.asked), s.asked)
	}
	want := "Roll 1 nodegroup(s) in 1 cluster(s) (staging: ng-a), and leave out the blocked prod?"
	if s.asked[0].Text != want {
		t.Errorf("question = %q, want %q", s.asked[0].Text, want)
	}
	if got := clusterStatuses(res); got["prod/ng-a"] != StepBlocked || got["staging/ng-a"] != StepSucceeded {
		t.Errorf("statuses = %v", got)
	}
}
