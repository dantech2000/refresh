package change

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// --only control-plane runs the control plane alone: the add-ons and the
// nodegroups stay where they are, as manual steps with a notice that
// names the next command. A second run with --only nodegroups rolls them
// to the version of the control plane, a third one with --only addons
// updates the add-ons, and nothing else moves in any of them.
//
// The command of the notice names the cluster as a positional and not
// with -c: CONTRACT.md, change 2, "The positional names what the command
// acts on".
func TestPlanUpgrade_ControlPlaneThenNodegroupsSeparately(t *testing.T) {
	w := newPlanWorld()

	cp := w.upgrade(t, "1.32", UpgradeRequest{Only: []Part{PartControlPlane}})
	if s := findStep(t, cp, "1.32", StepNodegroup, "workers-a"); s.Action != Manual || s.Reason != OutOfScope {
		t.Fatalf("nodegroup step = %s %s, want Manual OutOfScope", s.Action, s.Reason)
	}
	if s := findStep(t, cp, "1.32", StepAddon, "vpc-cni"); s.Action != Manual || s.Reason != OutOfScope {
		t.Fatalf("add-on step = %s %s, want Manual OutOfScope", s.Action, s.Reason)
	}
	notices := strings.Join(cp.Notices, "\n")
	if !strings.Contains(notices, "--only nodegroups") || !strings.Contains(notices, "refresh cluster upgrade prod-east --to 1.32 --only addons") {
		t.Fatalf("notices = %q, want the next commands", notices)
	}
	if len(cp.Blockers) > 0 {
		t.Fatalf("blockers = %q", blockerText(cp))
	}
	if events, _ := w.apply(cp, nil); !slices.Equal(events, []string{"cp→1.32"}) {
		t.Fatalf("changes with --only control-plane = %v", events)
	}

	cp = w.upgrade(t, "1.32", UpgradeRequest{Only: []Part{PartNodegroups}})
	if events, _ := w.apply(cp, nil); !slices.Equal(events, []string{"ng workers-a→1.32"}) {
		t.Fatalf("changes with --only nodegroups = %v", events)
	}

	cp = w.upgrade(t, "1.32", UpgradeRequest{Only: []Part{PartAddons}})
	if events, _ := w.apply(cp, nil); !slices.Equal(events, []string{"addon vpc-cni→" + latestFor("1.32")}) {
		t.Fatalf("changes with --only addons = %v", events)
	}
}

// Without the nodegroups, two hops are fine while the skew holds: their
// steps are manual.
func TestPlanUpgrade_TwoHopsWithoutNodegroups(t *testing.T) {
	cp := twoHopWorld().upgrade(t, "1.33", UpgradeRequest{Only: []Part{PartControlPlane, PartAddons}})
	for _, s := range cp.Steps() {
		if s.Ref.Kind == StepNodegroup && s.Action != Manual && s.Action != Skip {
			t.Fatalf("nodegroup step %s = %s, want Manual", s.Ref, s.Action)
		}
	}
	if len(cp.Blockers) > 0 {
		t.Fatalf("blockers = %q", blockerText(cp))
	}
}

// A nodegroup that the control plane would leave beyond the kubelet skew
// is not rolled early when the run leaves it alone, with --only or with
// -n: the readiness of the hop blocks.
func TestPlanUpgrade_ScopeStillGatesTheSkew(t *testing.T) {
	t.Run("--only control-plane", func(t *testing.T) {
		w := worldOf("1.31", "workers-a", "1.28")
		w.addons = map[string]string{"vpc-cni": latestFor("1.31")}
		cp := w.upgrade(t, "1.32", UpgradeRequest{Only: []Part{PartControlPlane}})
		if b := blockerText(cp); !strings.Contains(b, "kubelet skew") {
			t.Fatalf("blockers = %q, want the kubelet skew", b)
		}
		if got := hopTargets(cp); !slices.Equal(got, []string{"1.32"}) {
			t.Fatalf("hops = %v, want no early roll", got)
		}
	})
	t.Run("-n names another nodegroup", func(t *testing.T) {
		w := worldOf("1.31", "workers-a", "1.31", "workers-old", "1.28")
		w.addons = map[string]string{"vpc-cni": latestFor("1.31")}
		cp := w.upgrade(t, "1.32", UpgradeRequest{Nodegroups: []string{"workers-a"}})
		if b := blockerText(cp); !strings.Contains(b, "workers-old") {
			t.Fatalf("blockers = %q, want the kubelet skew of workers-old", b)
		}
	})
}

// Leaving the add-ons out cannot hide an add-on that the new control
// plane cannot run and that has no compatible version: that step stays
// blocked.
func TestPlanUpgrade_ScopeKeepsHardAddonBlockers(t *testing.T) {
	w := newPlanWorld()
	w.addons = map[string]string{"coredns": "v1.31.0-eksbuild.1"}
	w.catalogue = perVersion(map[string]map[string][]string{"coredns": {"1.31": {"v1.31.0-eksbuild.1"}, "1.32": {}}})
	cp := w.upgrade(t, "1.32", UpgradeRequest{Only: []Part{PartControlPlane}})
	if s := findStep(t, cp, "1.32", StepAddon, "coredns"); s.Action != Blocked {
		t.Fatalf("coredns step = %s, want still Blocked", s.Action)
	}
	if b := blockerFor(t, cp, StepAddon, "coredns"); b.Kind != BlockerUnavailable {
		t.Fatalf("blocker = %+v", b)
	}
}

// Any other blocked step that the run leaves alone turns manual. Its
// blocker goes, and a notice keeps the reason.
func TestPlanUpgrade_BlockedStepLeftOutTurnsManual(t *testing.T) {
	w := worldOf("1.32", "legacy", "1.32")
	w.nodegroup("legacy").amiType = "AL2_x86_64"
	cp := w.upgrade(t, "1.33", UpgradeRequest{Only: []Part{PartControlPlane}})
	if s := findStep(t, cp, "1.33", StepNodegroup, "legacy"); s.Action != Manual || s.Reason != OutOfScope {
		t.Fatalf("legacy step = %s %s, want Manual OutOfScope", s.Action, s.Reason)
	}
	if len(cp.Blockers) != 0 {
		t.Fatalf("blockers = %q, want none", blockerText(cp))
	}
	if n := strings.Join(cp.Notices, "\n"); !strings.Contains(n, "nodegroup legacy is left alone, and could not be changed: Amazon Linux 2 AMIs end at Kubernetes 1.32") {
		t.Fatalf("notices = %q, want the reason kept", n)
	}
}

// Two runs with the control plane alone, one after another: the first
// leaves vpc-cni on a build that the new control plane cannot run, so the
// second is blocked until the add-ons catch up. It does not leave vpc-cni
// two versions behind.
func TestPlanUpgrade_SecondControlPlaneOnlyRunWaitsForTheAddons(t *testing.T) {
	w := newPlanWorld()
	cpOnly := UpgradeRequest{Only: []Part{PartControlPlane}}
	w.apply(w.upgrade(t, "1.32", cpOnly), nil)

	cp := w.upgrade(t, "1.33", UpgradeRequest{Only: []Part{PartControlPlane, PartNodegroups}})
	b := blockerFor(t, cp, StepControlPlane, "")
	if b.Kind != BlockerNotReady || !strings.Contains(b.Message, "vpc-cni") || !strings.Contains(b.Message, "--only addons") {
		t.Fatalf("blocker = %+v, want vpc-cni to catch up first", b)
	}
	if s := findStep(t, cp, "1.33", StepControlPlane, ""); s.Action != Blocked {
		t.Fatalf("control-plane step = %s, want Blocked", s.Action)
	}

	// The add-ons catch up, and the next run with the control plane alone
	// goes ahead.
	w.apply(w.upgrade(t, "1.32", UpgradeRequest{Only: []Part{PartAddons}}), nil)
	if cp := w.upgrade(t, "1.33", cpOnly); len(cp.Blockers) > 0 {
		t.Fatalf("blockers after the add-ons caught up = %q", blockerText(cp))
	}
}

// The guard fails closed, in every scope: when the add-on versions for
// the live control plane cannot be read, the control-plane move is
// blocked, with the read on the failures of the plan. Advice to change
// --only cannot fix a catalogue that was not read, so the blocker has
// none.
func TestPlanUpgrade_BlocksOnUnreadAddonVersions(t *testing.T) {
	for _, only := range [][]Part{{PartControlPlane}, {PartControlPlane, PartAddons}, nil} {
		w := worldOf("1.32", "workers-a", "1.32")
		w.addons = map[string]string{"vpc-cni": latestFor("1.32")}
		w.catalogue = func(addon string, k fleet.Version) ([]string, error) {
			if k == ver("1.32") {
				return nil, accessDenied()
			}
			return w.builds(addon, k), nil
		}
		cp := w.upgrade(t, "1.33", UpgradeRequest{Only: only})
		b := blockerFor(t, cp, StepControlPlane, "")
		if b.Kind != BlockerUnread || !strings.Contains(b.Message, "vpc-cni") || !strings.Contains(b.Message, "could not be read: rerun to retry") {
			t.Fatalf("%v: blocker = %+v, want the unread versions of vpc-cni", only, b)
		}
		if strings.Contains(blockerText(cp), "--only") {
			t.Fatalf("%v: blockers = %q, want no advice about --only", only, blockerText(cp))
		}
		found := false
		for _, f := range cp.Failures {
			found = found || (f.Name == "vpc-cni" && f.Kind == apidoc.FailureAddon && f.Reason == apidoc.ReasonAccessDenied)
		}
		if !found {
			t.Fatalf("%v: failures = %+v, want the vpc-cni read", only, cp.Failures)
		}
		if cp.ExitCode() != 4 {
			t.Errorf("%v: exit code = %d, want 4", only, cp.ExitCode())
		}
	}
}

// -n rolls only the nodegroups that it names: the others are manual
// steps and keep their version.
func TestPlanUpgrade_OneNodegroupToTheControlPlane(t *testing.T) {
	w := worldOf("1.32", "workers-a", "1.31", "workers-b", "1.31")
	w.addons = map[string]string{"vpc-cni": latestFor("1.32")}

	err := w.upgradeErr(t, "1.32", UpgradeRequest{Only: []Part{PartNodegroups}, Nodegroups: []string{"workers-c"}})
	var notFound *NotFoundError
	if !errors.As(err, &notFound) || !slices.Equal(notFound.Names, []string{"workers-a", "workers-b"}) {
		t.Fatalf("a nodegroup that the cluster does not have: err = %v", err)
	}

	cp := w.upgrade(t, "1.32", UpgradeRequest{Only: []Part{PartNodegroups}, Nodegroups: []string{"workers-a"}})
	if s := findStep(t, cp, "1.32", StepNodegroup, "workers-b"); s.Action != Manual || s.Reason != OutOfScope {
		t.Fatalf("workers-b = %s %s, want Manual OutOfScope", s.Action, s.Reason)
	}
	if s := findStep(t, cp, "1.32", StepNodegroup, "workers-a"); s.Action != Submit {
		t.Fatalf("workers-a = %s, want Submit", s.Action)
	}
	if n := strings.Join(cp.Notices, "\n"); !strings.Contains(n, "nodegroup(s) workers-b stay on their version") {
		t.Fatalf("notices = %q, want workers-b named", n)
	}
	if events, _ := w.apply(cp, nil); !slices.Equal(events, []string{"ng workers-a→1.32"}) {
		t.Fatalf("changes = %v, want workers-a alone", events)
	}
}

// The commands that the plan names keep the account, the region, and the
// exclusions of the run: following them never changes what the user left
// out.
//
// Two things differ from the old command lines. The cluster is a
// positional (CONTRACT.md, change 2), and --skip is --skip-addon
// (CONTRACT.md, "Command flags": "cluster upgrade --skip becomes
// --skip-addon"). The old test skipped kube-proxy on a cluster without
// it; a name that matches nothing is an error now, so the cluster has it.
func TestPlanUpgrade_FollowUpsKeepExclusionsAndTarget(t *testing.T) {
	w := worldOf("1.31", "workers-a", "1.31", "protected", "1.31")
	w.addons = map[string]string{"vpc-cni": latestFor("1.31"), "kube-proxy": latestFor("1.31")}
	in := w.upgradeInputs()
	in.Command = FollowUp{Prefix: "refresh --profile 'prod admin' --region eu-west-1", Suffix: "--kube-context prod-ctx"}
	p, err := PlanUpgrade(w.snapshot(), UpgradeRequest{
		Target: Target{Region: east, Cluster: w.name}, To: ver("1.32"),
		Only: []Part{PartControlPlane}, SkipAddons: []string{"kube-proxy"}, SkipNodegroups: []string{"protected"},
	}, in)
	if err != nil {
		t.Fatal(err)
	}
	notices := strings.Join(p.Clusters[0].Notices, "\n")
	for _, want := range []string{
		"refresh --profile 'prod admin' --region eu-west-1 cluster upgrade prod-east --to 1.32 --only addons --skip-addon kube-proxy --skip-nodegroup protected --kube-context prod-ctx",
		"refresh --profile 'prod admin' --region eu-west-1 cluster upgrade prod-east --to 1.32 --only nodegroups --skip-addon kube-proxy --skip-nodegroup protected --kube-context prod-ctx",
	} {
		if !strings.Contains(notices, want) {
			t.Fatalf("notices lack %q:\n%s", want, notices)
		}
	}
}

// An add-on with no version at all for the live control plane blocks a
// control-plane move that leaves the add-ons out: nothing in that plan
// would update it. The remedy that the blocker names is the one that
// works, because updating the add-ons cannot. With the add-ons in the
// plan, the add-on step of the hop decides: a version for 1.33 exists, so
// nothing blocks.
//
// The flag in the blocker is --skip-addon, the new name of --skip
// (CONTRACT.md, "Command flags").
func TestPlanUpgrade_ControlPlaneOnlyBlocksOnAnEmptyLiveCatalogue(t *testing.T) {
	build := func() *planWorld {
		w := worldOf("1.32", "workers-a", "1.32")
		w.addons = map[string]string{"legacy": "v1.0.0-eksbuild.1"}
		w.catalogue = perVersion(map[string]map[string][]string{"legacy": {"1.32": {}, "1.33": {"v2.0.0-eksbuild.1"}}})
		return w
	}
	cp := build().upgrade(t, "1.33", UpgradeRequest{Only: []Part{PartControlPlane}})
	if b := blockerText(cp); !strings.Contains(b, "EKS lists no version of add-on(s) legacy") || !strings.Contains(b, "--skip-addon legacy") {
		t.Fatalf("blockers = %q, want legacy with --skip-addon", b)
	}
	for _, only := range [][]Part{nil, {PartControlPlane, PartAddons}} {
		if cp := build().upgrade(t, "1.33", UpgradeRequest{Only: only}); len(cp.Blockers) > 0 {
			t.Fatalf("%v: blockers = %q, want none", only, blockerText(cp))
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":           "''",
		"prod-east":  "prod-east",
		"a b":        "'a b'",
		"it's":       `'it'\''s'`,
		"v1.2_x:y=z": "v1.2_x:y=z",
		"$(rm)":      "'$(rm)'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
