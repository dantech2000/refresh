// Package health runs the checks that say whether a cluster can take a
// change, and turns their results into one verdict for every changing
// command: Go, Ask, or Block.
//
// A check is a value with a Run function. The runner gives every check the
// same Input, skips a check whose needs the input lacks, and runs the rest
// at once. Gate folds the results into a Verdict under the policy of the
// command's flags. Nothing here writes to a terminal.
package health

import (
	"context"
	"slices"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	metricsv1beta1 "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
)

// Check is one health check. The checks are a closed list in this package,
// so a struct with a function is enough: a table of values, and nothing to
// vary between implementations. A test builds a Check with its own Run.
type Check struct {
	Name string
	// Blocking says that a Fail blocks the change. A Fail of a check that
	// is not blocking is a warning.
	Blocking bool
	// FailClosed says that an Unreadable blocks too: the check reads what
	// a roll can break. Without it an Unreadable asks.
	FailClosed bool
	// Forceable says that --force turns a Fail or an Unreadable of this
	// check into a warning. Only the drain-blocker check sets it.
	Forceable bool
	Needs     Needs
	Run       func(ctx context.Context, in *Input) Result
}

// Needs says what a check reads. The runner skips a check whose input
// lacks one of them, and the check never runs.
type Needs struct {
	// Kube: the check reads the Kubernetes API.
	Kube bool
	// Metrics: the check reads the metrics.k8s.io API.
	Metrics bool
}

// Input is everything the checks read. A command builds it once per run,
// and nothing sets a field after Run starts.
type Input struct {
	// Snapshot is the whole cluster as the command read it, with every
	// nodegroup: the checks judge the cluster that absorbs a drain, not
	// only the targets.
	Snapshot *fleet.Snapshot
	// Targets names the nodegroups about to roll. Empty means the whole
	// cluster. Only the drain-blocker check and the target rule of the
	// node check read it.
	Targets []string
	// Clients are the AWS clients of the region of the cluster.
	Clients *awsx.Clients
	// Kube is nil without Kubernetes access. NoKube then says why, in the
	// words of a message to the user.
	Kube   kubernetes.Interface
	NoKube string
	// Metrics is nil without a metrics.k8s.io client.
	Metrics metricsv1beta1.NodeMetricsInterface
	// Now is the time of the run.
	Now time.Time

	// reads are the Kubernetes reads of one run, shared between the
	// checks. Run sets it.
	reads *reads
}

// Outcome is what a check found.
type Outcome string

// The outcomes.
const (
	Pass Outcome = "Pass"
	Warn Outcome = "Warn"
	Fail Outcome = "Fail"
	// Skipped: a need of the check is missing, or the check does not
	// apply to the cluster.
	Skipped Outcome = "Skipped"
	// Unreadable: a read that the verdict needs failed. Failures names it.
	Unreadable Outcome = "Unreadable"
)

// Result is what one check found. The runner fills Name, Blocking,
// FailClosed, and Forceable from the Check, so a check cannot contradict
// its own declaration.
type Result struct {
	Name       string
	Outcome    Outcome
	Blocking   bool
	FailClosed bool
	Forceable  bool
	// Message is one line. For Skipped it is the reason. For Unreadable
	// it says what was read and what was not.
	Message string
	Details []string
	// Findings are the named things behind a Warn or a Fail, sorted: node
	// names, "namespace/pod", "namespace/pdb". Worse compares them. A check
	// that measures a number leaves them empty.
	Findings []string
	// Failures are the reads that failed. Non-empty for Unreadable, and
	// for a Fail whose reads partly failed.
	Failures []apidoc.Failure
}

// AccessCheck is the name of the result that the runner adds about the
// Kubernetes access of the run.
const AccessCheck = "Kubernetes"

// Run runs checks against in, all at once, and returns one Result per
// check in the order of checks. A check whose Needs the input lacks
// returns Skipped with in.NoKube or "no metrics client" as its message. A
// check whose context ends returns Unreadable with the reason of the
// context.
//
// When a check of the run needs Kubernetes, a result named AccessCheck
// follows the others: a warning without a client, or when a client is set
// and the API could not be reached at all: no read got an answer, every
// read that failed could not connect, and the run's context has not
// ended. The Kubernetes checks are then Skipped: an unreachable API is one
// question, not a block. A timeout, the user's own deadline, or a read
// that failed after another answered keeps the checks Unreadable, with
// their failures, so a fail-closed check blocks.
func Run(ctx context.Context, checks []Check, in *Input) []Result {
	run := *in
	run.reads = newReads(ctx, &run)
	results := make([]Result, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		if r, skipped := skip(c, &run); skipped {
			results[i] = r
			continue
		}
		wg.Go(func() { results[i] = c.Run(ctx, &run) })
	}
	wg.Wait()
	for i, c := range checks {
		results[i].Name, results[i].Blocking, results[i].FailClosed, results[i].Forceable = c.Name, c.Blocking, c.FailClosed, c.Forceable
		if results[i].Outcome == "" {
			results[i] = unreadableCheck(ctx, results[i])
		}
		Locate(results[i].Failures, in.Snapshot.Cluster.Name, in.Snapshot.Cluster.Region)
	}
	if !slices.ContainsFunc(checks, func(c Check) bool { return c.Needs.Kube }) {
		return results
	}
	access, unreachable := run.reads.access()
	if unreachable {
		for i, c := range checks {
			if results[i].Outcome == Unreadable && (c.Needs.Kube || kubeConnectFailures(results[i].Failures)) {
				results[i] = Result{
					Name: c.Name, Blocking: c.Blocking, FailClosed: c.FailClosed, Forceable: c.Forceable,
					Outcome: Skipped, Message: access.Message,
				}
			}
		}
	}
	return append(results, access)
}

// kubeConnectFailures reports whether every failure is a Kubernetes read
// that did not connect: no IAM action, and a network error. A timeout is
// not a connection failure.
func kubeConnectFailures(fs []apidoc.Failure) bool {
	if len(fs) == 0 {
		return false
	}
	for _, f := range fs {
		if f.Operation != "" || f.Reason != apidoc.ReasonNetworkError {
			return false
		}
	}
	return true
}

// skip returns the Skipped result of a check whose needs in lacks.
func skip(c Check, in *Input) (Result, bool) {
	switch {
	case c.Needs.Kube && in.Kube == nil:
		return Result{Outcome: Skipped, Message: in.NoKube}, true
	case c.Needs.Metrics && in.Metrics == nil:
		return Result{Outcome: Skipped, Message: "no metrics client"}, true
	}
	return Result{}, false
}

// unreadableCheck returns the result of a check that returned nothing,
// which happens when its context ended before it decided.
func unreadableCheck(ctx context.Context, r Result) Result {
	err := ctx.Err()
	if err == nil {
		r.Outcome, r.Message = Unreadable, "the check returned no result"
		return r
	}
	f := fleet.FailureFromError(apidoc.FailureCluster, "", err)
	r.Outcome, r.Message = Unreadable, "not checked: "+f.Error
	r.Failures = append(r.Failures, f)
	return r
}

// Document returns the document form of a result.
func Document(r Result) apidoc.HealthResult {
	return apidoc.HealthResult{
		Name:     r.Name,
		Outcome:  apidoc.HealthOutcome(r.Outcome),
		Blocking: r.Blocking,
		Message:  r.Message,
		Details:  nonNil(r.Details),
		Findings: nonNil(r.Findings),
	}
}

// Documents returns the document form of every result.
func Documents(results []Result) []apidoc.HealthResult {
	out := make([]apidoc.HealthResult, len(results))
	for i, r := range results {
		out[i] = Document(r)
	}
	return out
}

// Doc returns the health part of a describe document: the results and
// the worst outcome among them, in the order Fail, Unreadable, Warn, Pass.
// A Skipped check does not count, and a run in which no check ran is
// Skipped: what was not measured is not healthy.
func Doc(results []Result) apidoc.Health {
	worst := Skipped
	for _, r := range results {
		if r.Outcome == Skipped {
			continue
		}
		if worst == Skipped || severity(r.Outcome) > severity(worst) {
			worst = r.Outcome
		}
	}
	return apidoc.Health{Summary: apidoc.HealthOutcome(worst), Checks: Documents(results)}
}

// severity orders the outcomes for a summary. Skipped and Pass are 0.
func severity(o Outcome) int {
	switch o {
	case Warn:
		return 1
	case Unreadable:
		return 2
	case Fail:
		return 3
	}
	return 0
}

// Failures returns the failures of every result, in order.
func Failures(results []Result) []apidoc.Failure {
	var out []apidoc.Failure
	for _, r := range results {
		out = append(out, r.Failures...)
	}
	return out
}

// nonNil returns s, or an empty list for nil: a document prints [] and
// never null.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Locate says where each of failures happened, for the ones that do not
// say it yet: the cluster, and its region. A failure of the cluster itself
// names the cluster already, in its name. Run does this for the failures
// of its checks, and a caller does it for the failures that it adds beside
// them, so that every failure of one verification reads the same.
func Locate(failures []apidoc.Failure, cluster, region string) {
	for i := range failures {
		f := &failures[i]
		if f.Cluster == "" && f.Kind != apidoc.FailureCluster {
			f.Cluster = cluster
		}
		if f.Region == "" {
			f.Region = region
		}
	}
}
