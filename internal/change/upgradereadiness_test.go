package change

import (
	"errors"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// oneHopWorld is a healthy cluster one minor version behind 1.32. It has
// the insights that a test gives it and no other.
func oneHopWorld(insights ...apidoc.Insight) *planWorld {
	w := newPlanWorld()
	w.catalogue = anyVersion(map[string][]string{"vpc-cni": {"v1.32.0-eksbuild.1", "v1.31.0-eksbuild.1"}})
	w.strict, w.insights = true, insights
	return w
}

// TestPlanUpgrade_Readiness is the insights half of the readiness of the
// hop after the live version, for a real run and for the preview of a dry
// run.
func TestPlanUpgrade_Readiness(t *testing.T) {
	skew := insightFor("Kubelet version skew", insightPassing, "1.32")
	apis := func(status string) apidoc.Insight {
		return insightFor("Deprecated APIs removed in 1.32", status, "1.32")
	}
	tests := []struct {
		name     string
		insights []apidoc.Insight
		edit     func(w *planWorld)
		req      UpgradeRequest
		// kind is the kind of the one blocker, or "" for a plan that is
		// not blocked. text is in the blocker, or in the notices.
		kind     BlockerKind
		text     []string
		failures int
		// calls is how often the planner asked for the insights.
		calls int
	}{
		{name: "passing insights", insights: []apidoc.Insight{skew, apis(insightPassing)}, calls: 1},
		{
			// EKS evaluates the insights about once a day, so right after
			// a control-plane hop the next minor version often has none.
			// None must block, and not pass as "0 blocking insights".
			name: "no insight after the refresh", calls: 1,
			kind: BlockerNotReady, text: []string{"insights for 1.32 not available yet", "--skip-insights-check"},
		},
		{
			name: "an insight for another version is none for this hop", insights: []apidoc.Insight{insightFor("Old", insightError, "1.31")}, calls: 1,
			kind: BlockerNotReady, text: []string{"insights for 1.32 not available yet"},
		},
		{
			name: "the refresh failed", insights: []apidoc.Insight{skew}, calls: 1,
			edit: func(w *planWorld) { w.refreshErr = errors.New("insights refresh failed: no details reported") },
			kind: BlockerUnread, text: []string{"could not refresh cluster insights for 1.32", "insights refresh failed", "--skip-insights-check"}, failures: 1,
		},
		{
			name: "the refresh did not finish in time", insights: []apidoc.Insight{skew}, calls: 1,
			edit: func(w *planWorld) { w.refreshErr = errors.New("insights refresh did not finish in time after 5m0s") },
			kind: BlockerUnread, text: []string{"did not finish in time"}, failures: 1,
		},
		{
			name: "the read failed", calls: 1,
			edit: func(w *planWorld) { w.insightsErr = accessDenied() },
			kind: BlockerUnread, text: []string{"could not read cluster insights for 1.32", "AccessDeniedException"}, failures: 1,
		},
		{
			name: "an UNKNOWN insight", insights: []apidoc.Insight{insightFor("Kubelet version skew", "UNKNOWN", "1.32"), apis(insightPassing)}, calls: 1,
			kind: BlockerNotReady, text: []string{"UNKNOWN", "Kubelet version skew", "--skip-insights-check"},
		},
		{
			name: "an ERROR insight and an UNKNOWN one", insights: []apidoc.Insight{insightFor("Kubelet version skew", "UNKNOWN", "1.32"), apis(insightError)}, calls: 1,
			kind: BlockerNotReady, text: []string{"1 blocking insight(s): Deprecated APIs removed in 1.32; 1 insight(s) with UNKNOWN status"},
		},
		{
			// No insight at all, and none is read.
			name: "--skip-insights-check", req: UpgradeRequest{SkipInsights: true}, calls: 0,
			text: []string{"cluster insights for 1.32 were not checked (--skip-insights-check)"},
		},

		// A dry run starts no refresh. An insight that is missing or
		// UNKNOWN is a notice in the preview, because a real run refreshes
		// first and then blocks on it.
		{
			name: "preview: no insight", edit: func(w *planWorld) { w.preview = true }, calls: 1,
			text: []string{"insights for 1.32 not evaluated yet; a real run will refresh them and block"},
		},
		{
			name: "preview: an UNKNOWN insight", insights: []apidoc.Insight{apis("UNKNOWN")}, edit: func(w *planWorld) { w.preview = true }, calls: 1,
			text: []string{"UNKNOWN", "a real run will refresh them"},
		},
		{name: "preview: a passing insight", insights: []apidoc.Insight{apis(insightPassing)}, edit: func(w *planWorld) { w.preview = true }, calls: 1},
		{
			name: "preview: an ERROR insight still blocks", insights: []apidoc.Insight{apis(insightError)}, edit: func(w *planWorld) { w.preview = true }, calls: 1,
			kind: BlockerNotReady, text: []string{"1 blocking insight(s): Deprecated APIs removed in 1.32"},
		},
		{
			// A failed read is a failure of the plan, exit 4, and does not
			// block a dry run.
			name: "preview: the read failed", calls: 1,
			edit: func(w *planWorld) { w.preview, w.insightsErr = true, accessDenied() },
			text: []string{"could not read cluster insights for 1.32"}, failures: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := oneHopWorld(tt.insights...)
			if tt.edit != nil {
				tt.edit(w)
			}
			cp := w.upgrade(t, "1.32", tt.req)
			text := strings.Join(cp.Notices, "\n")
			if tt.kind == "" {
				if len(cp.Blockers) != 0 {
					t.Fatalf("the plan is blocked: %s", blockerText(cp))
				}
			} else {
				b := blockerFor(t, cp, StepControlPlane, "")
				if len(cp.Blockers) != 1 || b.Kind != tt.kind || b.Step.Phase.Hop != ver("1.32") {
					t.Fatalf("blockers = %+v, want one %s blocker on the control plane of 1.32", cp.Blockers, tt.kind)
				}
				text = b.Message
			}
			for _, want := range tt.text {
				if !strings.Contains(text, want) {
					t.Errorf("text = %q, want %q in it", text, want)
				}
			}
			if len(cp.Failures) != tt.failures {
				t.Errorf("failures = %+v, want %d", cp.Failures, tt.failures)
			}
			if tt.failures > 0 && cp.ExitCode() != 4 {
				t.Errorf("exit code = %d, want 4", cp.ExitCode())
			}
			if w.insightCalls != tt.calls {
				t.Errorf("the insights were asked for %d times, want %d", w.insightCalls, tt.calls)
			}
			// The control-plane step keeps its own action: the blocker is
			// about the gate before it.
			if s := findStep(t, cp, "1.32", StepControlPlane, ""); s.Action != Submit {
				t.Errorf("control-plane step = %s, want Submit", s.Action)
			}
		})
	}
}

// A request for the version that the cluster runs moves no control
// plane, so it needs no insights: EKS has none for the version that the
// cluster already runs.
func TestPlanUpgrade_NoControlPlaneMoveNeedsNoInsights(t *testing.T) {
	w := worldOf("1.32", "workers-a", "1.31")
	w.strict = true
	cp := w.upgrade(t, "1.32", UpgradeRequest{})
	if len(cp.Blockers) > 0 {
		t.Fatalf("the plan is blocked: %s", blockerText(cp))
	}
	if w.insightCalls != 0 {
		t.Fatalf("the insights were asked for %d times, want 0", w.insightCalls)
	}
}

// EKS evaluates the upgrade insights for the next minor version only. A
// blocker for 1.33 appears once the cluster runs 1.32, so the plan cannot
// see it, and the readiness of the second hop is judged again from live
// state before its control plane moves.
func TestHopReadiness_BlocksLaterHop(t *testing.T) {
	w := newPlanWorld()
	cp := w.upgrade(t, "1.33", UpgradeRequest{})
	if len(cp.Blockers) > 0 {
		t.Fatalf("the plan cannot see the blocker of 1.33 yet: %s", blockerText(cp))
	}
	if w.insightCalls != 1 {
		t.Fatalf("the plan asked for the insights %d times; want once, for the hop after the live version", w.insightCalls)
	}

	w.apply(&ClusterPlan{Phases: hopOf(cp, "1.32")}, nil)
	w.insights = []apidoc.Insight{insightFor("Deprecated APIs removed in 1.33", insightError, "1.33")}
	r := HopReadiness(w.snapshot(), ver("1.33"), false, w.upgradeInsights)
	if r.Blocker == nil || r.Blocker.Kind != BlockerNotReady || !strings.Contains(r.Blocker.Message, "Deprecated APIs removed in 1.33") {
		t.Fatalf("readiness of 1.33 = %+v, want the blocking insight named", r)
	}
	if ref := r.Blocker.Step; ref == nil || ref.Kind != StepControlPlane || ref.Phase.Hop != ver("1.33") || ref.Phase.Target.Cluster != "prod-east" {
		t.Fatalf("blocker step = %+v, want the control plane of the hop to 1.33", ref)
	}

	// A control plane that is at the hop already needs no check.
	calls := w.insightCalls
	if r := HopReadiness(w.snapshot(), ver("1.32"), false, w.upgradeInsights); r.Blocker != nil || w.insightCalls != calls {
		t.Fatalf("readiness of the live version = %+v after %d more reads; want none", r, w.insightCalls-calls)
	}
}

// When EKS still has no insight for the target of the second hop, the hop
// must not start. --skip-insights-check lets it through.
func TestHopReadiness_LaterHopWithoutInsightsBlocks(t *testing.T) {
	w := newPlanWorld()
	w.strict = true
	// Only 1.32 was ever evaluated.
	w.insights = []apidoc.Insight{insightFor("Kubelet version skew", insightPassing, "1.32")}
	cp := w.upgrade(t, "1.33", UpgradeRequest{})
	if len(cp.Blockers) > 0 {
		t.Fatalf("the plan is blocked: %s", blockerText(cp))
	}
	w.apply(&ClusterPlan{Phases: hopOf(cp, "1.32")}, nil)

	r := HopReadiness(w.snapshot(), ver("1.33"), false, w.upgradeInsights)
	if r.Blocker == nil || !strings.Contains(r.Blocker.Message, "insights for 1.33 not available yet") {
		t.Fatalf("readiness of 1.33 = %+v, want the missing insights named", r)
	}
	if w.insightCalls != 2 {
		t.Fatalf("the insights were asked for %d times; want 2: in the plan, then live before the second hop", w.insightCalls)
	}
	r = HopReadiness(w.snapshot(), ver("1.33"), true, w.upgradeInsights)
	if r.Blocker != nil || len(r.Notices) != 1 || w.insightCalls != 2 {
		t.Fatalf("readiness with --skip-insights-check = %+v after %d reads", r, w.insightCalls)
	}
}

// The live check counts the kubelet skew of every nodegroup as it is
// now, and reads no insight for a hop that the skew blocks.
func TestHopReadiness_Skew(t *testing.T) {
	w := worldOf("1.32", "old", "1.29", "cur", "1.32")
	r := HopReadiness(w.snapshot(), ver("1.33"), false, w.upgradeInsights)
	if r.Blocker == nil || !strings.Contains(r.Blocker.Message, "nodegroup old at 1.29 would exceed the kubelet skew limit (3 minors) against 1.33") ||
		strings.Contains(r.Blocker.Message, "cur") {
		t.Fatalf("readiness = %+v, want the skew of old alone", r)
	}
	if w.insightCalls != 0 {
		t.Fatalf("the insights were read %d times for a hop that the skew blocks", w.insightCalls)
	}
}
