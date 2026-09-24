package nodegroup

import (
	"context"
	"errors"
	"fmt"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/monitoring"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

// nodegroupStatus is the outcome of one nodegroup in a `nodegroup update`
// run. docs/concepts/output.md documents the values; later versions may add
// values.
type nodegroupStatus string

const (
	// ngStarted: the update started, and the run did not wait for it
	// (--no-wait).
	ngStarted nodegroupStatus = "Started"
	// ngSucceeded: the EKS update ended Successful.
	ngSucceeded nodegroupStatus = "Succeeded"
	// ngSkipped: the run did not roll the nodegroup on purpose; reason says
	// why.
	ngSkipped nodegroupStatus = "Skipped"
	// ngFailed: the update could not start, or the EKS update ended Failed.
	ngFailed nodegroupStatus = "Failed"
	// ngCancelled: the EKS update ended Cancelled.
	ngCancelled nodegroupStatus = "Cancelled"
	// ngInProgress: the update started, and it was still in progress when
	// the run stopped watching it (a timeout, an interrupt, or a status poll
	// that failed). The EKS update may still be running.
	ngInProgress nodegroupStatus = "InProgress"
	// ngNotAttempted: the run stopped before it reached this nodegroup.
	ngNotAttempted nodegroupStatus = "NotAttempted"
)

// The reasons of a Skipped nodegroup.
const (
	skipAlreadyUpdating = "AlreadyUpdating"
	skipAlreadyLatest   = "AlreadyLatest"
	skipCustomAMI       = "CustomAMI"
)

// nodegroupResult is one nodegroup's entry in the run document.
type nodegroupResult struct {
	Name     string          `json:"name" yaml:"name"`
	Status   nodegroupStatus `json:"status" yaml:"status"`
	UpdateID string          `json:"updateId,omitempty" yaml:"updateId,omitempty"`
	// Reason says why a Skipped nodegroup was skipped: AlreadyUpdating,
	// AlreadyLatest, or CustomAMI.
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`
	// Failure is set when the status is Failed, Cancelled, InProgress, or
	// NotAttempted. The same failure is in the document's failures.
	Failure *diag.Failure `json:"failure,omitempty" yaml:"failure,omitempty"`
}

// updateRun is what an update run did in one cluster: one result per
// selected nodegroup, in selection order, and the post-roll verification.
type updateRun struct {
	cluster      string
	region       string
	nodegroups   []nodegroupResult
	verification *PostRollVerification
	// readFailures are failures that belong to no nodegroup result, such as
	// a nodegroup that post-roll verification could not describe.
	readFailures []diag.Failure
}

// newUpdateRun returns an empty run for cluster.
func newUpdateRun(cluster, region string) updateRun {
	return updateRun{cluster: cluster, region: region, nodegroups: []nodegroupResult{}}
}

// failures lists every failure of the run: each nodegroup's, then the
// read failures.
func (r updateRun) failures() []diag.Failure {
	var out []diag.Failure
	for _, ng := range r.nodegroups {
		if ng.Failure != nil {
			out = append(out, *ng.Failure)
		}
	}
	return append(out, r.readFailures...)
}

// count returns how many nodegroups have status s.
func (r updateRun) count(s nodegroupStatus) int {
	n := 0
	for _, ng := range r.nodegroups {
		if ng.Status == s {
			n++
		}
	}
	return n
}

// started reports the names of the nodegroups whose update started, in
// order, whatever happened to the update after that.
func (r updateRun) started() []string {
	var out []string
	for _, ng := range r.nodegroups {
		if ng.UpdateID != "" {
			out = append(out, ng.Name)
		}
	}
	return out
}

// startFailed reports whether a nodegroup could not be read or its update
// could not start. The exit code for that is 4.
func (r updateRun) startFailed() bool {
	for _, ng := range r.nodegroups {
		if ng.Status == ngFailed && ng.UpdateID == "" {
			return true
		}
	}
	return false
}

// rollFailures counts the started updates that ended Failed or Cancelled,
// or could not be monitored. The exit code for them is 1 in a
// single-cluster run.
func (r updateRun) rollFailures() int {
	n := 0
	for _, ng := range r.nodegroups {
		if ng.UpdateID == "" {
			continue
		}
		switch ng.Status {
		case ngFailed, ngCancelled:
			n++
		case ngInProgress:
			if ng.Failure != nil && ng.Failure.Reason == diag.ReasonNotMonitored {
				n++
			}
		}
	}
	return n
}

// rollFailed reports whether rollFailures is not zero.
func (r updateRun) rollFailed() bool { return r.rollFailures() > 0 }

// withCluster sets f's cluster and region to the run's and returns it.
func (r updateRun) withCluster(f diag.Failure) *diag.Failure {
	f.Cluster = r.cluster
	if f.Region == "" {
		f.Region = r.region
	}
	return &f
}

// fail records nodegroup ng as Failed with the failure of op on err.
func (r *updateRun) fail(ng, op string, err error) {
	r.nodegroups = append(r.nodegroups, nodegroupResult{
		Name:    ng,
		Status:  ngFailed,
		Failure: r.withCluster(diag.FromError(diag.KindNodegroup, ng, op, err)),
	})
}

// skip records nodegroup ng as Skipped for reason.
func (r *updateRun) skip(ng, reason string) {
	r.nodegroups = append(r.nodegroups, nodegroupResult{Name: ng, Status: ngSkipped, Reason: reason})
}

// notAttempted records nodegroup ng as not attempted because ctx ended.
func (r *updateRun) notAttempted(ctx context.Context, ng string) {
	f := diag.New(diag.KindNodegroup, ng, diag.ReasonNotAttempted, "the run stopped before this nodegroup: "+context.Cause(ctx).Error())
	r.nodegroups = append(r.nodegroups, nodegroupResult{Name: ng, Status: ngNotAttempted, Failure: r.withCluster(f)})
}

// applyMonitorResult sets the final status of each started nodegroup from the
// monitor's view of its update. monErr is what MonitorUpdates returned: an
// update that is still in progress after ErrMonitorTimeout or ErrCancelled
// gets a Timeout or Interrupted failure.
func (r *updateRun) applyMonitorResult(updates []refreshTypes.UpdateProgress, monErr error) {
	byName := make(map[string]refreshTypes.UpdateProgress, len(updates))
	for _, u := range updates {
		byName[u.NodegroupName] = u
	}
	for i := range r.nodegroups {
		ng := &r.nodegroups[i]
		u, ok := byName[ng.Name]
		if !ok || ng.UpdateID == "" {
			continue
		}
		var f *diag.Failure
		switch {
		case u.MonitorErr != nil:
			ng.Status = ngInProgress
			nf := diag.FromError(diag.KindUpdate, ng.Name, diag.OpDescribeUpdate, u.MonitorErr)
			nf.Reason, nf.Retryable = diag.ReasonNotMonitored, diag.ReasonNotMonitored.Retryable()
			nf.Error = "could not poll the update's status; it may still be running: " + nf.Error
			f = &nf
		case u.Status == ekstypes.UpdateStatusSuccessful:
			ng.Status = ngSucceeded
		case u.Status == ekstypes.UpdateStatusFailed:
			ng.Status = ngFailed
			msg := u.ErrorMessage
			if msg == "" {
				msg = "the EKS update ended Failed"
			}
			nf := diag.New(diag.KindUpdate, ng.Name, diag.ReasonUpdateFailed, msg)
			f = &nf
		case u.Status == ekstypes.UpdateStatusCancelled:
			ng.Status = ngCancelled
			nf := diag.New(diag.KindUpdate, ng.Name, diag.ReasonUpdateCancelled, "the EKS update ended Cancelled")
			f = &nf
		default:
			ng.Status = ngInProgress
			switch {
			case errors.Is(monErr, monitoring.ErrMonitorTimeout):
				nf := diag.New(diag.KindUpdate, ng.Name, diag.ReasonTimeout, "--wait-timeout passed before the update finished; it may still be running")
				f = &nf
			case errors.Is(monErr, monitoring.ErrCancelled):
				nf := diag.New(diag.KindUpdate, ng.Name, diag.ReasonInterrupted, "interrupted before the update finished; it continues in the background")
				f = &nf
			}
		}
		if f != nil {
			f.UpdateID = ng.UpdateID
			ng.Failure = r.withCluster(*f)
		}
	}
}

// updateDocument is the -o json/yaml document of a single-cluster
// `nodegroup update`.
type updateDocument struct {
	Cluster      string                `json:"cluster" yaml:"cluster"`
	Nodegroups   []nodegroupResult     `json:"nodegroups" yaml:"nodegroups"`
	Verification *PostRollVerification `json:"verification,omitempty" yaml:"verification,omitempty"`
	// Health is the pre-flight verdict, when a check ran.
	Health   *health.HealthSummary `json:"health,omitempty" yaml:"health,omitempty"`
	Failures diag.List             `json:"failures" yaml:"failures"`
}

// newUpdateDocument builds the document of run, with the health verdict
// when a check ran. The document's failures are the run's plus the health
// check's.
func newUpdateDocument(run updateRun, summary *health.HealthSummary) updateDocument {
	return updateDocument{
		Cluster:      run.cluster,
		Nodegroups:   run.nodegroups,
		Verification: run.verification,
		Health:       summary,
		Failures:     runFailures(run, summary),
	}
}

// runFailures is the sorted failures of run plus those of the health
// verdict, when there is one.
func runFailures(run updateRun, summary *health.HealthSummary) diag.List {
	fs := diag.List(run.failures())
	if summary != nil {
		fs = append(fs, healthFailures(run, summary)...)
	}
	diag.Sort(fs)
	return fs
}

// healthFailures returns the health verdict's failures with the run's
// region set.
func healthFailures(run updateRun, summary *health.HealthSummary) []diag.Failure {
	out := make([]diag.Failure, 0, len(summary.Failures))
	for _, f := range summary.Failures {
		if f.Region == "" {
			f.Region = run.region
		}
		out = append(out, f)
	}
	return out
}

// noMatchError is the selection error for a pattern that matched no
// nodegroup.
type noMatchError struct {
	cluster, pattern string
}

func (e *noMatchError) Error() string {
	return fmt.Sprintf("no nodegroups in %s match %q", e.cluster, e.pattern)
}

// listNodegroupsError wraps a failed ListNodegroups call during selection.
type listNodegroupsError struct{ err error }

func (e *listNodegroupsError) Error() string { return e.err.Error() }
func (e *listNodegroupsError) Unwrap() error { return e.err }

// selectionFailure is the cluster failure for a selection error: the
// nodegroup list could not be read, or the pattern matched nothing.
func selectionFailure(cluster, region string, err error) diag.Failure {
	var nm *noMatchError
	var le *listNodegroupsError
	var f diag.Failure
	switch {
	case errors.As(err, &nm):
		f = diag.New(diag.KindCluster, cluster, diag.ReasonNotFound, err.Error())
	case errors.As(err, &le):
		f = diag.FromError(diag.KindCluster, cluster, diag.OpListNodegroups, le.err)
	default:
		f = diag.FromError(diag.KindCluster, cluster, "", err)
	}
	f.Region = region
	return f
}
