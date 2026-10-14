package change

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// The planners compare the cluster with the values of EKS.
func TestPlanners_EKSValues(t *testing.T) {
	for _, v := range []struct{ got, want string }{
		{updateVersion, string(ekstypes.UpdateTypeVersionUpdate)},
		{updateRollback, string(ekstypes.UpdateTypeVersionRollback)},
		{updateInProgress, string(ekstypes.UpdateStatusInProgress)},
		{updateSuccessful, string(ekstypes.UpdateStatusSuccessful)},
		{supportExtended, string(ekstypes.SupportTypeExtended)},
		{clusterActive, string(ekstypes.ClusterStatusActive)},
		{clusterUpdating, string(ekstypes.ClusterStatusUpdating)},
		{nodegroupActive, string(ekstypes.NodegroupStatusActive)},
		{nodegroupUpdating, string(ekstypes.NodegroupStatusUpdating)},
		{customAMIType, string(ekstypes.AMITypesCustom)},
		{insightPassing, string(ekstypes.InsightStatusValuePassing)},
		{insightWarning, string(ekstypes.InsightStatusValueWarning)},
		{insightError, string(ekstypes.InsightStatusValueError)},
	} {
		if v.got != v.want {
			t.Errorf("a planner has %q for the EKS value %q", v.got, v.want)
		}
	}
	if !strings.HasPrefix(string(ekstypes.AMITypesAl2X8664), al2AMIPrefix) || strings.HasPrefix(string(ekstypes.AMITypesAl2023X8664Standard), al2AMIPrefix) {
		t.Errorf("the prefix %q does not tell Amazon Linux 2 from Amazon Linux 2023", al2AMIPrefix)
	}
}

// versionUpdate returns a successful update of the control plane to a
// version, of a type of EKS, in the history of a cluster.
func versionUpdate(id, typ, to string, created time.Time) fleet.Update {
	return fleet.Update{ID: id, Type: typ, Status: "Successful", Version: ver(to), CreatedAt: created}
}

// newRollbackWorld returns a cluster on 1.33 that was upgraded in place
// two days ago, with the nodegroup ng-new at 1.33 and ng-old at 1.32,
// kube-proxy at a build that only 1.33 runs, and vpc-cni at a build that
// 1.32 lists.
func newRollbackWorld() *planWorld {
	w := worldOf("1.33", "ng-new", "1.33", "ng-old", "1.32")
	w.addons = map[string]string{"kube-proxy": latestFor("1.33"), "vpc-cni": latestFor("1.32")}
	w.strict = true
	w.updates = []fleet.Update{
		versionUpdate("u-old", "VersionUpdate", "1.32", planNow.Add(-40*24*time.Hour)),
		versionUpdate("u-up", "VersionUpdate", "1.33", planNow.Add(-48*time.Hour)),
	}
	return w
}

func (w *planWorld) rollbackInsights() InsightsRead {
	w.insightCalls++
	return InsightsRead{Insights: slices.Clone(w.insights), Err: w.insightsErr}
}

func (w *planWorld) rollbackInputs() RollbackInputs {
	return RollbackInputs{Now: w.now, Version: w.offered, AddonVersions: w.versions, Insights: w.rollbackInsights}
}

// rollback plans a rollback of the world and returns the plan of its
// cluster.
func (w *planWorld) rollback(t *testing.T, req RollbackRequest) *ClusterPlan {
	t.Helper()
	req.Target = Target{Region: east, Cluster: w.name}
	p, err := PlanRollback(w.snapshot(), req, w.rollbackInputs())
	if err != nil {
		t.Fatalf("PlanRollback: %v", err)
	}
	if p.Kind != ClusterRollback || len(p.Clusters) != 1 || !p.At.Equal(w.now) {
		t.Fatalf("plan = kind %s, %d clusters, at %s", p.Kind, len(p.Clusters), p.At)
	}
	checkRefs(t, &p.Clusters[0])
	return &p.Clusters[0]
}

// rollbackInsight returns a rollback readiness insight.
func rollbackInsight(name, status string) apidoc.Insight {
	return apidoc.Insight{ID: "id-" + name, Name: name, Category: "ROLLBACK_READINESS", Status: status}
}

// stepRow is what the rollback tests compare of a step.
type stepRow struct {
	Kind     StepKind
	Name     string
	Action   Action
	Reason   Reason
	From, To string
	Force    bool
}

func stepRows(cp *ClusterPlan) []stepRow {
	var out []stepRow
	for _, s := range cp.Steps() {
		out = append(out, stepRow{Kind: s.Ref.Kind, Name: s.Ref.Name, Action: s.Action, Reason: s.Reason, From: s.From, To: s.To, Force: s.Force})
	}
	return out
}

func TestRollbackPlan_Eligible(t *testing.T) {
	w := newRollbackWorld()
	cp := w.rollback(t, RollbackRequest{})

	if cp.From != ver("1.33") || cp.To != ver("1.32") || len(cp.Blockers) > 0 {
		t.Fatalf("plan %s to %s, blockers %q", cp.From, cp.To, blockerText(cp))
	}
	upgraded := planNow.Add(-48 * time.Hour)
	want := &fleet.Rollback{PreviousVersion: ver("1.32"), UpgradedAt: upgraded, AvailableUntil: upgraded.Add(fleet.RollbackWindow)}
	if diff := cmp.Diff(want, cp.Rollback); diff != "" {
		t.Errorf("rollback (-want +got):\n%s", diff)
	}
	// The plan agrees with the window that the read side shows.
	if open, ok := fleet.RollbackWindowOf(w.snapshot(), planNow); !ok || open != *cp.Rollback {
		t.Errorf("fleet.RollbackWindowOf = %+v, %v; the plan has %+v", open, ok, cp.Rollback)
	}
	steps := []stepRow{
		{Kind: StepNodegroup, Name: "ng-new", Action: Submit, Reason: VersionAhead, From: "1.33", To: "1.32"},
		{Kind: StepNodegroup, Name: "ng-old", Action: Skip, Reason: AlreadyAtTarget, From: "1.32", To: "1.32"},
		// The add-ons are in dependency order. The build of vpc-cni is
		// one that 1.32 runs already, and kube-proxy goes to the newest
		// build that 1.32 runs.
		{Kind: StepAddon, Name: "vpc-cni", Action: Skip, Reason: AlreadyAtTarget, From: latestFor("1.32"), To: latestFor("1.32")},
		{Kind: StepAddon, Name: "kube-proxy", Action: Submit, Reason: VersionAhead, From: latestFor("1.33"), To: latestFor("1.32")},
		{Kind: StepControlPlane, Action: Submit, Reason: VersionAhead, From: "1.33", To: "1.32"},
	}
	if diff := cmp.Diff(steps, stepRows(cp)); diff != "" {
		t.Errorf("steps (-want +got):\n%s", diff)
	}
	// The order: nodegroups, add-ons, control plane. No phase has a hop.
	var phases []string
	for _, ph := range cp.Phases {
		phases = append(phases, ph.Ref.String())
	}
	if want := []string{"prod-east/Nodegroups", "prod-east/Addons", "prod-east/ControlPlane"}; !slices.Equal(phases, want) {
		t.Errorf("phases = %v, want %v", phases, want)
	}
	if workSteps(cp) != 3 {
		t.Errorf("steps to run = %d, want 3", workSteps(cp))
	}
	if !slices.Equal(w.offerCalls, []string{"1.32"}) || w.insightCalls != 1 {
		t.Errorf("the planner asked about the versions %v, and %d times for the insights; want 1.32, and once", w.offerCalls, w.insightCalls)
	}
}

func TestRollbackPlan_Ineligible(t *testing.T) {
	tests := []struct {
		name  string
		setup func(w *planWorld)
		kind  BlockerKind
		want  string
	}{
		{
			"window closed", func(w *planWorld) { w.updates[1].CreatedAt = planNow.Add(-8 * 24 * time.Hour) },
			BlockerUnavailable, "the 7-day rollback window closed about 2026-09-24",
		},
		{
			"created at its version", func(w *planWorld) { w.updates = w.updates[:1] },
			BlockerUnavailable, "a cluster created at its version cannot roll back",
		},
		{
			"a failed upgrade does not count", func(w *planWorld) { w.updates[1].Status = "Failed" },
			BlockerUnavailable, "cannot roll back",
		},
		{
			"extended support without the policy", func(w *planWorld) {
				w.policy = "STANDARD"
				w.offer = func(fleet.Version) VersionOffer { return VersionOffer{Offered: true, Tier: apidoc.SupportExtended} }
			},
			BlockerUnavailable, "the cluster's upgrade policy is STANDARD: change it to EXTENDED first",
		},
		{
			"extended support with no policy", func(w *planWorld) {
				w.offer = func(fleet.Version) VersionOffer { return VersionOffer{Offered: true, Tier: apidoc.SupportExtended} }
			},
			BlockerUnavailable, "the cluster's upgrade policy is not set: change it to EXTENDED first",
		},
		{
			"unsupported target", func(w *planWorld) {
				w.offer = func(fleet.Version) VersionOffer { return VersionOffer{Offered: true, Tier: apidoc.SupportUnsupported} }
			},
			BlockerUnavailable, "1.32 is no longer supported by EKS",
		},
		{
			"a target that EKS does not list", func(w *planWorld) { w.offer = func(fleet.Version) VersionOffer { return VersionOffer{} } },
			BlockerUnavailable, "EKS does not offer 1.32",
		},
		{
			"update in progress", func(w *planWorld) {
				u := versionUpdate("u-cfg", "VersionUpdate", "1.34", planNow)
				u.Status = "InProgress"
				w.updates = append(w.updates, u)
			},
			BlockerBusy, "no update in progress",
		},
		{
			"a cluster that is not ACTIVE", func(w *planWorld) { w.status = "UPDATING" },
			BlockerBusy, "the cluster is UPDATING with an update in progress",
		},
		{
			// A rollback in progress to another version is not the
			// rollback of this plan. Resuming it would move the nodes and
			// the add-ons to the wrong version.
			"a rollback to another version is in progress", func(w *planWorld) {
				u := versionUpdate("u-rb", "VersionRollback", "1.31", planNow)
				u.Status = "InProgress"
				w.updates = append(w.updates, u)
			},
			BlockerBusy, "a rollback to 1.31 is in progress (update u-rb), not to 1.32",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newRollbackWorld()
			tt.setup(w)
			cp := w.rollback(t, RollbackRequest{})
			if len(cp.Blockers) != 1 || cp.Blockers[0].Kind != tt.kind || cp.Blockers[0].Step != nil || !strings.Contains(cp.Blockers[0].Message, tt.want) {
				t.Fatalf("blockers = %+v, want one %s blocker about the cluster with %q", cp.Blockers, tt.kind, tt.want)
			}
			if cp.Decision() != DecisionBlocked || cp.ExitCode() != 3 {
				t.Errorf("decision %s, exit %d; want Blocked, 3", cp.Decision(), cp.ExitCode())
			}
			// The plan still shows what the rollback would do.
			if workSteps(cp) == 0 {
				t.Errorf("the blocked plan has no step: %v", stepLines(cp))
			}
		})
	}
}

func TestRollbackPlan_ExtendedSupportWithPolicy(t *testing.T) {
	w := newRollbackWorld()
	w.policy = "EXTENDED"
	w.offer = func(fleet.Version) VersionOffer { return VersionOffer{Offered: true, Tier: apidoc.SupportExtended} }
	cp := w.rollback(t, RollbackRequest{})
	if len(cp.Blockers) > 0 {
		t.Fatalf("blocked: %s", blockerText(cp))
	}
	if !strings.Contains(strings.Join(cp.Notices, "\n"), "extended support charges apply") {
		t.Errorf("notices = %v", cp.Notices)
	}
}

func TestRollbackPlan_Insights(t *testing.T) {
	tests := []struct {
		name    string
		status  string
		skip    bool
		blocked bool
		notice  string
	}{
		{"error blocks", insightError, false, true, ""},
		{"unknown blocks", "UNKNOWN", false, true, ""},
		{"warning is advisory", insightWarning, false, false, "rollback insight warnings: API usage"},
		{"error with --skip-insights-check", insightError, true, false, "bypassed with --skip-insights-check: API usage (ERROR)"},
		{"unknown with --skip-insights-check", "UNKNOWN", true, false, "bypassed with --skip-insights-check: API usage (UNKNOWN)"},
		{"warning with --skip-insights-check", insightWarning, true, false, "rollback insight warnings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newRollbackWorld()
			w.insights = []apidoc.Insight{rollbackInsight("API usage", tt.status), rollbackInsight("Kubelet skew", insightPassing)}
			cp := w.rollback(t, RollbackRequest{SkipInsights: tt.skip})
			if blocked := len(cp.Blockers) > 0; blocked != tt.blocked {
				t.Fatalf("blockers = %q, want blocked=%v", blockerText(cp), tt.blocked)
			}
			if tt.blocked {
				if b := blockerFor(t, cp, StepControlPlane, ""); b.Kind != BlockerNotReady || !strings.Contains(b.Message, "API usage") || !strings.Contains(b.Message, "--skip-insights-check") {
					t.Errorf("blocker = %+v", b)
				}
			}
			if tt.notice != "" && !strings.Contains(strings.Join(cp.Notices, "\n"), tt.notice) {
				t.Errorf("notices = %v, want %q", cp.Notices, tt.notice)
			}
			// --skip-insights-check sets force on the control-plane
			// request, so EKS skips its own insight checks too.
			if s := findStep(t, cp, "", StepControlPlane, ""); s.Force != tt.skip {
				t.Errorf("control-plane step has force %v, want %v", s.Force, tt.skip)
			}
		})
	}
}

func TestRollbackPlan_AutoModeNotice(t *testing.T) {
	w := newRollbackWorld()
	w.autoMode = true
	cp := w.rollback(t, RollbackRequest{})
	if !strings.Contains(strings.Join(cp.Notices, "\n"), "EKS Auto Mode: EKS rolls back the Auto Mode nodes itself") {
		t.Errorf("notices = %v", cp.Notices)
	}
}

// A skipped nodegroup and one with a custom AMI are manual steps.
//
// The old test skipped ng-new with the pattern "new". A name is exact
// now, and one that matches nothing is an error: CONTRACT.md, change 3,
// and "Targets".
func TestRollbackPlan_SkippedAndCustomNodegroupsAreManual(t *testing.T) {
	w := newRollbackWorld()
	w.nodegroups = append(w.nodegroups, &planNodegroup{name: "custom", version: "1.33", amiType: "CUSTOM"})
	cp := w.rollback(t, RollbackRequest{SkipNodegroups: []string{"ng-new"}})
	if s := findStep(t, cp, "", StepNodegroup, "ng-new"); s.Action != Manual || s.Reason != SkippedByFlag {
		t.Errorf("ng-new = %s %s, want Manual SkippedByFlag", s.Action, s.Reason)
	}
	if s := findStep(t, cp, "", StepNodegroup, "custom"); s.Action != Manual || s.Reason != CustomAMI {
		t.Errorf("custom = %s %s, want Manual CustomAMI", s.Action, s.Reason)
	}
	notices := strings.Join(cp.Notices, "\n")
	for _, want := range []string{
		"nodegroup ng-new is skipped: move it to 1.32 yourself; the control plane does not roll back while it is at 1.33",
		"nodegroup custom has a custom AMI: roll it to a 1.32 AMI yourself",
	} {
		if !strings.Contains(notices, want) {
			t.Errorf("notices = %q, want %q", notices, want)
		}
	}

	_, err := PlanRollback(w.snapshot(), RollbackRequest{SkipNodegroups: []string{"new"}}, w.rollbackInputs())
	var notFound *NotFoundError
	if !errors.As(err, &notFound) || notFound.Name != "new" || !slices.Equal(notFound.Names, []string{"custom", "ng-new", "ng-old"}) {
		t.Errorf("a part of a name: err = %v, want a NotFoundError with the names of the cluster", err)
	}
}

// The run changes the cluster in the documented order, and the
// control-plane step carries force only with --skip-insights-check.
func TestRollbackPlan_OrderAndRequest(t *testing.T) {
	for _, skip := range []bool{true, false} {
		w := newRollbackWorld()
		cp := w.rollback(t, RollbackRequest{SkipInsights: skip, RollbackTimeout: 3 * time.Hour})
		if s := findStep(t, cp, "", StepControlPlane, ""); s.Force != skip {
			t.Errorf("--skip-insights-check %v: the control-plane step has force %v", skip, s.Force)
		}
		for _, s := range cp.Steps() {
			if s.Ref.Kind != StepControlPlane && s.Force {
				t.Errorf("step %s has force without --force", s.Ref)
			}
		}
		events, _ := w.apply(cp, nil)
		want := []string{"ng ng-new→1.32", "addon kube-proxy→" + latestFor("1.32"), "cp→1.32"}
		if !slices.Equal(events, want) {
			t.Errorf("changes = %v, want %v", events, want)
		}
	}
	// --force goes with the nodegroup rolls.
	req := RollbackRequest{}
	req.Policy.Force = true
	cp := newRollbackWorld().rollback(t, req)
	if s := findStep(t, cp, "", StepNodegroup, "ng-new"); !s.Force {
		t.Error("the roll of ng-new has no force with --force")
	}
	if s := findStep(t, cp, "", StepControlPlane, ""); s.Force {
		t.Error("the control-plane step has force with --force alone")
	}
}

// A plan after each phase is derived from live state and has only what
// remains, and a plan after the rollback finished has nothing to do.
func TestRollbackPlan_ResumeAfterEachPhase(t *testing.T) {
	tests := []struct {
		name  string
		setup func(w *planWorld)
		want  []string
	}{
		{"after the nodegroups", func(w *planWorld) { w.nodegroup("ng-new").version = "1.32" }, []string{"addon kube-proxy→" + latestFor("1.32"), "cp→1.32"}},
		{"after the add-ons", func(w *planWorld) {
			w.nodegroup("ng-new").version = "1.32"
			w.addons["kube-proxy"] = latestFor("1.32")
		}, []string{"cp→1.32"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newRollbackWorld()
			tt.setup(w)
			if events, _ := w.apply(w.rollback(t, RollbackRequest{}), nil); !slices.Equal(events, tt.want) {
				t.Fatalf("changes = %v, want %v", events, tt.want)
			}
		})
	}

	t.Run("after the control plane", func(t *testing.T) {
		w := newRollbackWorld()
		w.apply(w.rollback(t, RollbackRequest{}), nil)
		w.updates = append(w.updates, versionUpdate("u-cp", "VersionRollback", "1.32", planNow))
		w.offerCalls, w.insightCalls = nil, 0

		cp := w.rollback(t, RollbackRequest{})
		if cp.From != ver("1.32") || cp.To != ver("1.32") || workSteps(cp) != 0 || len(cp.Blockers) != 0 {
			t.Fatalf("second plan = %s to %s, steps %v, blockers %q; want nothing to do at 1.32", cp.From, cp.To, stepLines(cp), blockerText(cp))
		}
		for _, s := range cp.Steps() {
			if s.Ref.Kind == StepControlPlane {
				t.Fatalf("the second plan has the control-plane step %+v", s)
			}
		}
		if n := strings.Join(cp.Notices, "\n"); !strings.Contains(n, "the control plane rolled back from 1.33 to 1.32 on about 2026-09-25 (update u-cp)") {
			t.Errorf("notices = %q, want the finished rollback named", n)
		}
		if cp.Decision() != DecisionNothingToDo || cp.Rollback != nil {
			t.Errorf("decision %s, rollback %+v; want NothingToDo and no window", cp.Decision(), cp.Rollback)
		}
		// Nothing is checked again for a rollback that is done.
		if len(w.offerCalls) != 0 || w.insightCalls != 0 {
			t.Errorf("the planner asked about the versions %v, and %d times for the insights", w.offerCalls, w.insightCalls)
		}
	})

	// Work that is left at the version of a finished rollback is still
	// planned.
	t.Run("after the control plane, with a nodegroup behind", func(t *testing.T) {
		w := newRollbackWorld()
		w.version = "1.32"
		w.updates = append(w.updates, versionUpdate("u-cp", "VersionRollback", "1.32", planNow))
		cp := w.rollback(t, RollbackRequest{})
		want := []string{"ng ng-new→1.32", "addon kube-proxy→" + latestFor("1.32")}
		if events, _ := w.apply(cp, nil); !slices.Equal(events, want) {
			t.Fatalf("changes = %v, want %v", events, want)
		}
	})
}

// The rollback to the target is in progress: the plan is eligible with
// no check, and its control-plane step attaches and sends nothing.
func TestRollbackPlan_AttachesToItsOwnRollback(t *testing.T) {
	w := newRollbackWorld()
	w.status = "UPDATING"
	u := versionUpdate("u-rb", "VersionRollback", "1.32", planNow)
	u.Status = "InProgress"
	w.updates = append(w.updates, u)
	cp := w.rollback(t, RollbackRequest{SkipInsights: true})
	if len(cp.Blockers) > 0 {
		t.Fatalf("blockers = %q", blockerText(cp))
	}
	if s := findStep(t, cp, "", StepControlPlane, ""); s.Action != Attach || s.Reason != InFlight || s.Force {
		t.Fatalf("control-plane step = %+v, want Attach InFlight with no force", s)
	}
	if len(w.offerCalls) != 0 || w.insightCalls != 0 {
		t.Errorf("the planner asked about the versions %v, and %d times for the insights; want nothing", w.offerCalls, w.insightCalls)
	}
	// The busy rule takes the cluster that is updating as the step that
	// attaches.
	if refuse, _ := Busy(w.snapshot(), ClusterRollback, cp.Steps()); len(refuse) != 0 {
		t.Errorf("the busy rule refused the plan: %+v", refuse)
	}
}

func TestRollbackPlan_BlockedPlanRefuses(t *testing.T) {
	w := newRollbackWorld()
	w.updates = nil
	cp := w.rollback(t, RollbackRequest{})
	if cp.Decision() != DecisionBlocked || cp.ExitCode() != 3 {
		t.Fatalf("decision %s, exit %d; want Blocked, 3", cp.Decision(), cp.ExitCode())
	}
	if cp.Rollback != nil {
		t.Errorf("rollback = %+v, want none: no upgrade was found", cp.Rollback)
	}
}

// The planner checks the timeout before it reads anything.
func TestRollbackPlan_TimeoutIsCheckedFirst(t *testing.T) {
	w := newRollbackWorld()
	_, err := PlanRollback(w.snapshot(), RollbackRequest{RollbackTimeout: time.Hour}, w.rollbackInputs())
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Reason != RefusalRollbackTimeout {
		t.Errorf("PlanRollback with a timeout of 1h: err = %v", err)
	}
	if len(w.offerCalls)+len(w.catalogueCalls)+w.insightCalls != 0 {
		t.Errorf("the planner read before it refused the timeout")
	}
}

// EKS counts the 7-day window from the end of the upgrade, and an update
// records only its start. The planner blocks only once even a slow update
// would have closed the window, and leaves the hours before that to EKS,
// with a notice.
func TestRollbackPlan_WindowSlack(t *testing.T) {
	tests := []struct {
		name    string
		age     time.Duration
		blocked bool
		notice  bool
	}{
		{"within the window", 7*24*time.Hour - time.Second, false, false},
		{"exactly 7 days from the start", 7 * 24 * time.Hour, false, true},
		{"just past 7 days from the start", 7*24*time.Hour + time.Hour, false, true},
		{"2 hours past", 7*24*time.Hour + 2*time.Hour, true, false},
		{"well past", 7*24*time.Hour + 3*time.Hour, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newRollbackWorld()
			w.updates[1].CreatedAt = planNow.Add(-tt.age)
			cp := w.rollback(t, RollbackRequest{})
			if blocked := len(cp.Blockers) > 0; blocked != tt.blocked {
				t.Fatalf("blockers = %q, want blocked=%v", blockerText(cp), tt.blocked)
			}
			if notice := strings.Contains(strings.Join(cp.Notices, "; "), "may have just closed"); notice != tt.notice {
				t.Errorf("notices = %v, want the notice=%v", cp.Notices, tt.notice)
			}
			// The upgrade was found in every case, so the plan has it.
			if cp.Rollback == nil || !cp.Rollback.UpgradedAt.Equal(planNow.Add(-tt.age)) {
				t.Errorf("rollback = %+v, want the upgrade", cp.Rollback)
			}
		})
	}
}

// A plan that a person confirmed a while ago is computed again before
// the first change. Here the window closed in between: the second plan is
// blocked.
func TestRollbackPlan_SecondPlanAfterTheWindowClosed(t *testing.T) {
	w := newRollbackWorld()
	req := RollbackRequest{SkipInsights: true}
	if cp := w.rollback(t, req); len(cp.Blockers) > 0 {
		t.Fatalf("the first plan is blocked: %s", blockerText(cp))
	}
	w.now = planNow.Add(6 * 24 * time.Hour) // 8 days after the upgrade
	cp := w.rollback(t, req)
	if len(cp.Blockers) != 1 || cp.Blockers[0].Kind != BlockerUnavailable || cp.Decision() != DecisionBlocked {
		t.Fatalf("second plan: blockers %+v, decision %s; want blocked by the closed window", cp.Blockers, cp.Decision())
	}
}

// A check that the rollback depends on and that cannot be read blocks
// the plan: unchecked, the run could roll nodegroups and add-ons back
// before EKS refuses the control plane.
func TestRollbackPlan_UnreadableChecksBlock(t *testing.T) {
	t.Run("insights", func(t *testing.T) {
		w := newRollbackWorld()
		w.insightsErr = accessDenied()
		cp := w.rollback(t, RollbackRequest{})
		if b := blockerFor(t, cp, StepControlPlane, ""); b.Kind != BlockerUnread || !strings.Contains(b.Message, "--skip-insights-check") {
			t.Errorf("blocker = %+v, want Unread, naming --skip-insights-check", b)
		}
		if cp.ExitCode() != 4 {
			t.Errorf("exit code = %d, want 4", cp.ExitCode())
		}
		cp = w.rollback(t, RollbackRequest{SkipInsights: true})
		if len(cp.Blockers) > 0 {
			t.Errorf("blockers with --skip-insights-check = %q, want none", blockerText(cp))
		}
		if len(cp.Failures) != 1 || !strings.Contains(strings.Join(cp.Notices, "\n"), "could not read the rollback readiness insights; --skip-insights-check") {
			t.Errorf("failures %+v, notices %v; want the read that failed, and a notice", cp.Failures, cp.Notices)
		}
	})
	t.Run("support status", func(t *testing.T) {
		w := newRollbackWorld()
		w.offer = func(fleet.Version) VersionOffer { return VersionOffer{Err: accessDenied()} }
		cp := w.rollback(t, RollbackRequest{SkipInsights: true})
		if len(cp.Blockers) != 1 || cp.Blockers[0].Kind != BlockerUnread || !strings.Contains(cp.Blockers[0].Message, "could not check that EKS still supports 1.32") {
			t.Errorf("blockers = %+v, want one Unread blocker", cp.Blockers)
		}
		if len(cp.Failures) != 1 || cp.Failures[0].Reason != apidoc.ReasonAccessDenied {
			t.Errorf("failures = %+v", cp.Failures)
		}
	})
	t.Run("update history", func(t *testing.T) {
		w := newRollbackWorld()
		w.noHistory = true
		cp := w.rollback(t, RollbackRequest{})
		if len(cp.Blockers) != 1 || cp.Blockers[0].Kind != BlockerUnread || !strings.Contains(cp.Blockers[0].Message, "could not read the update history of prod-east") {
			t.Errorf("blockers = %+v, want one Unread blocker", cp.Blockers)
		}
		if cp.ExitCode() != 4 {
			t.Errorf("exit code = %d, want 4", cp.ExitCode())
		}
	})
	t.Run("an add-on catalogue", func(t *testing.T) {
		w := newRollbackWorld()
		w.catalogue = func(addon string, k fleet.Version) ([]string, error) {
			if addon == "kube-proxy" {
				return nil, accessDenied()
			}
			return w.builds(addon, k), nil
		}
		cp := w.rollback(t, RollbackRequest{})
		if s := findStep(t, cp, "", StepAddon, "kube-proxy"); s.Action != Blocked {
			t.Errorf("kube-proxy = %s, want Blocked", s.Action)
		}
		if b := blockerFor(t, cp, StepAddon, "kube-proxy"); b.Kind != BlockerUnread || !strings.Contains(b.Message, "could not look up versions of kube-proxy compatible with 1.32") {
			t.Errorf("blocker = %+v", b)
		}
	})
	t.Run("an add-on with no version for the target", func(t *testing.T) {
		w := newRollbackWorld()
		w.catalogue = anyVersion(map[string][]string{"vpc-cni": {latestFor("1.32")}})
		cp := w.rollback(t, RollbackRequest{})
		if b := blockerFor(t, cp, StepAddon, "kube-proxy"); b.Kind != BlockerUnavailable || !strings.Contains(b.Message, "no version of kube-proxy is compatible with 1.32") {
			t.Errorf("blocker = %+v", b)
		}
		if len(cp.Failures) != 0 {
			t.Errorf("failures = %+v, want none", cp.Failures)
		}
	})
}

// A nodegroup that the run does not roll back, and that is still above
// the target, stops the run before the control plane: nodes newer than
// the control plane break the version skew policy. The plan itself is not
// blocked, because the add-ons can still move.
func TestRollbackControlPlaneBlockers(t *testing.T) {
	stopAtControlPlane := func(w *planWorld) func(Step) bool {
		return func(s Step) bool {
			return s.Ref.Kind == StepControlPlane && len(RollbackControlPlaneBlockers(w.snapshot(), ver("1.32"))) > 0
		}
	}
	req := RollbackRequest{SkipNodegroups: []string{"ng-new"}, SkipInsights: true}

	t.Run("after the add-ons", func(t *testing.T) {
		w := newRollbackWorld()
		cp := w.rollback(t, req)
		if len(cp.Blockers) > 0 {
			t.Fatalf("the plan is blocked: %s", blockerText(cp))
		}
		events, ok := w.apply(cp, stopAtControlPlane(w))
		if ok || !slices.Equal(events, []string{"addon kube-proxy→" + latestFor("1.32")}) || w.version != "1.33" {
			t.Fatalf("changes = %v, control plane at %s; want only the add-on", events, w.version)
		}
		blockers := RollbackControlPlaneBlockers(w.snapshot(), ver("1.32"))
		if len(blockers) != 1 || blockers[0].Kind != BlockerNotReady || !strings.Contains(blockers[0].Message, "ng-new at 1.33") {
			t.Fatalf("blockers = %+v, want one that names ng-new", blockers)
		}
		if ref := blockers[0].Step; ref == nil || ref.Kind != StepControlPlane || ref.Phase.Kind != PhaseControlPlane || !ref.Phase.Hop.IsZero() {
			t.Errorf("blocker step = %+v, want the control-plane step of the rollback", ref)
		}
	})
	// With no nodegroup and no add-on work before it, the stop changed
	// nothing.
	t.Run("with nothing changed", func(t *testing.T) {
		w := newRollbackWorld()
		w.addons["kube-proxy"] = latestFor("1.32")
		events, ok := w.apply(w.rollback(t, req), stopAtControlPlane(w))
		if ok || len(events) != 0 {
			t.Fatalf("changes = %v, want none", events)
		}
	})
	t.Run("every nodegroup is at the target or below", func(t *testing.T) {
		w := newRollbackWorld()
		w.nodegroup("ng-new").version = "1.31"
		if blockers := RollbackControlPlaneBlockers(w.snapshot(), ver("1.32")); len(blockers) != 0 {
			t.Fatalf("blockers = %+v, want none", blockers)
		}
	})
	// The old run ended with a plain error, exit 1, when the nodegroups
	// could not be read before the control plane. It is an Unread blocker
	// now, exit 4: CONTRACT.md, "Behaviour the new CLI does not copy", "A
	// gate that refuses exits 3. A gate that cannot read exits 4."
	t.Run("the nodegroups could not be read", func(t *testing.T) {
		w := newRollbackWorld()
		w.nodegroup("ng-new").unread = true
		blockers := RollbackControlPlaneBlockers(w.snapshot(), ver("1.32"))
		if len(blockers) != 1 || blockers[0].Kind != BlockerUnread {
			t.Fatalf("blockers = %+v, want one Unread blocker", blockers)
		}
	})
}

// The run executes what was confirmed. A nodegroup that appears after
// the plan, or an add-on whose installed version changed, is a difference
// that Diff reports between the confirmed plan and the second one.
//
// The old run refused both. The run now asks about them, and --yes
// accepts: docs/design/phase-4-changes.md, section 6, "cluster rollback
// refused any changed step: it asks, by the table of section 1; a changed
// target still refuses".
func TestRollbackPlan_SecondPlanShowsWhatChanged(t *testing.T) {
	plans := func(edit func(w *planWorld)) (reviewed, fresh *Plan) {
		w := newRollbackWorld()
		req := RollbackRequest{Target: Target{Region: east, Cluster: w.name}, SkipInsights: true}
		reviewed, err := PlanRollback(w.snapshot(), req, w.rollbackInputs())
		if err != nil {
			t.Fatal(err)
		}
		edit(w)
		fresh, err = PlanRollback(w.snapshot(), req, w.rollbackInputs())
		if err != nil {
			t.Fatal(err)
		}
		return reviewed, fresh
	}
	t.Run("a new nodegroup", func(t *testing.T) {
		reviewed, fresh := plans(func(w *planWorld) {
			w.nodegroups = append(w.nodegroups, &planNodegroup{name: "ng-late", version: "1.33"})
		})
		changes := Diff(reviewed, fresh)
		if len(changes) != 1 || changes[0].Kind != Added || changes[0].Step.Name != "ng-late" || changes[0].Effect() != AskAgain {
			t.Fatalf("changes = %+v, want ng-late added, with a question", changes)
		}
		if err := RollbackTargetChanged(reviewed, fresh); err != nil {
			t.Errorf("RollbackTargetChanged = %v, want nil: the target is the same", err)
		}
	})
	t.Run("another installed add-on version", func(t *testing.T) {
		reviewed, fresh := plans(func(w *planWorld) { w.addons["kube-proxy"] = "v1.33.9-eksbuild.9" })
		changes := Diff(reviewed, fresh)
		if len(changes) != 1 || changes[0].Kind != StepDiffers || !strings.Contains(changes[0].Message, "v1.33.9-eksbuild.9") {
			t.Fatalf("changes = %+v, want the new version of kube-proxy named", changes)
		}
	})
	t.Run("the same cluster", func(t *testing.T) {
		reviewed, fresh := plans(func(*planWorld) {})
		if changes := Diff(reviewed, fresh); len(changes) != 0 {
			t.Fatalf("changes = %+v, want none", changes)
		}
	})
	// The cluster was rolled back and upgraded again while the question
	// was open: the second plan returns to another version. Nothing
	// accepts that.
	t.Run("another target", func(t *testing.T) {
		reviewed, fresh := plans(func(w *planWorld) {
			w.version = "1.34"
			w.updates = append(w.updates, versionUpdate("u-again", "VersionUpdate", "1.34", planNow))
		})
		err := RollbackTargetChanged(reviewed, fresh)
		var refusal *RefusalError
		if !errors.As(err, &refusal) || refusal.Reason != RefusalTargetChanged || !strings.Contains(err.Error(), "changed from 1.32 to 1.33") {
			t.Fatalf("err = %v, want a refusal that names both targets", err)
		}
	})
}

// The rollback planner follows the same rule as the upgrade planner: a
// nodegroup at the target that is DEGRADED is not done. The plan keeps
// its step as a verification, beside the roll of the nodegroup above the
// target.
func TestRollbackPlan_DegradedNodegroupAtTargetIsVerified(t *testing.T) {
	w := newRollbackWorld()
	w.nodegroup("ng-old").status = "DEGRADED"
	cp := w.rollback(t, RollbackRequest{SkipInsights: true})
	if s := findStep(t, cp, "", StepNodegroup, "ng-old"); s.Action != Verify || s.Reason != Unsettled {
		t.Fatalf("ng-old = %s %s, want Verify Unsettled", s.Action, s.Reason)
	}
	if s := findStep(t, cp, "", StepNodegroup, "ng-new"); s.Action != Submit {
		t.Fatalf("ng-new = %s, want Submit", s.Action)
	}
	// A nodegroup above the target that is rolling already is attached
	// to.
	w.nodegroup("ng-new").status = "UPDATING"
	cp = w.rollback(t, RollbackRequest{SkipInsights: true})
	if s := findStep(t, cp, "", StepNodegroup, "ng-new"); s.Action != Attach || s.Reason != InFlight {
		t.Fatalf("ng-new while it rolls = %s %s, want Attach InFlight", s.Action, s.Reason)
	}
}

func TestRollbackPlan_Refusals(t *testing.T) {
	w := worldOf("1.0")
	_, err := PlanRollback(w.snapshot(), RollbackRequest{}, w.rollbackInputs())
	var refusal *RefusalError
	if !errors.As(err, &refusal) || refusal.Reason != RefusalNoPreviousVersion {
		t.Errorf("a cluster on the first minor version: err = %v", err)
	}
	snap := newRollbackWorld().snapshot()
	snap.Have.Cluster = fleet.Failed
	if _, err := PlanRollback(snap, RollbackRequest{}, w.rollbackInputs()); !errors.As(err, &refusal) || refusal.Reason != RefusalVersionUnknown {
		t.Errorf("a cluster that was not read: err = %v", err)
	}
}
