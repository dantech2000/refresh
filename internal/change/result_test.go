package change

import (
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/health"
)

// The step results of the exit-code tables.
var (
	stepOK       = StepResult{Status: StepSucceeded}
	stepStarted  = StepResult{Status: StepStarted}
	stepFailed   = StepResult{Status: StepFailed, Failure: &apidoc.Failure{Reason: apidoc.ReasonUpdateFailed}}
	stepCanceled = StepResult{Status: StepCancelled, Failure: &apidoc.Failure{Reason: apidoc.ReasonUpdateCancelled}}
	stepSkipped  = StepResult{Status: StepSkipped}
	stepBlocked  = StepResult{Status: StepBlocked}
	stepBusy     = StepResult{Status: StepBusy, Failure: &apidoc.Failure{Reason: apidoc.ReasonBusy}}
	stepTimeout  = StepResult{Status: StepInProgress, Failure: &apidoc.Failure{Reason: apidoc.ReasonTimeout}}
	stepUnseen   = StepResult{Status: StepInProgress, Failure: &apidoc.Failure{Reason: apidoc.ReasonNotMonitored}}
	stepAttached = StepResult{Status: StepInProgress, Attached: true}
	stepIssue    = StepResult{Status: StepFailed, VerifyIssue: "nodegroup web is DEGRADED"}
	stepUnwell   = StepResult{Status: StepSucceeded, VerifyIssue: "add-on vpc-cni is DEGRADED"}
	stepNever    = StepResult{Status: StepNotAttempted}
	stepStopped  = StepResult{Status: StepInProgress, Failure: &apidoc.Failure{Reason: apidoc.ReasonInterrupted}}
)

func resultOf(kind Kind, steps ...StepResult) Result {
	return Result{Plan: &Plan{Kind: kind}, Clusters: []ClusterResult{{Steps: steps}}}
}

// TestStepCode is the first table of "One exit code": the code of each
// step result.
func TestStepCode(t *testing.T) {
	tests := []struct {
		name string
		step StepResult
		code int
		none bool
	}{
		{name: "Succeeded", step: stepOK, code: 0},
		{name: "Skipped", step: stepSkipped, code: 0},
		{name: "Started", step: stepStarted, code: 0},
		{name: "InProgress from an attach under --no-wait", step: stepAttached, code: 0},
		{name: "Failed", step: stepFailed, code: 1},
		{name: "Cancelled", step: stepCanceled, code: 1},
		{name: "Blocked", step: stepBlocked, code: 3},
		{name: "InProgress with Timeout", step: stepTimeout, code: 4},
		{name: "InProgress with NotMonitored", step: stepUnseen, code: 4},
		{name: "Busy", step: stepBusy, code: 4},
		{name: "Failed from a verification issue", step: stepIssue, code: 5},
		{name: "an add-on that is updated and not healthy", step: stepUnwell, code: 5},
		{name: "NotAttempted", step: stepNever, none: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, ok := stepCode(tt.step)
			if ok == tt.none || code != tt.code {
				t.Errorf("stepCode = %d, %v; want %d, %v", code, ok, tt.code, !tt.none)
			}
		})
	}
}

// TestResultExitCode is the table of the design note that a test copies,
// row for row, and then the same rows interrupted.
func TestResultExitCode(t *testing.T) {
	gateRead := []apidoc.Failure{{Kind: apidoc.FailureCluster, Name: "prod", Reason: apidoc.ReasonThrottled}}
	// A gate with one warning that --yes went past. Its own code is 2.
	accepted := health.Gate([]health.Result{{Name: "Addons", Outcome: health.Warn, Message: "coredns is DEGRADED"}}, health.Policy{Yes: true})
	tests := []struct {
		name     string
		kind     Kind
		steps    []StepResult
		failures []apidoc.Failure
		// gate is the gate of the plan of the run.
		gate health.Verdict
		want int
	}{
		{name: "all Succeeded", kind: AddonUpdate, steps: []StepResult{stepOK, stepOK}, want: 0},
		{name: "all Started (--no-wait)", kind: AddonUpdate, steps: []StepResult{stepStarted, stepStarted}, want: 0},
		{name: "all attempted steps Failed", kind: AddonUpdate, steps: []StepResult{stepFailed, stepFailed, stepNever}, want: 1},
		{name: "independent targets: one Failed, one Succeeded", kind: AddonUpdate, steps: []StepResult{stepFailed, stepOK}, want: 4},
		{name: "independent targets: one Failed, one Started", kind: NodegroupScale, steps: []StepResult{stepFailed, stepStarted}, want: 4},
		{name: "cluster upgrade: the control plane Succeeded, an add-on Failed", kind: testUpgrade, steps: []StepResult{stepOK, stepFailed, stepNever}, want: 1},
		{name: "cluster upgrade: the control plane and the add-ons Succeeded, a nodegroup Failed", kind: testUpgrade, steps: []StepResult{stepOK, stepOK, stepOK, stepFailed}, want: 1},
		{name: "cluster rollback: the nodegroups and the add-ons Succeeded, the control plane Failed", kind: testRollback, steps: []StepResult{stepOK, stepOK, stepFailed}, want: 1},
		{name: "one Failed, one Skipped as already current", kind: AddonUpdate, steps: []StepResult{stepFailed, stepSkipped}, want: 1},
		{name: "one Failed, one Blocked", kind: AddonUpdate, steps: []StepResult{stepFailed, stepBlocked}, want: 3},
		{name: "independent targets: one Failed, one Blocked, one Succeeded", kind: NodegroupScale, steps: []StepResult{stepFailed, stepBlocked, stepOK}, want: 4},
		{name: "one verification issue, one Failed", kind: NodegroupScale, steps: []StepResult{stepIssue, stepFailed}, want: 5},
		{name: "one verification issue, one Failed, in an engine", kind: testUpgrade, steps: []StepResult{stepIssue, stepFailed}, want: 5},
		{name: "one verification issue, the rest Succeeded", kind: NodegroupScale, steps: []StepResult{stepIssue, stepOK, stepOK}, want: 5},
		{name: "one Busy, the rest Succeeded", kind: AddonUpdate, steps: []StepResult{stepBusy, stepOK}, want: 4},
		{name: "one InProgress with Timeout, the rest Succeeded", kind: AddonUpdate, steps: []StepResult{stepTimeout, stepOK}, want: 4},
		{name: "one Blocked, the rest Succeeded", kind: AddonUpdate, steps: []StepResult{stepBlocked, stepOK}, want: 3},
		{name: "all Succeeded, and a gate read that --yes went past", kind: AddonUpdate, steps: []StepResult{stepOK}, failures: gateRead, want: 4},
		{name: "all Succeeded, and a gate warning that --yes or an answer accepted", kind: AddonUpdate, steps: []StepResult{stepOK}, gate: accepted, want: 0},
		{name: "all Started, and a gate warning that was accepted", kind: NodegroupScale, steps: []StepResult{stepStarted}, gate: accepted, want: 0},
		{name: "one Failed, and a gate warning that was accepted", kind: AddonUpdate, steps: []StepResult{stepFailed}, gate: accepted, want: 1},
		{name: "one Blocked by --require-healthy over warnings alone, the rest Succeeded", kind: AddonUpdate, steps: []StepResult{stepBlocked, stepOK}, gate: accepted, want: 3},
		{name: "one Blocked, and a gate read that failed", kind: AddonUpdate, steps: []StepResult{stepBlocked}, failures: gateRead, want: 4},

		// The pairs of step classes that the rows above do not have.
		{name: "one Cancelled, one Succeeded", kind: AddonUpdate, steps: []StepResult{stepCanceled, stepOK}, want: 4},
		{name: "one Cancelled alone", kind: AddonUpdate, steps: []StepResult{stepCanceled}, want: 1},
		{name: "one NotMonitored, one Failed", kind: AddonUpdate, steps: []StepResult{stepUnseen, stepFailed}, want: 4},
		{name: "one Busy, one Blocked", kind: AddonUpdate, steps: []StepResult{stepBusy, stepBlocked}, want: 4},
		{name: "one unhealthy add-on, one Busy", kind: AddonUpdate, steps: []StepResult{stepUnwell, stepBusy}, want: 5},
		{name: "one unhealthy add-on, one Failed", kind: AddonUpdate, steps: []StepResult{stepUnwell, stepFailed}, want: 5},
		{name: "an attach under --no-wait, one Started", kind: AddonUpdate, steps: []StepResult{stepAttached, stepStarted}, want: 0},
		{name: "one Failed, one attached and not waited for", kind: AddonUpdate, steps: []StepResult{stepFailed, stepAttached}, want: 1},
		{name: "all Skipped, and a failure in the plan", kind: AddonUpdate, steps: []StepResult{stepSkipped}, failures: gateRead, want: 4},
		{name: "a failed read alone, in an engine", kind: testRollback, steps: []StepResult{stepNever}, failures: gateRead, want: 4},
		{name: "nothing attempted", kind: AddonUpdate, steps: []StepResult{stepNever}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := resultOf(tt.kind, tt.steps...)
			res.Failures = tt.failures
			res.Plan.Clusters = []ClusterPlan{{Gate: tt.gate}}
			if got := res.ExitCode(); got != tt.want {
				t.Errorf("ExitCode() = %d, want %d", got, tt.want)
			}
			// Any of the rows above, interrupted: 1.
			res.ended.interrupted = true
			if got := res.ExitCode(); got != 1 {
				t.Errorf("ExitCode() of the interrupted run = %d, want 1", got)
			}
			res.ended.interrupted = false
			res.Clusters[0].Steps = append(res.Clusters[0].Steps, stepStopped)
			if got := res.ExitCode(); got != 1 {
				t.Errorf("ExitCode() with a step that an interrupt ended = %d, want 1", got)
			}
		})
	}
}

// Fleet mode pools the steps of all its clusters.
func TestResultExitCode_FleetPools(t *testing.T) {
	res := Result{Plan: &Plan{Kind: NodegroupScale}, Clusters: []ClusterResult{
		{Target: Target{Cluster: "a"}, Steps: []StepResult{stepFailed}},
		{Target: Target{Cluster: "b"}, Steps: []StepResult{stepOK}},
	}}
	if got := res.ExitCode(); got != 4 {
		t.Errorf("one cluster failed and one succeeded: ExitCode() = %d, want 4", got)
	}
	res.Clusters[1].Steps = []StepResult{stepFailed}
	if got := res.ExitCode(); got != 1 {
		t.Errorf("every cluster failed: ExitCode() = %d, want 1", got)
	}
}

// The second table of "One exit code": what is not a step.
func TestResultExitCode_NotAStep(t *testing.T) {
	plan := &Plan{Kind: AddonUpdate}
	tests := []struct {
		name string
		res  Result
		want int
	}{
		{name: "a failure in the plan", res: Result{Plan: plan, Failures: []apidoc.Failure{{Kind: apidoc.FailureAddon}}}, want: 4},
		{name: "a question answered no", res: Result{Plan: plan, ended: ending{code: 1}}, want: 1},
		{name: "a refused plan", res: Result{Plan: plan, ended: ending{code: 3}}, want: 3},
		{name: "a refused plan with a failure", res: Result{Plan: plan, ended: ending{code: 3}, Failures: []apidoc.Failure{{Kind: apidoc.FailureAddon}}}, want: 4},
		{name: "the read budget ran out with nothing read", res: Result{ended: ending{code: 1}}, want: 1},
		{name: "a stop from the checkpoint, the steps before it Succeeded", want: 1,
			res: Result{Plan: plan, ended: ending{code: 1}, Clusters: []ClusterResult{{Steps: []StepResult{stepOK, stepNever}}}}},
		{name: "a stop from the checkpoint after a Blocked step", want: 3,
			res: Result{Plan: plan, ended: ending{code: 1}, Clusters: []ClusterResult{{Steps: []StepResult{stepBlocked, stepNever}}}}},
		{name: "a stop from the checkpoint after a verification issue", want: 5,
			res: Result{Plan: plan, ended: ending{code: 1}, Clusters: []ClusterResult{{Steps: []StepResult{stepIssue, stepNever}}}}},
		{name: "an interrupt, whatever else happened", res: Result{Plan: plan, ended: ending{code: 3, interrupted: true}, Failures: []apidoc.Failure{{Kind: apidoc.FailureAddon}}}, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.res.ExitCode(); got != tt.want {
				t.Errorf("ExitCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

// The status of a run is the name of the outcome with the highest code.
func TestStatusOf(t *testing.T) {
	tests := []struct {
		name  string
		kind  Kind
		steps []StepResult
		want  RunStatus
	}{
		{"all succeeded", AddonUpdate, []StepResult{stepOK, stepSkipped}, Succeeded},
		{"nothing to do", AddonUpdate, []StepResult{stepSkipped}, NothingToDo},
		{"started", AddonUpdate, []StepResult{stepStarted}, Started},
		{"failed", AddonUpdate, []StepResult{stepFailed}, Failed},
		{"partial failure", AddonUpdate, []StepResult{stepFailed, stepOK}, Incomplete},
		{"a failed phase of an upgrade", testUpgrade, []StepResult{stepOK, stepFailed}, Failed},
		{"blocked", testUpgrade, []StepResult{stepOK, stepBlocked}, RunBlocked},
		{"busy", AddonUpdate, []StepResult{stepBusy}, Incomplete},
		{"verify failed", AddonUpdate, []StepResult{stepUnwell}, VerifyFailed},
		{"interrupted", AddonUpdate, []StepResult{stepStopped}, Interrupted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusOf(tt.kind, tt.steps, nil); got != tt.want {
				t.Errorf("statusOf = %s, want %s", got, tt.want)
			}
		})
	}
}

// Every kind of change has a request type, or is refused by the planner.
func TestKinds(t *testing.T) {
	requests := map[Kind]Request{
		AddonUpdate:    AddonUpdateRequest{Target: prod, Addons: []string{"vpc-cni"}},
		NodegroupScale: ScaleRequest{Target: prod, Nodegroup: "web"},
	}
	for kind, req := range requests {
		if got := req.kind(); got != kind {
			t.Errorf("%T.kind() = %s, want %s", req, got, kind)
		}
		if got := req.cluster(); got != prod {
			t.Errorf("%T.cluster() = %v, want %v", req, got, prod)
		}
	}
	for _, kind := range []Kind{AddonUpdate, NodegroupScale} {
		if !kind.independent() {
			t.Errorf("%s: the targets are independent", kind)
		}
	}
	for _, kind := range []Kind{testUpgrade, testRollback} {
		if kind.independent() {
			t.Errorf("%s: the phases are a sequence", kind)
		}
	}
}
