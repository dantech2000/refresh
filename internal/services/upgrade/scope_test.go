package upgrade

import (
	"context"
	"strings"
	"testing"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

func TestParseParts(t *testing.T) {
	got, err := ParseParts([]string{"control-plane,ADDONS", " nodegroups ", "addons"})
	if err != nil || len(got) != 3 || got[0] != PartControlPlane || got[1] != PartAddons || got[2] != PartNodegroups {
		t.Fatalf("got %v, %v", got, err)
	}
	if got, err := ParseParts(nil); err != nil || got != nil {
		t.Fatalf("empty: got %v, %v", got, err)
	}
	if _, err := ParseParts([]string{"controlplane"}); err == nil || !strings.Contains(err.Error(), "control-plane, addons, nodegroups") {
		t.Fatalf("bad part: err = %v", err)
	}
	for _, empty := range [][]string{{""}, {","}, {" , "}} {
		if _, err := ParseParts(empty); err == nil {
			t.Fatalf("%q: an empty --only means every part", empty)
		}
	}
}

// --only control-plane runs the control plane alone: the add-ons and the
// nodegroups stay where they are, as manual steps with a notice that names
// the next command. A second run with --only nodegroups rolls them to the
// control plane's version, and nothing else moves.
func TestExecute_ControlPlaneThenNodegroupsSeparately(t *testing.T) {
	w := newWorld()
	m := newWorldMock(w)
	svc := newTestService(m)
	ctx := context.Background()

	plan, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{Only: []Part{PartControlPlane}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if s := findStep(t, plan.Hops[len(plan.Hops)-1].Steps, StepNodegroup, "workers-a"); s.Status != StatusManual || !strings.Contains(s.Reason, "--only control-plane") {
		t.Fatalf("nodegroup step = %+v, want manual, left out by --only", s)
	}
	if s := findStep(t, plan.Hops[len(plan.Hops)-1].Steps, StepAddon, "vpc-cni"); s.Status != StatusManual {
		t.Fatalf("addon step = %+v, want manual", s)
	}
	notices := strings.Join(plan.Notices, "\n")
	if !strings.Contains(notices, "--only nodegroups") || !strings.Contains(notices, "refresh addon update --all -c prod-east") {
		t.Fatalf("notices = %q, want the next commands", notices)
	}
	if len(plan.Blockers()) > 0 {
		t.Fatalf("blockers = %v", plan.Blockers())
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if w.clusterVersion != "1.32" || m.Calls.UpdateClusterVersion != 1 || m.Calls.UpdateAddon != 0 || m.Calls.UpdateNodegroupVersion != 0 {
		t.Fatalf("after --only control-plane: cluster %s, calls cp=%d addon=%d ng=%d", w.clusterVersion, m.Calls.UpdateClusterVersion, m.Calls.UpdateAddon, m.Calls.UpdateNodegroupVersion)
	}

	plan, err = svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{Only: []Part{PartNodegroups}})
	if err != nil {
		t.Fatalf("BuildPlan nodegroups: %v", err)
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute nodegroups: %v", err)
	}
	if w.ngVersions["workers-a"] != "1.32" || m.Calls.UpdateClusterVersion != 1 || m.Calls.UpdateAddon != 0 || m.Calls.UpdateNodegroupVersion != 1 {
		t.Fatalf("after --only nodegroups: ng %s, calls cp=%d addon=%d ng=%d", w.ngVersions["workers-a"], m.Calls.UpdateClusterVersion, m.Calls.UpdateAddon, m.Calls.UpdateNodegroupVersion)
	}

	plan, err = svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{Only: []Part{PartAddons}})
	if err != nil {
		t.Fatalf("BuildPlan addons: %v", err)
	}
	if _, err := svc.Execute(ctx, plan, ExecuteOptions{Yes: true}); err != nil {
		t.Fatalf("Execute addons: %v", err)
	}
	if m.Calls.UpdateAddon != 1 || m.Calls.UpdateNodegroupVersion != 1 || m.Calls.UpdateClusterVersion != 1 {
		t.Fatalf("after --only addons: calls cp=%d addon=%d ng=%d", m.Calls.UpdateClusterVersion, m.Calls.UpdateAddon, m.Calls.UpdateNodegroupVersion)
	}
}

// Scopes the plan cannot keep safe are refused before anything is read
// past the cluster: catching up beyond the control plane, and moving the
// control plane two versions while the add-ons stay behind.
func TestBuildPlan_ScopeRefusals(t *testing.T) {
	svc := newTestService(twoHopMock())
	ctx := context.Background()
	if _, err := svc.BuildPlan(ctx, "prod-east", "1.32", PlanOptions{Only: []Part{PartNodegroups}}); err == nil || !strings.Contains(err.Error(), "use --to 1.31") {
		t.Fatalf("nodegroups past the control plane: err = %v", err)
	}
	if _, err := svc.BuildPlan(ctx, "prod-east", "1.33", PlanOptions{Only: []Part{PartControlPlane, PartNodegroups}}); err == nil || !strings.Contains(err.Error(), "use --to 1.32") {
		t.Fatalf("two hops without add-ons: err = %v", err)
	}
	// Without nodegroups, two hops are fine while the skew holds.
	plan, err := svc.BuildPlan(ctx, "prod-east", "1.33", PlanOptions{Only: []Part{PartControlPlane, PartAddons}})
	if err != nil {
		t.Fatalf("two hops without nodegroups: %v", err)
	}
	for _, h := range plan.Hops {
		for _, s := range h.Steps {
			if s.Type == StepNodegroup && s.Status != StatusManual && s.Status != StatusCompleted {
				t.Fatalf("hop %s→%s nodegroup step %+v, want manual", h.From, h.To, s)
			}
		}
	}
}

// A nodegroup the control plane would leave beyond the kubelet skew is not
// rolled early when --only leaves nodegroups out: the readiness step blocks
// instead.
func TestBuildPlan_ControlPlaneOnlyStillGatesTheSkew(t *testing.T) {
	m := newWorldMock(&fakeWorld{
		clusterVersion: "1.31",
		addonVersions:  map[string]string{"vpc-cni": latestFor("1.31")},
		ngVersions:     map[string]string{"workers-a": "1.28"},
	})
	svc := newTestService(m)
	plan, err := svc.BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{Only: []Part{PartControlPlane}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if b := strings.Join(plan.Blockers(), "\n"); !strings.Contains(b, "kubelet skew") {
		t.Fatalf("blockers = %q, want the kubelet skew", b)
	}
}

// Leaving the add-ons out cannot hide an add-on the new control plane cannot
// run and that has no compatible version: that step stays blocked. A
// blocked nodegroup step that turns manual keeps its reason.
func TestBuildPlan_ScopeKeepsHardAddonBlockers(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.31").
		WithAddon("coredns", "v1.31.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddonVersions("coredns", []string{"v1.31.0-eksbuild.1"}, "1.31").
		WithAddonVersions("coredns", []string{}, "1.32").
		WithNodegroup("workers-a", "1.31", ekstypes.AMITypesAl2023X8664Standard).
		Build()
	plan, err := newTestService(m).BuildPlan(context.Background(), "prod-east", "1.32", PlanOptions{Only: []Part{PartControlPlane}})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if s := findStep(t, plan.Hops[len(plan.Hops)-1].Steps, StepAddon, "coredns"); s.Status != StatusBlocked {
		t.Fatalf("coredns step = %+v, want still blocked", s)
	}
	if len(plan.Blockers()) == 0 {
		t.Fatal("a control-plane-only plan hid the add-on blocker")
	}
}
