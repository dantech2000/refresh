package change

import (
	"slices"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// A nodegroup is rolled early, to the live control-plane version, only
// when the next control-plane step would push it beyond the kubelet skew,
// and only when that roll is itself within the skew. The decision depends
// on the nodegroup alone, never on its siblings.
func TestPlanUpgrade_PreRollRule(t *testing.T) {
	tests := []struct {
		name   string
		cp, to string
		// ngs are name and version pairs.
		ngs    []string
		custom []string
		skip   []string
		want   []string
		// blocked is the nodegroup that the one blocker names.
		blocked string
	}{
		{
			name: "steady-state one-minor lag rolls once, straight to the target",
			cp:   "1.29", to: "1.30", ngs: []string{"workers-a", "1.28", "workers-b", "1.29"},
			want: []string{"1.30 ng[workers-a workers-b]"},
		},
		{
			name: "two-minor lag is still within the skew of the next hop",
			cp:   "1.29", to: "1.30", ngs: []string{"workers-a", "1.27"},
			want: []string{"1.30 ng[workers-a]"},
		},
		{
			name: "three-minor lag is rolled early to the live control plane",
			cp:   "1.29", to: "1.30", ngs: []string{"old", "1.26", "cur", "1.29"},
			want: []string{"1.29 ng[old]", "1.30 ng[cur old]"},
		},
		{
			name: "a sibling one minor behind changes nothing",
			cp:   "1.29", to: "1.30", ngs: []string{"old", "1.26", "mid", "1.28", "cur", "1.29"},
			want: []string{"1.29 ng[old]", "1.30 ng[cur mid old]"},
		},
		{
			name: "already beyond the skew of the live control plane blocks",
			cp:   "1.29", to: "1.30", ngs: []string{"ancient", "1.25", "mid", "1.28"},
			want: []string{"1.30 ng[ancient mid]"}, blocked: "ancient",
		},
		{
			name: "a custom AMI is not rolled early and blocks",
			cp:   "1.29", to: "1.30", ngs: []string{"byo", "1.26"}, custom: []string{"byo"},
			want: []string{"1.30 ng[]"}, blocked: "byo",
		},
		{
			name: "a skipped nodegroup is not rolled early and blocks",
			cp:   "1.29", to: "1.30", ngs: []string{"legacy", "1.26"}, skip: []string{"legacy"},
			want: []string{"1.30 ng[]"}, blocked: "legacy",
		},
		{
			name: "several hops roll early only before the first one",
			cp:   "1.29", to: "1.32", ngs: []string{"a", "1.26", "b", "1.28", "c", "1.29"},
			want: []string{"1.29 ng[a]", "1.30 ng[a b c]", "1.31 ng[a b c]", "1.32 ng[a b c]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := worldOf(tt.cp, tt.ngs...)
			w.addons = map[string]string{"vpc-cni": latestFor(tt.cp)}
			for _, n := range tt.custom {
				w.nodegroup(n).amiType = "CUSTOM"
			}
			cp := w.upgrade(t, tt.to, UpgradeRequest{SkipNodegroups: tt.skip})
			if got := planShape(cp); !slices.Equal(got, tt.want) {
				t.Fatalf("plan = %q, want %q", got, tt.want)
			}
			if tt.blocked == "" && len(cp.Blockers) > 0 {
				t.Fatalf("the plan is blocked: %s", blockerText(cp))
			}
			if tt.blocked != "" && (len(cp.Blockers) != 1 || !strings.Contains(cp.Blockers[0].Message, tt.blocked)) {
				t.Fatalf("blockers = %q, want one that names %s", blockerText(cp), tt.blocked)
			}
			if len(tt.want) == 1 {
				return
			}
			// The hop at the live version never updates add-ons that the
			// live control plane can run, moves no control plane, and
			// rolls for the reason PreRoll.
			for _, ph := range hopOf(cp, tt.cp) {
				for _, s := range ph.Steps {
					switch {
					case s.Ref.Kind == StepAddon:
						t.Fatalf("the early hop has the add-on step %+v; the add-ons run on %s", s, tt.cp)
					case s.Ref.Kind == StepControlPlane && s.Action != Skip:
						t.Fatalf("the early hop moves the control plane: %+v", s)
					case s.Ref.Kind == StepNodegroup && (s.Action != Submit || s.Reason != PreRoll || s.To != tt.cp):
						t.Fatalf("early roll = %+v, want Submit PreRoll to %s", s, tt.cp)
					}
				}
			}
		})
	}
}

// Steady state, with the nodegroups one minor version behind, and an
// upgrade of one hop: every nodegroup rolls exactly once, straight to the
// target.
func TestPlanUpgrade_SteadyStateLagRollsEachNodegroupOnce(t *testing.T) {
	w := worldOf("1.29", "workers-a", "1.28", "workers-b", "1.29")
	w.addons = map[string]string{"vpc-cni": latestFor("1.29")}
	events, _ := w.apply(w.upgrade(t, "1.30", UpgradeRequest{}), nil)
	want := []string{"cp→1.30", "addon vpc-cni→" + latestFor("1.30"), "ng workers-a→1.30", "ng workers-b→1.30"}
	if !slices.Equal(events, want) {
		t.Fatalf("changes = %v, want %v", events, want)
	}
}

// 1.29 to 1.32 with nodegroups at 1.26, 1.28, and 1.29: only the one at
// 1.26 is rolled early, to 1.29, before the control plane leaves 1.29.
// After that every nodegroup rolls once in each hop.
func TestPlanUpgrade_MultiHopPreRollsOnlyTheNodegroupThatNeedsIt(t *testing.T) {
	w := worldOf("1.29", "a", "1.26", "b", "1.28", "c", "1.29")
	w.addons = map[string]string{"vpc-cni": latestFor("1.29")}
	events, _ := w.apply(w.upgrade(t, "1.32", UpgradeRequest{}), nil)
	want := []string{"ng a→1.29"}
	for _, v := range []string{"1.30", "1.31", "1.32"} {
		want = append(want, "cp→"+v, "addon vpc-cni→"+latestFor(v), "ng a→"+v, "ng b→"+v, "ng c→"+v)
	}
	if !slices.Equal(events, want) {
		t.Fatalf("changes = %v, want %v", events, want)
	}
}

// A run from 1.29 to 1.31 fails in the add-on phase of 1.30, after the
// control plane moved. The next plan is derived from live state: the
// add-ons catch up to 1.30, the nodegroup at 1.27, which the next step to
// 1.31 would put beyond the skew, is rolled early to 1.30, the one at
// 1.29 is not, and the run converges.
func TestPlanUpgrade_InterruptedHopPreRollsFromLiveState(t *testing.T) {
	w := worldOf("1.29", "a", "1.27", "b", "1.29")
	w.addons = map[string]string{"vpc-cni": latestFor("1.29")}

	cp := w.upgrade(t, "1.31", UpgradeRequest{})
	if got, want := planShape(cp), []string{"1.30 ng[a b]", "1.31 ng[a b]"}; !slices.Equal(got, want) {
		t.Fatalf("first plan = %q, want %q: 1.27 is within the skew of 1.30", got, want)
	}
	if _, ok := w.apply(cp, failAddons("")); ok || w.version != "1.30" {
		t.Fatalf("the first run ended with the control plane at %s; want a failure after it moved to 1.30", w.version)
	}

	cp = w.upgrade(t, "1.31", UpgradeRequest{})
	if got, want := planShape(cp), []string{"1.30 ng[a]", "1.31 ng[a b]"}; !slices.Equal(got, want) {
		t.Fatalf("second plan = %q, want %q", got, want)
	}
	if s := findStep(t, cp, "1.30", StepAddon, "vpc-cni"); s.Action != Submit || s.To != latestFor("1.30") {
		t.Fatalf("catch-up add-on step = %+v, want Submit to %s", s, latestFor("1.30"))
	}
	events, _ := w.apply(cp, nil)
	want := []string{
		"addon vpc-cni→" + latestFor("1.30"), "ng a→1.30",
		"cp→1.31", "addon vpc-cni→" + latestFor("1.31"), "ng a→1.31", "ng b→1.31",
	}
	if !slices.Equal(events, want) {
		t.Fatalf("changes of the second run = %v, want %v", events, want)
	}
	if cp := w.upgrade(t, "1.31", UpgradeRequest{}); workSteps(cp) != 0 {
		t.Fatalf("steps to run after the second run = %v", stepLines(cp))
	}
}

// A run from 1.31 to 1.33 was interrupted once the control plane reached
// 1.32. The next plan brings the add-ons, which 1.32 cannot run, up
// before the control plane moves to 1.33. The nodegroup at 1.31 stays
// within the kubelet skew of 1.33, so it is not rolled early: it rolls
// once, to 1.33.
func TestPlanUpgrade_MidHopCatchUpBeforeNextControlPlane(t *testing.T) {
	w := newPlanWorld()
	w.version = "1.32"
	cp := w.upgrade(t, "1.33", UpgradeRequest{})

	if got, want := hopTargets(cp), []string{"1.32", "1.33"}; !slices.Equal(got, want) {
		t.Fatalf("hops = %v, want a catch-up hop at 1.32 and then 1.33", got)
	}
	if s := findStep(t, cp, "1.32", StepAddon, "vpc-cni"); s.Action != Submit || s.To != latestFor("1.32") {
		t.Fatalf("catch-up add-on step = %+v, want Submit to %s", s, latestFor("1.32"))
	}
	for _, e := range stepOrder(cp, "1.32") {
		if strings.HasPrefix(e, "Nodegroup") {
			t.Fatalf("the catch-up hop has %q; an add-on catch-up must not roll nodegroups", e)
		}
	}
	events, _ := w.apply(cp, nil)
	want := []string{
		"addon vpc-cni→" + latestFor("1.32"),
		"cp→1.33", "addon vpc-cni→" + latestFor("1.33"), "ng workers-a→1.33",
	}
	if !slices.Equal(events, want) {
		t.Fatalf("changes = %v, want %v", events, want)
	}
}

// Whether the plan starts with a catch-up hop depends on the add-ons that
// the request does not skip, by exact name.
func TestPlanUpgrade_CatchUpFromIncompatibleAddon(t *testing.T) {
	tests := []struct {
		name    string
		version string
		skip    []string
		want    []string
	}{
		// Without nodegroups, the add-on alone starts the catch-up hop.
		{"an add-on that the live control plane cannot run", "1.32", nil, []string{"1.32", "1.33"}},
		{"the add-on is skipped", "1.32", []string{"vpc-cni"}, []string{"1.33"}},
		// The add-ons and the nodegroups match the control plane.
		{"nothing trails the control plane", "1.31", nil, []string{"1.32", "1.33"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newPlanWorld()
			w.version, w.nodegroups = tt.version, nil
			if tt.version == "1.31" {
				w.nodegroups = []*planNodegroup{{name: "workers-a", version: "1.31"}}
			}
			cp := w.upgrade(t, "1.33", UpgradeRequest{SkipAddons: tt.skip})
			if got := hopTargets(cp); !slices.Equal(got, tt.want) {
				t.Fatalf("hops = %v, want %v", got, tt.want)
			}
		})
	}
}

// A nodegroup that is beyond the kubelet skew of the live control plane
// already is not rolled early across that gap: there is no catch-up hop,
// and the readiness of the next hop blocks on it. Its sibling within the
// skew does not change that, and is not named.
func TestPlanUpgrade_NoPreRollAcrossExistingSkewGap(t *testing.T) {
	w := worldOf("1.31", "ng-a", "1.30", "ng-b", "1.27")
	w.addons = map[string]string{"vpc-cni": latestFor("1.31")}
	cp := w.upgrade(t, "1.32", UpgradeRequest{})
	if got := hopTargets(cp); !slices.Equal(got, []string{"1.32"}) {
		t.Fatalf("hops = %v, want 1.32 alone", got)
	}
	b := blockerFor(t, cp, StepControlPlane, "")
	if b.Kind != BlockerNotReady || !strings.Contains(b.Message, "ng-b") || strings.Contains(b.Message, "ng-a") {
		t.Fatalf("blocker = %+v, want the kubelet skew of ng-b alone", b)
	}
	if w.insightCalls != 0 {
		t.Errorf("the insights were read %d times; a hop that the skew blocks reads none", w.insightCalls)
	}
}

// orderWorld is a cluster on 1.31 with kube-proxy, which is compatible
// with one minor version, and vpc-cni, whose build stays compatible with
// the next one.
func orderWorld() *planWorld {
	w := newPlanWorld()
	w.addons = map[string]string{"vpc-cni": latestFor("1.31"), "kube-proxy": latestFor("1.31")}
	w.keepsCompat = map[string]bool{"vpc-cni": true}
	return w
}

// An add-on that the new control plane cannot run updates before the
// nodegroup rolls, and one that is behind but compatible updates after
// them.
func TestPlanUpgrade_IncompatibleAddonsBeforeRollsRestAfter(t *testing.T) {
	w := worldOf("1.32", "workers-a", "1.32")
	w.addons = map[string]string{"vpc-cni": "v1.19.0-eksbuild.1", "kube-proxy": "v1.32.0-eksbuild.1"}
	w.catalogue = perVersion(map[string]map[string][]string{
		"vpc-cni":    {"1.32": {"v1.20.0-eksbuild.1", "v1.19.0-eksbuild.1"}, "1.33": {"v1.20.0-eksbuild.1", "v1.19.0-eksbuild.1"}},
		"kube-proxy": {"1.32": {"v1.32.0-eksbuild.1"}, "1.33": {"v1.33.0-eksbuild.1"}},
	})
	cp := w.upgrade(t, "1.33", UpgradeRequest{})
	if got := hopTargets(cp); len(got) != 1 {
		t.Fatalf("hops = %v, want one", got)
	}
	want := []string{"ControlPlane", "Addon kube-proxy!", "Nodegroup workers-a", "Addon vpc-cni"}
	if got := stepOrder(cp, "1.33"); !slices.Equal(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	kp := findStep(t, cp, "1.33", StepAddon, "kube-proxy")
	if kp.Action != Submit || kp.From != "v1.32.0-eksbuild.1" || kp.To != "v1.33.0-eksbuild.1" || kp.Ref.Phase.Kind != PhaseAddonsBefore {
		t.Fatalf("kube-proxy step = %+v", kp)
	}
}

// Across hops the planner judges compatibility from the version that the
// add-on will run when the hop starts, not from the live one.
func TestPlanUpgrade_LaterHopJudgesSimulatedAddonVersion(t *testing.T) {
	w := newPlanWorld()
	w.addons = map[string]string{"vpc-cni": "v1.19.0-eksbuild.1", "kube-proxy": "v1.31.0-eksbuild.1"}
	w.catalogue = perVersion(map[string]map[string][]string{
		// The live build of vpc-cni leaves the catalogue at 1.33, but by
		// then the hop to 1.32 has moved it to v1.20, which 1.33 lists.
		"vpc-cni": {
			"1.31": {"v1.19.0-eksbuild.1"},
			"1.32": {"v1.20.0-eksbuild.1", "v1.19.0-eksbuild.1"},
			"1.33": {"v1.21.0-eksbuild.1", "v1.20.0-eksbuild.1"},
		},
		"kube-proxy": {"1.31": {"v1.31.0-eksbuild.1"}, "1.32": {"v1.32.0-eksbuild.1"}, "1.33": {"v1.33.0-eksbuild.1"}},
	})
	cp := w.upgrade(t, "1.33", UpgradeRequest{})
	if got := hopTargets(cp); !slices.Equal(got, []string{"1.32", "1.33"}) {
		t.Fatalf("hops = %v, want 1.32 and 1.33", got)
	}
	want := []string{"ControlPlane", "Addon kube-proxy!", "Nodegroup workers-a", "Addon vpc-cni"}
	for _, hop := range hopTargets(cp) {
		if got := stepOrder(cp, hop); !slices.Equal(got, want) {
			t.Fatalf("hop %s steps = %v, want %v", hop, got, want)
		}
	}
	if s := findStep(t, cp, "1.33", StepAddon, "vpc-cni"); s.From != "v1.20.0-eksbuild.1" || s.To != "v1.21.0-eksbuild.1" {
		t.Fatalf("vpc-cni in the second hop = %+v, want from the version of the first hop", s)
	}
}

// A blocked add-on and a skipped one keep their places: the one with no
// compatible version goes before the rolls, and the skipped one after
// them.
func TestPlanUpgrade_BlockedAddonBeforeSkippedAfter(t *testing.T) {
	w := worldOf("1.32", "workers-a", "1.32")
	w.addons = map[string]string{"coredns": "v1.11.0-eksbuild.1", "aws-ebs-csi-driver": "v1.30.0-eksbuild.1"}
	w.catalogue = anyVersion(nil)
	cp := w.upgrade(t, "1.33", UpgradeRequest{SkipAddons: []string{"aws-ebs-csi-driver"}})
	want := []string{"ControlPlane", "Addon coredns!", "Nodegroup workers-a", "Addon aws-ebs-csi-driver"}
	if got := stepOrder(cp, "1.33"); !slices.Equal(got, want) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if s := findStep(t, cp, "1.33", StepAddon, "coredns"); s.Action != Blocked {
		t.Fatalf("coredns = %s, want Blocked", s.Action)
	}
	if s := findStep(t, cp, "1.33", StepAddon, "aws-ebs-csi-driver"); s.Action != Manual {
		t.Fatalf("aws-ebs-csi-driver = %s, want Manual", s.Action)
	}
}

// Each hop runs as control plane, the add-ons that it needs, the
// nodegroup rolls, and the other add-ons.
func TestPlanUpgrade_RequiredAddonsBeforeRollsRestAfter(t *testing.T) {
	w := orderWorld()
	cp := w.upgrade(t, "1.33", UpgradeRequest{})
	var phases []string
	for _, ph := range cp.Phases {
		phases = append(phases, ph.Ref.String())
	}
	wantPhases := []string{
		"prod-east/1.32/ControlPlane", "prod-east/1.32/AddonsBefore", "prod-east/1.32/Nodegroups", "prod-east/1.32/AddonsAfter",
		"prod-east/1.33/ControlPlane", "prod-east/1.33/AddonsBefore", "prod-east/1.33/Nodegroups", "prod-east/1.33/AddonsAfter",
	}
	if !slices.Equal(phases, wantPhases) {
		t.Fatalf("phases = %q\nwant %q", phases, wantPhases)
	}
	events, _ := w.apply(cp, nil)
	want := []string{
		"cp→1.32", "addon kube-proxy→" + latestFor("1.32"), "ng workers-a→1.32", "addon vpc-cni→" + latestFor("1.32"),
		"cp→1.33", "addon kube-proxy→" + latestFor("1.33"), "ng workers-a→1.33", "addon vpc-cni→" + latestFor("1.33"),
	}
	if !slices.Equal(events, want) {
		t.Fatalf("changes = %v\nwant %v", events, want)
	}
}

// A hop in which the new control plane can run every add-on has no
// AddonsBefore phase: the rolls follow the control plane.
func TestPlanUpgrade_NoRequiredAddonsPhaseWhenAllCompatible(t *testing.T) {
	w := orderWorld()
	w.addons = map[string]string{"vpc-cni": latestFor("1.31")}
	cp := w.upgrade(t, "1.32", UpgradeRequest{})
	var kinds []PhaseKind
	for _, ph := range cp.Phases {
		kinds = append(kinds, ph.Ref.Kind)
	}
	if want := []PhaseKind{PhaseControlPlane, PhaseNodegroups, PhaseAddonsAfter}; !slices.Equal(kinds, want) {
		t.Fatalf("phases = %v, want %v", kinds, want)
	}
}

// A run that fails in the add-on phase after the rolls has rolled the
// nodegroups already. The next plan has only that add-on, and runs it.
func TestPlanUpgrade_AfterRollsTrailingAddonsFailed(t *testing.T) {
	w := orderWorld()
	if _, ok := w.apply(w.upgrade(t, "1.32", UpgradeRequest{}), failAddons("vpc-cni")); ok {
		t.Fatal("the first run did not fail")
	}
	if w.nodegroup("workers-a").version != "1.32" || w.addons["kube-proxy"] != latestFor("1.32") {
		t.Fatalf("world = %+v, want the rolls and kube-proxy done", w)
	}

	cp := w.upgrade(t, "1.32", UpgradeRequest{})
	if got := hopTargets(cp); len(got) != 1 || workSteps(cp) != 1 {
		t.Fatalf("second plan = %v, want one hop with one step to run", stepLines(cp))
	}
	if s := findStep(t, cp, "1.32", StepAddon, "vpc-cni"); s.Action != Submit || s.Ref.Phase.Kind != PhaseAddonsAfter {
		t.Fatalf("vpc-cni = %+v, want Submit after the rolls", s)
	}
	if events, _ := w.apply(cp, nil); !slices.Equal(events, []string{"addon vpc-cni→" + latestFor("1.32")}) {
		t.Fatalf("changes of the second run = %v, want only the vpc-cni update", events)
	}
}

// A run over two hops fails in the trailing add-ons of the first one. The
// next plan needs no catch-up hop, because the add-on still runs on the
// live control plane, and finishes the next hop in the new order.
func TestPlanUpgrade_MultiHopAfterRollsTrailingAddonsFailed(t *testing.T) {
	w := orderWorld()
	events, ok := w.apply(w.upgrade(t, "1.33", UpgradeRequest{}), failAddons("vpc-cni"))
	if ok || w.version != "1.32" || len(events) != 3 {
		t.Fatalf("the first run made %v and ended at %s; want a stop in the trailing add-ons of 1.32", events, w.version)
	}

	cp := w.upgrade(t, "1.33", UpgradeRequest{})
	if got := hopTargets(cp); !slices.Equal(got, []string{"1.33"}) {
		t.Fatalf("hops of the second plan = %v, want 1.33 alone, with no catch-up", got)
	}
	// The live 1.31 build of vpc-cni is not in the catalogue of 1.33, so
	// it goes before the rolls now, with kube-proxy.
	want := []string{"ControlPlane", "Addon vpc-cni!", "Addon kube-proxy!", "Nodegroup workers-a"}
	if got := stepOrder(cp, "1.33"); !slices.Equal(got, want) {
		t.Fatalf("steps of the second plan = %v, want %v", got, want)
	}
	events, _ = w.apply(cp, nil)
	wantEvents := []string{"cp→1.33", "addon vpc-cni→" + latestFor("1.33"), "addon kube-proxy→" + latestFor("1.33"), "ng workers-a→1.33"}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("changes of the second run = %v, want %v", events, wantEvents)
	}
}

// resumeWorld is a cluster on 1.32 where workers-a is at 1.32 already
// and workers-b is still to roll: the state that a second run finds.
func resumeWorld() *planWorld {
	return worldOf("1.32", "workers-a", "1.32", "workers-b", "1.31")
}

// A nodegroup at the target version is not done while it is not settled.
// A second run after a failed verification finds it DEGRADED, and one
// after an interrupt finds it UPDATING: the plan keeps the step, as a
// Verify step that sends nothing, and the nodegroup that is behind still
// rolls after it.
func TestPlanUpgrade_UnsettledNodegroupAtTargetIsVerified(t *testing.T) {
	for _, status := range []string{"DEGRADED", "UPDATING", "CREATING"} {
		t.Run(status, func(t *testing.T) {
			w := resumeWorld()
			w.nodegroup("workers-a").status = status
			cp := w.upgrade(t, "1.32", UpgradeRequest{})
			a := findStep(t, cp, "1.32", StepNodegroup, "workers-a")
			if a.Action != Verify || a.Reason != Unsettled || a.From != "1.32" || a.To != "1.32" {
				t.Fatalf("workers-a = %+v, want Verify Unsettled at 1.32", a)
			}
			if b := findStep(t, cp, "1.32", StepNodegroup, "workers-b"); b.Action != Submit {
				t.Fatalf("workers-b = %s, want Submit", b.Action)
			}
			if got := stepOrder(cp, "1.32"); !slices.Equal(got, []string{"ControlPlane", "Nodegroup workers-a", "Nodegroup workers-b"}) {
				t.Fatalf("steps = %v, want workers-a before workers-b", got)
			}
			// A skipped nodegroup is left alone, settled or not.
			cp = w.upgrade(t, "1.32", UpgradeRequest{SkipNodegroups: []string{"workers-a"}})
			if s := findStep(t, cp, "1.32", StepNodegroup, "workers-a"); s.Action != Skip {
				t.Fatalf("skipped workers-a = %s, want Skip", s.Action)
			}
		})
	}
}

// A second run over several hops must not move the control plane past a
// nodegroup that the last roll left DEGRADED. The nodegroup is at the
// live version, so no regular hop would look at it before the next
// control-plane step: a catch-up hop verifies it first.
func TestPlanUpgrade_MultiHopVerifiesDegradedNodegroupBeforeTheControlPlane(t *testing.T) {
	w := newPlanWorld()
	w.version, w.addons["vpc-cni"] = "1.32", latestFor("1.32")
	w.nodegroup("workers-a").version, w.nodegroup("workers-a").status = "1.32", "DEGRADED"
	cp := w.upgrade(t, "1.34", UpgradeRequest{})
	if got := hopTargets(cp); !slices.Equal(got, []string{"1.32", "1.33", "1.34"}) {
		t.Fatalf("hops = %v, want a catch-up hop at 1.32 first", got)
	}
	if got := stepOrder(cp, "1.32"); !slices.Equal(got, []string{"ControlPlane", "Nodegroup workers-a"}) {
		t.Fatalf("catch-up steps = %v", got)
	}
	if s := findStep(t, cp, "1.32", StepNodegroup, "workers-a"); s.Action != Verify || s.Reason != Unsettled {
		t.Fatalf("catch-up step = %s %s, want Verify Unsettled", s.Action, s.Reason)
	}
	first := cp.Steps()[0]
	for _, s := range cp.Steps() {
		if s.work() {
			first = s
			break
		}
	}
	if first.Ref.Kind != StepNodegroup || first.Action != Verify {
		t.Fatalf("the first step that runs is %s %s; want the verification, before any control-plane move", first.Ref, first.Action)
	}
	// With the nodegroups left out, nothing verifies it.
	cp = w.upgrade(t, "1.33", UpgradeRequest{Only: []Part{PartControlPlane, PartAddons}})
	if got := hopTargets(cp); !slices.Equal(got, []string{"1.33"}) {
		t.Fatalf("hops without nodegroups = %v, want 1.33 alone", got)
	}
}

// The catalogue of the live version decides what blocks the next
// control-plane step. A failed read of it is on the plan once, also when
// a later hop reads the same catalogue.
func TestPlanUpgrade_LiveCatalogueFailureIsReportedOnce(t *testing.T) {
	w := newPlanWorld()
	w.version = "1.32"
	w.catalogue = func(string, fleet.Version) ([]string, error) { return nil, accessDenied() }
	cp := w.upgrade(t, "1.34", UpgradeRequest{})
	n := 0
	for _, f := range cp.Failures {
		if f.Kind == apidoc.FailureAddon && f.Name == "vpc-cni" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("failures = %+v, want the vpc-cni read once", cp.Failures)
	}
}
