package change

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// verifyRun runs a roll of ng-a and ng-b with Kubernetes access and no
// gate, so every Kubernetes request is one of the baseline or of the
// verification. It returns the Kubernetes requests that had arrived when
// each roll was sent, and all of them at the end.
func verifyRun(t *testing.T, seed func(s *fleettest.State, c *fleettest.Cluster), edit func(r *RollRequest), prepare func(e *env)) (res Result, atRoll [][]string, all []string) {
	t.Helper()
	e := newEnv(t, rollWorld(seed))
	srv := e.withKube(t)
	e.onRequest(fleettest.EKSUpdateNodegroupVersion, func(int) { atRoll = append(atRoll, kubeOps(srv)) })
	if prepare != nil {
		prepare(e)
	}
	req := rollOf("ng-a", "ng-b")
	req.Policy.Skip = true
	if edit != nil {
		edit(&req)
	}
	res, err := Run(testContext(t), e.deps, req, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	return res, atRoll, kubeOps(srv)
}

// Safety rule 7, the cases with no verification: --skip-verify,
// --no-wait, nothing started, a started roll that did not succeed, and a
// context that ended. The first three take no baseline either.
func TestRoll_VerificationSuppressed(t *testing.T) {
	failB := func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Nodegroup("ng-b").UpdateScripts = []fleettest.UpdateScript{{After: 15 * time.Second, Result: fleettest.UpdateFailed}}
	}
	never := func(s *fleettest.State, _ *fleettest.Cluster) { s.DefaultUpdate = fleettest.UpdateScript{Never: true} }
	tests := []struct {
		name string
		seed func(s *fleettest.State, c *fleettest.Cluster)
		edit func(r *RollRequest)
		// baseline says that the run lists the nodes and the Pending pods
		// once, before the first roll.
		baseline bool
		exit     int
	}{
		{name: "skip-verify", edit: func(r *RollRequest) { r.SkipVerify = true }},
		{name: "no-wait", edit: func(r *RollRequest) { r.NoWait = true }},
		{name: "nothing started", edit: func(r *RollRequest) { r.Nodegroups = []string{"ng-c"} }},
		{name: "a started roll failed", seed: failB, baseline: true, exit: exitcode.Incomplete},
		{name: "the wait timed out", seed: never, edit: func(r *RollRequest) { r.Limits.Wait = time.Minute }, baseline: true, exit: exitcode.Incomplete},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, _, all := verifyRun(t, tt.seed, tt.edit, nil)
			if got := res.Clusters[0].Verification; len(got) != 0 {
				t.Errorf("verification = %+v, want none", got)
			}
			want := 0
			if tt.baseline {
				want = 2
			}
			if len(all) != want {
				t.Errorf("Kubernetes requests = %v, want %d: the baseline alone, or nothing", all, want)
			}
			if res.ExitCode() != tt.exit {
				t.Errorf("exit = %d, want %d (%v)", res.ExitCode(), tt.exit, res.Err)
			}
		})
	}
}

// Safety rule 7, the cases with a verification: one baseline before the
// first roll, the checks over the rolls that succeeded, new nodes that
// are not Ready as exit code 5, and a verification that cannot read as
// exit code 4.
func TestRoll_Verification(t *testing.T) {
	t.Run("one baseline, then the checks", func(t *testing.T) {
		res, atRoll, all := verifyRun(t, nil, nil, nil)
		if res.ExitCode() != 0 || res.Status != Succeeded {
			t.Fatalf("status = %s, exit %d (%v)", res.Status, res.ExitCode(), res.Err)
		}
		// Both lists of the baseline are in before the first roll, and
		// nothing more is read before the second one.
		if len(atRoll) != 2 || len(atRoll[0]) != 2 || len(atRoll[1]) != 2 {
			t.Fatalf("Kubernetes requests at each roll = %v, want the two lists of the baseline at both", atRoll)
		}
		joined := strings.Join(atRoll[0], " ")
		if !strings.Contains(joined, string(fleettest.KubeListNodes)) || !strings.Contains(joined, string(fleettest.KubeListPods)) {
			t.Errorf("the baseline = %v, want a list of the nodes and one of the pods", atRoll[0])
		}
		if len(all) <= 2 {
			t.Errorf("Kubernetes requests = %v, want the lists of the verification after the baseline", all)
		}
		var names []string
		for _, c := range res.Clusters[0].Verification {
			names = append(names, c.Name+"="+string(c.Outcome))
		}
		want := []string{"NodegroupActive=Pass", "NodesReady=Pass", "NewPendingPods=Pass"}
		if !slices.Equal(names, want) {
			t.Errorf("verification = %v, want %v", names, want)
		}
		if msg := res.Clusters[0].Verification[0].Message; msg != "ng-a, ng-b ACTIVE" {
			t.Errorf("the status check covers %q, want both rolls", msg)
		}
	})
	t.Run("new nodes not Ready", func(t *testing.T) {
		res, _, _ := verifyRun(t, func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{{After: 30 * time.Second, Roll: fleettest.RollScript{NodesNeverReady: true}}}
		}, nil, nil)
		if res.ExitCode() != exitcode.VerifyFailed || res.Status != VerifyFailed {
			t.Errorf("status = %s, exit %d, want VerifyFailed, exit 5 (%v)", res.Status, res.ExitCode(), res.Err)
		}
		i := slices.IndexFunc(res.Clusters[0].Verification, func(c health.Result) bool { return c.Name == "NodesReady" })
		if i < 0 || res.Clusters[0].Verification[i].Outcome != health.Fail {
			t.Errorf("verification = %+v, want NodesReady to fail", res.Clusters[0].Verification)
		}
		if s := res.Clusters[0].Steps[0]; s.Status != StepSucceeded || !strings.Contains(s.VerifyIssue, "NodesReady") {
			t.Errorf("ng-a = %s with the issue %q, want Succeeded with the NodesReady issue", s.Status, s.VerifyIssue)
		}
	})
	t.Run("a nodegroup that is not ACTIVE", func(t *testing.T) {
		res, _, _ := verifyRun(t, func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Nodegroup("ng-b").UpdateScripts = []fleettest.UpdateScript{{After: 30 * time.Second, ResourceStatus: fleettest.StatusDegraded}}
		}, nil, nil)
		if res.ExitCode() != exitcode.VerifyFailed {
			t.Errorf("exit = %d, want 5 (%v)", res.ExitCode(), res.Err)
		}
		// The issue is on the nodegroup that it is about.
		if a, b := res.Clusters[0].Steps[0], res.Clusters[0].Steps[1]; a.VerifyIssue != "" || !strings.Contains(b.VerifyIssue, "ng-b is DEGRADED") {
			t.Errorf("issues: ng-a %q, ng-b %q; want one on ng-b alone", a.VerifyIssue, b.VerifyIssue)
		}
	})
}

// Safety rule 7, a verification that cannot read: exit code 4, with the
// failure of the read. Without Kubernetes access from the start, the
// checks of the nodes and the pods are skipped, which is no failure: the
// gate said so before the roll.
func TestRoll_VerificationUnreadable(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		res, _, _ := verifyRun(t, nil, nil, func(e *env) {
			// The pods can be listed for the baseline, and not after it.
			e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
				if n == 1 {
					e.world.SetFault(fleettest.KubeListPods, fleettest.Fault{Deny: true})
				}
			})
		})
		if res.ExitCode() != exitcode.Incomplete || res.Status != Incomplete {
			t.Errorf("status = %s, exit %d, want Incomplete, exit 4 (%v)", res.Status, res.ExitCode(), res.Err)
		}
		i := slices.IndexFunc(res.Clusters[0].Verification, func(c health.Result) bool { return c.Name == "NewPendingPods" })
		if i < 0 || res.Clusters[0].Verification[i].Outcome != health.Unreadable {
			t.Errorf("verification = %+v, want NewPendingPods unreadable", res.Clusters[0].Verification)
		}
		if len(res.Failures) == 0 {
			t.Error("the result has no failure for the read that failed")
		}
	})
	t.Run("the nodegroup cannot be read", func(t *testing.T) {
		res, _, _ := verifyRun(t, nil, nil, func(e *env) {
			// The describes of the plan and of the fresh reads go through.
			e.onRequest(fleettest.EKSDescribeUpdate, func(n int) {
				if n == 1 {
					e.world.SetFault(fleettest.EKSDescribeNodegroup, fleettest.Fault{Deny: true})
				}
			})
		})
		if res.ExitCode() != exitcode.Incomplete {
			t.Errorf("exit = %d, want 4 (%v)", res.ExitCode(), res.Err)
		}
		if !slices.ContainsFunc(res.Failures, func(f apidoc.Failure) bool { return f.Reason == apidoc.ReasonAccessDenied }) {
			t.Errorf("failures = %+v, want the denied read", res.Failures)
		}
	})
	t.Run("no Kubernetes access", func(t *testing.T) {
		e := newEnv(t, rollWorld(nil))
		res, err := Run(testContext(t), e.deps, rollOf("ng-a"), Hooks{})
		if err != nil || res.ExitCode() != 0 {
			t.Fatalf("run: %v, exit %d (%v)", err, res.ExitCode(), res.Err)
		}
		var names []string
		for _, c := range res.Clusters[0].Verification {
			names = append(names, c.Name+"="+string(c.Outcome))
		}
		want := []string{"NodegroupActive=Pass", "NodesReady=Skipped", "NewPendingPods=Skipped"}
		if !slices.Equal(names, want) {
			t.Errorf("verification = %v, want %v", names, want)
		}
	})
}

// Safety rule 7, the last case with no verification: the run is
// interrupted when the last roll has ended. Nothing more is read, and the
// interrupt is the result.
func TestRoll_NoVerificationAfterAnInterrupt(t *testing.T) {
	e := newEnv(t, rollWorld(nil))
	srv := e.withKube(t)
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	req := rollOf("ng-a")
	req.Policy.Skip = true
	res, err := Run(ctx, e.deps, req, Hooks{Event: func(ev Event) {
		if ev.Kind == StepChanged && ev.Result.Status == StepSucceeded {
			cancel()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Clusters[0].Verification) != 0 || len(kubeOps(srv)) != 2 {
		t.Errorf("verification = %+v, Kubernetes requests %v; want none after the baseline", res.Clusters[0].Verification, kubeOps(srv))
	}
	if res.ExitCode() != exitcode.Failure || res.Status != Interrupted {
		t.Errorf("status = %s, exit %d, want Interrupted, exit 1", res.Status, res.ExitCode())
	}
}

// afterUnfollowedRoll returns the requests that the fake may get after a
// roll that cannot be followed: nothing, or with polls the fresh read and
// the roll of the next nodegroup, and then only its polls and the Auto
// Scaling watch beside them, as they are in after. A verification read in
// after is not in the result.
func afterUnfollowedRoll(t *testing.T, after []string, polls bool) []string {
	t.Helper()
	if !polls {
		return nil
	}
	want := []string{string(fleettest.EKSDescribeNodegroup), string(fleettest.EKSUpdateNodegroupVersion)}
	beside := []string{string(fleettest.EKSDescribeUpdate), string(fleettest.AutoScalingDescribeScalingActivities)}
	polled := false
	for _, op := range after[min(len(want), len(after)):] {
		polled = polled || op == beside[0]
		if slices.Contains(beside, op) {
			want = append(want, op)
		}
	}
	if !polled {
		t.Errorf("the next roll was not polled: %v", after)
	}
	return want
}

// A roll that EKS accepts with no update ID cannot be followed: its step
// is InProgress with the reason NotMonitored, the message says that the
// roll may be running, and the run exits 4. Nothing shows that the roll
// succeeded, so nothing is verified, also when another roll of the batch
// was followed to its end: after the baseline, the run lists nothing in
// Kubernetes, and after its last roll it only polls.
func TestRoll_NoUpdateID(t *testing.T) {
	tests := []struct {
		name    string
		targets []string
		want    map[string]StepStatus
		polls   bool
	}{
		{name: "the only roll", targets: []string{"ng-a"}, want: map[string]StepStatus{"prod/ng-a": StepInProgress}},
		{name: "beside a roll that is followed", targets: []string{"ng-a", "ng-b"}, polls: true,
			want: map[string]StepStatus{"prod/ng-a": StepInProgress, "prod/ng-b": StepSucceeded}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, rollWorld(nil))
			srv := e.withKube(t)
			// The front answers the roll of ng-a itself, so the fake does
			// not log it.
			sent := -1
			var baseline []string
			deps := e.intercept(t, func(r *http.Request) bool {
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/node-groups/ng-a/update-version") {
					return false
				}
				sent, baseline = len(e.srv.Requests()), kubeOps(srv)
				return true
			})
			deps.Kube = e.deps.Kube
			req := rollOf(tt.targets...)
			req.Policy.Skip = true
			res, err := Run(testContext(t), deps, req, Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			if sent < 0 {
				t.Fatal("the roll of ng-a was not sent")
			}
			if diff := cmp.Diff(tt.want, clusterStatuses(res)); diff != "" {
				t.Errorf("statuses (-want +got):\n%s", diff)
			}
			a := res.Clusters[0].Steps[0]
			if a.Failure == nil || a.Failure.Reason != apidoc.ReasonNotMonitored || a.UpdateID != "" ||
				!strings.Contains(a.Failure.Error, "returned no update ID") || !strings.Contains(a.Failure.Error, "may still be running") {
				t.Errorf("ng-a: update %q, failure %+v; want NotMonitored, no update ID, and a message that the roll may still be running", a.UpdateID, a.Failure)
			}
			if res.ExitCode() != exitcode.Incomplete || res.Status != Incomplete {
				t.Errorf("status = %s, exit %d; want Incomplete, exit 4", res.Status, res.ExitCode())
			}
			if len(baseline) == 0 {
				t.Error("no baseline was taken before the roll")
			}
			if diff := cmp.Diff(baseline, kubeOps(srv)); diff != "" {
				t.Errorf("Kubernetes requests after the roll of ng-a (-at the roll +at the end):\n%s", diff)
			}
			if diff := cmp.Diff(afterUnfollowedRoll(t, e.operations(sent), tt.polls), e.operations(sent)); diff != "" {
				t.Errorf("AWS requests after the roll of ng-a (-want +got):\n%s", diff)
			}
			if res.Clusters[0].Verification != nil {
				t.Errorf("verification = %+v, want none", res.Clusters[0].Verification)
			}
		})
	}
}
