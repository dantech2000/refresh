package live

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// fakeRollbacker plays the rollback engine: it calls the hooks in the order
// ExecuteRollback does and records the phases.
type fakeRollbacker struct {
	mu     sync.Mutex
	plan   *upgrade.RollbackPlan
	built  int
	phases []string
	rolled []string
}

func (f *fakeRollbacker) BuildRollbackPlan(context.Context, string, upgrade.RollbackOptions) (*upgrade.RollbackPlan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.built++
	return f.plan, nil
}

func (f *fakeRollbacker) ExecuteRollback(ctx context.Context, plan *upgrade.RollbackPlan, opts upgrade.ExecuteOptions) (*upgrade.Report, error) {
	var ngs []string
	for _, s := range plan.Steps {
		if s.Type == upgrade.StepNodegroup && s.Status == upgrade.StatusPending {
			ngs = append(ngs, s.Target)
		}
	}
	for _, ph := range []string{
		fmt.Sprintf("nodegroup rollbacks to %s (%d nodegroup(s))", plan.TargetVersion, len(ngs)),
		fmt.Sprintf("addon downgrades for %s (1 addon(s), dependency order)", plan.TargetVersion),
		fmt.Sprintf("control plane rollback %s → %s", plan.CurrentVersion, plan.TargetVersion),
	} {
		if !opts.Confirm(ph) {
			return &upgrade.Report{Status: upgrade.RunAborted}, upgrade.ErrAborted
		}
		opts.PhaseStart(ph)
		f.mu.Lock()
		f.phases = append(f.phases, ph)
		f.mu.Unlock()
		if strings.HasPrefix(ph, "nodegroup") {
			for _, ng := range ngs {
				if err := opts.NodegroupGate(ctx, ng); err != nil {
					return &upgrade.Report{Status: upgrade.RunBlocked}, err
				}
				octx, cancel := context.WithCancel(ctx)
				cancel()
				opts.NodegroupObserver(octx, ng)
				f.mu.Lock()
				f.rolled = append(f.rolled, ng)
				f.mu.Unlock()
			}
		}
	}
	return &upgrade.Report{Status: upgrade.RunSucceeded}, nil
}

func rollbackPlan() *upgrade.RollbackPlan {
	until := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	return &upgrade.RollbackPlan{ClusterName: "prod-api", CurrentVersion: "1.31", TargetVersion: "1.30", AvailableUntil: &until,
		Steps: []upgrade.Step{
			{Type: upgrade.StepReadiness, Description: "rollback eligibility", Status: upgrade.StatusPending, Reason: "rollback available until about 2026-09-30"},
			{Type: upgrade.StepNodegroup, Target: "ng-general", Version: "1.30", Status: upgrade.StatusPending},
			{Type: upgrade.StepAddon, Target: "kube-proxy", Version: "v1.30.0", Status: upgrade.StatusPending},
			{Type: upgrade.StepControlPlane, Description: "control plane rollback 1.31 → 1.30", Version: "1.30", Status: upgrade.StatusPending},
		}}
}

var rollbackAction = state.Action{Kind: state.ActionRollback, Cluster: "prod-api"}

func newRollbackRig(t *testing.T) (*upgradeRig, *fakeRollbacker) {
	t.Helper()
	rig := newUpgradeRig(t)
	f := &fakeRollbacker{plan: rollbackPlan()}
	rig.b.newRollbacker = func(aws.Config) rollbacker { return f }
	return rig, f
}

func TestReadinessShowsTheRollbackWindow(t *testing.T) {
	rig, _ := newRollbackRig(t)
	b := rig.b
	var rb *upgrade.RollbackAvailability
	b.svc.upgradeCheck = func(context.Context, aws.Config, string) (*clustersvc.UpgradeReport, error) {
		r := report()
		r.Rollback = rb
		return r, nil
	}
	rb = &upgrade.RollbackAvailability{PreviousVersion: "1.30", AvailableUntil: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
		Insights: &upgrade.InsightCounts{Error: 1, Passing: 3}}
	if err := b.RunReadiness(t.Context(), "prod-api"); err != nil {
		t.Fatal(err)
	}
	b.Close()
	st, _ := b.State(t.Context())
	c := st.Clusters[0]
	if c.Name != "prod-api" || c.RollbackTo != "1.30" {
		t.Fatalf("cluster = %s rollback %q", c.Name, c.RollbackTo)
	}
	var found bool
	for _, ch := range st.Readiness["prod-api"].Checks {
		if ch.Name == "rollback window" && ch.Status == state.CheckPass && strings.Contains(strings.Join(ch.Detail, " "), "1 error") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no rollback window check in %+v", st.Readiness["prod-api"].Checks)
	}

	// A later run without a window clears it.
	rb = nil
	if err := b.RunReadiness(t.Context(), "prod-api"); err != nil {
		t.Fatal(err)
	}
	b.Close()
	if st, _ := b.State(t.Context()); st.Clusters[0].RollbackTo != "" {
		t.Fatalf("rollback still offered: %q", st.Clusters[0].RollbackTo)
	}
}

func TestRollbackDryRunIsTheCLIPlan(t *testing.T) {
	rig, _ := newRollbackRig(t)
	p, err := rig.b.Plan(t.Context(), rollbackAction)
	if err != nil || p.Blocked != "" {
		t.Fatalf("plan = %+v, %v", p, err)
	}
	if p.Command != "refresh --region us-east-1 cluster rollback -c prod-api" || p.Changes[0] != (state.Change{Field: "control plane", From: "1.31", To: "1.30"}) {
		t.Fatalf("plan = %+v", p)
	}

	rig.b.opts.AllowChanges = false
	if p, _ := rig.b.Plan(t.Context(), rollbackAction); p.Blocked != ErrReadOnly.Error() {
		t.Fatalf("read-only plan blocked = %q", p.Blocked)
	}
}

func TestBlockedRollbackDryRun(t *testing.T) {
	rig, f := newRollbackRig(t)
	f.plan.Steps[0].Status, f.plan.Steps[0].Reason = upgrade.StatusBlocked, "the 7-day rollback window closed about 2026-09-20"
	p, err := rig.b.Plan(t.Context(), rollbackAction)
	if err != nil || !strings.Contains(p.Blocked, "window closed") {
		t.Fatalf("plan blocked = %q, %v", p.Blocked, err)
	}
	if err := rig.b.Start(t.Context(), rollbackAction); err == nil || !strings.Contains(err.Error(), "no dry run") {
		t.Fatalf("Start after a blocked dry run = %v", err)
	}
}

func TestRollbackRunsThroughTheEngine(t *testing.T) {
	rig, f := newRollbackRig(t)
	if err := rig.b.Start(t.Context(), rollbackAction); err == nil || !strings.Contains(err.Error(), "no dry run") {
		t.Fatalf("Start without a dry run = %v", err)
	}
	if _, err := rig.b.Plan(t.Context(), rollbackAction); err != nil {
		t.Fatal(err)
	}
	if err := rig.b.Start(t.Context(), rollbackAction); err != nil {
		t.Fatal(err)
	}
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	if len(st.Upgrades) != 1 {
		t.Fatalf("upgrades = %+v", st.Upgrades)
	}
	u := st.Upgrades[0]
	if !u.Rollback || u.Running() || u.Failed != "" || u.From != "1.31" || u.To != "1.30" {
		t.Fatalf("rollback = %+v", u)
	}
	var names []string
	for _, p := range u.Phases {
		names = append(names, p.Name)
	}
	if got := strings.Join(names, ","); got != "Plan,Nodegroups 1.30,Add-ons 1.30,Control plane rollback 1.30" {
		t.Fatalf("phases = %s", got)
	}
	for _, e := range u.Events {
		if e.Subject == "phase" && e.Level != state.LevelProgress {
			t.Fatalf("phase %q matched no timeline phase", e.Text)
		}
	}
	if f.built != 2 || strings.Join(f.rolled, ",") != "ng-general" {
		t.Fatalf("built %d plans, rolled %v", f.built, f.rolled)
	}
	if !strings.Contains(joinText(st.Feed), "rollback done · 1.30") {
		t.Fatalf("feed:\n%s", joinText(st.Feed))
	}
}
