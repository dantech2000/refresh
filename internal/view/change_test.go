package view

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// A plan of two clusters: each section names its cluster, a roll shows
// what its nodes run and the AMI that it moves to, a scale shows its sizes, a busy target shows the update
// in flight, and a blocker names its step.
func TestPlanView_Plain(t *testing.T) {
	doc := apidoc.Plan{
		Operation: apidoc.OperationNodegroupUpdate,
		Clusters: []apidoc.ClusterPlan{
			{
				Cluster: "prod", Region: "us-east-1", Version: "1.33", TargetVersion: "1.34", Decision: apidoc.PlanBlocked,
				Steps: []apidoc.PlanStep{
					{Phase: apidoc.PhaseControlPlane, Kind: apidoc.StepControlPlane, Action: apidoc.ActionSubmit, From: "1.33", To: "1.34"},
					{Phase: apidoc.PhaseRoll, Kind: apidoc.StepNodegroup, Name: "web", Action: apidoc.ActionSubmit, Reason: apidoc.ReasonAMIOutdated, From: "1.33", To: "1.33", FromRelease: "1.33.0-20250101", FromImage: "ami-old", ToImage: "ami-new"},
					{Phase: apidoc.PhaseRoll, Kind: apidoc.StepNodegroup, Name: "batch", Action: apidoc.ActionSkip, Reason: apidoc.ReasonInFlight, From: "1.33", To: "1.33", InFlight: "ConfigUpdate"},
				},
				Blockers: []apidoc.Blocker{
					{Kind: apidoc.BlockerBusy, Message: "prod is busy"},
					{Kind: apidoc.BlockerUnread, Step: &apidoc.StepID{Phase: apidoc.PhaseRoll, Kind: apidoc.StepNodegroup, Name: "web"}, Message: "could not read web"},
				},
				Gate: &apidoc.Gate{
					Decision: apidoc.GateBlock, Reasons: []string{"Nodes: 1/3 nodes ready"}, Forced: []string{"DrainBlockers: a PDB allows no eviction"},
					Checks: []apidoc.HealthResult{{Name: "Nodes", Outcome: apidoc.HealthFail, Message: "1/3 nodes ready"}},
				},
				Notices: []string{"first", "second"},
			},
			{
				Cluster: "dev", Region: "us-east-1", Decision: apidoc.PlanAsk,
				Steps: []apidoc.PlanStep{{
					Phase: apidoc.PhaseScale, Kind: apidoc.StepScale, Name: "web", Action: apidoc.ActionSubmit,
					Sizes: &apidoc.ScaleSizes{Before: apidoc.Sizes{Desired: 2, Min: 1, Max: 3}, After: apidoc.Sizes{Desired: 3, Min: 1, Max: 3}},
				}},
				Gate: &apidoc.Gate{Decision: apidoc.GateAsk, Accepted: []string{"Quotas: 91% in use"}},
			},
		},
		SkippedRegions: []string{"ap-east-1", "me-south-1"},
	}
	v := PlanView(doc)
	if v.Label != "Blocked" || v.Status != Fail {
		t.Errorf("head = %s (%d), want the worst decision: Blocked", v.Label, v.Status)
	}
	var b strings.Builder
	if err := v.Plain(&b); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"FIELD\tVALUE",
		"plan_prod.operation\tNodegroupUpdate",
		"plan_prod.cluster\tprod",
		"plan_prod.region\tus-east-1",
		"plan_prod.version\t1.33",
		"plan_prod.target_version\t1.34",
		"plan_prod.decision\tBlocked",
		"steps_prod.control-plane.kind\tControlPlane",
		"steps_prod.control-plane.action\tSubmit",
		"steps_prod.control-plane.reason\t-",
		"steps_prod.control-plane.from\t1.33",
		"steps_prod.control-plane.to\t1.34",
		"steps_prod.control-plane.note\t-",
		"steps_prod.web.kind\tNodegroup",
		"steps_prod.web.action\tSubmit",
		"steps_prod.web.reason\tAMIOutdated",
		"steps_prod.web.from\t1.33 (release 1.33.0-20250101, AMI ami-old)",
		"steps_prod.web.to\t1.33 (AMI ami-new)",
		"steps_prod.web.note\t-",
		"steps_prod.batch.kind\tNodegroup",
		"steps_prod.batch.action\tSkip",
		"steps_prod.batch.reason\tInFlight",
		"steps_prod.batch.from\t1.33",
		"steps_prod.batch.to\t1.33",
		"steps_prod.batch.note\tConfigUpdate in flight",
		"blockers_prod.1.kind\tBusy",
		"blockers_prod.1.step\t-",
		"blockers_prod.1.message\tprod is busy",
		"blockers_prod.2.kind\tUnread",
		"blockers_prod.2.step\tweb",
		"blockers_prod.2.message\tcould not read web",
		"health_gate_prod.decision\tBlock",
		"health_gate_prod.reasons\tNodes: 1/3 nodes ready",
		"health_gate_prod.forced\tDrainBlockers: a PDB allows no eviction",
		"checks_prod.Nodes.outcome\tFail",
		"checks_prod.Nodes.message\t1/3 nodes ready",
		"notices_prod.1\tfirst",
		"notices_prod.2\tsecond",
		"plan_dev.operation\tNodegroupUpdate",
		"plan_dev.cluster\tdev",
		"plan_dev.region\tus-east-1",
		"plan_dev.decision\tAsk",
		"steps_dev.web.kind\tScale",
		"steps_dev.web.action\tSubmit",
		"steps_dev.web.reason\t-",
		"steps_dev.web.from\tdesired 2, min 1, max 3",
		"steps_dev.web.to\tdesired 3, min 1, max 3",
		"steps_dev.web.note\t-",
		"health_gate_dev.decision\tAsk",
		"health_gate_dev.accepted\tQuotas: 91% in use",
		"skipped_regions.regions\tap-east-1, me-south-1",
		"",
	}, "\n")
	if diff := cmp.Diff(want, b.String()); diff != "" {
		t.Errorf("plain (-want +got):\n%s", diff)
	}
}

func TestRunView_Results(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 30, 0, time.UTC)
	doc := apidoc.Run{
		Operation: apidoc.OperationAddonUpdate, Status: apidoc.RunIncomplete,
		Clusters: []apidoc.ClusterRun{{
			ClusterPlan: apidoc.ClusterPlan{Cluster: "prod", Region: "us-east-1", Steps: []apidoc.PlanStep{
				{Kind: apidoc.StepAddon, Name: "vpc-cni", From: "v1", To: "v2"}, {Kind: apidoc.StepAddon, Name: "coredns", From: "v3", To: "v3"},
			}},
			Status: apidoc.RunIncomplete,
			Results: []apidoc.StepResult{
				{Kind: apidoc.StepAddon, Name: "vpc-cni", Status: apidoc.StepBusy, InFlight: "AddonUpdate", Failure: &apidoc.Failure{Reason: apidoc.ReasonBusy, Error: "busy"}},
				{Kind: apidoc.StepAddon, Name: "coredns", Status: apidoc.StepSucceeded, Attached: true, StartedAt: at, FinishedAt: at},
			},
		}},
	}
	v := RunView(doc)
	if v.Label != "Incomplete" || v.Status != Warn {
		t.Errorf("head = %s (%d)", v.Label, v.Status)
	}
	var b strings.Builder
	if err := v.Plain(&b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"results.vpc-cni.status\tBusy", "results.vpc-cni.to\tv2", "results.vpc-cni.note\tAddonUpdate in flight", "results.vpc-cni.failure\tBusy: busy",
		"results.coredns.note\tattached", "results.coredns.started\t2026-01-01 00:00:30 UTC",
	} {
		if !strings.Contains(b.String(), want+"\n") {
			t.Errorf("plain has no row %q:\n%s", want, b.String())
		}
	}
}

// Every status of a document has a glyph of its own meaning.
func TestChangeStatuses(t *testing.T) {
	for d, want := range map[apidoc.PlanDecision]Status{
		apidoc.PlanProceed: Healthy, apidoc.PlanAsk: Warn, apidoc.PlanBlocked: Fail, apidoc.PlanNothingToDo: Neutral,
	} {
		if got := DecisionStatus(d); got != want {
			t.Errorf("DecisionStatus(%s) = %d, want %d", d, got, want)
		}
	}
	for s, want := range map[apidoc.RunStatus]Status{
		apidoc.RunSucceeded: Healthy, apidoc.RunNothingToDo: Healthy, apidoc.RunStarted: Progress,
		apidoc.RunRefused: Fail, apidoc.RunBlocked: Fail, apidoc.RunFailed: Fail, apidoc.RunVerifyFailed: Fail,
		apidoc.RunDeclined: Warn, apidoc.RunStopped: Warn, apidoc.RunIncomplete: Warn, apidoc.RunInterrupted: Warn,
	} {
		if got := RunStatus(s); got != want {
			t.Errorf("RunStatus(%s) = %d, want %d", s, got, want)
		}
	}
	for s, want := range map[apidoc.StepStatus]Status{
		apidoc.StepSucceeded: Healthy, apidoc.StepStarted: Progress, apidoc.StepInProgress: Progress,
		apidoc.StepFailed: Fail, apidoc.StepCancelled: Fail, apidoc.StepBlocked: Fail,
		apidoc.StepBusy: Warn, apidoc.StepSkipped: Neutral, apidoc.StepNotAttempted: Unknown,
	} {
		if got := StepStatus(s); got != want {
			t.Errorf("StepStatus(%s) = %d, want %d", s, got, want)
		}
	}
}

// The lines of a plan before a question, and the line of a step.
func TestPlanLinesAndStepLine(t *testing.T) {
	doc := apidoc.Plan{Operation: apidoc.OperationAddonUpdate, Clusters: []apidoc.ClusterPlan{{
		Cluster: "prod", Region: "us-east-1",
		Steps: []apidoc.PlanStep{
			{Kind: apidoc.StepAddon, Name: "vpc-cni", Action: apidoc.ActionSubmit, Reason: apidoc.ReasonVersionBehind, From: "v1", To: "v2"},
			{Kind: apidoc.StepAddon, Name: "coredns", Action: apidoc.ActionSubmit, Reason: apidoc.ReasonRepair, From: "v3", To: "v3"},
			{Kind: apidoc.StepAddon, Name: "adot", Action: apidoc.ActionAttach, Reason: apidoc.ReasonInFlight, From: "v5", To: "v5"},
			{Kind: apidoc.StepNodegroup, Name: "web", Action: apidoc.ActionVerify, Reason: apidoc.ReasonUnsettled, To: "1.33"},
			{Kind: apidoc.StepAddon, Name: "kube-proxy", Action: apidoc.ActionSkip, Reason: apidoc.ReasonAlreadyAtTarget, From: "v4", To: "v4"},
			{Kind: apidoc.StepAddon, Name: "busy", Action: apidoc.ActionSkip, Reason: apidoc.ReasonInFlight, From: "v6", To: "v7", InFlight: "AddonUpdate"},
			{Kind: apidoc.StepAddon, Name: "unknown", Action: apidoc.ActionSkip, Reason: apidoc.ReasonInFlight, From: "v6", To: "v7"},
			{Kind: apidoc.StepAddon, Name: "mine", Action: apidoc.ActionManual},
			{Kind: apidoc.StepAddon, Name: "broken", Action: apidoc.ActionBlocked},
		},
	}}}
	want := []string{
		"PLAN  AddonUpdate, prod in us-east-1",
		"  vpc-cni     update v1 to v2 (VersionBehind)",
		"  coredns     apply v3 again (Repair)",
		"  adot        wait for the update to v5 that is running (InFlight)",
		"  web         verify at 1.33 (Unsettled)",
		"  kube-proxy  skip, at v4 (AlreadyAtTarget)",
		"  busy        skip, at v6 (InFlight: AddonUpdate)",
		"  unknown     skip, at v6 (InFlight)",
		"  mine        left to you",
		"  [X] broken      blocked",
	}
	if diff := cmp.Diff(want, PlanLines(Theme{}, doc)); diff != "" {
		t.Errorf("PlanLines (-want +got):\n%s", diff)
	}

	for _, tt := range []struct {
		step apidoc.StepResult
		want string
	}{
		{apidoc.StepResult{Kind: apidoc.StepAddon, Name: "vpc-cni", Status: apidoc.StepStarted, UpdateID: "u-1"}, "[~] vpc-cni: Started (update u-1)"},
		{apidoc.StepResult{Kind: apidoc.StepAddon, Name: "vpc-cni", Status: apidoc.StepSucceeded}, "[OK] vpc-cni: Succeeded"},
		{apidoc.StepResult{Kind: apidoc.StepAddon, Name: "vpc-cni", Status: apidoc.StepFailed, Failure: &apidoc.Failure{Error: "it broke"}}, "[X] vpc-cni: Failed: it broke"},
		{
			apidoc.StepResult{Kind: apidoc.StepAddon, Name: "vpc-cni", Status: apidoc.StepInProgress, Attached: true},
			"[~] vpc-cni: InProgress (an update that was already running; not waited for)",
		},
		{apidoc.StepResult{Kind: apidoc.StepControlPlane, Status: apidoc.StepBlocked}, "[X] control-plane: Blocked"},
	} {
		if got := StepLine(Theme{}, tt.step); got != tt.want {
			t.Errorf("StepLine = %q, want %q", got, tt.want)
		}
	}
}

// A plan in which nothing would be changed because a target is changing
// already is not a plan with nothing to do: its head says so, beside the
// Busy failure that it lists.
func TestPlanView_BusyOnlyHasItsOwnWords(t *testing.T) {
	plan := func(reason apidoc.StepReason) apidoc.Plan {
		return apidoc.Plan{Operation: apidoc.OperationNodegroupUpdate, Clusters: []apidoc.ClusterPlan{{
			Cluster: "prod", Region: "us-east-1", Decision: apidoc.PlanNothingToDo,
			Steps: []apidoc.PlanStep{{Phase: apidoc.PhaseRoll, Kind: apidoc.StepNodegroup, Name: "web", Action: apidoc.ActionSkip, Reason: reason, From: "1.33", To: "1.33"}},
		}}}
	}
	busy := PlanView(plan(apidoc.ReasonInFlight))
	if busy.Label != "TargetBusy" || busy.Status != Warn {
		t.Errorf("a busy-only plan is %q (%v), want TargetBusy as a warning", busy.Label, busy.Status)
	}
	if head := busy.Render(Theme{}, 100)[0]; strings.Contains(head, "NothingToDo") {
		t.Errorf("the head of a busy-only plan says NothingToDo: %q", head)
	}
	idle := PlanView(plan(apidoc.ReasonAlreadyLatest))
	if idle.Label != string(apidoc.PlanNothingToDo) || idle.Status != Neutral {
		t.Errorf("a plan with nothing to do is %q (%v), want NothingToDo", idle.Label, idle.Status)
	}
}

// The view of a run lists the blockers of the plan of each cluster: a
// cluster that its plan blocked has Blocked rows, and the blockers say
// why.
func TestRunView_ListsBlockers(t *testing.T) {
	doc := apidoc.Run{Operation: apidoc.OperationNodegroupUpdate, Status: apidoc.RunBlocked, Clusters: []apidoc.ClusterRun{{
		ClusterPlan: apidoc.ClusterPlan{
			Cluster: "prod", Region: "us-east-1", Decision: apidoc.PlanBlocked,
			Steps: []apidoc.PlanStep{{Phase: apidoc.PhaseRoll, Kind: apidoc.StepNodegroup, Name: "web", Action: apidoc.ActionBlocked}},
			Blockers: []apidoc.Blocker{{
				Kind: apidoc.BlockerNotReady, Message: "nodegroup web is DEGRADED, not ACTIVE; it was not rolled",
				Step: &apidoc.StepID{Phase: apidoc.PhaseRoll, Kind: apidoc.StepNodegroup, Name: "web"},
			}},
		},
		Status:  apidoc.RunBlocked,
		Results: []apidoc.StepResult{{Phase: apidoc.PhaseRoll, Kind: apidoc.StepNodegroup, Name: "web", Status: apidoc.StepBlocked}},
	}}}
	var b strings.Builder
	if err := RunView(doc).Plain(&b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"blockers.1.kind\tNotReady", "blockers.1.step\tweb", "blockers.1.message\tnodegroup web is DEGRADED, not ACTIVE; it was not rolled"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("the plain view lacks %q:\n%s", want, b.String())
		}
	}
	if table := strings.Join(RunView(doc).Render(Theme{}, 140), "\n"); !strings.Contains(table, "BLOCKERS") {
		t.Errorf("the table has no BLOCKERS section:\n%s", table)
	}
}
