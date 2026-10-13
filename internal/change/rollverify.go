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
	if ctx.Err() != nil {
		// The rolls ended, and the run ended before they were verified.
		// An interrupt is the result of the run. A deadline leaves the
		// run incomplete: nothing says that the new nodes are healthy.
		if !r.interruptedNow() {
			f := apidoc.NewFailure(apidoc.FailureCluster, cp.Cluster, apidoc.ReasonTimeout,
				"--wait-timeout passed before the verification; the rolls ended and were not verified")
			f.Region = cp.Region
			r.addFailures([]apidoc.Failure{f})
		}
		return
	}
	var names []string
	for _, i := range started {
		if results[i].Status != StepSucceeded {
			return
		}
		names = append(names, ph.Steps[i].Ref.Name)
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
	r.verification = append(r.verification, checks...)
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
	if r.interruptedNow() {
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
	for i := range failures {
		if failures[i].Cluster == "" && failures[i].Kind != apidoc.FailureCluster {
			failures[i].Cluster = cp.Cluster
		}
		if failures[i].Region == "" {
			failures[i].Region = cp.Region
		}
	}
	r.addFailures(failures)
}
