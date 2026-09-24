package upgrade

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/diag"
)

// RunStatus is how an Execute run ended. docs/concepts/output.md documents
// the values; later versions may add values.
type RunStatus string

const (
	// RunSucceeded: every pending phase finished (or there was nothing to
	// do).
	RunSucceeded RunStatus = "Succeeded"
	// RunFailed: a phase failed. Report.Failure says why.
	RunFailed RunStatus = "Failed"
	// RunBlocked: a gate stopped the run before a phase started or while it
	// ran: the live readiness re-check, a pre-roll gate, or an addon's
	// post-update health gate. Report.Failure is set when the gate could not
	// read what it needed.
	RunBlocked RunStatus = "Blocked"
	// RunInterrupted: the user stopped the run (Ctrl+C or SIGTERM). Started
	// EKS updates keep running.
	RunInterrupted RunStatus = "Interrupted"
	// RunTimedOut: --wait-timeout passed. Started EKS updates keep running.
	RunTimedOut RunStatus = "TimedOut"
	// RunAborted: the user declined a phase confirmation.
	RunAborted RunStatus = "Aborted"
)

// addFailure appends f to the plan's failures unless the same failure is
// there already (a lookup repeated for each hop fails the same way).
func (p *Plan) addFailure(f diag.Failure) {
	if !slices.Contains(p.Failures, f) {
		p.Failures = append(p.Failures, f)
	}
}

// gateError is an error from a gate: the readiness re-check before a
// control-plane step, a pre-roll nodegroup gate, or an addon's post-update
// health gate. failures are the reads the gate could not make.
type gateError struct {
	err      error
	failures []diag.Failure
}

func (e *gateError) Error() string { return e.err.Error() }
func (e *gateError) Unwrap() error { return e.err }

// itemError names the item a phase error is about. The IAM action that
// failed is err's diag.WithOperation tag. failure, when set, is the failure
// to report as is.
type itemError struct {
	kind     diag.Kind
	name     string
	updateID string
	failure  *diag.Failure
	err      error
}

func (e *itemError) Error() string { return e.err.Error() }
func (e *itemError) Unwrap() error { return e.err }

// onItem wraps err as an error about the item kind/name, from the IAM
// action op. It returns nil for a nil err.
func onItem(kind diag.Kind, name, op string, err error) error {
	if err == nil {
		return nil
	}
	if op != "" {
		err = diag.WithOperation(op, err)
	}
	return &itemError{kind: kind, name: name, err: err}
}

// updateEndedError is an EKS update that ended Failed or Cancelled.
type updateEndedError struct {
	what, updateID string
	status         ekstypes.UpdateStatus
	details        string
}

func (e *updateEndedError) Error() string {
	return fmt.Sprintf("%s %s: %s", e.what, strings.ToLower(string(e.status)), e.details)
}

// stopState returns how a run that Execute stopped with err ended, and the
// failure behind it. gate is set when err came from a phase's precheck: a
// precheck error of any kind stops the run before the phase starts, so it is
// Blocked (fail closed), with the read failure when there is one.
func stopState(ctx context.Context, cluster string, err error, gate bool) (RunStatus, *diag.Failure) {
	if errors.Is(err, ErrAborted) {
		return RunAborted, nil
	}
	if ctx.Err() != nil {
		status, reason := RunInterrupted, diag.ReasonInterrupted
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status, reason = RunTimedOut, diag.ReasonTimeout
		}
		f := itemFailure(cluster, err)
		f.Reason, f.Retryable = reason, reason.Retryable()
		return status, &f
	}
	var ge *gateError
	if errors.As(err, &ge) {
		if len(ge.failures) > 0 {
			f := ge.failures[0]
			return RunBlocked, &f
		}
		return RunBlocked, nil
	}
	f := itemFailure(cluster, err)
	if gate {
		return RunBlocked, &f
	}
	return RunFailed, &f
}

// itemFailure builds the failure for err: about the item an itemError
// names, else about the cluster.
func itemFailure(cluster string, err error) diag.Failure {
	var ie *itemError
	if !errors.As(err, &ie) {
		return diag.FromError(diag.KindCluster, cluster, "", err)
	}
	if ie.failure != nil {
		return *ie.failure
	}
	var f diag.Failure
	var ended *updateEndedError
	if errors.As(ie.err, &ended) {
		reason := diag.ReasonUpdateFailed
		if ended.status == ekstypes.UpdateStatusCancelled {
			reason = diag.ReasonUpdateCancelled
		}
		f = diag.New(diag.KindUpdate, ie.name, reason, ended.Error())
		f.UpdateID = ended.updateID
	} else {
		f = diag.FromError(ie.kind, ie.name, "", ie.err)
		f.UpdateID = ie.updateID
	}
	// The control plane's items (its version update) are named by the
	// cluster itself.
	if ie.kind != diag.KindCluster {
		f.Cluster = cluster
	}
	return f
}
