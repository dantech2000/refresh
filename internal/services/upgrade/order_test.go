package upgrade

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// catalogues wires DescribeAddonVersions from a per-addon, per-version
// catalogue (newest first).
func catalogues(m *mocks.EKSAPI, byAddon map[string]map[string][]string) {
	m.DescribeAddonVersionsFn = func(_ context.Context, in *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
		name, k8s := aws.ToString(in.AddonName), aws.ToString(in.KubernetesVersion)
		var infos []ekstypes.AddonVersionInfo
		for _, v := range byAddon[name][k8s] {
			infos = append(infos, ekstypes.AddonVersionInfo{
				AddonVersion:    aws.String(v),
				Compatibilities: []ekstypes.Compatibility{{ClusterVersion: aws.String(k8s)}},
			})
		}
		return &eks.DescribeAddonVersionsOutput{Addons: []ekstypes.AddonInfo{{AddonName: in.AddonName, AddonVersions: infos}}}, nil
	}
}

// stepOrder lists a hop's steps as "Type target", with a "!" on addon steps
// that run before the nodegroup rolls.
func stepOrder(hop Hop) []string {
	out := make([]string, 0, len(hop.Steps))
	for _, s := range hop.Steps {
		e := string(s.Type)
		if s.Target != "" {
			e += " " + s.Target
		}
		if s.BeforeNodegroups {
			e += "!"
		}
		out = append(out, e)
	}
	return out
}

// An addon the new control plane cannot run (kube-proxy tracks the minor)
// updates before the nodegroup rolls; one that is behind but compatible
// (vpc-cni) updates after them.
func TestBuildPlan_IncompatibleAddonsBeforeRollsRestAfter(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithAddon("vpc-cni", "v1.19.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddon("kube-proxy", "v1.32.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithNodegroup("workers-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	catalogues(m, map[string]map[string][]string{
		"vpc-cni":    {"1.32": {"v1.20.0-eksbuild.1", "v1.19.0-eksbuild.1"}, "1.33": {"v1.20.0-eksbuild.1", "v1.19.0-eksbuild.1"}},
		"kube-proxy": {"1.32": {"v1.32.0-eksbuild.1"}, "1.33": {"v1.33.0-eksbuild.1"}},
	})
	plan, err := newTestService(m).BuildPlan(context.Background(), "prod-east", "1.33", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Hops) != 1 {
		t.Fatalf("hops = %+v, want one", plan.Hops)
	}
	want := []string{"Readiness", "ControlPlane", "Addon kube-proxy!", "Nodegroup workers-a", "Addon vpc-cni"}
	if got := stepOrder(plan.Hops[0]); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	kp := findStep(t, plan.Hops[0].Steps, StepAddon, "kube-proxy")
	if kp.Status != StatusPending || kp.Version != "v1.33.0-eksbuild.1" || !strings.Contains(kp.Reason, "not compatible with 1.33") {
		t.Fatalf("kube-proxy step = %+v", kp)
	}
}

// Across hops the planner judges compatibility from the version the addon
// will run when the hop starts, not the live one.
func TestBuildPlan_LaterHopJudgesSimulatedAddonVersion(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.31").
		WithAddon("vpc-cni", "v1.19.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddon("kube-proxy", "v1.31.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	catalogues(m, map[string]map[string][]string{
		// vpc-cni's live build leaves the catalogue at 1.33, but by then
		// the 1.32 hop has moved it to v1.20, which 1.33 still lists.
		"vpc-cni": {
			"1.31": {"v1.19.0-eksbuild.1"},
			"1.32": {"v1.20.0-eksbuild.1", "v1.19.0-eksbuild.1"},
			"1.33": {"v1.21.0-eksbuild.1", "v1.20.0-eksbuild.1"},
		},
		"kube-proxy": {"1.31": {"v1.31.0-eksbuild.1"}, "1.32": {"v1.32.0-eksbuild.1"}, "1.33": {"v1.33.0-eksbuild.1"}},
	})
	plan, err := newTestService(m).BuildPlan(context.Background(), "prod-east", "1.33", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Hops) != 2 {
		t.Fatalf("hops = %+v, want 1.31→1.32→1.33", plan.Hops)
	}
	want := []string{"Readiness", "ControlPlane", "Addon kube-proxy!", "Nodegroup workers-a", "Addon vpc-cni"}
	for _, hop := range plan.Hops {
		if got := stepOrder(hop); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("hop %s→%s steps = %v, want %v", hop.From, hop.To, got, want)
		}
	}
}

// Blocked and skipped addons keep their place rules: a blocked one (no
// compatible version) is before the rolls, a skipped one after them.
func TestBuildPlan_BlockedAddonBeforeSkippedAfter(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithAddon("coredns", "v1.11.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddon("aws-ebs-csi-driver", "v1.30.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithNodegroup("workers-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	catalogues(m, map[string]map[string][]string{"coredns": {}})
	plan, err := newTestService(m).BuildPlan(context.Background(), "prod-east", "1.33",
		PlanOptions{SkipAddons: []string{"aws-ebs-csi-driver"}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	want := []string{"Readiness", "ControlPlane", "Addon coredns!", "Nodegroup workers-a", "Addon aws-ebs-csi-driver"}
	if got := stepOrder(plan.Hops[0]); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if s := findStep(t, plan.Hops[0].Steps, StepAddon, "coredns"); s.Status != StatusBlocked {
		t.Fatalf("coredns = %+v, want blocked", s)
	}
	if s := findStep(t, plan.Hops[0].Steps, StepAddon, "aws-ebs-csi-driver"); s.Status != StatusManual {
		t.Fatalf("aws-ebs-csi-driver = %+v, want manual", s)
	}
}

// recordNamedMutations records the engine's changes as "cp→v", "addon
// name→v", and "ng name→v", in order.
func recordNamedMutations(m *mocks.EKSAPI) func() []string {
	var (
		mu     sync.Mutex
		events []string
	)
	record := func(e string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	cp, addon, ng := m.UpdateClusterVersionFn, m.UpdateAddonFn, m.UpdateNodegroupVersionFn
	m.UpdateClusterVersionFn = func(ctx context.Context, in *eks.UpdateClusterVersionInput, o ...func(*eks.Options)) (*eks.UpdateClusterVersionOutput, error) {
		record("cp→" + aws.ToString(in.Version))
		return cp(ctx, in, o...)
	}
	m.UpdateAddonFn = func(ctx context.Context, in *eks.UpdateAddonInput, o ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		record("addon " + aws.ToString(in.AddonName) + "→" + aws.ToString(in.AddonVersion))
		return addon(ctx, in, o...)
	}
	m.UpdateNodegroupVersionFn = func(ctx context.Context, in *eks.UpdateNodegroupVersionInput, o ...func(*eks.Options)) (*eks.UpdateNodegroupVersionOutput, error) {
		record("ng " + aws.ToString(in.NodegroupName) + "→" + aws.ToString(in.Version))
		return ng(ctx, in, o...)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), events...)
	}
}

// orderWorld is a 1.31 cluster with kube-proxy (compatible with one minor)
// and vpc-cni (its build stays compatible with the next minor).
func orderWorld() *fakeWorld {
	w := newWorld()
	w.addonVersions = map[string]string{"vpc-cni": latestFor("1.31"), "kube-proxy": latestFor("1.31")}
	w.keepsCompat = map[string]bool{"vpc-cni": true}
	return w
}

// The engine runs each hop as control plane → required addons → nodegroup
// rolls → remaining addons, and names each phase.
func TestExecute_RequiredAddonsBeforeRollsRestAfter(t *testing.T) {
	w := orderWorld()
	m := newWorldMock(w)
	events := recordNamedMutations(m)
	svc := newTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.33", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var phases []string
	report, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true, PhaseStart: func(l string) { phases = append(phases, l) }})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := []string{
		"cp→1.32", "addon kube-proxy→" + latestFor("1.32"), "ng workers-a→1.32", "addon vpc-cni→" + latestFor("1.32"),
		"cp→1.33", "addon kube-proxy→" + latestFor("1.33"), "ng workers-a→1.33", "addon vpc-cni→" + latestFor("1.33"),
	}
	if got := events(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("mutation order = %v\nwant %v", got, want)
	}
	wantPhases := []string{
		"control plane 1.31 → 1.32",
		"required addons for 1.32 (1 update(s), before nodegroup rolls)",
		"nodegroup rolls to 1.32 (1 nodegroup(s))",
		"addons for 1.32 (1 update(s), dependency order)",
		"control plane 1.32 → 1.33",
		"required addons for 1.33 (1 update(s), before nodegroup rolls)",
		"nodegroup rolls to 1.33 (1 nodegroup(s))",
		"addons for 1.33 (1 update(s), dependency order)",
	}
	if strings.Join(phases, "\n") != strings.Join(wantPhases, "\n") {
		t.Fatalf("phases = %q\nwant %q", phases, wantPhases)
	}
	if strings.Join(report.Completed, "\n") != strings.Join(wantPhases, "\n") {
		t.Fatalf("completed = %q", report.Completed)
	}
}

// A hop with no incompatible addon has no required-addons phase: the rolls
// follow the control plane directly.
func TestExecute_NoRequiredAddonsPhaseWhenAllCompatible(t *testing.T) {
	w := orderWorld()
	w.addonVersions = map[string]string{"vpc-cni": latestFor("1.31")}
	svc := newTestService(newWorldMock(w))
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var phases []string
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true, PhaseStart: func(l string) { phases = append(phases, l) }}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, p := range phases {
		if strings.HasPrefix(p, "required addons") {
			t.Fatalf("phases = %q, want no required-addons phase", phases)
		}
	}
	if len(phases) != 3 || !strings.HasPrefix(phases[1], "nodegroup rolls") || !strings.HasPrefix(phases[2], "addons for 1.32") {
		t.Fatalf("phases = %q, want control plane, rolls, addons", phases)
	}
}

// A run that fails in the trailing addon phase has already rolled the
// nodegroups. The rerun plans only the trailing addon and runs it.
func TestResume_AfterRollsTrailingAddonsFailed(t *testing.T) {
	w := orderWorld()
	w.failAddon = "vpc-cni"
	m := newWorldMock(w)
	events := recordNamedMutations(m)
	svc := newTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	report, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true})
	if err == nil {
		t.Fatal("first run should fail in the trailing addon phase")
	}
	if !strings.HasPrefix(report.StoppedAt, "addons for 1.32") || len(report.Remaining) != 0 {
		t.Fatalf("report = %+v, want stopped at the trailing addons with nothing after", report)
	}
	if w.ngVersions["workers-a"] != "1.32" || w.addonVersions["kube-proxy"] != latestFor("1.32") {
		t.Fatalf("world = %+v, want the rolls and kube-proxy done", w)
	}

	w.failAddon = ""
	plan, err = svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan (rerun): %v", err)
	}
	if len(plan.Hops) != 1 || plan.PendingSteps() != 1 {
		t.Fatalf("rerun plan = %+v, want one hop with one pending step", plan.Hops)
	}
	if s := findStep(t, plan.Hops[0].Steps, StepAddon, "vpc-cni"); s.Status != StatusPending || s.BeforeNodegroups {
		t.Fatalf("vpc-cni = %+v, want pending after the rolls", s)
	}
	before := len(events())
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute (rerun): %v", err)
	}
	if got := events()[before:]; strings.Join(got, ",") != "addon vpc-cni→"+latestFor("1.32") {
		t.Fatalf("rerun mutations = %v, want only the vpc-cni update", got)
	}
}

// In a multi-hop run that failed in the first hop's trailing addons, the
// rerun needs no catch-up hop (the addon still runs on the live control
// plane) and finishes the next hop in the new order.
func TestResume_MultiHopAfterRollsTrailingAddonsFailed(t *testing.T) {
	w := orderWorld()
	w.failAddon = "vpc-cni"
	m := newWorldMock(w)
	events := recordNamedMutations(m)
	svc := newTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.33", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	report, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true})
	if err == nil || !strings.HasPrefix(report.StoppedAt, "addons for 1.32") {
		t.Fatalf("first run: err = %v, stoppedAt = %q, want a stop in the 1.32 trailing addons", err, report.StoppedAt)
	}
	if len(report.Remaining) == 0 || !strings.HasPrefix(report.Remaining[0], "control plane 1.32 → 1.33") {
		t.Fatalf("remaining = %q, want the 1.33 hop", report.Remaining)
	}

	w.failAddon = ""
	plan, err = svc.BuildPlan(ctx, "prod-east", "1.33", PlanOptions{})
	if err != nil {
		t.Fatalf("BuildPlan (rerun): %v", err)
	}
	if len(plan.Hops) != 1 || plan.Hops[0].From != "1.32" || plan.Hops[0].To != "1.33" {
		t.Fatalf("rerun hops = %+v, want only 1.32→1.33 (no catch-up)", plan.Hops)
	}
	// vpc-cni's live 1.31 build is not in the 1.33 catalogue, so it goes
	// before the rolls now, with kube-proxy.
	want := []string{"Readiness", "ControlPlane", "Addon vpc-cni!", "Addon kube-proxy!", "Nodegroup workers-a"}
	if got := stepOrder(plan.Hops[0]); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rerun steps = %v, want %v", got, want)
	}
	before := len(events())
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute (rerun): %v", err)
	}
	wantEvents := []string{"cp→1.33", "addon vpc-cni→" + latestFor("1.33"), "addon kube-proxy→" + latestFor("1.33"), "ng workers-a→1.33"}
	if got := events()[before:]; strings.Join(got, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("rerun mutations = %v, want %v", got, wantEvents)
	}
}
