package change

import (
	"context"
	"sync"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/health"
)

// Run executes the change on the goroutine of its caller and returns when
// it has ended. Cancelling ctx is an interrupt.
//
// The error is non-nil only when Run could not do its job as a program:
// the request is invalid or names something that does not exist, the
// cluster could not be read at all, or a hook returned an error. A change
// that failed is a Result, and not an error. With an error, the Result
// has a plan when one was computed, and then says what was done.
//
// Consent is positive. Run submits only when Policy.Yes is set, or
// Common.Expect is a reviewed plan of the same request that the current
// plan matches, or Ask returned yes for the plan being executed. A run
// with none of the three is refused before any change, with exit code 1:
// that is a run without Policy.Yes and without a matching Expect whose
// Hooks have no Ask.
//
// Run computes the plan, asks at most one question before the first
// submit, and computes the plan again after every answer of a person: it
// compares the two with Diff before it submits anything. Nothing is
// submitted before every question is answered. An Expect that is not a
// plan of req is an error before anything is read (ErrOtherRequest).
//
// Before each submit Run reads the target again and applies the policy to
// the health that it finds, so the consent covers what is submitted: see
// regate.
func Run(ctx context.Context, d Deps, req Request, h Hooks) (Result, error) {
	r := &runner{ctx: ctx, d: d, req: req, common: req.base(), hooks: h}
	if _, ok := req.(UpgradeRequest); ok {
		r.refresh = &insightsRefresher{
			d: d, poll: r.common.Limits.Poll,
			progress: func(text string) { r.emit(Event{Kind: Progress, Text: text}) },
			// The refresh has its own limit, and the read budget does not
			// run while it waits.
			hold: func() func() {
				r.read.pause()
				return r.read.start
			},
		}
	}
	if expect := r.common.Expect; expect != nil {
		if err := sameRequest(expect, req); err != nil {
			return Result{Status: Failed, Err: err, ended: ending{code: exitcode.Failure}}, err
		}
	}
	p, res, err := r.prepare()
	if p == nil {
		return res, err
	}
	defer func() {
		r.read.stop()
		if r.runWait != nil {
			r.runWait.stop()
		}
	}()
	return r.execute(p), r.hookErr
}

// runner is the state of one run.
type runner struct {
	// ctx is the context of the caller. An interrupt ends it.
	ctx    context.Context //nolint:containedctx // the context of the run, which the budgets derive theirs from
	d      Deps
	req    Request
	common Common
	hooks  Hooks

	// mu serialises the event hook, so a front end needs no lock when
	// observers report beside a wait.
	mu sync.Mutex

	// readCtx and read are the context and the budget of the reads and
	// the submits outside a wait. Each plan gets new ones.
	readCtx context.Context //nolint:containedctx // see ctx
	read    *budget

	// verification and failures collect what the steps find beside their
	// own result.
	verification []health.Result
	failures     []apidoc.Failure
	// interrupted says that the user stopped the run before a step.
	interrupted bool

	// accepted are the health results that the consent to the plan
	// covers: the checks of the gate of the plan, and what a later answer
	// or --yes accepted. regate compares a fresh read with them.
	accepted []health.Result
	// waited says that a step of this cluster has waited, so the read of
	// the plan is old.
	waited bool
	// steps are the steps of the phase that runs, and remaining the ones
	// from the step that runs on.
	steps, remaining []Step
	// stop ends the run before its next step: a question during the run
	// was answered no, or could not be asked. hookErr is the error of the
	// hook in the second case, which Run returns.
	stop    *stop
	hookErr error

	// refresh starts the insights refreshes of an upgrade. It is nil for
	// every other kind.
	refresh *insightsRefresher
	// runWaitCtx and runWait are the context and the wait budget of an
	// upgrade or a rollback: one --wait-timeout for the whole run, from
	// its first submit or attachment on. They are nil before that, and
	// for every other kind, whose steps each have their own budget.
	runWaitCtx context.Context //nolint:containedctx // see ctx
	runWait    *budget
	// unverified says that the check after the step that runs could not
	// read what it needs. A run that is a sequence stops there.
	unverified bool
	// busySaid are the notices of the busy rule that the step of an
	// upgrade or a rollback has said already.
	busySaid struct {
		step    StepRef
		notices []string
	}
	// afterStep are the events that the step that runs holds back until
	// its result is reported: see stepEnded.
	afterStep []Event
	// confirmedPhase is the phase that the start question of an upgrade
	// with ConfirmPhases named. Its own question is answered already.
	confirmedPhase *PhaseRef
}

// stop is how a run ended that a question during the run stopped.
type stop struct {
	status RunStatus
	err    error
}

// emit sends one event to the hook.
func (r *runner) emit(e Event) {
	if r.hooks.Event == nil {
		return
	}
	e.At = r.d.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks.Event(e)
}

func (r *runner) notice(text string) {
	r.emit(Event{Kind: Notice, Text: text})
}

// newRead starts a read budget of limit, and ends the one before. The
// budget of a plan is --timeout. The plan of a fleet has no budget of its
// own: planFleet gives one to the discovery and one to each cluster.
func (r *runner) newRead(limit time.Duration) {
	if r.read != nil {
		r.read.stop()
	}
	// Once the wait budget of an upgrade or a rollback runs, it bounds
	// the reads of the later phases too.
	parent := r.ctx
	if r.runWaitCtx != nil {
		parent = r.runWaitCtx
	}
	r.readCtx, r.read = r.d.budget(parent, limit, ErrReadTimeout)
	r.read.start()
}

// holdBudgets stops the clocks of the run while a person answers a
// question or a checkpoint holds the run, and returns the function that
// runs them again.
func (r *runner) holdBudgets() (release func()) {
	r.read.pause()
	wait := r.runWait
	held := wait != nil && wait.running
	if held {
		wait.pause()
	}
	return func() {
		r.read.start()
		if held {
			wait.start()
		}
	}
}

// planLimit returns the read budget of one plan of the request.
func (r *runner) planLimit() time.Duration {
	if roll, ok := r.req.(RollRequest); ok && roll.Fleet != nil {
		return 0
	}
	return r.common.Limits.Read
}

// interruptError is the message of a run that the user interrupted. It
// says "interrupted" in its own words, and is still the cancelled context
// for errors.Is: the words of the runtime, "context canceled", are not
// part of what a person reads.
type interruptError string

func (e interruptError) Error() string { return string(e) }

// Is reports that an interrupt is the cancelled context.
func (interruptError) Is(target error) bool { return target == context.Canceled }

// waitBudget starts the wait budget of one step: --wait-timeout from its
// submit or its attachment on. Its timer has the grace of the last poll.
//
// An upgrade and a rollback have one wait budget for the whole run: it
// starts at the first submit or attachment, and every later call returns
// the same budget. It bounds the waits, the reads and the submits of the
// later phases, and the verification.
func (r *runner) waitBudget() (context.Context, *budget) {
	if !r.req.kind().independent() {
		if r.runWait == nil {
			r.runWaitCtx, r.runWait = r.d.budget(r.ctx, r.common.Limits.Wait, ErrWaitTimeout)
			r.runWait.grace = finalPollGrace
			r.runWait.start()
		}
		r.waited = true
		return r.runWaitCtx, r.runWait
	}
	ctx, wait := r.d.budget(r.ctx, r.common.Limits.Wait, ErrWaitTimeout)
	wait.grace = finalPollGrace
	wait.start()
	r.waited = true
	return ctx, wait
}

// releaseWait ends the wait budget of a step. The wait budget of an
// upgrade or a rollback is the budget of the whole run, and stays.
func (r *runner) releaseWait(wait *budget) {
	if wait != nil && wait != r.runWait {
		wait.stop()
	}
}

// waitOptions returns the options of a wait for step s under the budget
// wait. Each poll is an event. The wait cuts its last pause to the time
// that the budget has left and checks the budget after each answer, on
// the clock of the run: the last poll is due at the deadline, and none
// comes after it. See budget for the answer of that poll.
func (r *runner) waitOptions(s Step, wait *budget) WaitOptions {
	return WaitOptions{
		Interval: r.common.Limits.Poll,
		After:    r.d.after,
		Progress: func(p UpdateProgress) {
			r.emit(Event{Kind: UpdatePolled, Phase: s.Ref.Phase, Step: s.Ref, Update: &p})
		},
		budget: wait,
	}
}
