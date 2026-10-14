package change

import (
	"context"
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

// The requests of a rollback of the upgraded engine world that
// engine_guard_test.go does not name.
const sentB32 = "eks:UpdateNodegroupVersion ng-b 1.32"

// rollEndsDegraded makes the next roll of a nodegroup succeed and leave
// the nodegroup DEGRADED, which the verification after the roll reports.
func rollEndsDegraded(name string) func(*fleettest.State, *fleettest.Cluster) {
	return func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Nodegroup(name).UpdateScripts = []fleettest.UpdateScript{{After: 30 * time.Second, ResourceStatus: fleettest.StatusDegraded}}
	}
}

// wantVerifyStopped checks that the run ended with exit code 5 at the
// roll of ng-a, whose step succeeded with an issue, and that every later
// step was not attempted.
func wantVerifyStopped(t *testing.T, res Result, issue string) {
	t.Helper()
	if res.Status != VerifyFailed || res.ExitCode() != exitcode.VerifyFailed {
		t.Errorf("status = %s, exit %d, err %v; want VerifyFailed, exit 5", res.Status, res.ExitCode(), res.Err)
	}
	a := stepResultOf(t, res, StepNodegroup, "ng-a")
	if a.Status != StepSucceeded || !strings.Contains(a.VerifyIssue, issue) {
		t.Errorf("ng-a = %s with the issue %q; want Succeeded with an issue that has %q", a.Status, a.VerifyIssue, issue)
	}
	steps := res.Clusters[0].Steps
	at := slices.IndexFunc(steps, func(s StepResult) bool { return s.Ref == a.Ref })
	for _, s := range steps[at+1:] {
		if s.Status != StepNotAttempted {
			t.Errorf("%s = %s after the issue; want NotAttempted", s.Ref, s.Status)
		}
	}
}

// In an upgrade, an issue that the check after a roll finds stops the
// run with exit code 5: the next nodegroup is not rolled, and the add-ons
// after the nodegroups stay.
func TestEngineVerify_UpgradeStopsAtAnIssue(t *testing.T) {
	stopped := []string{sentRefresh, sentCP33, sentCNI33, sentDNS33, sentA33}
	t.Run("the nodegroup is not ACTIVE after its roll", func(t *testing.T) {
		e := newEnv(t, engineWorld(rollEndsDegraded("ng-a")))
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
		wantVerifyStopped(t, res, "ng-a is DEGRADED")
		if diff := cmp.Diff(stopped, e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
		if res.Err == nil || !strings.Contains(res.Err.Error(), "refresh cluster upgrade prod --to 1.33") {
			t.Errorf("message = %v, want the command that goes on", res.Err)
		}
	})
	t.Run("the new nodes are not Ready", func(t *testing.T) {
		e := newEnv(t, engineWorld(func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{{After: 30 * time.Second, Roll: fleettest.RollScript{NodesNeverReady: true}}}
		}))
		e.withKube(t)
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
		wantVerifyStopped(t, res, "NodesReady")
		if diff := cmp.Diff(stopped, e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
	})
}

// In a rollback the nodegroups go first. An issue after a roll stops the
// run with exit code 5 before the add-ons and the control plane move.
func TestEngineVerify_RollbackStopsAtAnIssue(t *testing.T) {
	t.Run("after a roll of this run", func(t *testing.T) {
		e := newEnv(t, upgradedWorld(rollEndsDegraded("ng-a")))
		res := mustRun(t, e, rollbackOf(), Hooks{})
		wantVerifyStopped(t, res, "ng-a is DEGRADED")
		if diff := cmp.Diff([]string{sentA32}, e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
	})
	t.Run("a nodegroup that an earlier run rolled back and left DEGRADED", func(t *testing.T) {
		e := newEnv(t, upgradedWorld(func(s *fleettest.State, c *fleettest.Cluster) {
			c.Nodegroup("ng-a").Version = "1.32"
			degradeNodegroup("ng-a")(s, c)
		}))
		req := rollbackOf()
		// ng-b is left alone, so the only nodegroup step is the check of ng-a.
		req.SkipNodegroups = []string{"ng-b"}
		res := mustRun(t, e, req, Hooks{})
		wantVerifyStopped(t, res, "ng-a is DEGRADED")
		if len(e.sent()) != 0 {
			t.Errorf("requests %v; want none: the control plane does not move past the issue", e.sent())
		}
	})
}

// A nodegroup that cannot be read after its roll is incomplete data, exit
// code 4, and not an issue. The run stops there too.
func TestEngineVerify_UnreadIsIncomplete(t *testing.T) {
	tests := []struct {
		name string
		kube bool
		deny fleettest.Scope
	}{
		{"the nodegroup", false, fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup, Nodegroup: "ng-a"}},
		{"the pods", true, fleettest.Scope{Operation: fleettest.KubeListPods}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, engineWorld(nil))
			if tt.kube {
				e.withKube(t)
			}
			// The reads before the roll go through: the request of the
			// roll is the handshake, and every read after it is the check.
			e.onRequest(fleettest.EKSUpdateNodegroupVersion, func(int) {
				e.world.SetScopedFault(tt.deny, fleettest.Fault{Deny: true})
			})
			// The fault ends before the next step, so only the check that
			// could not read stops the run, and no later read does.
			hooks := Hooks{Checkpoint: func(context.Context, StepRef) (Checkpoint, error) {
				e.world.SetScopedFault(tt.deny, fleettest.Fault{})
				return Checkpoint{}, nil
			}}
			res := mustRun(t, e, upgradeTo("1.33"), hooks)
			if res.Status != Incomplete || res.ExitCode() != exitcode.Incomplete {
				t.Errorf("status = %s, exit %d, err %v; want Incomplete, exit 4", res.Status, res.ExitCode(), res.Err)
			}
			if a := stepResultOf(t, res, StepNodegroup, "ng-a"); a.VerifyIssue != "" {
				t.Errorf("ng-a has the issue %q; a check that cannot read is not an issue", a.VerifyIssue)
			}
			if !slices.ContainsFunc(res.Failures, func(f apidoc.Failure) bool { return f.Reason == apidoc.ReasonAccessDenied }) {
				t.Errorf("failures = %+v, want the denied read", res.Failures)
			}
			if !slices.ContainsFunc(res.Clusters[0].Verification, func(c health.Result) bool { return c.Outcome == health.Unreadable }) {
				t.Errorf("verification = %+v, want a check that is Unreadable", res.Clusters[0].Verification)
			}
			if slices.Contains(e.sent(), sentB33) || slices.Contains(e.sent(), sentProxy33) {
				t.Errorf("requests %v; the run went on past a roll that it could not check", e.sent())
			}
		})
	}
}

// --skip-verify leaves out the check after a roll, so a run goes on past
// a nodegroup that its roll left DEGRADED, and a nodegroup that is at the
// target and DEGRADED succeeds unchecked.
func TestEngineVerify_SkipVerify(t *testing.T) {
	t.Run("an upgrade goes on", func(t *testing.T) {
		e := newEnv(t, engineWorld(rollEndsDegraded("ng-a")))
		req := upgradeTo("1.33")
		req.SkipVerify = true
		res := mustRun(t, e, req, Hooks{})
		if res.Status != Succeeded || res.ExitCode() != exitcode.OK {
			t.Errorf("status = %s, exit %d, err %v; want Succeeded", res.Status, res.ExitCode(), res.Err)
		}
		if diff := cmp.Diff(wholeUpgrade(), e.sent()); diff != "" {
			t.Errorf("requests (-want +got):\n%s", diff)
		}
		if slices.ContainsFunc(res.Clusters[0].Verification, func(c health.Result) bool { return strings.HasPrefix(c.Name, "Node") }) {
			t.Errorf("verification = %+v, want no check of a nodegroup", res.Clusters[0].Verification)
		}
	})
	t.Run("a rollback goes on", func(t *testing.T) {
		e := newEnv(t, upgradedWorld(rollEndsDegraded("ng-a")))
		req := rollbackOf()
		req.SkipVerify = true
		res := mustRun(t, e, req, Hooks{})
		if res.Status != Succeeded || res.ExitCode() != exitcode.OK {
			t.Errorf("status = %s, exit %d, err %v; want Succeeded", res.Status, res.ExitCode(), res.Err)
		}
		if !slices.Contains(e.sent(), sentB32) || !slices.Contains(e.sent(), sentCP32) {
			t.Errorf("requests %v; want the roll of ng-b and the control plane", e.sent())
		}
	})
	t.Run("a DEGRADED nodegroup at the target is not checked", func(t *testing.T) {
		e := newEnv(t, engineWorld(func(s *fleettest.State, c *fleettest.Cluster) {
			c.Version = "1.33"
			c.Addon("vpc-cni").Version, c.Addon("coredns").Version, c.Addon("kube-proxy").Version = cni33, dns33, proxy33
			c.Nodegroup("ng-a").Version = "1.33"
			degradeNodegroup("ng-a")(s, c)
		}))
		req := upgradeTo("1.33")
		req.SkipNodegroups, req.SkipVerify = []string{"ng-b"}, true
		res := mustRun(t, e, req, Hooks{})
		if res.ExitCode() != exitcode.OK || len(e.sent()) != 0 {
			t.Errorf("status = %s, exit %d, requests %v; want exit 0 and no request", res.Status, res.ExitCode(), e.sent())
		}
	})
}
