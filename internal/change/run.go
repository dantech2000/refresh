package change

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/health"
)

// maxQuestions is how many plans in a row may each need a new answer.
// The plan after that many is refused: the cluster keeps changing.
const maxQuestions = 2

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

// checkpoint calls the checkpoint hook before next, and reports whether
// the run may go on. what names next in a message. A hook that says stop
// ends the run as Stopped, and a hook that fails ends it as Failed: the
// steps that were not reached are NotAttempted. A nil hook says go on.
func (r *runner) checkpoint(next StepRef, what string) bool {
	if r.hooks.Checkpoint == nil {
		return true
	}
	release := r.holdBudgets()
	answer, err := r.hooks.Checkpoint(r.ctx, next)
	release()
	switch {
	case err != nil && r.ctx.Err() != nil:
		r.recordInterrupt()
		return false
	case err != nil:
		r.stop = &stop{status: Failed, err: fmt.Errorf("the checkpoint before %s failed: %w; nothing more was changed", what, err)}
		r.hookErr = err
		return false
	case answer.Stop:
		r.stop = &stop{status: Stopped, err: fmt.Errorf("stopped before %s; nothing more was changed", what)}
		return false
	}
	return true
}

// answer is how a question to a person ended.
type answer int

const (
	// answeredYes: the person said yes.
	answeredYes answer = iota
	// answeredNo: the person said no.
	answeredNo
	// askInterrupted: the run ended before an answer came.
	askInterrupted
	// askFailed: the question could not be asked, or its answer not read.
	askFailed
)

// ask asks q through the hook and says how that ended, with the error of
// the hook for the two endings that have one. It is the one reading of
// what Ask returns: every question of a run goes through it. The caller
// stops the budgets, and decides what each ending does to the run.
func (r *runner) ask(q Question) (answer, error) {
	yes, err := r.hooks.Ask(r.ctx, q)
	switch {
	case err != nil && r.ctx.Err() != nil:
		return askInterrupted, err
	case err != nil:
		return askFailed, err
	case !yes:
		return answeredNo, nil
	}
	return answeredYes, nil
}

// askDuringRun asks a question that comes up after the run started: about
// a plan of a cluster of a fleet that changed, or about health that got
// worse. The budgets do not run while a person answers. A no ends the run
// as Declined, and a question that could not be asked ends it as Failed,
// with the error of the hook as the error of Run: either way nothing more
// is submitted. The caller handles an interrupt, and goes on after a yes.
func (r *runner) askDuringRun(q Question) (answer, error) {
	release := r.holdBudgets()
	a, err := r.ask(q)
	release()
	switch a {
	case askFailed:
		r.stop = &stop{status: Failed, err: fmt.Errorf("could not ask %q: %w; nothing more was changed", q.Text, err)}
		r.hookErr = err
	case answeredNo:
		r.stop = &stop{status: Declined, err: errors.New("declined; nothing more was changed")}
	}
	return a, err
}

// planLimit returns the read budget of one plan of the request.
func (r *runner) planLimit() time.Duration {
	if roll, ok := r.req.(RollRequest); ok && roll.Fleet != nil {
		return 0
	}
	return r.common.Limits.Read
}

// prepare computes the plan and gets it confirmed. It returns the plan to
// execute, or a nil plan with the result and the error of a run that ends
// before it submits anything.
func (r *runner) prepare() (*Plan, Result, error) {
	prev := r.common.Expect
	// A plan that a person reviewed before the call is confirmed already.
	// Run has checked that it is a plan of this request, and next compares
	// the first plan with it.
	confirmed := prev != nil
	asked := 0
	for {
		r.newRead(r.planLimit())
		p, err := plan(r.readCtx, r.d, r.req, r.refresh)
		if err == nil && r.userInterrupted() {
			// The interrupt ended some of the reads, and the planner took
			// what they left for a cluster that cannot be read: a plan
			// with blockers. An interrupted run has no plan.
			err = fmt.Errorf("the plan was not finished: %w", context.Cause(r.ctx))
		}
		if err != nil {
			res, err := r.unplanned(prev, err)
			r.read.stop()
			return nil, res, err
		}
		// A rollback that would now return to another version than the
		// one that was confirmed is refused, whatever the answer and the
		// flags: the nodes and the add-ons would go to a version that
		// nobody confirmed.
		if prev != nil {
			if changed := RollbackTargetChanged(prev, p); changed != nil {
				// A refusal is a result of the run, and not an error of Run.
				r.announce(p)
				res := r.unstarted(p, Refused, ending{code: exitcode.Blocked})
				res.Err = changed
				r.read.stop()
				return nil, res, nil //nolint:nilerr // see above
			}
		}
		var diff []Change
		if prev != nil {
			diff = Diff(prev, p)
		}
		if prev == nil || len(diff) > 0 {
			r.announce(p)
		}
		q, end := r.next(p, diff, confirmed, asked)
		if end != nil {
			r.read.stop()
			return nil, *end, nil
		}
		if q == nil {
			accept(p)
			return p, Result{}, nil
		}
		// The budgets do not run while a person answers.
		r.read.stop()
		switch a, err := r.ask(*q); a {
		case askInterrupted:
			// An interrupt is a result of the run, not an error of Run.
			return nil, r.stopped(p, "before anything was changed"), nil
		case askFailed:
			res := r.unstarted(p, Failed, ending{code: exitcode.Failure})
			res.Err = fmt.Errorf("could not ask %q: %w; nothing was changed", q.Text, err)
			return nil, res, err
		case answeredNo:
			res := r.unstarted(p, Declined, ending{code: exitcode.Failure})
			res.Err = errors.New("declined; nothing was changed")
			return nil, res, nil
		case answeredYes:
			if q.Kind == ConfirmPhase {
				phase := q.Step.Phase
				r.confirmedPhase = &phase
			}
		}
		prev, confirmed = p, true
		asked++
	}
}

// accept records in the gate of each cluster what the consent to the
// plan went past: the warnings of a gate that asks, which a person
// accepted with an answer or with the plan in Common.Expect. The gate then
// reads as the gate of a run with --yes does, and the Run document says
// what was accepted. A plan reaches this only with every question
// answered.
func accept(p *Plan) {
	for i := range p.Clusters {
		acceptCluster(&p.Clusters[i])
	}
}

// acceptCluster is accept for the plan of one cluster.
func acceptCluster(cp *ClusterPlan) {
	if gate := &cp.Gate; gate.Decision == health.Ask {
		gate.Decision, gate.Accepted = health.Go, slices.Clone(gate.Reasons)
	}
}

// announce reports a plan, its gate, and its notices.
func (r *runner) announce(p *Plan) {
	r.emit(Event{Kind: PlanReady, Plan: p})
	for i := range p.Clusters {
		cp := &p.Clusters[i]
		if cp.Gate.Decision != "" && !cp.Gate.Skipped {
			r.emit(Event{Kind: GateReport, Phase: PhaseRef{Target: cp.Target}, Checks: cp.Checks, Verdict: &cp.Gate})
		}
	}
}

// next decides what follows a plan: a result that ends the run, a
// question, or neither, which means execute. diff compares the plan with
// the one that was confirmed, when there is one.
func (r *runner) next(p *Plan, diff []Change, confirmed bool, asked int) (*Question, *Result) {
	yes := r.common.Policy.Yes
	for _, c := range diff {
		if c.Effect() == Continue || (yes && c.Effect() == AskAgain) {
			r.notice(c.Message)
		}
	}
	switch p.Decision() {
	case DecisionBlocked:
		res := r.unstarted(p, Refused, ending{code: p.ExitCode()})
		res.Err = errors.New(blockedSummary(p))
		return nil, &res
	case DecisionNothingToDo:
		// A target that is already changing is reported by its step, as
		// in a run that has other targets too: the step looks up the
		// update in flight. Nothing is submitted, so nothing is asked.
		if slices.ContainsFunc(allPlanSteps(p), Step.busy) {
			return nil, nil
		}
		res := r.unstarted(p, NothingToDo, ending{})
		// The steps were not attempted, which is not what stops a run
		// with nothing to do: its failures are.
		res.Err = summary(Result{Plan: p, Failures: res.Failures})
		return nil, &res
	}
	if worstEffect(diff) == Refuse {
		res := r.unstarted(p, Refused, ending{code: exitcode.Blocked})
		res.Err = fmt.Errorf("the plan changed since it was confirmed (%s); nothing was started", messages(diff, Refuse))
		return nil, &res
	}
	q := r.question(p, diff, confirmed)
	switch {
	case q == nil:
		return nil, nil
	case r.hooks.Ask == nil:
		res := r.unstarted(p, Refused, ending{code: exitcode.Failure})
		res.Err = errors.New(unanswered(q.Kind) + "; nothing was changed")
		return nil, &res
	case asked >= maxQuestions:
		res := r.unstarted(p, Refused, ending{code: exitcode.Blocked})
		res.Err = fmt.Errorf("the cluster keeps changing (%s); nothing was started", strings.Join(q.Reasons, "; "))
		return nil, &res
	}
	return q, nil
}

// allPlanSteps returns the steps of every cluster of the plan.
func allPlanSteps(p *Plan) []Step {
	var out []Step
	for i := range p.Clusters {
		out = append(out, p.Clusters[i].Steps()...)
	}
	return out
}

// unanswered is why a run that needs an answer to a question of kind k
// is refused when nobody can be asked.
func unanswered(k QuestionKind) string {
	switch k {
	case PlanChanged:
		return "changes of the plan need an answer and nobody can be asked; pass --yes"
	case ConfirmStart:
		return "nobody confirmed the change and nobody can be asked; pass --yes"
	}
	return "health warnings need an answer and nobody can be asked; pass --yes or --require-healthy"
}

// question returns the question that a plan needs before it runs, or nil.
// --yes answers every question. A plan that is not confirmed yet asks
// once: about the warnings of the gate when it has some, and else to
// confirm the start. It asks also when nobody can be asked, and next then
// refuses the run: no answer is not a yes. A confirmed plan asks again
// only about what changed: the steps, or a health check that is worse
// than it was.
func (r *runner) question(p *Plan, diff []Change, confirmed bool) *Question {
	if r.common.Policy.Yes {
		return nil
	}
	if !confirmed && p.fleet() {
		return fleetQuestion(p)
	}
	if !confirmed && !p.Kind.independent() {
		return r.engineQuestion(p)
	}
	if !confirmed {
		for i := range p.Clusters {
			if v := p.Clusters[i].Gate; v.Decision == health.Ask {
				return &Question{Kind: GateWarnings, Text: capitalize(v.Question()), Reasons: reasonStrings(v.Reasons)}
			}
		}
		return &Question{Kind: ConfirmStart, Text: startQuestion(p)}
	}
	var changed, worse []string
	for _, c := range diff {
		switch {
		case c.Kind == GateWorse:
			worse = append(worse, c.Message)
		case c.Effect() == AskAgain:
			changed = append(changed, c.Message)
		}
	}
	switch {
	case len(changed) > 0:
		reasons := slices.Concat(changed, worse)
		return &Question{Kind: PlanChanged, Text: "The plan changed (" + strings.Join(reasons, "; ") + "). Continue?", Reasons: reasons}
	case len(worse) > 0:
		return &Question{Kind: GateWarnings, Text: "The health checks got worse (" + strings.Join(worse, "; ") + "). Continue?", Reasons: worse}
	}
	return nil
}

// messages joins the messages of the changes with the effect.
func messages(diff []Change, effect Effect) string {
	var parts []string
	for _, c := range diff {
		if c.Effect() == effect {
			parts = append(parts, c.Message)
		}
	}
	return strings.Join(parts, "; ")
}

func reasonStrings(reasons []health.Reason) []string {
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = r.String()
	}
	return out
}

// capitalize returns s with an upper-case first letter.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// startQuestion is the question that confirms the start of a plan, such
// as "Update add-on vpc-cni in prod from v1.18.0 to v1.19.0?".
func startQuestion(p *Plan) string {
	var steps []Step
	cluster := ""
	for i := range p.Clusters {
		cluster = p.Clusters[i].Cluster
		for _, s := range p.Clusters[i].Steps() {
			if s.work() {
				steps = append(steps, s)
			}
		}
	}
	verb, noun := "Change", "target"
	switch p.Kind {
	case AddonUpdate:
		verb, noun = "Update", "add-on"
	case NodegroupUpdate:
		verb, noun = "Roll", "nodegroup"
	}
	if len(steps) == 1 {
		s := steps[0]
		switch {
		case p.Kind == NodegroupUpdate:
			return fmt.Sprintf("Roll nodegroup %s in %s onto the latest AMI of Kubernetes %s?", s.Ref.Name, cluster, s.To)
		case s.Sizes != nil:
			return fmt.Sprintf("Scale nodegroup %s in %s from %s to %s?", s.Ref.Name, cluster, s.Sizes.Before, s.Sizes.After)
		case s.Action == Attach:
			return fmt.Sprintf("Wait for the update of %s %s in %s to %s?", noun, s.Ref.Name, cluster, s.To)
		case s.From == s.To:
			return fmt.Sprintf("Apply %s %s in %s again at %s?", noun, s.Ref.Name, cluster, s.To)
		}
		return fmt.Sprintf("%s %s %s in %s from %s to %s?", verb, noun, s.Ref.Name, cluster, s.From, s.To)
	}
	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = s.Ref.Name
	}
	return fmt.Sprintf("%s %d %ss in %s (%s)?", verb, len(steps), noun, cluster, strings.Join(names, ", "))
}

// fleetQuestion is the one question of a fleet roll: it names the
// nodegroups that would be rolled in each cluster, and the clusters that
// are left out. The plan above it has the decision of every cluster. When
// the gate of a cluster has warnings, the question is about them too, and
// a yes accepts them.
func fleetQuestion(p *Plan) *Question {
	q := &Question{Kind: ConfirmStart}
	var rolls, left []string
	count := 0
	for i := range p.Clusters {
		cp := &p.Clusters[i]
		var names []string
		for _, s := range cp.Steps() {
			if s.work() {
				names = append(names, s.Ref.Name)
			}
		}
		switch {
		case cp.Decision() == DecisionBlocked:
			left = append(left, cp.Cluster)
			continue
		case len(names) == 0:
			continue
		}
		count += len(names)
		rolls = append(rolls, cp.Cluster+": "+strings.Join(names, ", "))
		if v := cp.Gate; v.Decision == health.Ask {
			q.Kind = GateWarnings
			for _, reason := range v.Reasons {
				q.Reasons = append(q.Reasons, cp.Cluster+": "+reason.String())
			}
		}
	}
	q.Text = fmt.Sprintf("Roll %d nodegroup(s) in %d cluster(s) (%s)", count, len(rolls), strings.Join(rolls, "; "))
	if len(q.Reasons) > 0 {
		q.Text += ", despite the health warnings (" + strings.Join(q.Reasons, "; ") + ")"
	}
	if len(left) > 0 {
		q.Text += ", and leave out the blocked " + strings.Join(left, ", ")
	}
	q.Text += "?"
	return q
}

// blockedSummary is the one line of a plan that cannot run: its blockers,
// or the summary of the gate.
func blockedSummary(p *Plan) string {
	var parts []string
	for i := range p.Clusters {
		cp := &p.Clusters[i]
		for _, b := range cp.Blockers {
			parts = append(parts, b.Message)
		}
		if len(cp.Blockers) == 0 && cp.Gate.Decision == health.Block {
			parts = append(parts, cp.Gate.Summary()+"; nothing was started")
		}
	}
	return strings.Join(parts, "; ")
}

// unplanned returns the result of a run whose plan could not be computed.
// A context that ended is an interrupt or a timeout, and a Result. Any
// other error is the error of Run.
func (r *runner) unplanned(prev *Plan, err error) (Result, error) {
	if r.readCtx.Err() == nil {
		return Result{Status: Failed, Plan: prev, Err: err, ended: ending{code: exitcode.Failure}}, err
	}
	res := Result{Status: Interrupted, Plan: prev, ended: ending{interrupted: true}}
	res.Err = interruptError("interrupted before anything was changed")
	if timedOut(r.readCtx) {
		// Nothing was read, so there is no document to call incomplete.
		res.Status, res.ended = Failed, ending{code: exitcode.Failure}
		what := r.req.cluster().Cluster
		if what == "" {
			what = "the fleet"
		}
		res.Err = fmt.Errorf("timed out reading %s (increase --timeout); nothing was changed", what)
	}
	if prev != nil {
		res.Clusters = notAttempted(prev)
	}
	return res, nil
}

// interruptError is the message of a run that the user interrupted. It
// says "interrupted" in its own words, and is still the cancelled context
// for errors.Is: the words of the runtime, "context canceled", are not
// part of what a person reads.
type interruptError string

func (e interruptError) Error() string { return string(e) }

// Is reports that an interrupt is the cancelled context.
func (interruptError) Is(target error) bool { return target == context.Canceled }

// stopped returns the result of a run that an interrupt ended before it
// submitted anything.
func (r *runner) stopped(p *Plan, when string) Result {
	res := r.unstarted(p, Interrupted, ending{interrupted: true})
	res.Err = interruptError("interrupted " + when)
	if timedOut(r.ctx) {
		res.Status, res.ended = Failed, ending{code: exitcode.Failure}
		res.Err = fmt.Errorf("timed out %s", when)
	}
	return res
}

// unstarted returns the result of a run that ends with a plan and with
// nothing submitted: every step is NotAttempted.
func (r *runner) unstarted(p *Plan, status RunStatus, end ending) Result {
	res := Result{Status: status, Plan: p, Clusters: notAttempted(p), Failures: p.allFailures(), ended: end}
	for i := range res.Clusters {
		res.Clusters[i].Status = status
	}
	return res
}

// notAttempted returns one result per step of the plan, none attempted.
func notAttempted(p *Plan) []ClusterResult {
	out := make([]ClusterResult, len(p.Clusters))
	for i := range p.Clusters {
		cp := &p.Clusters[i]
		out[i] = ClusterResult{Target: cp.Target}
		for _, s := range cp.Steps() {
			out[i].Steps = append(out[i].Steps, StepResult{Ref: s.Ref, Status: StepNotAttempted})
		}
	}
	return out
}

// execute runs the phases of a confirmed plan and returns the result.
//
// The clusters of a fleet run one after another. Each begins with its
// turn, which plans it again and can leave it out: see turn. A cluster
// that is left out, or that fails, does not stop the next one. A run that
// was halted does: the steps of every later cluster are NotAttempted.
func (r *runner) execute(p *Plan) Result {
	res := Result{Plan: p, ended: ending{executed: true}}
	fleet := p.fleet()
	for i := range p.Clusters {
		outcome, why := turnRun, ""
		if fleet {
			outcome, why = r.turn(p, i)
		}
		cp := &p.Clusters[i]
		cr := ClusterResult{Target: cp.Target}
		r.accepted, r.waited = slices.Clone(cp.Checks), false
		switch {
		case outcome == turnRun && !p.Kind.independent():
			cr.Steps = r.runEngine(cp)
		case outcome == turnRun:
			for _, ph := range cp.Phases {
				r.emit(Event{Kind: PhaseStarted, Phase: ph.Ref})
				cr.Steps = append(cr.Steps, r.runPhase(cp, ph)...)
				r.emit(Event{Kind: PhaseEnded, Phase: ph.Ref})
			}
		case outcome == turnBlocked:
			cr.Steps = r.blockedResults(cp, why)
			for i := range cr.Steps {
				r.emit(Event{Kind: StepChanged, Phase: cr.Steps[i].Ref.Phase, Step: cr.Steps[i].Ref, Result: &cr.Steps[i]})
			}
		default:
			for _, s := range cp.Steps() {
				cr.Steps = append(cr.Steps, StepResult{Ref: s.Ref, Status: StepNotAttempted})
			}
		}
		cr.Verification, r.verification = r.verification, nil
		failures := slices.Concat(planFailures(cp, cr.Steps), r.failures)
		r.failures = nil
		cr.Status = statusOf(p.Kind, cr.Steps, failures)
		res.Clusters = append(res.Clusters, cr)
		res.Failures = append(res.Failures, failures...)
	}
	res.Failures = append(res.Failures, p.Failures...)
	res.ended.interrupted = r.interrupted || res.interruptedStep()
	res.Status = statusOf(p.Kind, allSteps(res.Clusters), res.Failures)
	res.Err = summary(res)
	switch {
	case res.ended.interrupted:
		res.Status = Interrupted
		// A cluster whose steps were all left out by the interrupt had
		// work to do: it is not NothingToDo.
		for i := range res.Clusters {
			c := &res.Clusters[i]
			if c.Status == NothingToDo && slices.ContainsFunc(c.Steps, func(s StepResult) bool { return s.Status == StepNotAttempted }) {
				c.Status = Interrupted
			}
		}
	case r.stop != nil && r.stop.status == Stopped:
		// A run that a checkpoint stopped did not do what it was asked to
		// do, so it is no success: it exits 1, and the highest code of the
		// steps and the failures still wins. The label is Stopped only
		// when nothing else went wrong: a failure after a stop request is
		// still a failure.
		clean := res.ExitCode() == exitcode.OK
		res.ended.code = exitcode.Failure
		if clean {
			res.Status, res.Err = Stopped, r.stop.err
		}
	case r.stop != nil:
		// The steps keep their results, and the highest code still wins.
		res.ended.code = exitcode.Failure
		res.Status, res.Err = r.stop.status, r.stop.err
	}
	// The message of the run is final here. An upgrade or a rollback that
	// stopped part way, a declined later phase included, says after it
	// what completed and how to go on.
	if hint := r.resumeHint(res); hint != "" && res.Err != nil {
		res.Err = fmt.Errorf("%w; %s", res.Err, hint)
	}
	return res
}

// planFailures returns the failures of a cluster plan that belong to no
// step of the run: the reads that failed and the reads of the gate. A
// target that the plan listed as busy has its failure on its step.
func planFailures(cp *ClusterPlan, steps []StepResult) []apidoc.Failure {
	own := func(f apidoc.Failure) bool {
		return f.Reason == apidoc.ReasonBusy && slices.ContainsFunc(steps, func(s StepResult) bool {
			return s.Failure != nil && s.Ref.Name == f.Name
		})
	}
	var out []apidoc.Failure
	for _, f := range slices.Concat(cp.Failures, cp.Gate.Failures) {
		if !own(f) {
			out = append(out, f)
		}
	}
	return out
}

func allSteps(clusters []ClusterResult) []StepResult {
	var out []StepResult
	for _, c := range clusters {
		out = append(out, c.Steps...)
	}
	return out
}

// runPhase runs the steps of one phase and returns one result per step,
// in plan order. A step that the run never reaches is NotAttempted: after
// an interrupt, after the read budget ran out, and after a question
// during the run that was answered no or could not be asked.
//
// The steps run one after another, and a step that fails or is blocked
// does not stop the next one: the targets are independent. A phase that
// is one batch runs in runBatch. One that stops at its first failure is
// built with the first change that has one.
func (r *runner) runPhase(cp *ClusterPlan, ph Phase) []StepResult {
	if ph.Batch {
		return r.runBatch(cp, ph)
	}
	results := make([]StepResult, 0, len(ph.Steps))
	halted := false
	for i, s := range ph.Steps {
		if !halted && (s.work() || s.busy()) && r.outOfTime(cp) {
			halted = true
		}
		res := StepResult{Ref: s.Ref, Status: StepNotAttempted}
		if !halted {
			r.steps, r.remaining = ph.Steps, ph.Steps[i:]
			res = r.runStep(cp, s)
			r.stepEnded(ph.Ref, &res)
		}
		results = append(results, res)
		halted = halted || r.stop != nil
	}
	return results
}

// stepEnded reports the result of a step that ran alone, and then what
// the step held back for after it: the check of its confirmation. The
// events are in the order of what happened: the update ended, and then it
// was checked. A front end that prints both shows the outcome of a step
// above the verification of that step.
func (r *runner) stepEnded(ph PhaseRef, res *StepResult) {
	r.emit(Event{Kind: StepChanged, Phase: ph, Step: res.Ref, Result: res})
	held := r.afterStep
	r.afterStep = nil
	for _, e := range held {
		r.emit(e)
	}
}

// outOfTime reports whether the run can start no further step: the user
// stopped it, or the time for its reads ran out, which is a failure of
// the cluster. It runs the clock of the read budget again, which a wait
// stopped.
func (r *runner) outOfTime(cp *ClusterPlan) bool {
	r.read.start()
	switch {
	case r.ctx.Err() != nil && !timedOut(r.ctx):
		r.interrupted = true
		return true
	case r.ctx.Err() == nil && !r.read.check() && r.readCtx.Err() == nil:
		return false
	}
	f := apidoc.NewFailure(apidoc.FailureCluster, cp.Cluster, apidoc.ReasonTimeout,
		"the --timeout budget ran out before every change was started (increase --timeout)")
	f.Region = cp.Region
	r.failures = append(r.failures, f)
	return true
}

// runStep runs one step. A new kind of step adds a case here and a file
// with its submit, its wait, and its confirmation.
func (r *runner) runStep(cp *ClusterPlan, s Step) StepResult {
	if res, ok := cp.decided(s); ok {
		return res
	}
	switch s.Ref.Kind {
	case StepAddon:
		return r.runAddonStep(cp, s)
	case StepScale:
		return r.runScaleStep(cp, s)
	}
	f := apidoc.NewFailure(apidoc.FailureCluster, cp.Cluster, apidoc.ReasonUnknown, "this build cannot run a "+string(s.Ref.Kind)+" step")
	return StepResult{Ref: s.Ref, Status: StepFailed, Failure: &f}
}

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

// summary is the one line for the user about a run that did not end
// clean, or nil.
func summary(res Result) error {
	if res.ExitCode() == exitcode.OK {
		return nil
	}
	var parts []string
	for _, c := range res.Clusters {
		// The steps of a fleet say which cluster they are of.
		where := ""
		if len(res.Clusters) > 1 {
			where = c.Cluster + ": "
		}
		for _, s := range c.Steps {
			code, ok := stepCode(s)
			if !ok || code == exitcode.OK {
				continue
			}
			switch {
			case s.VerifyIssue != "":
				parts = append(parts, where+s.VerifyIssue)
			case s.Failure != nil:
				parts = append(parts, where+s.Failure.Error)
			default:
				parts = append(parts, where+stepName(s.Ref)+" is "+string(s.Status))
			}
		}
	}
	for _, f := range res.Failures {
		parts = append(parts, f.Error)
	}
	msg := strings.Join(parts, "; ")
	if res.ended.interrupted {
		if msg == "" {
			return interruptError("interrupted before the next change was sent")
		}
		return interruptError("interrupted: " + msg)
	}
	return errors.New(msg)
}
