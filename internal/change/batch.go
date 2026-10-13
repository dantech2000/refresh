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
	var submit []int
	for i, s := range ph.Steps {
		results[i] = StepResult{Ref: s.Ref, Status: StepNotAttempted}
		if s.Action == Submit {
			submit = append(submit, i)
		}
	}
	if len(submit) > 0 && r.outOfTime(cp) {
		return results
	}
	for i, s := range ph.Steps {
		switch {
		case s.Action == Submit:
			continue
		case s.Action == Blocked:
			results[i].Status = StepBlocked
		case s.busy():
			results[i] = cp.busyResult(s)
		case s.work():
			// The roll table gives no Attach and no Verify step.
			results[i].Status = StepFailed
			results[i].Failure = r.stepFailure(cp, results[i], apidoc.ReasonUnknown, "a roll cannot "+string(s.Action)+" "+stepName(s.Ref))
		default:
			results[i].Status = StepSkipped
		}
		report(i)
	}
	if len(submit) == 0 {
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
	defer func() {
		if wait != nil {
			wait.stop()
		}
	}()
	for _, i := range submit {
		// The step is the one of the plan, and not a copy: a fresh read
		// that decides against the roll says so in the plan.
		results[i] = r.submitRoll(cp, &ph.Steps[i])
		// The run ended: the steps that were not reached stay NotAttempted.
		if r.readCtx.Err() != nil {
			if results[i].Status != StepNotAttempted {
				report(i)
			}
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
//     now. The step of the plan gets the new action and reason, and the
//     plan the notice, so the document of the run says why nothing was
//     sent.
//  6. Otherwise: UpdateNodegroupVersion, with the version of the fresh
//     read, which is the version of the plan, and the force of the plan.
//
// Rows 2 to 5 compare everything that DecideAMIUpdate read for the plan
// and that one DescribeNodegroup returns: the status, the AMI type, and
// the AMI release version.
//
// A request that EKS refuses, or that fails, is Failed. A request that
// EKS accepts with no update ID is InProgress, and cannot be followed.
func (r *runner) submitRoll(cp *ClusterPlan, s *Step) StepResult {
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
	if d := redecideRoll(*s, fresh); d.Action != Submit {
		note := fmt.Sprintf("nodegroup %s of %s runs the AMI release %s, and the plan read %s: it was rolled elsewhere since the plan, and is not rolled again",
			s.Ref.Name, cp.Cluster, orUnknown(fresh.Release), orUnknown(s.FromRelease))
		if d.Action == Manual {
			note = fmt.Sprintf("nodegroup %s of %s has a custom AMI (AMI type CUSTOM) since the plan, and is not rolled", s.Ref.Name, cp.Cluster)
		}
		s.Action, s.Reason, s.Force = d.Action, d.Reason, false
		cp.Notices = append(cp.Notices, note)
		r.notice(note)
		res.Status = StepSkipped
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

// waitTogether waits for every accepted update of a batch on one ticker.
// On each tick every update that has not ended is polled, all at once,
// with the poll of WaitUpdate, and the answers are reported in plan
// order.
//
// An update that ends does not end the wait for the others. A poll error
// that another poll will not fix settles its own step as InProgress, and
// no other step. When ctx ends, each update that has not ended is
// InProgress, with the reason Timeout or Interrupted. The answer of the
// last poll counts: an update that it shows as finished is finished.
//
// It sends on ticks after the polls of each tick, without blocking, which
// paces the Auto Scaling watch beside the wait.
func (r *runner) waitTogether(ctx context.Context, cp *ClusterPlan, ph Phase, started []int, results []StepResult, wait *budget, ticks chan<- struct{}) {
	clients := r.d.AWS.PollClients(cp.Region)
	o := WaitOptions{Interval: r.common.Limits.Poll, After: r.d.after, budget: wait}
	type follow struct {
		step int
		poll *updatePoll
	}
	type answer struct {
		progress     UpdateProgress
		report, done bool
		err          error
	}
	open := make([]follow, len(started))
	for n, i := range started {
		open[n] = follow{step: i, poll: newUpdatePoll(ph.Steps[i].Ref.Resource(), results[i].UpdateID)}
	}
	settle := func(f follow, err error) {
		res := results[f.step]
		if err == nil {
			res.Status, res.FinishedAt = StepSucceeded, r.d.now()
		} else {
			res = r.waitFailed(ctx, cp, res, err)
		}
		results[f.step] = res
		r.emit(Event{Kind: StepChanged, Phase: ph.Ref, Step: res.Ref, Result: &res})
	}
	for len(open) > 0 && o.pause(ctx) {
		answers := fanout.ForEach(ctx, open, len(open), func(ctx context.Context, f follow) answer {
			var a answer
			a.progress, a.report, a.done, a.err = f.poll.poll(ctx, clients)
			return a
		})
		select {
		case ticks <- struct{}{}:
		default:
		}
		var still []follow
		for n, f := range open {
			a := answers[n]
			if a.report {
				ref := ph.Steps[f.step].Ref
				r.emit(Event{Kind: UpdatePolled, Phase: ph.Ref, Step: ref, Update: &a.progress})
			}
			if a.done {
				settle(f, a.err)
				continue
			}
			still = append(still, f)
		}
		open = still
		if len(open) > 0 && o.over(ctx) {
			break
		}
	}
	for _, f := range open {
		settle(f, f.poll.ended(ctx))
	}
}

// interruptedNow records an interrupt of the run, and reports whether
// there was one. A deadline is not an interrupt.
func (r *runner) interruptedNow() bool {
	if r.ctx.Err() != nil && !timedOut(r.ctx) {
		r.interrupted = true
		return true
	}
	return false
}
