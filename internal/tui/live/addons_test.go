package live

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/services/addons"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/tui/state"
)

type addonRig struct {
	b *Backend
	// plan is what the preview returns; results by add-on name.
	mu      sync.Mutex
	plan    []addons.AddonUpdateResult
	results map[string]*addons.AddonUpdateResult
	errs    map[string]error
	order   []string // add-ons updated, in order, with the pinned version
}

func newAddonRig(t *testing.T) *addonRig {
	t.Helper()
	rows := prodRows()
	rows[0].Nodegroups[1].Status = "ACTIVE"
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": rows}}
	rig := &addonRig{b: newTestBackend(t, f, "us-east-1"), results: map[string]*addons.AddonUpdateResult{}, errs: map[string]error{}}
	rig.plan = []addons.AddonUpdateResult{
		{AddonName: "vpc-cni", PreviousVersion: "v1.18.3", NewVersion: "v1.19.2", Status: addons.StatusDryRun},
		{AddonName: "coredns", PreviousVersion: "v1.11.1", NewVersion: "v1.11.4", Status: addons.StatusDryRun, Warning: "coredns has a custom Corefile"},
		{AddonName: "kube-proxy", PreviousVersion: "v1.31.7", NewVersion: "v1.31.7", Status: addons.StatusUpToDate},
	}
	rig.b.opts.AllowChanges = true
	rig.b.addon = addonServices{
		preview: func(context.Context, aws.Config, string) ([]addons.AddonUpdateResult, error) {
			rig.mu.Lock()
			defer rig.mu.Unlock()
			return append([]addons.AddonUpdateResult(nil), rig.plan...), nil
		},
		update: func(_ context.Context, _ aws.Config, cluster, addon, version string, _ time.Duration) (*addons.AddonUpdateResult, error) {
			rig.mu.Lock()
			defer rig.mu.Unlock()
			rig.order = append(rig.order, cluster+"/"+addon+"@"+version)
			if err := rig.errs[addon]; err != nil {
				return nil, err
			}
			if r := rig.results[addon]; r != nil {
				return r, nil
			}
			return &addons.AddonUpdateResult{AddonName: addon, NewVersion: version, Status: addons.StatusCompleted}, nil
		},
	}
	rig.b.sweep(t.Context())
	return rig
}

var addonUpdate = state.Action{Kind: state.ActionAddons, Cluster: "prod-api"}

func TestAddonDryRunIsTheServicePreview(t *testing.T) {
	rig := newAddonRig(t)
	p, err := rig.b.Plan(t.Context(), addonUpdate)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 2 || p.Changes[1].Field != "coredns" || p.Blocked != "" {
		t.Fatalf("plan = %+v", p)
	}
	if !strings.HasSuffix(p.Command, "addon update --all -c prod-api --dependency-order --health-check --wait") {
		t.Fatalf("command = %q", p.Command)
	}
	if p.Gates[0].Text != "coredns" || p.Gates[0].Status != state.CheckWarn {
		t.Fatalf("gates = %+v", p.Gates)
	}
}

func TestAddonUpdateRunsInOrderPinnedToThePlan(t *testing.T) {
	rig := newAddonRig(t)
	if _, err := rig.b.Plan(t.Context(), addonUpdate); err != nil {
		t.Fatal(err)
	}
	if err := rig.b.Start(t.Context(), addonUpdate); err != nil {
		t.Fatal(err)
	}
	rig.b.Close()
	if got := strings.Join(rig.order, ","); got != "prod-api/vpc-cni@v1.19.2,prod-api/coredns@v1.11.4" {
		t.Fatalf("updated %s", got)
	}
	st, _ := rig.b.State(t.Context())
	text := joinText(st.Feed)
	for _, want := range []string{"add-ons update started vpc-cni → coredns", "vpc-cni ACTIVE v1.19.2", "add-ons add-ons updated"} {
		if !strings.Contains(text, want) {
			t.Errorf("feed lacks %q:\n%s", want, text)
		}
	}
	for _, c := range st.Clusters {
		if c.Name == "prod-api" && strings.Contains(c.Busy, "add-ons") {
			t.Fatalf("still busy: %q", c.Busy)
		}
	}
}

func TestAddonUpdateNeedsTheSameDryRun(t *testing.T) {
	rig := newAddonRig(t)
	if err := rig.b.Start(t.Context(), addonUpdate); err == nil || !strings.Contains(err.Error(), "no dry run") {
		t.Fatalf("Start without a dry run = %v", err)
	}
	if _, err := rig.b.Plan(t.Context(), addonUpdate); err != nil {
		t.Fatal(err)
	}
	// A newer version appears between the dry run and y.
	rig.mu.Lock()
	rig.plan[0].NewVersion = "v1.19.3"
	rig.mu.Unlock()
	if err := rig.b.Start(t.Context(), addonUpdate); err == nil || !strings.Contains(err.Error(), "changed since the dry run") {
		t.Fatalf("Start with a changed plan = %v", err)
	}
	if len(rig.order) != 0 {
		t.Fatal("an add-on was updated from a plan the user did not see")
	}
	// The claim was released: a fresh dry run can start.
	if _, err := rig.b.Plan(t.Context(), addonUpdate); err != nil {
		t.Fatal(err)
	}
	if err := rig.b.Start(t.Context(), addonUpdate); err != nil {
		t.Fatal(err)
	}
	rig.b.Close()
}

func TestAFailedAddonDoesNotStopTheOthers(t *testing.T) {
	rig := newAddonRig(t)
	rig.errs["vpc-cni"] = errors.New("InvalidParameterException: conflicts")
	rig.results["coredns"] = &addons.AddonUpdateResult{AddonName: "coredns", NewVersion: "v1.11.4", Status: addons.StatusCompletedWithIssues, HealthIssues: "InsufficientNumberOfReplicas"}
	if _, err := rig.b.Plan(t.Context(), addonUpdate); err != nil {
		t.Fatal(err)
	}
	if err := rig.b.Start(t.Context(), addonUpdate); err != nil {
		t.Fatal(err)
	}
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	text := joinText(st.Feed)
	for _, want := range []string{"vpc-cni update failed", "coredns updated with health issues InsufficientNumberOfReplicas", "2 of 2 need attention"} {
		if !strings.Contains(text, want) {
			t.Errorf("feed lacks %q:\n%s", want, text)
		}
	}
}

func TestAddonUpdateWhileRollingIsBusy(t *testing.T) {
	rig := newAddonRig(t)
	rig.b.mu.Lock()
	rig.b.claimed[rig.b.targets["prod-api"]] = "rolling ng-general"
	rig.b.mu.Unlock()
	p, _ := rig.b.Plan(t.Context(), addonUpdate)
	if !strings.Contains(p.Blocked, "busy: rolling ng-general") {
		t.Fatalf("plan blocked = %q", p.Blocked)
	}
	if err := rig.b.Start(t.Context(), addonUpdate); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("Start = %v", err)
	}
}

func TestAnAddonThatCannotBePreviewedBlocksTheUpdate(t *testing.T) {
	rig := newAddonRig(t)
	rig.plan = append(rig.plan, addons.AddonUpdateResult{AddonName: "aws-ebs-csi-driver", Status: addons.StatusFailed,
		Failure: &diag.Failure{Error: "AccessDenied"}})
	p, err := rig.b.Plan(t.Context(), addonUpdate)
	if err != nil || !strings.Contains(p.Blocked, "could not preview aws-ebs-csi-driver") {
		t.Fatalf("plan blocked = %q, %v", p.Blocked, err)
	}
	// The failure appears between the dry run and y.
	rig.mu.Lock()
	rig.plan = rig.plan[:3]
	rig.mu.Unlock()
	if _, err := rig.b.Plan(t.Context(), addonUpdate); err != nil {
		t.Fatal(err)
	}
	rig.mu.Lock()
	rig.plan = append(rig.plan, addons.AddonUpdateResult{AddonName: "aws-ebs-csi-driver", Status: addons.StatusFailed,
		Failure: &diag.Failure{Error: "AccessDenied"}})
	rig.mu.Unlock()
	if err := rig.b.Start(t.Context(), addonUpdate); err == nil || !strings.Contains(err.Error(), "could not preview aws-ebs-csi-driver") {
		t.Fatalf("Start = %v", err)
	}
	if len(rig.order) != 0 {
		t.Fatalf("updated %v with an add-on unread", rig.order)
	}
}
