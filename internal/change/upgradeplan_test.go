package change

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// twoHopWorld is a cluster two minor versions behind 1.33 with one
// add-on and one nodegroup, all healthy. The installed add-on build stays
// in the catalogue, as it does in EKS, so the live control plane can run
// it and the plan has no catch-up hop.
func twoHopWorld() *planWorld {
	w := newPlanWorld()
	w.catalogue = anyVersion(map[string][]string{"vpc-cni": {"v1.33.0-eksbuild.1", "v1.31.0-eksbuild.1"}})
	return w
}

// A cluster two minor versions behind has a plan of two hops, each with
// the whole sequence: control plane, nodegroups, add-ons. The new control
// plane can run the installed add-on build, so its update waits for the
// rolls.
func TestPlanUpgrade_TwoMinorsBehindYieldsTwoHops(t *testing.T) {
	cp := twoHopWorld().upgrade(t, "1.33", UpgradeRequest{})

	if cp.From != ver("1.31") || cp.To != ver("1.33") {
		t.Fatalf("plan goes from %s to %s", cp.From, cp.To)
	}
	if got, want := hopTargets(cp), []string{"1.32", "1.33"}; !slices.Equal(got, want) {
		t.Fatalf("hops = %v, want %v", got, want)
	}
	for _, hop := range hopTargets(cp) {
		want := []string{"ControlPlane", "Nodegroup workers-a", "Addon vpc-cni"}
		if got := stepOrder(cp, hop); !slices.Equal(got, want) {
			t.Fatalf("hop %s steps = %v, want %v", hop, got, want)
		}
		var kinds []PhaseKind
		for _, ph := range hopOf(cp, hop) {
			kinds = append(kinds, ph.Ref.Kind)
		}
		if want := []PhaseKind{PhaseControlPlane, PhaseNodegroups, PhaseAddonsAfter}; !slices.Equal(kinds, want) {
			t.Fatalf("hop %s phases = %v, want %v", hop, kinds, want)
		}
	}
	if len(cp.Blockers) > 0 {
		t.Fatalf("the plan is blocked: %s", blockerText(cp))
	}
	if workSteps(cp) == 0 {
		t.Fatal("the plan has no step to run")
	}
	// The newest build is the same for both versions. The first hop is
	// planned to it, and the second hop starts from where the first one
	// ends, while the live build still decides that the step is not done.
	if s := findStep(t, cp, "1.33", StepAddon, "vpc-cni"); s.Action != Submit || s.From != "v1.33.0-eksbuild.1" || s.To != "v1.33.0-eksbuild.1" {
		t.Fatalf("second-hop add-on step = %+v", s)
	}
}

// A nodegroup beyond the kubelet skew blocks the plan, and the blocker
// names it. At 1.27 it is beyond the skew of the live 1.31 control plane
// already, so it cannot be rolled early either.
func TestPlanUpgrade_LaggingNodegroupBlocks(t *testing.T) {
	w := twoHopWorld()
	w.nodegroups = []*planNodegroup{{name: "ancient", version: "1.27"}}
	cp := w.upgrade(t, "1.32", UpgradeRequest{})

	if len(cp.Blockers) == 0 || !strings.Contains(cp.Blockers[0].Message, "ancient") {
		t.Fatalf("blockers = %q, want one that names the nodegroup ancient", blockerText(cp))
	}
	if b := cp.Blockers[0]; b.Kind != BlockerNotReady || b.Step == nil || b.Step.Kind != StepControlPlane || b.Step.Phase.Hop != ver("1.32") {
		t.Fatalf("blocker = %+v, want NotReady on the control-plane step of the hop", b)
	}
	if cp.Decision() != DecisionBlocked {
		t.Fatalf("decision = %s, want Blocked", cp.Decision())
	}
}

func TestPlanUpgrade_Refusals(t *testing.T) {
	tests := []struct {
		name string
		to   string
		req  UpgradeRequest
		edit func(w *planWorld)
		want RefusalReason
		text string
	}{
		{name: "a target older than the control plane", to: "1.30", want: RefusalOlderTarget, text: "older than current version 1.31"},
		// The world offers 1.28 to 1.35, so an unknown target is refused
		// the way EKS refuses it.
		{name: "a version that EKS does not offer", to: "1.99", want: RefusalNotOffered, text: "not offered"},
		{name: "another major version", to: "2.0", want: RefusalOtherMajor},
		{name: "too many hops", to: "1.2000000000", want: RefusalTooManyHops, text: "one plan crosses at most 100"},
		// Scopes that the plan cannot keep safe are refused before
		// anything is read past the cluster: a catch-up beyond the control
		// plane, and a control plane that moves two versions while the
		// add-ons stay behind.
		{
			name: "nodegroups past the control plane", to: "1.32", req: UpgradeRequest{Only: []Part{PartNodegroups}},
			want: RefusalNeedsControlPlane, text: "use --to 1.31",
		},
		{
			name: "two hops without add-ons", to: "1.33", req: UpgradeRequest{Only: []Part{PartControlPlane, PartNodegroups}},
			want: RefusalNeedsAddons, text: "use --to 1.32",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := twoHopWorld()
			if tt.edit != nil {
				tt.edit(w)
			}
			err := w.upgradeErr(t, tt.to, tt.req)
			var refusal *RefusalError
			if !errors.As(err, &refusal) || refusal.Reason != tt.want || !strings.Contains(err.Error(), tt.text) {
				t.Fatalf("err = %v, want a refusal for %s with %q", err, tt.want, tt.text)
			}
			if tt.want != RefusalNotOffered && len(w.offerCalls)+len(w.catalogueCalls)+w.insightCalls != 0 {
				t.Errorf("the planner asked for %v, %v, and %d insights before it refused", w.offerCalls, w.catalogueCalls, w.insightCalls)
			}
		})
	}
}

// A version check that failed is a failure of the plan, and the plan
// goes on: EKS refuses a version that it does not offer.
func TestPlanUpgrade_VersionCheckFailed(t *testing.T) {
	w := twoHopWorld()
	w.offer = func(fleet.Version) VersionOffer { return VersionOffer{Err: accessDenied()} }
	cp := w.upgrade(t, "1.32", UpgradeRequest{})
	if len(cp.Blockers) > 0 || workSteps(cp) == 0 {
		t.Fatalf("plan = %v, blockers %q; want a plan that runs", stepLines(cp), blockerText(cp))
	}
	want := []apidoc.Failure{{
		Kind: apidoc.FailureCluster, Name: "prod-east", Region: east, Reason: apidoc.ReasonAccessDenied,
		Error: "AccessDeniedException: not authorized to perform the call", AWSErrorCode: "AccessDeniedException",
	}}
	if diff := cmp.Diff(want, cp.Failures); diff != "" {
		t.Errorf("failures (-want +got):\n%s", diff)
	}
	if cp.ExitCode() != 4 {
		t.Errorf("exit code = %d, want 4", cp.ExitCode())
	}
}

// A request for the version that the cluster runs, with the add-ons and
// the nodegroups current too, derives every step as done: nothing to do,
// and no error.
func TestPlanUpgrade_FullySatisfiedClusterIsNoOp(t *testing.T) {
	w := newPlanWorld()
	w.addons["vpc-cni"] = "v1.33.0-eksbuild.1"
	w.catalogue = anyVersion(map[string][]string{"vpc-cni": {"v1.33.0-eksbuild.1"}})
	cp := w.upgrade(t, "1.31", UpgradeRequest{})

	if n := workSteps(cp); n != 0 {
		t.Fatalf("steps to run = %d, want 0: %v", n, stepLines(cp))
	}
	if len(cp.Blockers) > 0 {
		t.Fatalf("the plan is blocked: %s", blockerText(cp))
	}
	if cp.Decision() != DecisionNothingToDo {
		t.Errorf("decision = %s, want NothingToDo", cp.Decision())
	}
	for _, s := range cp.Steps() {
		if s.Action != Skip || s.Reason != AlreadyAtTarget {
			t.Errorf("step %s = %s %s, want Skip AlreadyAtTarget", s.Ref, s.Action, s.Reason)
		}
	}
}

// An ERROR insight for the hop blocks its control plane, and a WARNING
// one is a notice.
func TestPlanUpgrade_Insights(t *testing.T) {
	t.Run("an ERROR insight blocks", func(t *testing.T) {
		w := twoHopWorld()
		w.insights = []apidoc.Insight{insightFor("Deprecated APIs removed in 1.32", insightError, "1.32")}
		cp := w.upgrade(t, "1.32", UpgradeRequest{})
		if len(cp.Blockers) != 1 || !strings.Contains(cp.Blockers[0].Message, "Deprecated APIs") {
			t.Fatalf("blockers = %q, want the insight named", blockerText(cp))
		}
	})
	t.Run("a WARNING insight does not block", func(t *testing.T) {
		w := twoHopWorld()
		w.insights = []apidoc.Insight{insightFor("Deprecated APIs in 2 manifests", insightWarning, "1.32")}
		cp := w.upgrade(t, "1.32", UpgradeRequest{})
		if len(cp.Blockers) > 0 {
			t.Fatalf("a WARNING insight blocked: %s", blockerText(cp))
		}
		if !strings.Contains(strings.Join(cp.Notices, "\n"), "Deprecated APIs") {
			t.Fatalf("notices = %q, want the insight named", cp.Notices)
		}
	})
}

// A custom-AMI nodegroup is a manual step, never a request.
func TestPlanUpgrade_CustomAMINodegroupIsManual(t *testing.T) {
	w := twoHopWorld()
	w.nodegroups = []*planNodegroup{{name: "byo-ami", version: "1.31", amiType: "CUSTOM"}}
	cp := w.upgrade(t, "1.32", UpgradeRequest{})
	if s := findStep(t, cp, "1.32", StepNodegroup, "byo-ami"); s.Action != Manual || s.Reason != CustomAMI {
		t.Fatalf("custom-AMI nodegroup step = %s %s, want Manual CustomAMI", s.Action, s.Reason)
	}
}

// An add-on with no version for the hop target blocks the plan, and so
// does one whose catalogue could not be read. The second is reported as a
// read that failed, not as an incompatibility.
func TestPlanUpgrade_AddonCatalogueBlocks(t *testing.T) {
	t.Run("no compatible version", func(t *testing.T) {
		w := newPlanWorld()
		w.addons = map[string]string{"legacy-addon": "v0.9.0"}
		w.catalogue = anyVersion(nil)
		cp := w.upgrade(t, "1.32", UpgradeRequest{})
		s := findStep(t, cp, "1.32", StepAddon, "legacy-addon")
		b := blockerFor(t, cp, StepAddon, "legacy-addon")
		if s.Action != Blocked || b.Kind != BlockerUnavailable || !strings.Contains(b.Message, "no version of legacy-addon is compatible") {
			t.Fatalf("step = %s, blocker = %+v; want blocked as incompatible", s.Action, b)
		}
		if len(cp.Failures) != 0 {
			t.Errorf("failures = %+v, want none: nothing failed to read", cp.Failures)
		}
	})
	t.Run("the lookup failed", func(t *testing.T) {
		w := twoHopWorld()
		w.catalogue = func(string, fleet.Version) ([]string, error) { return nil, accessDenied() }
		cp := w.upgrade(t, "1.32", UpgradeRequest{})
		s := findStep(t, cp, "1.32", StepAddon, "vpc-cni")
		b := blockerFor(t, cp, StepAddon, "vpc-cni")
		if s.Action != Blocked || b.Kind != BlockerUnread {
			t.Fatalf("step = %s, blocker = %+v; want blocked as unread", s.Action, b)
		}
		if strings.Contains(b.Message, "is compatible") || !strings.Contains(b.Message, "could not look up") {
			t.Fatalf("blocker = %q, want an explanation of the failed read, not a claim of incompatibility", b.Message)
		}
		found := false
		for _, f := range cp.Failures {
			found = found || (f.Kind == apidoc.FailureAddon && f.Name == "vpc-cni" && f.Reason == apidoc.ReasonAccessDenied && f.Cluster == "prod-east")
		}
		if !found {
			t.Fatalf("failures = %+v, want the vpc-cni read", cp.Failures)
		}
	})
}

// Resume by planning again: with the control plane at the hop target
// and the add-on and the nodegroup behind, the control-plane step is done
// and the other two are to run.
func TestPlanUpgrade_PartiallyUpgradedClusterMarksCompletedSteps(t *testing.T) {
	w := twoHopWorld()
	w.version = "1.32"
	cp := w.upgrade(t, "1.32", UpgradeRequest{})

	if got := hopTargets(cp); len(got) != 1 {
		t.Fatalf("hops = %v, want one", got)
	}
	if s := findStep(t, cp, "1.32", StepControlPlane, ""); s.Action != Skip || s.Reason != AlreadyAtTarget {
		t.Fatalf("control-plane step = %s %s, want Skip AlreadyAtTarget", s.Action, s.Reason)
	}
	if s := findStep(t, cp, "1.32", StepAddon, "vpc-cni"); s.Action != Submit {
		t.Fatalf("add-on step = %s, want Submit", s.Action)
	}
	if s := findStep(t, cp, "1.32", StepNodegroup, "workers-a"); s.Action != Submit || s.From != "1.31" || s.To != "1.32" {
		t.Fatalf("nodegroup step = %+v, want Submit from 1.31 to 1.32", s)
	}
}

// A skipped add-on and a skipped nodegroup are manual steps.
//
// The old test skipped the nodegroup with the pattern "workers". A name
// is exact now: CONTRACT.md, change 3, "Resource names are exact;
// patterns go through -f/--filter".
func TestPlanUpgrade_SkipListsAreManualSteps(t *testing.T) {
	cp := twoHopWorld().upgrade(t, "1.32", UpgradeRequest{SkipAddons: []string{"vpc-cni"}, SkipNodegroups: []string{"workers-a"}})
	if s := findStep(t, cp, "1.32", StepAddon, "vpc-cni"); s.Action != Manual || s.Reason != SkippedByFlag {
		t.Fatalf("skipped add-on = %s %s, want Manual SkippedByFlag", s.Action, s.Reason)
	}
	if s := findStep(t, cp, "1.32", StepNodegroup, "workers-a"); s.Action != Manual || s.Reason != SkippedByFlag {
		t.Fatalf("skipped nodegroup = %s %s, want Manual SkippedByFlag", s.Action, s.Reason)
	}
}

// A skip name is exact. The old planner took "cni" as a name that
// matches no add-on and went on, took "VPC-CNI" as vpc-cni, and took
// "workers" as a pattern for workers-a. Each is an error now, with the
// names that the cluster has: CONTRACT.md, "Targets": "Names are exact. A
// name that matches nothing is an error that lists the close matches."
func TestPlanUpgrade_SkipNamesAreExact(t *testing.T) {
	tests := []struct {
		name string
		req  UpgradeRequest
		kind StepKind
		bad  string
		have []string
	}{
		{"a part of an add-on name", UpgradeRequest{SkipAddons: []string{"cni"}}, StepAddon, "cni", []string{"vpc-cni"}},
		{"an add-on name in another case", UpgradeRequest{SkipAddons: []string{"VPC-CNI"}}, StepAddon, "VPC-CNI", []string{"vpc-cni"}},
		{"a part of a nodegroup name", UpgradeRequest{SkipNodegroups: []string{"workers"}}, StepNodegroup, "workers", []string{"workers-a"}},
		{"a nodegroup that -n names and the cluster does not have", UpgradeRequest{Nodegroups: []string{"workers-c"}}, StepNodegroup, "workers-c", []string{"workers-a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := twoHopWorld()
			err := w.upgradeErr(t, "1.32", tt.req)
			var notFound *NotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("err = %v, want a NotFoundError", err)
			}
			want := &NotFoundError{Target: Target{Region: east, Cluster: "prod-east"}, Kind: tt.kind, Name: tt.bad, Names: tt.have}
			if diff := cmp.Diff(want, notFound); diff != "" {
				t.Errorf("error (-want +got):\n%s", diff)
			}
			if len(w.catalogueCalls) != 0 || w.insightCalls != 0 {
				t.Errorf("the planner read %v and %d insights for a name that does not exist", w.catalogueCalls, w.insightCalls)
			}
		})
	}
}

// A skipped add-on is marked manual before its catalogue is read, so it
// is never a blocker, whether a compatible version exists or not.
func TestPlanUpgrade_SkippedAddonIsNeverLookedUp(t *testing.T) {
	w := newPlanWorld()
	w.addons = map[string]string{"coredns": "v1.11.0-eksbuild.1", "vpc-cni": latestFor("1.31")}
	w.keepsCompat = map[string]bool{"vpc-cni": true}
	w.catalogue = func(addon string, k fleet.Version) ([]string, error) {
		if addon == "coredns" {
			return nil, accessDenied()
		}
		return w.builds(addon, k), nil
	}
	cp := w.upgrade(t, "1.32", UpgradeRequest{SkipAddons: []string{"coredns"}})
	if len(cp.Blockers) > 0 || len(cp.Failures) > 0 {
		t.Fatalf("blockers %q, failures %+v; want none for a skipped add-on", blockerText(cp), cp.Failures)
	}
	for _, call := range w.catalogueCalls {
		if strings.HasPrefix(call, "coredns@") {
			t.Fatalf("the catalogue of the skipped add-on was read: %v", w.catalogueCalls)
		}
	}
	if s := findStep(t, cp, "1.32", StepAddon, "coredns"); s.Action != Manual || s.To != "" {
		t.Fatalf("skipped add-on step = %+v, want Manual with no version", s)
	}
}

// EKS publishes no Amazon Linux 2 AMI past 1.32, so a plan that would
// roll an AL2 nodegroup to 1.33 is blocked before the control plane
// moves. A nodegroup left behind with --skip-nodegroup stays a manual
// step, and up to 1.32 an AL2 nodegroup still rolls.
func TestPlanUpgrade_AL2NodegroupPast132Blocks(t *testing.T) {
	build := func(clusterVersion string) *planWorld {
		w := worldOf(clusterVersion, "legacy", clusterVersion, "modern", clusterVersion)
		w.nodegroup("legacy").amiType = "AL2_x86_64"
		w.addons = map[string]string{"vpc-cni": latestFor(clusterVersion)}
		w.keepsCompat = map[string]bool{"vpc-cni": true}
		return w
	}

	cp := build("1.32").upgrade(t, "1.33", UpgradeRequest{})
	b := blockerFor(t, cp, StepNodegroup, "legacy")
	if s := findStep(t, cp, "1.33", StepNodegroup, "legacy"); s.Action != Blocked || b.Kind != BlockerUnavailable ||
		!strings.Contains(b.Message, "Amazon Linux 2 AMIs end at Kubernetes 1.32") {
		t.Fatalf("AL2 step = %s, blocker %+v; want blocked", s.Action, b)
	}
	if s := findStep(t, cp, "1.33", StepNodegroup, "modern"); s.Action != Submit {
		t.Fatalf("AL2023 step = %s, want Submit", s.Action)
	}
	if len(cp.Blockers) != 1 {
		t.Fatalf("blockers = %q, want the one of the AL2 nodegroup", blockerText(cp))
	}

	cp = build("1.32").upgrade(t, "1.33", UpgradeRequest{SkipNodegroups: []string{"legacy"}})
	if s := findStep(t, cp, "1.33", StepNodegroup, "legacy"); s.Action != Manual || len(cp.Blockers) != 0 {
		t.Fatalf("skipped AL2 step = %s, blockers %q; want Manual and none", s.Action, blockerText(cp))
	}

	cp = build("1.31").upgrade(t, "1.32", UpgradeRequest{})
	if s := findStep(t, cp, "1.32", StepNodegroup, "legacy"); s.Action != Submit {
		t.Fatalf("AL2 step to 1.32 = %s, want Submit", s.Action)
	}
}

// --force goes with the request of every nodegroup step that the run
// submits, and with no other step.
func TestPlanUpgrade_ForceIsOnTheRolls(t *testing.T) {
	req := UpgradeRequest{}
	req.Policy.Force = true
	cp := twoHopWorld().upgrade(t, "1.32", req)
	for _, s := range cp.Steps() {
		if want := s.Ref.Kind == StepNodegroup; s.Force != want {
			t.Errorf("step %s has force %v, want %v", s.Ref, s.Force, want)
		}
	}
}

// A nodegroup that could not be read blocks its step: nothing shows its
// version.
func TestPlanUpgrade_UnreadNodegroupBlocks(t *testing.T) {
	w := twoHopWorld()
	w.nodegroups = append(w.nodegroups, &planNodegroup{name: "workers-b", unread: true})
	cp := w.upgrade(t, "1.32", UpgradeRequest{})
	if s := findStep(t, cp, "1.32", StepNodegroup, "workers-b"); s.Action != Blocked {
		t.Fatalf("step = %s, want Blocked", s.Action)
	}
	if b := blockerFor(t, cp, StepNodegroup, "workers-b"); b.Kind != BlockerUnread {
		t.Fatalf("blocker = %+v, want Unread", b)
	}
}

// The plan of an upgrade takes the steps of its own targets through the
// busy rule: a control plane that is updating is the step that attaches,
// and a nodegroup that is rolling is the step that attaches or verifies.
// The planner derives the steps first, so that a resumed upgrade does not
// refuse its own work.
func TestPlanUpgrade_StepsPassTheBusyRule(t *testing.T) {
	t.Run("the control plane is updating", func(t *testing.T) {
		w := twoHopWorld()
		w.status = "UPDATING"
		cp := w.upgrade(t, "1.32", UpgradeRequest{})
		if s := findStep(t, cp, "1.32", StepControlPlane, ""); s.Action != Attach || s.Reason != InFlight {
			t.Fatalf("control-plane step = %s %s, want Attach InFlight", s.Action, s.Reason)
		}
		if refuse, _ := Busy(w.snapshot(), ClusterUpgrade, cp.Steps()); len(refuse) != 0 {
			t.Fatalf("the busy rule refused the plan: %+v", refuse)
		}
	})
	t.Run("a nodegroup is rolling after an interrupt", func(t *testing.T) {
		w := worldOf("1.33", "ng-a", "1.32", "ng-b", "1.32")
		w.nodegroup("ng-a").status = "UPDATING"
		cp := w.upgrade(t, "1.33", UpgradeRequest{})
		if s := findStep(t, cp, "1.33", StepNodegroup, "ng-a"); s.Action != Attach || s.Reason != InFlight {
			t.Fatalf("ng-a step = %s %s, want Attach InFlight", s.Action, s.Reason)
		}
		if s := findStep(t, cp, "1.33", StepNodegroup, "ng-b"); s.Action != Submit {
			t.Fatalf("ng-b step = %s, want Submit", s.Action)
		}
		if refuse, _ := Busy(w.snapshot(), ClusterUpgrade, cp.Steps()); len(refuse) != 0 {
			t.Fatalf("the busy rule refused the plan: %+v", refuse)
		}
	})
}
