package change

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// regate applies the policy of the run to health results that were read
// after the plan was accepted: the fresh read of a target before its
// submit, and the read of the cluster before a submit that follows a
// wait. The consent to the plan covers the health that the plan showed,
// and no more. It returns false when the step must not go on, with the
// result of the step, and asked when a person answered yes.
//
// It is the pre-phase table of the health design. The first row that
// matches decides.
//
//  1. The gate blocks, which --require-healthy does for any warning: the
//     step is Blocked, exit code 3, as a plan that the gate blocks.
//     Nothing is sent for it.
//  2. Nothing is worse than what was accepted: the step goes on.
//  3. Something is worse, with --yes: the step goes on, and a notice
//     records what --yes went past.
//  4. Something is worse, and nobody can be asked: the step is Failed.
//     Nothing is sent for it.
//  5. Something is worse, and the step has asked maxQuestions times
//     already (mayAsk is false): the step is Failed, and nobody is asked a
//     question whose answer could not count. The target keeps changing.
//  6. Something is worse: the run asks. Yes accepts it for the rest of
//     the run. No ends the run as Declined: this step and the later ones
//     are NotAttempted.
//
// A read that a check could not make is a failure of the run on every
// row, also when --yes or an answer goes past the check: the run then
// exits 4.
//
// A step that this blocks or fails does not stop the next one, which has
// its own read.
func (r *runner) regate(cp *ClusterPlan, res StepResult, checks []health.Result, mayAsk bool) (out StepResult, ok, asked bool) {
	name := stepName(res.Ref)
	verdict := health.Gate(checks, r.common.Policy)
	r.addFailures(verdict.Failures)
	if verdict.Decision == health.Block {
		res.Status = StepBlocked
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown, verdict.Summary()+"; "+name+" was not changed")
		return res, false, false
	}
	worse := health.Worse(r.accepted, checks)
	if len(worse) == 0 {
		return res, true, false
	}
	reasons := reasonStrings(worse)
	text := "the health checks got worse since the plan (" + strings.Join(reasons, "; ") + ")"
	switch {
	case r.common.Policy.Yes:
		r.notice(text + "; going on with --yes")
	case r.hooks.Ask == nil:
		res.Status, res.FinishedAt = StepFailed, r.d.now()
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown,
			text+", which needs an answer, and nobody can be asked; pass --yes or --require-healthy. "+name+" was not changed")
		return res, false, false
	case !mayAsk:
		res.Status, res.FinishedAt = StepFailed, r.d.now()
		res.Failure = r.stepFailure(cp, res, apidoc.ReasonUnknown, name+" keeps changing ("+strings.Join(reasons, "; ")+"); it was not changed. Run the command again")
		return res, false, false
	default:
		q := Question{Kind: GateWarnings, Step: res.Ref, Text: capitalize(text) + ". Continue with " + name + "?", Reasons: reasons}
		// The budgets do not run while a person answers.
		r.read.pause()
		yes, err := r.hooks.Ask(r.ctx, q)
		r.read.start()
		switch {
		case err != nil && r.ctx.Err() != nil:
			return r.unsent(cp, res, err), false, false
		case err != nil:
			r.stop = &stop{status: Failed, err: fmt.Errorf("could not ask %q: %w; nothing more was changed", q.Text, err)}
			r.hookErr = err
		case !yes:
			r.stop = &stop{status: Declined, err: errors.New("declined; nothing more was changed")}
		}
		if r.stop != nil {
			res.Status = StepNotAttempted
			return res, false, false
		}
		asked = true
	}
	r.accepted = mergeAccepted(r.accepted, checks)
	return res, true, asked
}

// mergeAccepted adds cur to the results that the consent covers. A check
// that both have keeps the worse outcome of the two and the findings of
// both, so a finding that was accepted once is not asked about again.
func mergeAccepted(accepted, cur []health.Result) []health.Result {
	out := slices.Clone(accepted)
	for _, c := range cur {
		i := slices.IndexFunc(out, func(a health.Result) bool { return a.Name == c.Name })
		if i < 0 {
			out = append(out, c)
			continue
		}
		merged := out[i]
		if c.Outcome != health.Skipped && len(health.Worse([]health.Result{{Name: c.Name, Outcome: merged.Outcome}}, []health.Result{{Name: c.Name, Outcome: c.Outcome}})) > 0 {
			merged.Outcome, merged.Message = c.Outcome, c.Message
		}
		merged.Findings = slices.Sorted(slices.Values(slices.Concat(merged.Findings, c.Findings)))
		merged.Findings = slices.Compact(merged.Findings)
		out[i] = merged
	}
	return out
}

// addFailures adds failures to the ones of the run, each one once: the
// same read can fail before each step.
func (r *runner) addFailures(failures []apidoc.Failure) {
	for _, f := range failures {
		same := func(have apidoc.Failure) bool {
			return have.Kind == f.Kind && have.Name == f.Name && have.Operation == f.Operation && have.Reason == f.Reason && have.Error == f.Error
		}
		if !slices.ContainsFunc(r.failures, same) {
			r.failures = append(r.failures, f)
		}
	}
}

// recheck reads the cluster again before a submit that follows a wait of
// this run. The wait took time, so the read of the plan says nothing any
// more about what else is changing, or about the health of the targets
// that are still to come. It serves every kind of change: the kind of the
// request picks the column of the busy rule, and checks picks the gate.
//
// It applies the busy rule with the steps of the phase as the steps of
// the run, as the plan did: a cluster that is busy now with a change that
// is not of this run blocks this step, as it would have blocked the plan.
// A target of this run that is still changing, because its wait timed
// out, does not. Then it runs the checks that checks returns for the new
// snapshot, over the targets that remain, and regate decides. checks
// returns nil when no target remains to judge.
//
// It returns false when the step must not go on, with its result.
func (r *runner) recheck(cp *ClusterPlan, res StepResult, checks func(snap *fleet.Snapshot) []health.Check) (StepResult, bool) {
	clients := r.d.AWS.Clients(cp.Region)
	fetch := r.d.Fetch
	if fetch == nil {
		fetch = &fleet.Fetcher{Now: r.d.Now}
	}
	snap, err := fetch.Fresh().Fetch(r.readCtx, clients, cp.Cluster, fleet.Need{Nodegroups: true, Addons: true})
	if err != nil {
		return r.unsent(cp, res, err), false
	}
	name := stepName(res.Ref)
	if refuse, _ := Busy(snap, r.req.kind(), r.steps); len(refuse) > 0 {
		reason := apidoc.ReasonBusy
		parts := make([]string, len(refuse))
		for i, b := range refuse {
			parts[i] = strings.Replace(b.Message, "nothing was started", name+" was not changed", 1)
			if b.Kind != BlockerBusy {
				// The cluster could not be read in full: exit code 4.
				reason = apidoc.ReasonUnknown
				r.addFailures(snap.Failures)
			}
		}
		res.Status = StepBlocked
		res.Failure = r.stepFailure(cp, res, reason, strings.Join(parts, "; "))
		return res, false
	}
	if r.common.Policy.Skip {
		return res, true
	}
	set := checks(snap)
	if len(set) == 0 {
		return res, true
	}
	results := health.Run(r.readCtx, set, &health.Input{Snapshot: snap, Clients: clients, Now: snap.FetchedAt})
	out, ok, _ := r.regate(cp, res, results, true)
	return out, ok
}

// remainingAddons returns the gate of an add-on update over the add-ons
// that remain: the Submit steps from the step that runs on. A target that
// is changing now is left out: its own step reports it as busy or
// attaches to it. A target that snap could not read is left out too: its
// own fresh read decides.
func (r *runner) remainingAddons(snap *fleet.Snapshot) []health.Check {
	var targets []string
	for _, s := range r.remaining {
		i := slices.IndexFunc(snap.Addons, func(a fleet.Addon) bool { return a.Name == s.Ref.Name })
		if s.Action == Submit && i >= 0 && snap.Addons[i].State == fleet.Collected && !addonInFlight(snap.Addons[i].Status) {
			targets = append(targets, s.Ref.Name)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	return []health.Check{health.Addons(targets)}
}

// freshAddonHealth returns the result of the add-on check for one target,
// from the fresh read before its submit.
func (r *runner) freshAddonHealth(cp *ClusterPlan, name string, fresh Settled) []health.Result {
	snap := &fleet.Snapshot{
		Cluster: cp.snapshot.Cluster,
		Addons:  []fleet.Addon{{Name: name, State: fleet.Collected, Version: fresh.Version, Status: fresh.Status}},
	}
	return health.Run(r.readCtx, []health.Check{health.Addons([]string{name})}, &health.Input{
		Snapshot: snap, Clients: r.d.AWS.Clients(cp.Region), Now: r.d.now(),
	})
}
