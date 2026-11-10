package change

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/health"
)

// waitOutcome is the one table of a wait that did not succeed, for a step
// of a run and for the step of a watch. A failure is about the update only
// when there is an update ID, the control plane is named as such, and a
// budget that ran out is named by its cause.
func TestWaitOutcome(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	prod := Target{Region: east, Cluster: "prod"}
	ng := StepRef{Phase: PhaseRef{Target: prod, Kind: PhaseRoll}, Kind: StepNodegroup, Name: "web"}
	cp := StepRef{Phase: PhaseRef{Target: prod, Kind: PhaseControlPlane}, Kind: StepControlPlane}
	ended := func(cause error) context.Context {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		return ctx
	}
	live := context.Background()
	tests := []struct {
		name       string
		ctx        context.Context
		ref        StepRef
		updateID   string
		err        error
		status     StepStatus
		reason     apidoc.Reason
		kind       apidoc.FailureKind
		message    string
		finishedAt bool
	}{
		{name: "the update failed", ctx: live, ref: ng, updateID: "u-1",
			err:    &UpdateError{Resource: ng.Resource(), UpdateID: "u-1", Status: "Failed"},
			status: StepFailed, reason: apidoc.ReasonUpdateFailed, kind: apidoc.FailureUpdate, finishedAt: true},
		{name: "the update was cancelled", ctx: live, ref: ng, updateID: "u-1",
			err:    &UpdateError{Resource: ng.Resource(), UpdateID: "u-1", Status: "Cancelled"},
			status: StepCancelled, reason: apidoc.ReasonUpdateCancelled, kind: apidoc.FailureUpdate, finishedAt: true},
		{name: "the update could not be followed", ctx: live, ref: ng, updateID: "u-1",
			err:    &UnobservedError{Resource: ng.Resource(), Err: errors.New("denied")},
			status: StepInProgress, reason: apidoc.ReasonNotMonitored, kind: apidoc.FailureUpdate},
		{name: "--wait-timeout", ctx: ended(ErrWaitTimeout), ref: ng, updateID: "u-1", err: context.Canceled,
			status: StepInProgress, reason: apidoc.ReasonTimeout, kind: apidoc.FailureUpdate,
			message: "--wait-timeout passed before the update of nodegroup web finished; it may still be running"},
		{name: "--timeout, in the first reads of a watch", ctx: ended(ErrReadTimeout), ref: ng, updateID: "u-1", err: context.Canceled,
			status: StepInProgress, reason: apidoc.ReasonTimeout, kind: apidoc.FailureUpdate,
			message: "the --timeout budget ran out before the update of nodegroup web finished; it may still be running"},
		{name: "an interrupt, with no update ID", ctx: ended(context.Canceled), ref: cp, err: context.Canceled,
			status: StepInProgress, reason: apidoc.ReasonInterrupted, kind: apidoc.FailureCluster,
			message: "interrupted while the update of the control plane of prod was running; it may still be running"},
		{name: "the done function", ctx: live, ref: ng, err: errors.New("the add-on update failed: status DEGRADED"),
			status: StepFailed, reason: apidoc.ReasonUpdateFailed, kind: apidoc.FailureNodegroup, finishedAt: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := waitOutcome(tt.ctx, prod, StepResult{Ref: tt.ref, UpdateID: tt.updateID}, tt.err, at)
			f := got.Failure
			if got.Status != tt.status || f == nil || f.Reason != tt.reason || f.Kind != tt.kind {
				t.Fatalf("status %s, failure %+v; want %s with a %s failure of kind %s", got.Status, f, tt.status, tt.reason, tt.kind)
			}
			if f.Cluster != "prod" || f.Region != east || f.UpdateID != tt.updateID {
				t.Errorf("failure at %s/%s of update %q, want prod/%s of %q", f.Region, f.Cluster, f.UpdateID, east, tt.updateID)
			}
			if tt.message != "" && f.Error != tt.message {
				t.Errorf("message = %q, want %q", f.Error, tt.message)
			}
			if got.FinishedAt.IsZero() == tt.finishedAt {
				t.Errorf("finished at %v, want finished %t", got.FinishedAt, tt.finishedAt)
			}
		})
	}
}

// The result of a watch has the status of a run with the same step and
// failures: a verification that could not read is incomplete, and an
// issue wins over it.
func TestWatchResult(t *testing.T) {
	prod := Target{Region: east, Cluster: "prod"}
	r := Resource{Target: prod, Kind: StepNodegroup, Name: "web"}
	ref := StepRef{Phase: PhaseRef{Target: prod, Kind: PhaseRoll}, Kind: StepNodegroup, Name: "web"}
	plan := &Plan{Kind: NodegroupUpdate}
	unread := []apidoc.Failure{apidoc.NewFailure(apidoc.FailureCluster, "prod", apidoc.ReasonUnknown, "the verification after the roll could not read what it needs")}
	interrupted := failureOf(prod, StepResult{Ref: ref}, apidoc.ReasonInterrupted, "interrupted")
	tests := []struct {
		name     string
		res      StepResult
		failures []apidoc.Failure
		want     RunStatus
		exit     int
	}{
		{name: "succeeded", res: StepResult{Ref: ref, Status: StepSucceeded}, want: Succeeded, exit: 0},
		{name: "a verification that could not read", res: StepResult{Ref: ref, Status: StepSucceeded}, failures: unread, want: Incomplete, exit: 4},
		{name: "an issue wins over it", res: StepResult{Ref: ref, Status: StepSucceeded, VerifyIssue: "NodesReady: 1 node is not ready"}, failures: unread, want: VerifyFailed, exit: 5},
		{name: "failed", res: StepResult{Ref: ref, Status: StepFailed, Failure: failureOf(prod, StepResult{Ref: ref}, apidoc.ReasonUpdateFailed, "failed")}, want: Failed, exit: 1},
		{name: "interrupted", res: StepResult{Ref: ref, Status: StepInProgress, Failure: interrupted}, want: Interrupted, exit: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := watchResult(plan, r, tt.res, []health.Result{}, tt.failures)
			if got.Status != tt.want || got.ExitCode() != tt.exit {
				t.Errorf("status %s, exit %d; want %s, %d", got.Status, got.ExitCode(), tt.want, tt.exit)
			}
			if tt.want != Succeeded && (got.Err == nil || strings.TrimSpace(got.Err.Error()) == "") {
				t.Errorf("no message for %s", got.Status)
			}
		})
	}
}
