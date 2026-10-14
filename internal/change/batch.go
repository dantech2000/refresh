package change

import (
	"context"
	"fmt"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fanout"
	"github.com/dantech2000/refresh/internal/health"
)

// runBatch runs the steps of a batch phase, the nodegroups of one roll,
// and returns one result per step, in plan order.
//
// The plan gated the whole batch, and the run has consent for it. From
// there the order is fixed:
//
//  1. The baseline of the verification, once, when verification will run.
//  2. For each Submit step in order: a fresh read of the nodegroup, and
//     then the request. See submitRoll for what the fresh read stops. A
//     submit that fails does not stop the next one. A run that ended
//     does: the later steps are NotAttempted.
//  3. With --no-wait the phase ends here.
//  4. One wait for every accepted update, on one ticker, with the node
//     observers and the Auto Scaling watch beside it.
//  5. The verification, over the steps that succeeded, against the
//     baseline of step 1. See verifyRoll for when it does not run.
//
// The fresh reads and the submits run under the read budget. The wait
// budget starts at the first accepted submit, and bounds the wait and the
// verification.
func (r *runner) runBatch(cp *ClusterPlan, ph Phase) []StepResult {
	results := make([]StepResult, len(ph.Steps))
	report := func(i int) {
		r.emit(Event{Kind: StepChanged, Phase: ph.Ref, Step: ph.Steps[i].Ref, Result: &results[i]})
	}
	// The plan decided the steps that send nothing: Blocked, Busy, and
	// Skipped. Their rows are what was decided, and they are reported
	// whatever happens to the submits, also when the run has no time left
	// to start one.
	var submit []int
	for i, s := range ph.Steps {
		results[i] = StepResult{Ref: s.Ref, Status: StepNotAttempted}
		if s.Action == Submit {
			submit = append(submit, i)
			continue
		}
		if res, ok := cp.decided(s); ok {
			results[i] = res
		} else {
			// The roll table gives no Attach and no Verify step.
			results[i].Status = StepFailed
			results[i].Failure = r.stepFailure(cp, results[i], apidoc.ReasonUnknown, "a roll cannot "+string(s.Action)+" "+stepName(s.Ref))
		}
		report(i)
	}
	if len(submit) == 0 || r.outOfTime(cp) {
		return results
	}
	kube, _, noKube := kubeAccess(r.d, cp.snapshot)
	verify := !r.common.SkipVerify && !r.common.NoWait
	var base health.Baseline
	if verify {
		base = health.TakeBaseline(r.readCtx, kube, cp.Cluster)
	}

	var (
		waitCtx context.Context
		wait    *budget
		started []int
		// unfollowed says that EKS accepted a roll that the run cannot
		// follow: nothing shows that it succeeded.
		unfollowed bool
	)
	defer func() { r.releaseWait(wait) }()
	for _, i := range submit {
		// A step whose request never left says so itself: startRoll checks
		// the context before its read and before its request, and records
		// the interrupt or the timeout. A roll that EKS accepted is always
		// followed, also when the read context ended right after the
		// answer: the wait has its own budget, and it records an interrupt.
		results[i] = r.startRoll(cp, ph.Steps[i])
		if results[i].Status == StepNotAttempted {
			// The run ended: this step and the later ones were not reached.
			break
		}
		switch results[i].Status {
		case StepStarted:
			started = append(started, i)
			if wait == nil && !r.common.NoWait {
				waitCtx, wait = r.waitBudget()
			}
		case StepInProgress:
			unfollowed = true
		}
		report(i)
	}
	// The read budget stops here: the wait has its own.
	r.read.pause()
	if r.common.NoWait || len(started) == 0 {
		return results
	}

	ticks := make(chan struct{}, 1)
	observe := r.observers(cp, ph, started, results, kube, noKube, ticks)
	// The observers cannot change the result: RunAlongside returns what
	// the wait returns, and joins them before it does.
	_ = fanout.RunAlongside(waitCtx, observe, func(ctx context.Context) error {
		r.waitTogether(ctx, cp, ph, started, results, wait, ticks)
		return nil
	})

	if verify {
		r.verifyRoll(waitCtx, cp, ph, started, results, unfollowed, verifyAccess{base: base, kube: kube, noKube: noKube, wait: wait})
	}
	return results
}

// startRoll sends the roll of one step of a batch: submitRoll for a roll
// onto the latest AMI, and submitEngineRoll for the roll of an upgrade or
// a rollback, which moves the nodegroup to another Kubernetes version.
func (r *runner) startRoll(cp *ClusterPlan, s Step) StepResult {
	if r.req.kind().independent() {
		return r.submitRoll(cp, s)
	}
	return r.submitEngineRoll(cp, s)
}

// submitRoll starts the roll of one nodegroup: a fresh read of the
// nodegroup, after the gate, and then the request. The fresh read decides
// first, and the first row that matches wins. Only the last row sends a
// request.
//
//  1. The read fails: Failed, with the failure of the read.
//  2. The nodegroup is changing (CREATING, UPDATING, DELETING): Busy, with
//     the type of the update in flight. Someone else started a change
//     since the plan, and this run does not attach to it.
//  3. The nodegroup is not ACTIVE: Blocked.
//  4. Its Kubernetes version is not the one of the plan: Failed. The plan
//     that was shown and accepted pins another version.
//  5. redecideRoll decides against the roll: Skipped, with a notice. The
//     nodegroup was rolled elsewhere since the plan, or has a custom AMI
//     now. The plan is not changed: it stays the plan that was confirmed.
//     The result of the step carries the words, in Note, so the document
//     of the run shows what the plan said and what the fresh read found.
//  6. Otherwise: UpdateNodegroupVersion, with the version of the fresh
//     read, which is the version of the plan, and the force of the plan.
//
// Rows 2 to 5 compare everything that DecideAMIUpdate read for the plan
// and that one DescribeNodegroup returns: the status, the AMI type, and
// the AMI release version.
//
// A request that EKS refuses, or that fails, is Failed. A request that
// EKS accepts with no update ID is InProgress, and cannot be followed.
func (r *runner) submitRoll(cp *ClusterPlan, s Step) StepResult {
	res := StepResult{Ref: s.Ref}
	resource := s.Ref.Resource()
	clients := r.d.AWS.Clients(cp.Region)
	// A run that ended reads nothing and sends nothing.
	if err := r.readCtx.Err(); err != nil {
		return r.unsent(cp, res, err)
	}
	fresh, err := describe(r.readCtx, clients, resource)
	switch {
	case err != nil:
		return r.unsent(cp, res, err)
	case nodegroupInFlight(fresh.Status):
		return r.busyTarget(cp, res, clients, resource, fresh.Status)
	case fresh.Status != string(ekstypes.NodegroupStatusActive):
		res.Status = StepBlocked
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown, notRollable(s.Ref.Name, fresh.Status))
		return res
	case fresh.Version != s.To:
		res.Status, res.FinishedAt = StepFailed, r.d.now()
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown,
			fmt.Sprintf("nodegroup %s changed to Kubernetes %s since the plan, which pins %s; nothing was sent. Run the command again", s.Ref.Name, orUnknown(fresh.Version), s.To))
		return res
	}
	if d, changed := redecideRoll(s.FromRelease, fresh); changed {
		note := fmt.Sprintf("nodegroup %s of %s runs the AMI release %s, and the plan read %s: it was rolled elsewhere since the plan, and is not rolled again",
			s.Ref.Name, cp.Cluster, orUnknown(fresh.Release), orUnknown(s.FromRelease))
		if d.Action == Manual {
			note = fmt.Sprintf("nodegroup %s of %s has a custom AMI (AMI type CUSTOM) since the plan, and is not rolled", s.Ref.Name, cp.Cluster)
		}
		r.notice(note)
		res.Status, res.Note = StepSkipped, note
		return res
	}
	if err := r.readCtx.Err(); err != nil {
		return r.unsent(cp, res, err)
	}
	res.StartedAt = r.d.now()
	id, err := StartNodegroupRoll(r.readCtx, clients, cp.Target, s.Ref.Name, fresh.Version, s.Force)
	switch {
	case err != nil:
		return r.unsent(cp, res, err)
	case id == "":
		res.Status = StepInProgress
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonNotMonitored, "EKS accepted the roll of nodegroup "+s.Ref.Name+" and returned no update ID; it cannot be followed, and it may still be running")
		return res
	}
	res.Status, res.UpdateID = StepStarted, id
	return res
}

// orUnknown returns s, or "unknown" for an empty value.
func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// waitTogether waits for every accepted update of a batch on one ticker,
// with the loop of WaitUpdate (waitUpdates), and turns what that reports
// into the events and the results of the steps, in plan order.
//
// A poll error that another poll will not fix settles its own step as
// InProgress, and no other step. When ctx ends, each update that has not
// ended is InProgress, with the reason Timeout or Interrupted.
//
// It sends on ticks after the polls of each tick, without blocking, which
// paces the Auto Scaling watch beside the wait.
func (r *runner) waitTogether(ctx context.Context, cp *ClusterPlan, ph Phase, started []int, results []StepResult, wait *budget, ticks chan<- struct{}) {
	clients := r.d.AWS.PollClients(cp.Region)
	o := WaitOptions{Interval: r.common.Limits.Poll, After: r.d.after, budget: wait}
	polls := make([]*updatePoll, len(started))
	for n, i := range started {
		polls[n] = newUpdatePoll(ph.Steps[i].Ref.Resource(), results[i].UpdateID)
	}
	waitUpdates(ctx, clients, polls, o, updateHooks{
		progress: func(n int, p UpdateProgress) {
			r.emit(Event{Kind: UpdatePolled, Phase: ph.Ref, Step: ph.Steps[started[n]].Ref, Update: &p})
		},
		settled: func(n int, err error) {
			res := results[started[n]]
			if err == nil {
				res.Status, res.FinishedAt = StepSucceeded, r.d.now()
			} else {
				res = r.waitFailed(ctx, cp, res, err)
			}
			results[started[n]] = res
			r.emit(Event{Kind: StepChanged, Phase: ph.Ref, Step: res.Ref, Result: &res})
		},
		tick: func() {
			select {
			case ticks <- struct{}{}:
			default:
			}
		},
	})
}

// userInterrupted reports whether the user stopped the run. A deadline is
// not an interrupt. It records nothing: see recordInterrupt.
func (r *runner) userInterrupted() bool {
	return r.ctx.Err() != nil && !timedOut(r.ctx)
}

// recordInterrupt makes an interrupt of the run its result: the run then
// ends as Interrupted, with exit code 1, whatever its steps did. It
// reports whether there was an interrupt to record.
func (r *runner) recordInterrupt() bool {
	if !r.userInterrupted() {
		return false
	}
	r.interrupted = true
	return true
}
