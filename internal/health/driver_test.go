package health_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

type kubetestRequest = kubetest.Request

// driver is what a changing command of phase 4 does with the gate: the
// plan step, the gate before each phase, and the verification after a
// roll. It proves the API end to end against the fakes, with the world
// changing between the steps as the parity scenarios do.
type driver struct {
	t      *testing.T
	h      *harness
	policy health.Policy
	plan   []health.Result
}

// planStep runs the gate once for every nodegroup the plan would roll.
func (d *driver) planStep(ctx context.Context, targets ...string) health.Verdict {
	d.t.Helper()
	d.plan = health.Run(ctx, health.Preflight(), d.h.input(ctx, targets...))
	return health.Gate(d.plan, d.policy)
}

// phaseGate runs the gate again right before a phase, on a fresh input,
// and returns the verdict and what got worse since the plan.
func (d *driver) phaseGate(ctx context.Context, targets ...string) (health.Verdict, []health.Reason) {
	d.t.Helper()
	results := health.Run(ctx, health.Preflight(), d.h.input(ctx, targets...))
	return health.Gate(results, d.policy), health.Worse(d.plan, results)
}

// phase is what a command does before a phase: the gate again, the question
// of the pre-phase table when something got worse, then the submit. It
// returns whether the phase ran.
func (d *driver) phase(ctx context.Context, confirm func(question string) bool, submit func(), targets ...string) bool {
	d.t.Helper()
	v, worse := d.phaseGate(ctx, targets...)
	switch {
	case v.Decision == health.Block:
		return false
	case len(worse) > 0 && !d.policy.Yes:
		var parts []string
		for _, r := range worse {
			parts = append(parts, r.String())
		}
		if !confirm("health got worse since the plan (" + strings.Join(parts, "; ") + "); proceed?") {
			return false
		}
	}
	submit()
	return true
}

// Something gets worse between the plan step and the phase: the gate asks
// again with the Worse reasons. No stops the phase and submits nothing;
// yes submits.
func TestDriver_AsksAgainBeforeThePhase(t *testing.T) {
	h := newHarness(t)
	ctx := testContext(t)
	d := &driver{t: t, h: h}
	if v := d.planStep(ctx, "ng-a"); v.Decision != health.Go {
		t.Fatalf("plan step: %+v", v)
	}
	h.world.Edit(func(s *fleettest.State) { nodeNotReady("b-1")(s, s.Cluster(east, "prod")) })
	submitted := 0
	submit := func() {
		if _, err := h.provider.Clients(east).EKS.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
			ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a"),
		}); err != nil {
			t.Fatal(err)
		}
		submitted++
	}
	var question string
	if ran := d.phase(ctx, func(q string) bool { question = q; return false }, submit, "ng-a"); ran || submitted != 0 {
		t.Errorf("declined: ran = %v, submitted = %d", ran, submitted)
	}
	if !strings.Contains(question, "Nodes: 3/4 nodes ready") {
		t.Errorf("question = %q, want the Worse reason", question)
	}
	if n := countAWS(h.aws, fleettest.EKSUpdateNodegroupVersion); n != 0 {
		t.Errorf("updates after a no = %d", n)
	}
	if ran := d.phase(ctx, func(string) bool { return true }, submit, "ng-a"); !ran || submitted != 1 {
		t.Errorf("accepted: ran = %v, submitted = %d", ran, submitted)
	}
	if n := countAWS(h.aws, fleettest.EKSUpdateNodegroupVersion); n != 1 {
		t.Errorf("updates after a yes = %d", n)
	}
}

// A budget tightens after the dry run: the plan step passes, and the gate
// before the roll blocks on the fresh read.
func TestDriver_PDBTightensAfterTheDryRun(t *testing.T) {
	h := newHarness(t)
	ctx := testContext(t)
	d := &driver{t: t, h: h, policy: health.Policy{Yes: true}}
	if v := d.planStep(ctx, "ng-a"); v.Decision != health.Go {
		t.Fatalf("plan step: %+v", v)
	}
	h.world.Edit(func(_ *fleettest.State) {})
	h.world.Edit(func(s *fleettest.State) { tight("web")(s, s.Cluster(east, "prod")) })
	v, worse := d.phaseGate(ctx, "ng-a")
	if v.Decision != health.Block || v.Exit != 3 {
		t.Errorf("phase gate = %+v, want Block with exit 3", v)
	}
	if len(worse) != 1 || worse[0].Check != "DrainBlockers" {
		t.Errorf("worse = %+v, want the drain blockers", worse)
	}
	// --force rolls past it, and the forced reason is recorded.
	d.policy.Force = true
	if v, _ := d.phaseGate(ctx, "ng-a"); v.Decision != health.Go || len(v.Forced) != 1 || v.Exit != 2 {
		t.Errorf("forced phase gate = %+v, want Go with one forced reason and exit 2", v)
	}
}

// A nodegroup degrades between two phases: the gate before its roll blocks,
// and the plan's verdict is not reused.
func TestDriver_NodegroupDegradesBetweenPhases(t *testing.T) {
	h := newHarness(t)
	ctx := testContext(t)
	d := &driver{t: t, h: h, policy: health.Policy{Yes: true}}
	if v := d.planStep(ctx, "ng-a", "ng-b"); v.Decision != health.Go {
		t.Fatalf("plan step: %+v", v)
	}
	// Phase 1: roll ng-a to the end, verify it.
	base := health.TakeBaseline(ctx, h.client, "prod")
	if v, worse := d.phaseGate(ctx, "ng-a"); v.Decision != health.Go || len(worse) != 0 {
		t.Fatalf("phase 1 gate = %+v, worse = %+v", v, worse)
	}
	h.roll(fleettest.RollScript{})
	h.world.Advance(rollDuration)
	verified := health.Run(ctx, health.Verify([]string{"ng-a"}, base, health.WaitOptions{}), h.input(ctx, "ng-a"))
	if code := health.VerifyExit(verified); code != 0 {
		t.Fatalf("verification of ng-a: %d %+v", code, verified)
	}
	// Between the phases ng-b degrades and its nodes stop being Ready.
	h.world.Edit(func(s *fleettest.State) {
		c := s.Cluster(east, "prod")
		c.Nodegroup("ng-b").Status = fleettest.StatusDegraded
		for _, n := range c.Nodes {
			if n.Nodegroup == "ng-b" {
				n.NotReady = true
			}
		}
	})
	v, worse := d.phaseGate(ctx, "ng-b")
	if v.Decision != health.Block || v.Exit != 3 {
		t.Errorf("phase 2 gate = %+v, want Block", v)
	}
	if len(worse) == 0 || worse[0].Check != "Nodes" {
		t.Errorf("worse = %+v, want the nodes", worse)
	}
}

// An add-on update starts elsewhere between the plan and the phase: the
// gate reads a fresh snapshot that shows it, and its own verdict does not
// change. Phase 4's busy check reads the same snapshot.
func TestDriver_AddonUpdatedElsewhere(t *testing.T) {
	h := newHarness(t, func(s *fleettest.State, c *fleettest.Cluster) {
		c.Addons = []*fleettest.Addon{{Name: "vpc-cni", Version: "v1.19.0-eksbuild.1", UpdateScripts: []fleettest.UpdateScript{{After: time.Hour}}}}
		s.AddonVersions = []fleettest.AddonVersion{{Addon: "vpc-cni", Version: "v1.19.1-eksbuild.1", ClusterVersions: []string{"1.33"}}}
	})
	ctx := testContext(t)
	d := &driver{t: t, h: h, policy: health.Policy{Yes: true}}
	if v := d.planStep(ctx, "ng-a"); v.Decision != health.Go {
		t.Fatalf("plan step: %+v", v)
	}
	version := "v1.19.1-eksbuild.1"
	if _, err := h.world.UpdateAddon(east, "prod", "vpc-cni", fleettest.AddonRequest{Version: &version}); err != nil {
		t.Fatal(err)
	}
	v, worse := d.phaseGate(ctx, "ng-a")
	if v.Decision != health.Go || len(worse) != 0 {
		t.Errorf("phase gate = %+v, worse = %+v; the health gate does not judge add-ons", v, worse)
	}
	// The fresh read that the gate made sees the foreign update.
	in := h.input(ctx, "ng-a")
	if in.Snapshot.Have.Addons != 0 {
		t.Fatal("the gate's input reads no add-ons; the busy check adds Need.Addons")
	}
	if n := countAWS(h.aws, fleettest.EKSDescribeNodegroup); n < 6 {
		t.Errorf("DescribeNodegroup calls = %d; every gate re-reads the nodegroups", n)
	}
}

// --require-healthy blocks a warning at the phase gate, and a warning that
// the plan step already showed does not ask again when nothing got worse.
func TestDriver_WarningsAcrossSteps(t *testing.T) {
	h := newHarness(t, nodeNotReady("b-1"))
	ctx := testContext(t)
	d := &driver{t: t, h: h}
	if v := d.planStep(ctx, "ng-a"); v.Decision != health.Ask || v.Question() == "" {
		t.Fatalf("plan step: %+v", v)
	}
	if v, worse := d.phaseGate(ctx, "ng-a"); v.Decision != health.Ask || len(worse) != 0 {
		t.Errorf("phase gate = %+v, worse = %+v; the same warning does not ask again", v, worse)
	}
	d.policy.RequireHealthy = true
	if v, _ := d.phaseGate(ctx, "ng-a"); v.Decision != health.Block || v.Exit != 3 {
		t.Errorf("--require-healthy = %+v, want Block with exit 3", v)
	}
}
