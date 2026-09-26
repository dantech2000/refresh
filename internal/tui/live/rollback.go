package live

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// A rollback runs `cluster rollback`'s planner and engine
// (upgrade.Service.BuildRollbackPlan + ExecuteRollback) on the Upgrade
// screen, with the same dry run, confirmation, pause, stop, questions, and
// pre-roll gate as an upgrade.

// rollbacker is the rollback planner and engine. Tests replace it.
type rollbacker interface {
	BuildRollbackPlan(ctx context.Context, cluster string, opts upgrade.RollbackOptions) (*upgrade.RollbackPlan, error)
	ExecuteRollback(ctx context.Context, plan *upgrade.RollbackPlan, opts upgrade.ExecuteOptions) (*upgrade.Report, error)
}

func defaultRollbacker(opts Options) func(aws.Config) rollbacker {
	return func(cfg aws.Config) rollbacker {
		return upgrade.NewService(factory.NewEKSClient(cfg), opts.Logger)
	}
}

// rollbackWindow is what a readiness run said about a rollback: from the
// control-plane version it saw, to the previous one, until about when.
type rollbackWindow struct {
	from, to string
	until    time.Time
}

// rollbackSteps names a rollback plan's pending changes, in order.
func rollbackSteps(p *upgrade.RollbackPlan) []string {
	var out []string
	for _, s := range p.Steps {
		if s.Status == upgrade.StatusPending && s.Type != upgrade.StepReadiness {
			out = append(out, fmt.Sprintf("%s %s %s", s.Type, s.Target, s.Version))
		}
	}
	return out
}

// planRollback turns a rollback plan into the dry-run dialog.
func planRollback(c state.Cluster, t target, plan *upgrade.RollbackPlan) state.Plan {
	p := state.Plan{
		Title:   fmt.Sprintf("Roll back cluster · %s %s → %s", c.Name, plan.CurrentVersion, plan.TargetVersion),
		Command: regionFlag(t) + "cluster rollback -c " + t.name,
		Changes: []state.Change{{Field: "control plane", From: plan.CurrentVersion, To: plan.TargetVersion}},
	}
	if plan.AvailableUntil != nil {
		p.Facts = append(p.Facts, state.Fact{Key: "window", Value: "until about " + plan.AvailableUntil.UTC().Format("2006-01-02 15:04 MST"), Note: "EKS counts 7 days from the end of the upgrade"})
	}
	for _, s := range plan.Steps {
		key := s.Target
		if key == "" {
			key = s.Description
		}
		switch {
		case s.Status == upgrade.StatusBlocked:
			p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckFail, Text: key, Note: s.Reason})
		case s.Status == upgrade.StatusManual:
			p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckWarn, Text: key, Note: "manual: " + s.Reason})
		case s.Type == upgrade.StepReadiness:
			p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckPass, Text: key, Note: s.Reason})
		case s.Status != upgrade.StatusPending:
		case s.Type == upgrade.StepNodegroup:
			p.Facts = append(p.Facts, state.Fact{Key: key, Value: "roll back to " + s.Version})
		case s.Type == upgrade.StepAddon:
			p.Facts = append(p.Facts, state.Fact{Key: key, Value: "→ " + s.Version, Note: s.Reason})
		}
	}
	for _, n := range plan.Notices {
		p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckWarn, Text: n})
	}
	for _, f := range plan.Failures {
		p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckWarn, Text: "could not read " + f.Name, Note: f.Error})
	}
	p.Gates = append(p.Gates,
		state.PlanGate{Status: state.CheckPending, Text: "nodegroup health gate", Note: "checked before each rollback roll; warnings ask y/n"},
		state.PlanGate{Status: state.CheckPending, Text: "PDB drain blockers", Note: "checked before each rollback roll; a blocker stops the run"})
	switch {
	case plan.Blocked():
		p.Blocked = strings.Join(plan.Blockers(), "; ")
	case plan.PendingSteps() == 0:
		p.Blocked = fmt.Sprintf("nothing to do: %s is rolled back to %s", c.Name, plan.TargetVersion)
	}
	return p
}

// startRollback claims the cluster and runs the rollback in the background.
func (b *Backend) startRollback(ctx context.Context, a state.Action) error {
	cfg, t, err := b.cfgFor(a.Cluster)
	if err != nil {
		return err
	}
	c, _ := b.cluster(a.Cluster)
	b.mu.Lock()
	if busy := b.busyOf(a.Cluster, c); busy != "" {
		b.mu.Unlock()
		return fmt.Errorf("%s is busy: %s", a.Cluster, busy)
	}
	acc, planned := b.acceptedRollbacks[t]
	if !planned {
		b.mu.Unlock()
		return errors.New("no dry run for this rollback: open its dry run (B) first")
	}
	delete(b.acceptedRollbacks, t)
	b.claimed[t] = "rolling back"
	u := &liveUpgrade{t: t, answers: make(chan bool, 1), wake: make(chan struct{}, 1)}
	u.st = state.Upgrade{Rollback: true, Cluster: a.Cluster, From: c.Version, To: acc.target, StartedAt: b.now(),
		Phases: []state.Phase{{Name: "Plan", Weight: 0.05, Status: state.PhaseRunning, StartedAt: b.now()}}}
	b.upgrades = append(b.upgrades, u)
	b.upgradeEvent(u, state.LevelProgress, "plan", "building the rollback plan for "+acc.target, "")
	b.emit(state.Event{Cluster: a.Cluster, Source: state.SourceUpgrade, Level: state.LevelProgress, Subject: "rollback", Text: "started " + c.Version + " → " + acc.target})
	runCtx := b.runCtx
	b.mu.Unlock()
	if runCtx == nil {
		runCtx = ctx
	}
	b.work.Add(1)
	go func() {
		defer b.work.Done()
		b.runRollback(runCtx, cfg, u, acc)
	}()
	return nil
}

func (b *Backend) runRollback(ctx context.Context, cfg aws.Config, u *liveUpgrade, acc acceptedUpgrade) {
	if d := b.opts.UpgradeTimeout; d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	svc := b.newRollbacker(cfg)
	plan, err := svc.BuildRollbackPlan(ctx, u.t.name, upgrade.RollbackOptions{})
	if err != nil {
		b.endUpgrade(u, "the plan could not be built: "+awsinternal.FormatAWSError(err, "building the rollback plan for "+u.t.name).Error(), false)
		return
	}
	switch {
	case plan.Blocked():
		b.endUpgrade(u, "blocked: "+strings.Join(plan.Blockers(), "; "), false)
		return
	case plan.TargetVersion != acc.target:
		b.endUpgrade(u, fmt.Sprintf("the rollback target is now %s, not %s as in the dry run; open the dry run again", plan.TargetVersion, acc.target), false)
		return
	}
	if steps := rollbackSteps(plan); !slices.Equal(steps, acc.steps) {
		if !b.ask(ctx, u, "the plan changed since the dry run: "+stepDiff(acc.steps, steps)+"; go on with the new plan?") {
			b.endUpgrade(u, "", true)
			return
		}
	}

	b.mu.Lock()
	b.setRollbackPhases(u, plan)
	b.mu.Unlock()

	report, err := svc.ExecuteRollback(ctx, plan, b.engineOptions(ctx, cfg, u, b.progressOf(u)))
	b.finishRun(ctx, u, report, err)
}

// setRollbackPhases lays out the timeline in the engine's order: nodegroups,
// add-ons, control plane, each only when it has pending steps. The caller
// holds b.mu.
func (b *Backend) setRollbackPhases(u *liveUpgrade, plan *upgrade.RollbackPlan) {
	to := plan.TargetVersion
	u.st.Phases[0].Status, u.st.Phases[0].EndedAt, u.st.Phases[0].Progress = state.PhaseDone, b.now(), 1
	u.st.Phases[0].Summary = fmt.Sprintf("%d step(s) back to %s", len(rollbackSteps(plan)), to)
	var ng, ad, cp []state.PhaseItem
	for _, s := range plan.Steps {
		if s.Status != upgrade.StatusPending {
			continue
		}
		switch s.Type {
		case upgrade.StepNodegroup:
			ng = append(ng, state.PhaseItem{Name: s.Target, Text: s.Version})
		case upgrade.StepAddon:
			ad = append(ad, state.PhaseItem{Name: s.Target, Text: s.Version})
		case upgrade.StepControlPlane:
			cp = append(cp, state.PhaseItem{Name: "control plane", Text: plan.CurrentVersion + " → " + to})
		}
	}
	for _, p := range []struct {
		name, label string
		items       []state.PhaseItem
		w           float64
	}{
		{"Nodegroups " + to, "nodegroup rollbacks to " + to, ng, 0.45},
		{"Add-ons " + to, "addon downgrades for " + to, ad, 0.1},
		// "Control plane" first, so the timeline estimates its progress.
		{"Control plane rollback " + to, "control plane rollback", cp, 0.4},
	} {
		if len(p.items) == 0 {
			continue
		}
		u.st.Phases = append(u.st.Phases, state.Phase{Name: p.name, Items: p.items, Weight: p.w})
		u.labels = append(u.labels, p.label)
	}
}
