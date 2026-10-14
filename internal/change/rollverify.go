package change

import (
	"context"
	"errors"
	"slices"
	"strings"

	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// nodegroupActiveCheck is the name of the verification check whose
// findings are nodegroups. The findings of the other two are nodes and
// pods.
const nodegroupActiveCheck = "NodegroupActive"

// errUnreached is the error of a Kubernetes API that answered before a
// roll and could not be reached after it.
var errUnreached = errors.New("the Kubernetes API could not be reached, so the nodes and the pods were not verified")

// verifyAccess is what the verification of a batch needs beside its
// steps: the baseline that was taken before the first submit, the
// Kubernetes client of the cluster or why there is none, and the wait
// budget, which bounds the verification too.
type verifyAccess struct {
	base   health.Baseline
	kube   kubernetes.Interface
	noKube string
	wait   *budget
}

// verifyRoll verifies a batch after its wait: health.Verify over the
// nodegroups whose roll succeeded, against the one baseline of the batch.
// The caller has left it out already for --skip-verify and --no-wait, and
// for a batch in which nothing was started. It also does not run when:
//
//   - a roll that was started did not succeed: it failed, was cancelled,
//     timed out, or could not be followed. The nodes are then in a state
//     that the checks would only describe again.
//   - the context ended. An interrupt is then the result of the run, and
//     a deadline adds a failure: the rolls ended and were not verified.
//
// It reads the rolled nodegroups again, since the check of their status
// needs a read from after the wait. A check that fails is an issue: the
// steps that it is about count as exit code 5. A check that could not
// read adds a failure to the run, which is exit code 4. An issue wins
// over a failure, as the highest code does.
func (r *runner) verifyRoll(ctx context.Context, cp *ClusterPlan, ph Phase, started []int, results []StepResult, unfollowed bool, a verifyAccess) {
	if unfollowed {
		return
	}
	// A roll that did not succeed says why in its own row: one that is
	// still in progress after a deadline has its Timeout failure there, and
	// nothing ended that could be verified.
	var names []string
	for _, i := range started {
		if results[i].Status != StepSucceeded {
			return
		}
		names = append(names, ph.Steps[i].Ref.Name)
	}
	if ctx.Err() != nil {
		// Every roll ended, and the run ended before they were verified.
		// An interrupt is the result of the run. A deadline leaves the
		// run incomplete: nothing says that the new nodes are healthy.
		if !r.recordInterrupt() {
			f := apidoc.NewFailure(apidoc.FailureCluster, cp.Cluster, apidoc.ReasonTimeout,
				"--wait-timeout passed before the verification of "+cp.Cluster+"; its rolls ended and were not verified")
			f.Region = cp.Region
			r.addFailures([]apidoc.Failure{f})
			r.unverified = true
		}
		return
	}
	clients := r.d.AWS.Clients(cp.Region)
	fetch := r.d.Fetch
	if fetch == nil {
		fetch = &fleet.Fetcher{Now: r.d.Now}
	}
	// The error of the read is in the snapshot, as a failure, and the
	// nodegroup check then says that it could not read.
	snap, _ := fetch.Fresh().Fetch(ctx, clients, cp.Cluster, fleet.Need{OnlyNodegroups: names})
	if snap.Cluster.Name == "" {
		snap.Cluster.Name, snap.Cluster.Region = cp.Cluster, cp.Region
	}
	opts := health.WaitOptions{Interval: r.common.Limits.Poll, After: r.d.after, Now: r.d.now}
	if opts.Interval <= 0 {
		opts.Interval = DefaultPollInterval
	}
	if left, ok := a.wait.left(); ok {
		opts.Deadline = r.d.now().Add(left)
	}
	checks := health.Run(ctx, health.Verify(names, a.base, opts), &health.Input{
		Snapshot: snap, Targets: names, Clients: clients, Kube: a.kube, NoKube: a.noKube, Now: r.d.now(),
	})
	unreached := false
	checks = slices.DeleteFunc(checks, func(c health.Result) bool {
		if c.Name != health.AccessCheck {
			return false
		}
		// Without a client the gate said so already. With one, the API
		// answered before the roll and does not answer now.
		unreached = a.base.Taken && c.Outcome != health.Pass
		return true
	})
	r.verification = append(r.verification, r.verificationRows(ph, names, checks)...)
	r.emit(Event{Kind: VerifyReport, Phase: ph.Ref, Checks: checks})

	for _, c := range checks {
		if c.Outcome != health.Fail {
			continue
		}
		for _, i := range started {
			// The status check names the nodegroups that it is about.
			// A node or a pod belongs to the batch as a whole.
			if c.Name == nodegroupActiveCheck && !slices.Contains(c.Findings, ph.Steps[i].Ref.Name) {
				continue
			}
			if results[i].VerifyIssue == "" {
				results[i].VerifyIssue = c.Name + ": " + c.Message
				r.emit(Event{Kind: StepChanged, Phase: ph.Ref, Step: results[i].Ref, Result: &results[i]})
			}
		}
	}
	if r.recordInterrupt() {
		return
	}
	failures := slices.Concat(health.Failures(checks), snap.Failures)
	if unreached {
		failures = append(failures, fleet.KubeFailure(apidoc.FailureCluster, cp.Cluster, "reaching the Kubernetes API after the roll", errUnreached))
	}
	if len(failures) == 0 && health.VerifyExit(checks) == exitcode.Incomplete {
		var unread []string
		for _, c := range checks {
			if c.Outcome == health.Unreadable {
				unread = append(unread, c.Name+": "+c.Message)
			}
		}
		failures = append(failures, apidoc.NewFailure(apidoc.FailureCluster, cp.Cluster, apidoc.ReasonUnknown,
			"the verification after the roll could not read what it needs ("+strings.Join(unread, "; ")+")"))
	}
	health.Locate(failures, cp.Cluster, cp.Region)
	r.addFailures(failures)
	r.unverified = r.unverified || len(failures) > 0
}

// verificationRows returns the checks of one verification as rows of the
// result. A roll onto a new AMI verifies its batch once, and its rows
// keep the names of the checks. An upgrade and a rollback verify each
// nodegroup alone, and an upgrade over several hops verifies it once for
// each hop, so the name of a row there also has the nodegroup and the
// hop: "NodegroupActive/ng-a@1.33". The name of a row is its FIELD in the
// plain output, and no FIELD is on two rows.
func (r *runner) verificationRows(ph Phase, names []string, checks []health.Result) []health.Result {
	if r.req.kind().independent() || len(names) != 1 {
		return checks
	}
	suffix := "/" + names[0]
	if !ph.Ref.Hop.IsZero() {
		suffix += "@" + ph.Ref.Hop.String()
	}
	out := slices.Clone(checks)
	for i := range out {
		out[i].Name += suffix
	}
	return out
}
