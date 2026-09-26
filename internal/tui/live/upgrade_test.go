package live

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/health"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// fakeUpgrader plays the orchestrator: it calls the hooks in the order
// Execute does (Confirm, then PhaseStart, then per nodegroup the gate and
// the observer) and records what happened.
type fakeUpgrader struct {
	mu       sync.Mutex
	plan     *upgrade.Plan
	built    int
	phases   []string
	rolled   []string
	phaseRun chan string // receives each phase as it starts, when set
	// phaseAck, when set, holds the run after each phase announcement until
	// the test lets it go on.
	phaseAck chan struct{}
	// failRoll, when set, fails the roll of that nodegroup; duringRoll runs
	// while it rolls.
	failRoll   string
	duringRoll func()
}

func (f *fakeUpgrader) BuildPlan(context.Context, string, string, upgrade.PlanOptions) (*upgrade.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.built++
	return f.plan, nil
}

func (f *fakeUpgrader) Execute(ctx context.Context, plan *upgrade.Plan, opts upgrade.ExecuteOptions) (*upgrade.Report, error) {
	for _, hop := range plan.Hops {
		for _, ph := range []string{"control plane " + hop.From + " → " + hop.To, "addons for " + hop.To + " (1 update(s), dependency order)", "nodegroup rolls to " + hop.To + " (2 nodegroup(s))"} {
			if !opts.Confirm(ph) {
				return &upgrade.Report{Status: upgrade.RunAborted}, upgrade.ErrAborted
			}
			opts.PhaseStart(ph)
			f.mu.Lock()
			f.phases = append(f.phases, ph)
			f.mu.Unlock()
			if f.phaseRun != nil {
				f.phaseRun <- ph
			}
			if f.phaseAck != nil {
				<-f.phaseAck
			}
			opts.Progress("working on %s", ph)
			if strings.HasPrefix(ph, "nodegroup rolls") {
				for _, ng := range []string{"ng-a", "ng-b"} {
					if err := opts.NodegroupGate(ctx, ng); err != nil {
						return &upgrade.Report{Status: upgrade.RunFailed}, fmt.Errorf("%s failed: %w", ph, err)
					}
					octx, cancel := context.WithCancel(ctx)
					done := make(chan struct{})
					go func() { opts.NodegroupObserver(octx, ng); close(done) }()
					cancel() // the roll's wait ended
					<-done
					if ng == f.failRoll {
						if f.duringRoll != nil {
							f.duringRoll()
						}
						return &upgrade.Report{Status: upgrade.RunFailed}, fmt.Errorf("%s failed: roll failed: NodeCreationFailure", ph)
					}
					f.mu.Lock()
					f.rolled = append(f.rolled, ng)
					f.mu.Unlock()
				}
			}
		}
	}
	return &upgrade.Report{Status: upgrade.RunSucceeded}, nil
}

func upgradePlan() *upgrade.Plan {
	return &upgrade.Plan{ClusterName: "prod-api", CurrentVersion: "1.31", TargetVersion: "1.32", Hops: []upgrade.Hop{{From: "1.31", To: "1.32", Steps: []upgrade.Step{
		{Type: upgrade.StepControlPlane, Description: "control plane to 1.32", Version: "1.32", Status: upgrade.StatusPending},
		{Type: upgrade.StepAddon, Target: "vpc-cni", Version: "v1.19.2", Status: upgrade.StatusPending},
		{Type: upgrade.StepNodegroup, Target: "ng-a", Version: "1.32", Status: upgrade.StatusPending},
		{Type: upgrade.StepNodegroup, Target: "ng-b", Version: "1.32", Status: upgrade.StatusPending},
	}}}}
}

type upgradeRig struct {
	b  *Backend
	f  *fakeUpgrader
	hw []string // health warnings the gate finds
	// hwOnly, when set, limits hw to the gate before that nodegroup.
	hwOnly   string
	blockers []health.PDBInfo // PDBs the drain-blocker check finds
	mu       sync.Mutex
}

func newUpgradeRig(t *testing.T) *upgradeRig {
	t.Helper()
	rows := prodRows()
	rows[0].Nodegroups[1].Status = "ACTIVE"
	fl := &fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": rows}}
	rig := &upgradeRig{b: newTestBackend(t, fl, "us-east-1"), f: &fakeUpgrader{plan: upgradePlan()}}
	b := rig.b
	b.opts.AllowChanges = true
	b.svc.buildPlan = func(context.Context, aws.Config, string, string) (*upgrade.Plan, error) { return upgradePlan(), nil }
	b.newUpgrader = func(aws.Config) upgrader { return rig.f }
	rr := newRollRig(t) // reuse its fake kube, health, and observer
	b.roll = rr.b.roll
	b.roll.drainBlockers = func(context.Context, aws.Config, string, string, kubernetes.Interface) (health.DrainBlockerReport, error) {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		return health.DrainBlockerReport{Blockers: rig.blockers, Scoped: true}, nil
	}
	b.roll.healthCheck = func(_ context.Context, _ aws.Config, _ string, ngs []string, _ kubernetes.Interface, _ health.NodeMetricsLister) health.HealthSummary {
		rig.mu.Lock()
		defer rig.mu.Unlock()
		s := health.HealthSummary{Decision: health.DecisionProceed}
		if rig.hwOnly != "" && (len(ngs) != 1 || ngs[0] != rig.hwOnly) {
			return s
		}
		// Skipped checks do not change the decision, as in RunAllChecks.
		s.Results = append(s.Results, health.HealthResult{Name: "Service Quotas", Status: health.StatusPass, Skipped: true})
		for _, w := range rig.hw {
			s.Decision = health.DecisionWarn
			s.Results = append(s.Results, health.HealthResult{Name: w, Status: health.StatusWarn})
		}
		return s
	}
	b.sweep(t.Context())
	return rig
}

var upgradeAction = state.Action{Kind: state.ActionUpgrade, Cluster: "prod-api"}

func (rig *upgradeRig) start(t *testing.T) {
	t.Helper()
	p, err := rig.b.Plan(t.Context(), upgradeAction)
	if err != nil || p.Blocked != "" {
		t.Fatalf("plan = %+v, %v", p, err)
	}
	if err := rig.b.Start(t.Context(), upgradeAction); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeRunsThroughTheOrchestrator(t *testing.T) {
	rig := newUpgradeRig(t)
	rig.start(t)
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	if len(st.Upgrades) != 1 {
		t.Fatalf("upgrades = %+v", st.Upgrades)
	}
	u := st.Upgrades[0]
	if u.Running() || u.Failed != "" || u.Stopped || u.Progress() != 1 {
		t.Fatalf("upgrade = %+v", u)
	}
	names := []string{}
	for _, p := range u.Phases {
		names = append(names, p.Name)
	}
	if got := strings.Join(names, ","); got != "Plan,Control plane 1.32,Add-ons 1.32,Nodegroups 1.32" {
		t.Fatalf("phases = %s", got)
	}
	if rig.f.built != 1 || strings.Join(rig.f.rolled, ",") != "ng-a,ng-b" {
		t.Fatalf("built %d plans, rolled %v", rig.f.built, rig.f.rolled)
	}
	// Each nodegroup roll shows on the rolls screen as part of the upgrade.
	if len(st.Rolls) != 2 || st.Rolls[0].UpgradeOf != "prod-api" {
		t.Fatalf("rolls = %+v", st.Rolls)
	}
	for _, c := range st.Clusters {
		if c.Name == "prod-api" && c.Busy != "" {
			t.Fatalf("still busy: %q", c.Busy)
		}
	}
	if !strings.Contains(joinText(st.Feed), "upgrade done · 1.32") {
		t.Fatalf("feed:\n%s", joinText(st.Feed))
	}
	// A finished upgrade shows every item done, not pending.
	for _, ph := range st.Upgrades[0].Phases {
		for _, it := range ph.Items {
			if it.Status != state.PhaseDone {
				t.Fatalf("%s item %s is %v after the upgrade", ph.Name, it.Name, it.Status)
			}
		}
	}
}

func TestUpgradeNeedsItsDryRun(t *testing.T) {
	rig := newUpgradeRig(t)
	if err := rig.b.Start(t.Context(), upgradeAction); err == nil || !strings.Contains(err.Error(), "no dry run") {
		t.Fatalf("Start = %v", err)
	}
}

func TestHealthWarningsBeforeARollAreAsked(t *testing.T) {
	rig := newUpgradeRig(t)
	rig.hw = []string{"PodDisruptionBudgets"}
	rig.start(t)
	// The question stays up until it is answered, so look until it shows.
	var q string
	for q == "" {
		st, _ := rig.b.State(t.Context())
		if !st.Upgrades[0].Running() {
			t.Fatal("the upgrade ended without asking")
		}
		q = st.Upgrades[0].Question
		runtime.Gosched()
	}
	if !strings.Contains(q, "health warnings before rolling ng-a: PodDisruptionBudgets (Warn)") {
		t.Fatalf("question = %q", q)
	}
	// No: the run stops before rolling anything.
	if err := rig.b.Answer(t.Context(), "prod-api", false); err != nil {
		t.Fatal(err)
	}
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	rig.f.mu.Lock()
	rolled := rig.f.rolled
	rig.f.mu.Unlock()
	if u := st.Upgrades[0]; !u.Stopped || u.Failed != "" || len(rolled) != 0 {
		t.Fatalf("upgrade = %+v, rolled %v", u, rolled)
	}
	if err := rig.b.Answer(t.Context(), "prod-api", true); err == nil {
		t.Fatal("an answer with no question was taken")
	}
}

func TestPauseHoldsAndStopEndsBeforeTheNextPhase(t *testing.T) {
	rig := newUpgradeRig(t)
	rig.f.phaseRun = make(chan string)
	rig.f.phaseAck = make(chan struct{})
	rig.start(t)
	first := <-rig.f.phaseRun // the control plane phase started; the run waits
	if !strings.HasPrefix(first, "control plane") {
		t.Fatalf("first phase = %q", first)
	}
	if err := rig.b.TogglePause(t.Context(), "prod-api"); err != nil {
		t.Fatal(err)
	}
	// Paused: the add-ons phase waits in Confirm. Asking to stop now ends
	// the run there, with the control plane done and nothing else started.
	if err := rig.b.StopAfterCurrent(t.Context(), "prod-api"); err != nil {
		t.Fatal(err)
	}
	rig.f.phaseAck <- struct{}{} // let the control plane phase finish
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	u := st.Upgrades[0]
	if !u.Stopped || len(rig.f.phases) != 1 {
		t.Fatalf("upgrade = %+v, phases run %v", u, rig.f.phases)
	}
	if err := rig.b.TogglePause(t.Context(), "prod-api"); err == nil {
		t.Fatal("pause on a finished upgrade succeeded")
	}
}

func TestAChangedPlanIsAsked(t *testing.T) {
	rig := newUpgradeRig(t)
	p := upgradePlan()
	p.Hops[0].Steps = append(p.Hops[0].Steps, upgrade.Step{Type: upgrade.StepAddon, Target: "coredns", Version: "v1.11.4", Status: upgrade.StatusPending})
	rig.f.plan = p
	rig.start(t)
	for {
		st, _ := rig.b.State(t.Context())
		if q := st.Upgrades[0].Question; q != "" {
			if !strings.Contains(q, "the plan changed since the dry run: + Addon coredns v1.11.4;") {
				t.Fatalf("question = %q", q)
			}
			break
		}
	}
	if err := rig.b.Answer(t.Context(), "prod-api", true); err != nil {
		t.Fatal(err)
	}
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	if u := st.Upgrades[0]; u.Running() || u.Failed != "" || u.Stopped {
		t.Fatalf("upgrade = %+v", u)
	}
}

func TestBlockedRealPlanFailsWithoutRunning(t *testing.T) {
	rig := newUpgradeRig(t)
	p := upgradePlan()
	p.Hops[0].Steps = append([]upgrade.Step{{Type: upgrade.StepReadiness, Description: "readiness", Status: upgrade.StatusBlocked, Reason: "insight ERROR"}}, p.Hops[0].Steps...)
	rig.f.plan = p
	rig.start(t)
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	if u := st.Upgrades[0]; !strings.HasPrefix(u.Failed, "blocked:") || len(rig.f.phases) != 0 {
		t.Fatalf("upgrade = %+v, phases %v", u, rig.f.phases)
	}
}

func TestDrainBlockersStopTheUpgradeBeforeARoll(t *testing.T) {
	rig := newUpgradeRig(t)
	rig.blockers = []health.PDBInfo{{Namespace: "shop", Name: "checkout"}}
	rig.start(t)
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	u := st.Upgrades[0]
	if !strings.Contains(u.Failed, "PDB shop/checkout allows 0 disruptions") || u.Question != "" {
		t.Fatalf("upgrade = %+v", u)
	}
	rig.f.mu.Lock()
	defer rig.f.mu.Unlock()
	if len(rig.f.rolled) != 0 {
		t.Fatalf("rolled %v past a drain blocker", rig.f.rolled)
	}
}

func TestOneAnswerPerQuestion(t *testing.T) {
	rig := newUpgradeRig(t)
	rig.hw = []string{"PodDisruptionBudgets"}
	rig.hwOnly = "ng-a" // the only question of the run
	rig.start(t)
	for {
		st, _ := rig.b.State(t.Context())
		if st.Upgrades[0].Question != "" {
			break
		}
		if !st.Upgrades[0].Running() {
			t.Fatal("the upgrade ended without asking")
		}
		runtime.Gosched()
	}
	if err := rig.b.Answer(t.Context(), "prod-api", true); err != nil {
		t.Fatal(err)
	}
	if err := rig.b.Answer(t.Context(), "prod-api", false); err == nil {
		t.Fatal("a second answer to one question was taken")
	}
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	rig.f.mu.Lock()
	defer rig.f.mu.Unlock()
	if u := st.Upgrades[0]; u.Stopped || u.Failed != "" || strings.Join(rig.f.rolled, ",") != "ng-a,ng-b" {
		t.Fatalf("upgrade = %+v, rolled %v", u, rig.f.rolled)
	}
}

func TestAStopDoesNotHideAFailure(t *testing.T) {
	rig := newUpgradeRig(t)
	rig.f.failRoll = "ng-a"
	// Stop while ng-a rolls; then its roll fails.
	rig.f.duringRoll = func() {
		if err := rig.b.StopAfterCurrent(t.Context(), "prod-api"); err != nil {
			t.Error(err)
		}
	}
	rig.start(t)
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	if u := st.Upgrades[0]; u.Stopped || !strings.Contains(u.Failed, "NodeCreationFailure") {
		t.Fatalf("upgrade = %+v", u)
	}
}

func TestAStopBetweenPhasesKeepsTheFinishedPhaseDone(t *testing.T) {
	rig := newUpgradeRig(t)
	rig.f.phaseRun = make(chan string)
	rig.f.phaseAck = make(chan struct{})
	rig.start(t)
	<-rig.f.phaseRun // the control plane phase started
	if err := rig.b.StopAfterCurrent(t.Context(), "prod-api"); err != nil {
		t.Fatal(err)
	}
	rig.f.phaseAck <- struct{}{}
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	u := st.Upgrades[0]
	if !u.Stopped || u.Phases[1].Status != state.PhaseDone || u.Phases[2].Status != state.PhasePending {
		t.Fatalf("phases = %+v", u.Phases)
	}
}

func TestSkippedChecksAloneAreNotAsked(t *testing.T) {
	rig := newUpgradeRig(t)
	rig.start(t)
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	rig.f.mu.Lock()
	defer rig.f.mu.Unlock()
	if u := st.Upgrades[0]; u.Failed != "" || u.Stopped || strings.Join(rig.f.rolled, ",") != "ng-a,ng-b" {
		t.Fatalf("upgrade = %+v, rolled %v", u, rig.f.rolled)
	}
	for _, e := range st.Upgrades[0].Events {
		if e.Subject == "question" {
			t.Fatalf("asked %q with only skipped checks", e.Text)
		}
	}
}

// A real control-plane update ran ten minutes with the bar at 5% and its
// item pending. The snapshot now estimates from the elapsed time, capped
// short of done, and marks the item running.
func TestControlPlanePhaseShowsProgress(t *testing.T) {
	b := newTestBackend(t, &fleet{rows: map[string][]statussvc.ClusterStatus{}}, "us-east-1")
	now := b.now()
	u := &liveUpgrade{st: state.Upgrade{Phases: []state.Phase{
		{Name: "Plan", Status: state.PhaseDone},
		{Name: "Control plane 1.35", Status: state.PhaseRunning, StartedAt: now.Add(-5 * time.Minute),
			Items: []state.PhaseItem{{Name: "control plane", Text: "1.34 → 1.35"}}},
		{Name: "Add-ons 1.35", Status: state.PhasePending, Items: []state.PhaseItem{{Name: "coredns"}}},
	}}}
	st := b.upgradeSnapshot(u)
	cp := st.Phases[1]
	if cp.Progress < 0.49 || cp.Progress > 0.51 || cp.Items[0].Status != state.PhaseRunning {
		t.Fatalf("control plane = %+v", cp)
	}
	if st.Phases[2].Progress != 0 || st.Phases[2].Items[0].Status != state.PhasePending {
		t.Fatalf("a pending phase moved: %+v", st.Phases[2])
	}
	if u.st.Phases[1].Progress != 0 {
		t.Fatal("the snapshot changed the upgrade itself")
	}
	u.st.Phases[1].StartedAt = now.Add(-20 * time.Minute)
	if p := b.upgradeSnapshot(u).Phases[1].Progress; p != 0.95 {
		t.Fatalf("a long update shows %v, want 0.95 until EKS says done", p)
	}
}
