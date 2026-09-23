package cluster

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/mocks"
	"github.com/dantech2000/refresh/internal/services/upgrade"
)

// fakeGateChecker is a scripted healthGateChecker.
type fakeGateChecker struct {
	blockers   []health.PDBInfo
	drainErr   error
	decision   health.Decision
	targets    []string
	drainCalls int
	runCalls   int
}

func (f *fakeGateChecker) SetTargetNodegroups(names []string) { f.targets = names }

func (f *fakeGateChecker) RunAllChecks(context.Context, string) health.HealthSummary {
	f.runCalls++
	d := f.decision
	if d == "" {
		d = health.DecisionProceed
	}
	s := health.HealthSummary{Decision: d}
	if d != health.DecisionProceed {
		status := health.StatusWarn
		if d == health.DecisionBlock {
			status = health.StatusFail
		}
		s.Results = []health.HealthResult{{Name: "Node Health", Status: status, IsBlocking: d == health.DecisionBlock, Message: "2 nodes NotReady"}}
	}
	return s
}

func (f *fakeGateChecker) DrainBlockers(context.Context, string, []string) (health.DrainBlockerReport, error) {
	f.drainCalls++
	return health.DrainBlockerReport{Blockers: f.blockers, Scoped: true}, f.drainErr
}

// gateWorld is a cluster at 1.32 with one nodegroup at 1.31: upgrading to
// 1.32 plans a single nodegroup roll and no control-plane move.
func gateWorld(t *testing.T) (*mocks.EKSAPI, *upgrade.Service, *upgrade.Plan) {
	t.Helper()
	m := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithNodegroup("web", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		WithUpdateStatuses("u-web", ekstypes.UpdateStatusSuccessful).
		Build()
	m.UpdateNodegroupVersionFn = func(_ context.Context, in *eks.UpdateNodegroupVersionInput, _ ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
		return &eks.UpdateNodegroupVersionOutput{Update: &ekstypes.Update{
			Id: aws.String("u-" + aws.ToString(in.NodegroupName)), Status: ekstypes.UpdateStatusInProgress,
		}}, nil
	}
	svc := upgrade.NewService(m, slog.New(slog.DiscardHandler))
	svc.PollInterval = time.Millisecond
	plan, err := svc.BuildPlan(context.Background(), "prod", "1.32", upgrade.PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Blocked() || plan.PendingSteps() != 1 {
		t.Fatalf("plan: blocked=%v pending=%d, want one pending roll", plan.Blocked(), plan.PendingSteps())
	}
	return m, svc, plan
}

func newTestGate(checker *fakeGateChecker, hasKube bool, warn *bytes.Buffer) *nodegroupHealthGate {
	return &nodegroupHealthGate{
		cluster: "prod",
		yes:     true,
		warn:    warn,
		connect: func(context.Context) (healthGateChecker, bool) { return checker, hasKube },
	}
}

func runGated(t *testing.T, svc *upgrade.Service, plan *upgrade.Plan, g *nodegroupHealthGate) error {
	t.Helper()
	opts := upgrade.ExecuteOptions{Yes: true, Force: g.force, NodegroupGate: g.check}
	_, err := svc.Execute(context.Background(), plan, opts)
	return err
}

// A PDB that allows 0 disruptions on the nodegroup's pods stops the phase
// before EKS is asked to roll anything.
func TestUpgradeGate_DrainBlockerFailsBeforeRoll(t *testing.T) {
	m, svc, plan := gateWorld(t)
	checker := &fakeGateChecker{blockers: []health.PDBInfo{{Namespace: "shop", Name: "checkout", ExpectedPods: 2}}}
	var warn bytes.Buffer

	err := runGated(t, svc, plan, newTestGate(checker, true, &warn))
	if err == nil || !strings.Contains(err.Error(), "shop/checkout") || !strings.Contains(err.Error(), "web") {
		t.Fatalf("err = %v, want the drain blocker and nodegroup named", err)
	}
	if m.Calls.UpdateNodegroupVersion != 0 {
		t.Fatalf("UpdateNodegroupVersion calls = %d, want 0", m.Calls.UpdateNodegroupVersion)
	}
}

// --force evicts through PDBs, so a drain blocker becomes a warning.
func TestUpgradeGate_ForceRollsPastDrainBlocker(t *testing.T) {
	m, svc, plan := gateWorld(t)
	checker := &fakeGateChecker{blockers: []health.PDBInfo{{Namespace: "shop", Name: "checkout", ExpectedPods: 2}}}
	var warn bytes.Buffer
	g := newTestGate(checker, true, &warn)
	g.force = true

	if err := runGated(t, svc, plan, g); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if m.Calls.UpdateNodegroupVersion != 1 || !strings.Contains(warn.String(), "shop/checkout") {
		t.Fatalf("rolls = %d, warn = %q; want 1 roll and the blocker in a warning", m.Calls.UpdateNodegroupVersion, warn.String())
	}
}

// A PDB list failure is not "no blockers".
func TestUpgradeGate_DrainCheckErrorFails(t *testing.T) {
	m, svc, plan := gateWorld(t)
	checker := &fakeGateChecker{drainErr: context.DeadlineExceeded}

	err := runGated(t, svc, plan, newTestGate(checker, true, &bytes.Buffer{}))
	if err == nil || !strings.Contains(err.Error(), "PodDisruptionBudgets") {
		t.Fatalf("err = %v, want the PDB check failure", err)
	}
	if m.Calls.UpdateNodegroupVersion != 0 {
		t.Fatalf("UpdateNodegroupVersion calls = %d, want 0", m.Calls.UpdateNodegroupVersion)
	}
}

// Without Kubernetes access the PDB check is skipped with a warning, the
// AWS-side health checks still run, and the roll proceeds.
func TestUpgradeGate_NoKubeClientWarnsAndProceeds(t *testing.T) {
	m, svc, plan := gateWorld(t)
	checker := &fakeGateChecker{}
	var warn bytes.Buffer

	if err := runGated(t, svc, plan, newTestGate(checker, false, &warn)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(warn.String(), "PDB drain-blocker checks before nodegroup rolls are skipped") {
		t.Fatalf("warn = %q, want the skipped PDB check", warn.String())
	}
	if checker.drainCalls != 0 || checker.runCalls != 1 {
		t.Fatalf("drain calls = %d, health runs = %d; want 0 and 1", checker.drainCalls, checker.runCalls)
	}
	if m.Calls.UpdateNodegroupVersion != 1 {
		t.Fatalf("UpdateNodegroupVersion calls = %d, want 1", m.Calls.UpdateNodegroupVersion)
	}
}

func TestUpgradeGate_BlockDecisionFails(t *testing.T) {
	m, svc, plan := gateWorld(t)
	checker := &fakeGateChecker{decision: health.DecisionBlock}

	err := runGated(t, svc, plan, newTestGate(checker, true, &bytes.Buffer{}))
	if err == nil || !strings.Contains(err.Error(), "Node Health") {
		t.Fatalf("err = %v, want the blocking check named", err)
	}
	if m.Calls.UpdateNodegroupVersion != 0 {
		t.Fatalf("UpdateNodegroupVersion calls = %d, want 0", m.Calls.UpdateNodegroupVersion)
	}
	if len(checker.targets) != 1 || checker.targets[0] != "web" {
		t.Fatalf("health check scoped to %v, want [web]", checker.targets)
	}
}

// Health warnings follow the phase-confirmation rules: without --yes the
// user is asked, and declining stops the roll.
func TestUpgradeGate_WarningsNeedConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		yes     bool
		confirm func(string) bool
		wantErr bool
	}{
		{name: "yes proceeds", yes: true},
		{name: "confirmed", confirm: func(string) bool { return true }},
		{name: "declined", confirm: func(string) bool { return false }, wantErr: true},
		{name: "no prompt available", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, svc, plan := gateWorld(t)
			g := newTestGate(&fakeGateChecker{decision: health.DecisionWarn}, true, &bytes.Buffer{})
			g.yes, g.confirm = tc.yes, tc.confirm

			err := runGated(t, svc, plan, g)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			wantRolls := 1
			if tc.wantErr {
				wantRolls = 0
			}
			if m.Calls.UpdateNodegroupVersion != wantRolls {
				t.Fatalf("UpdateNodegroupVersion calls = %d, want %d", m.Calls.UpdateNodegroupVersion, wantRolls)
			}
		})
	}
}

func TestUpgradeGate_SkipHealthCheck(t *testing.T) {
	m, svc, plan := gateWorld(t)
	var warn bytes.Buffer
	g := newTestGate(&fakeGateChecker{decision: health.DecisionBlock}, true, &warn)
	g.skip = true
	g.connect = func(context.Context) (healthGateChecker, bool) {
		t.Fatal("--skip-health-check must not resolve a Kubernetes client")
		return nil, false
	}

	if err := runGated(t, svc, plan, g); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if m.Calls.UpdateNodegroupVersion != 1 || !strings.Contains(warn.String(), "--skip-health-check") {
		t.Fatalf("rolls = %d, warn = %q", m.Calls.UpdateNodegroupVersion, warn.String())
	}
}
