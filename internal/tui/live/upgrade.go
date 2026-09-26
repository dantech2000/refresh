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
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// upgrader is the orchestrator of `cluster upgrade`. Tests replace it.
type upgrader interface {
	BuildPlan(ctx context.Context, cluster, target string, opts upgrade.PlanOptions) (*upgrade.Plan, error)
	Execute(ctx context.Context, plan *upgrade.Plan, opts upgrade.ExecuteOptions) (*upgrade.Report, error)
}

func defaultUpgrader(opts Options) func(aws.Config) upgrader {
	return func(cfg aws.Config) upgrader {
		return upgrade.NewService(factory.NewEKSClient(cfg), opts.Logger)
	}
}

// errStoppedByUser stops a run from a gate when the user asked to stop.
var errStoppedByUser = errors.New("stopped as asked; in-flight EKS updates finish, and a rerun resumes from live cluster state")

// liveUpgrade is an upgrade this backend runs.
type liveUpgrade struct {
	t  target
	st state.Upgrade
	// answers carries the user's answer to st.Question.
	answers chan bool
	// wake is signalled when pause or stop changes, so a waiting Confirm
	// looks again.
	wake chan struct{}
	// labels are the engine's phase labels, by timeline phase (Phases[i+1]).
	labels []string
}

// pendingSteps names a plan's pending steps, in order: what the user
// confirms in the dry run.
func pendingSteps(p *upgrade.Plan) []string {
	var out []string
	for _, hop := range p.Hops {
		for _, s := range hop.Steps {
			if s.Status == upgrade.StatusPending {
				out = append(out, fmt.Sprintf("%s %s %s", s.Type, s.Target, s.Version))
			}
		}
	}
	return out
}

// warned names the checks that warned or failed, skipping skipped ones.
func warned(s health.HealthSummary) []string {
	var out []string
	for _, r := range s.Results {
		if !r.Skipped && r.Status != health.StatusPass {
			out = append(out, r.Name+" ("+string(r.Status)+")")
		}
	}
	return out
}

// stepDiff names the steps now has that was lacks (+) and the steps was has
// that now lacks (−), so the user sees exactly what a changed plan runs.
func stepDiff(was, now []string) string {
	var parts []string
	for _, s := range now {
		if !slices.Contains(was, s) {
			parts = append(parts, "+ "+s)
		}
	}
	for _, s := range was {
		if !slices.Contains(now, s) {
			parts = append(parts, "− "+s)
		}
	}
	if len(parts) == 0 {
		return "the same steps in a new order"
	}
	return strings.Join(parts, ", ")
}

// planUpgradeLive records what the user confirms with y: the target and the
// pending steps of the preview.
func (b *Backend) planUpgradeLive(t target, plan *upgrade.Plan) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.acceptedUpgrades[t] = acceptedUpgrade{target: plan.TargetVersion, steps: pendingSteps(plan)}
}

type acceptedUpgrade struct {
	target string
	steps  []string
}

// startUpgrade claims the cluster and runs the upgrade in the background.
func (b *Backend) startUpgrade(ctx context.Context, a state.Action) error {
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
	acc, planned := b.acceptedUpgrades[t]
	if !planned {
		b.mu.Unlock()
		return errors.New("no dry run for this upgrade: open its dry run (U) first")
	}
	delete(b.acceptedUpgrades, t)
	b.claimed[t] = "upgrading"
	u := &liveUpgrade{t: t, answers: make(chan bool, 1), wake: make(chan struct{}, 1)}
	u.st = state.Upgrade{Cluster: a.Cluster, From: c.Version, To: acc.target, StartedAt: b.now(),
		Phases: []state.Phase{{Name: "Plan", Weight: 0.05, Status: state.PhaseRunning, StartedAt: b.now()}}}
	b.upgrades = append(b.upgrades, u)
	b.upgradeEvent(u, state.LevelProgress, "plan", "building the plan for "+acc.target, "insights may refresh first")
	b.emit(state.Event{Cluster: a.Cluster, Source: state.SourceUpgrade, Level: state.LevelProgress, Subject: "upgrade", Text: "started " + c.Version + " → " + acc.target})
	runCtx := b.runCtx
	b.mu.Unlock()
	if runCtx == nil {
		runCtx = ctx
	}
	b.work.Add(1)
	go func() {
		defer b.work.Done()
		b.runUpgrade(runCtx, cfg, u, acc)
	}()
	return nil
}

// upgradeEvent records an event on u's timeline. The caller holds b.mu.
func (b *Backend) upgradeEvent(u *liveUpgrade, lvl state.Level, subject, text, detail string) {
	u.st.Events = appendCapped(u.st.Events, b.stamp(state.Event{Cluster: b.keyOf(u.t), Source: state.SourceUpgrade, Level: lvl, Subject: subject, Text: text, Detail: detail}), logCap)
}

// ask puts a question on u and waits for the answer (false when ctx ends).
func (b *Backend) ask(ctx context.Context, u *liveUpgrade, q string) bool {
	b.mu.Lock()
	u.st.Question = q
	b.upgradeEvent(u, state.LevelWarn, "question", q, "y go on · n stop")
	b.mu.Unlock()
	var yes bool
	select {
	case yes = <-u.answers:
	case <-ctx.Done():
	}
	b.mu.Lock()
	u.st.Question = ""
	answer := "no"
	if yes {
		answer = "yes"
	}
	b.upgradeEvent(u, state.LevelInfo, "answer", answer, "")
	b.mu.Unlock()
	return yes
}

func (b *Backend) runUpgrade(ctx context.Context, cfg aws.Config, u *liveUpgrade, acc acceptedUpgrade) {
	if d := b.opts.UpgradeTimeout; d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	svc := b.newUpgrader(cfg)
	progress := func(format string, args ...any) {
		b.mu.Lock()
		b.upgradeEvent(u, state.LevelInfo, "", fmt.Sprintf(format, args...), "")
		b.mu.Unlock()
	}

	// The real plan: not a preview, so it refreshes insights and blocks
	// until EKS has evaluated them, as `cluster upgrade` does.
	plan, err := svc.BuildPlan(ctx, u.t.name, acc.target, upgrade.PlanOptions{Progress: progress})
	if err != nil {
		b.endUpgrade(u, "the plan could not be built: "+awsinternal.FormatAWSError(err, "building the upgrade plan for "+u.t.name).Error(), false)
		return
	}
	if plan.Blocked() {
		b.endUpgrade(u, "blocked: "+strings.Join(plan.Blockers(), "; "), false)
		return
	}
	if steps := pendingSteps(plan); !slices.Equal(steps, acc.steps) {
		if !b.ask(ctx, u, "the plan changed since the dry run: "+stepDiff(acc.steps, steps)+"; go on with the new plan?") {
			b.endUpgrade(u, "", true)
			return
		}
	}

	b.mu.Lock()
	b.setPhases(u, plan)
	b.mu.Unlock()

	opts := upgrade.ExecuteOptions{
		Progress: progress,
		PhaseStart: func(label string) {
			b.mu.Lock()
			b.startPhase(u, label)
			b.mu.Unlock()
		},
		// Every phase was confirmed with the dry run. Confirm holds a phase
		// while the run is paused and declines once a stop is asked for.
		Confirm: func(string) bool { return b.waitToGoOn(ctx, u) },
		NodegroupGate: func(gctx context.Context, ng string) error {
			return b.upgradeGate(gctx, cfg, u, ng)
		},
		NodegroupObserver: func(octx context.Context, ng string) {
			b.observeUpgradeRoll(octx, u, ng)
		},
	}
	report, err := svc.Execute(ctx, plan, opts)
	// Only the two sentinels mean the user stopped the run: a stop asked for
	// during a phase that then fails is still a failure.
	switch {
	case err == nil:
		b.endUpgrade(u, "", false)
	case errors.Is(err, upgrade.ErrAborted):
		// Confirm declined before the next phase: the running one finished.
		b.mu.Lock()
		if cur := u.st.Current(); cur >= 0 {
			u.st.Phases[cur].Status, u.st.Phases[cur].Progress, u.st.Phases[cur].EndedAt = state.PhaseDone, 1, b.now()
		}
		b.mu.Unlock()
		b.endUpgrade(u, "", true)
	case errors.Is(err, errStoppedByUser):
		b.endUpgrade(u, "", true)
	default:
		why := err.Error()
		if report != nil && report.Failure != nil {
			why = report.Failure.Error
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) { // the run deadline, not one call's
			why = fmt.Sprintf("the upgrade ran past %s; in-flight EKS updates continue, and a rerun resumes from live cluster state: %s", b.opts.UpgradeTimeout, why)
		}
		b.endUpgrade(u, why, false)
	}
}

// waitToGoOn is the engine's Confirm: it waits while the run is paused and
// returns false once a stop was asked for.
func (b *Backend) waitToGoOn(ctx context.Context, u *liveUpgrade) bool {
	for {
		b.mu.Lock()
		stop, paused := u.st.StopAfter, u.st.Paused
		b.mu.Unlock()
		switch {
		case stop:
			return false
		case !paused:
			return true
		}
		select {
		case <-u.wake:
		case <-ctx.Done():
			return false
		}
	}
}

// upgradeGate is the pre-roll gate: the health gate of `nodegroup update`
// for this nodegroup. Blockers stop the run; warnings are the user's call.
func (b *Backend) upgradeGate(ctx context.Context, cfg aws.Config, u *liveUpgrade, ng string) error {
	b.mu.Lock()
	stop := u.st.StopAfter
	b.mu.Unlock()
	if stop {
		return errStoppedByUser
	}
	kube, metrics, _ := b.roll.kubeFor(ctx, cfg, u.t.name)
	// As `cluster upgrade` does without --force: a PDB that would refuse an
	// eviction stops the run before the roll, whatever the user answers.
	if kube != nil {
		report, err := b.roll.drainBlockers(ctx, cfg, u.t.name, ng, kube)
		if err != nil {
			return fmt.Errorf("checking PodDisruptionBudgets for nodegroup %s: %w", ng, err)
		}
		if names := report.Names(); len(names) > 0 {
			return fmt.Errorf("%d drain blocker(s) would stop draining nodegroup %s: %s; let the workloads recover, relax the PDBs, or narrow PDB selectors so each pod matches one PDB",
				len(names), ng, strings.Join(names, "; "))
		}
	}
	summary := b.roll.healthCheck(ctx, cfg, u.t.name, []string{ng}, kube, metrics)
	if _, blocked := healthGates(summary); len(blocked) > 0 {
		return fmt.Errorf("the health gate blocks the roll of %s: %s", ng, strings.Join(blocked, ", "))
	}
	// As `cluster upgrade` asks: on a WARN decision, naming the checks that
	// warned or failed. A skipped check is not a warning here.
	if summary.Decision == health.DecisionWarn {
		if !b.ask(ctx, u, fmt.Sprintf("health warnings before rolling %s: %s; roll it?", ng, strings.Join(warned(summary), ", "))) {
			return errStoppedByUser
		}
	}
	return nil
}

// observeUpgradeRoll shows one nodegroup roll of the upgrade on the rolls
// screen until the engine's wait ends (octx).
func (b *Backend) observeUpgradeRoll(octx context.Context, u *liveUpgrade, ng string) {
	kube, _, how := b.roll.kubeFor(octx, b.cfgOf(u.t), u.t.name)
	b.mu.Lock()
	r := &liveRoll{t: u.t, tracker: noderoll.NewTracker(), warned: map[string]bool{}}
	r.st = state.Roll{Nodegroup: ng, FromVersion: u.st.From, ToVersion: u.st.To, ToAMI: "latest for " + u.st.To,
		StartedAt: b.now(), UpgradeOf: b.keyOf(u.t), MaxUnavailableText: "per update config"}
	b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: state.LevelInfo, Subject: "node view", Text: how})
	b.rolls = append(b.rolls, r)
	b.claimed[u.t] = "upgrading · rolling " + ng
	b.mu.Unlock()

	var obs rollObserver
	if kube != nil {
		obs = b.roll.observe(kube, ng)
		if err := obs.StartInformers(octx); err == nil {
			defer obs.StopInformers()
		}
		if err := obs.CaptureBaseline(octx); err != nil {
			obs = nil
		}
	}
	tick := time.NewTicker(b.opts.ObserveInterval)
	defer tick.Stop()
	b.observeOnce(octx, r, obs, true)
	for {
		select {
		case <-octx.Done():
			b.mu.Lock()
			r.st.EndedAt = b.now()
			b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: state.LevelInfo, Subject: ng, Text: "roll wait ended", Detail: "the upgrade's timeline has the result"})
			b.claimed[u.t] = "upgrading"
			b.mu.Unlock()
			return
		case <-tick.C:
			b.observeOnce(octx, r, obs, false)
		}
	}
}

func (b *Backend) cfgOf(t target) aws.Config {
	cfg := b.base.Copy()
	cfg.Region = t.region
	return cfg
}

// setPhases lays out the timeline from the plan: the plan phase, then each
// hop's control plane, add-ons, and nodegroups that have pending steps. The
// caller holds b.mu.
func (b *Backend) setPhases(u *liveUpgrade, plan *upgrade.Plan) {
	u.st.Phases[0].Status, u.st.Phases[0].EndedAt, u.st.Phases[0].Progress = state.PhaseDone, b.now(), 1
	u.st.Phases[0].Summary = fmt.Sprintf("%d step(s) to %s", len(pendingSteps(plan)), plan.TargetVersion)
	for _, hop := range plan.Hops {
		var cp, ad, ng []state.PhaseItem
		for _, s := range hop.Steps {
			if s.Status != upgrade.StatusPending {
				continue
			}
			item := state.PhaseItem{Name: s.Target, Text: s.Version}
			switch s.Type {
			case upgrade.StepControlPlane:
				cp = append(cp, state.PhaseItem{Name: "control plane", Text: hop.From + " → " + hop.To})
			case upgrade.StepAddon:
				ad = append(ad, item)
			case upgrade.StepNodegroup:
				ng = append(ng, item)
			}
		}
		for _, p := range []struct {
			name  string
			label string
			items []state.PhaseItem
			w     float64
		}{
			{"Control plane " + hop.To, fmt.Sprintf("control plane %s → %s", hop.From, hop.To), cp, 0.35},
			{"Add-ons " + hop.To, "addons for " + hop.To, ad, 0.15},
			{"Nodegroups " + hop.To, "nodegroup rolls to " + hop.To, ng, 0.45},
		} {
			if len(p.items) == 0 {
				continue
			}
			u.st.Phases = append(u.st.Phases, state.Phase{Name: p.name, Summary: "", Items: p.items, Weight: p.w})
			u.labels = append(u.labels, p.label)
		}
	}
}

// startPhase marks the phase whose engine label starts with label's prefix
// as running, and the one before it done. The caller holds b.mu.
func (b *Backend) startPhase(u *liveUpgrade, label string) {
	for i, prefix := range u.labels {
		if !strings.HasPrefix(label, prefix) {
			continue
		}
		idx := i + 1 // Phases[0] is the plan
		for j := 1; j < idx; j++ {
			if u.st.Phases[j].Status == state.PhaseRunning || u.st.Phases[j].Status == state.PhasePending {
				u.st.Phases[j].Status, u.st.Phases[j].Progress, u.st.Phases[j].EndedAt = state.PhaseDone, 1, b.now()
			}
		}
		u.st.Phases[idx].Status, u.st.Phases[idx].StartedAt = state.PhaseRunning, b.now()
		b.claimed[u.t] = "upgrading · " + strings.ToLower(u.st.Phases[idx].Name)
		b.upgradeEvent(u, state.LevelProgress, "phase", label, "")
		return
	}
	b.upgradeEvent(u, state.LevelInfo, "phase", label, "")
}

// endUpgrade records how the run ended and frees the cluster.
func (b *Backend) endUpgrade(u *liveUpgrade, failed string, stopped bool) {
	b.mu.Lock()
	now := b.now()
	u.st.EndedAt = now
	u.st.Question = ""
	cur := u.st.Current()
	switch {
	case failed != "":
		u.st.Failed = failed
		if cur >= 0 {
			u.st.Phases[cur].Status, u.st.Phases[cur].EndedAt, u.st.Phases[cur].Summary = state.PhaseFailed, now, failed
		}
		b.upgradeEvent(u, state.LevelError, "upgrade", "failed", failed)
	case stopped:
		u.st.Stopped = true
		if cur >= 0 {
			u.st.Phases[cur].Status, u.st.Phases[cur].EndedAt = state.PhaseStopped, now
		}
		b.upgradeEvent(u, state.LevelWarn, "upgrade", "stopped", "in-flight EKS updates finish; a rerun resumes from live cluster state")
	default:
		for i := range u.st.Phases {
			if u.st.Phases[i].Status != state.PhaseDone {
				u.st.Phases[i].Status, u.st.Phases[i].Progress, u.st.Phases[i].EndedAt = state.PhaseDone, 1, now
			}
		}
		b.upgradeEvent(u, state.LevelOK, "upgrade", "done · "+b.keyOf(u.t)+" on "+u.st.To, now.Sub(u.st.StartedAt).Round(time.Second).String())
	}
	lvl, text := state.LevelOK, "upgrade done · "+u.st.To
	switch {
	case failed != "":
		lvl, text = state.LevelError, "upgrade failed"
	case stopped:
		lvl, text = state.LevelWarn, "upgrade stopped"
	}
	b.emit(state.Event{Cluster: b.keyOf(u.t), Source: state.SourceUpgrade, Level: lvl, Subject: "upgrade", Text: text, Detail: failed})
	delete(b.claimed, u.t)
	b.mu.Unlock()
	b.Refresh()
}

// upgradeSnapshot copies an upgrade for State. The caller holds b.mu.
func (b *Backend) upgradeSnapshot(u *liveUpgrade) state.Upgrade {
	st := u.st
	st.Cluster = b.keyOf(u.t)
	st.Phases = make([]state.Phase, len(u.st.Phases))
	now := b.now()
	for i, p := range u.st.Phases {
		p.Items = slices.Clone(p.Items)
		if p.Status == state.PhaseRunning && strings.HasPrefix(p.Name, "Control plane") {
			// EKS reports no progress for a control-plane update, which
			// takes about ten minutes: estimate from the elapsed time, and
			// never show it done before EKS says so. A real upgrade sat at
			// "5%" with a pending item for the whole phase.
			est := min(0.95, float64(now.Sub(p.StartedAt))/float64(controlPlaneTypical))
			p.Progress = est
			for j := range p.Items {
				p.Items[j].Status, p.Items[j].Progress = state.PhaseRunning, est
			}
		}
		st.Phases[i] = p
	}
	st.Events = slices.Clone(u.st.Events)
	return st
}

// controlPlaneTypical is how long an EKS control-plane version update
// usually takes (the progress estimate's full scale).
const controlPlaneTypical = 10 * time.Minute

// runningUpgrade returns the running upgrade on a fleet key. The caller
// holds b.mu.
func (b *Backend) runningUpgrade(key string) *liveUpgrade {
	t, ok := b.targets[key]
	if !ok {
		return nil
	}
	for i := len(b.upgrades) - 1; i >= 0; i-- {
		if u := b.upgrades[i]; u.t == t && u.st.Running() {
			return u
		}
	}
	return nil
}

func poke(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
