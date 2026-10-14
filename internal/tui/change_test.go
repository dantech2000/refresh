package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

var prodTarget = change.Target{Region: "us-east-1", Cluster: "c-000"}

// rollPlan is the plan of a roll of web in c-000.
func rollPlan() *change.Plan {
	ref := change.PhaseRef{Target: prodTarget, Kind: change.PhaseRoll}
	return &change.Plan{Kind: change.NodegroupUpdate, At: now, Clusters: []change.ClusterPlan{{
		Target: prodTarget, From: v("1.33"), To: v("1.33"),
		Gate: health.Verdict{Decision: health.Go},
		Phases: []change.Phase{{Ref: ref, Steps: []change.Step{
			{Ref: change.StepRef{Phase: ref, Kind: change.StepNodegroup, Name: "web"}, Action: apidoc.ActionSubmit, Reason: change.AMIOutdated, From: "1.33.0-20250101", To: "1.33.0-20260101"},
		}}},
	}}}
}

// upgradePlan is the plan of an upgrade of c-000 to 1.34 with every part.
func upgradePlan() *change.Plan {
	hop := v("1.34")
	cp := change.PhaseRef{Target: prodTarget, Kind: change.PhaseControlPlane, Hop: hop}
	ng := change.PhaseRef{Target: prodTarget, Kind: change.PhaseNodegroups, Hop: hop}
	ad := change.PhaseRef{Target: prodTarget, Kind: change.PhaseAddonsAfter, Hop: hop}
	return &change.Plan{Kind: change.ClusterUpgrade, At: now, Clusters: []change.ClusterPlan{{
		Target: prodTarget, From: v("1.33"), To: hop,
		Gate:    health.Verdict{Decision: health.Ask, Reasons: []health.Reason{{Check: "PDBs", Outcome: health.Warn, Message: "the budget web lets no pod go"}}},
		Notices: []string{"the control plane cannot be rolled back after 1.34 is in"},
		Phases: []change.Phase{
			{Ref: cp, Steps: []change.Step{{Ref: change.StepRef{Phase: cp, Kind: change.StepControlPlane}, Action: apidoc.ActionSubmit, From: "1.33", To: "1.34"}}},
			{Ref: ng, Batch: true, Steps: []change.Step{
				{Ref: change.StepRef{Phase: ng, Kind: change.StepNodegroup, Name: "api"}, Action: apidoc.ActionSubmit, Reason: change.VersionBehind, From: "1.33", To: "1.34"},
				{Ref: change.StepRef{Phase: ng, Kind: change.StepNodegroup, Name: "web"}, Action: apidoc.ActionSubmit, Reason: change.VersionBehind, From: "1.33", To: "1.34"},
			}},
			{Ref: ad, Steps: []change.Step{{Ref: change.StepRef{Phase: ad, Kind: change.StepAddon, Name: "coredns"}, Action: apidoc.ActionSubmit, From: "v1.11.3-eksbuild.2", To: "v1.12.0-eksbuild.1"}}},
		},
	}}}
}

// proposalOf is the proposal the backend would return for a plan.
func proposalOf(id state.ProposalID, a state.Action, p *change.Plan) state.Proposal {
	return state.Proposal{ID: id, Action: a, Plan: p, Doc: p.Document(),
		Command: "refresh --profile prod-admin --region us-east-1 nodegroup update web -c c-000 --kubeconfig /home/me/.kube/config",
		Blocked: p.Decision() == change.DecisionBlocked}
}

// rollChange is a roll of web in c-000 that runs: the old nodes drain,
// the new ones join, and a question is open when asking is set.
func rollChange(id state.ChangeID, asking bool) state.Change {
	p := rollPlan()
	step := p.Clusters[0].Phases[0].Steps[0].Ref
	phase := p.Clusters[0].Phases[0].Ref
	snap := noderoll.Snapshot{
		Nodes: []noderoll.NodeView{
			{Name: "ip-10-0-1-10.ec2.internal", Phase: noderoll.PhaseDraining, Ready: true, Pods: 2, PodsTotal: 5, PodList: []noderoll.PodView{{Name: "default/web-7f9c-abcde"}, {Name: "default/web-7f9c-fghij", Terminating: true}}},
			{Name: "ip-10-0-1-11.ec2.internal", Phase: noderoll.PhaseReady, Ready: true},
			{Name: "ip-10-0-2-20.ec2.internal", Phase: noderoll.PhaseJoining, OnTarget: true},
			{Name: "ip-10-0-2-21.ec2.internal", Phase: noderoll.PhaseReady, OnTarget: true, Ready: true, Pressure: []string{"MemoryPressure"}},
		},
		Total: 4, ReadyTarget: 1, Draining: 1, Joining: 1,
		Warnings: []noderoll.WarnEvent{{Node: "ip-10-0-1-10.ec2.internal", Object: "Pod/web-7f9c-abcde", Reason: "FailedDraining", Message: "Cannot evict pod as it would violate the pod's disruption budget"}},
	}
	c := state.Change{ID: id, View: change.View{
		Version: 7, State: change.Running, Kind: change.NodegroupUpdate, Target: prodTarget, StartedAt: now.Add(-3 * time.Minute), Plan: p,
		Steps:  []change.StepResult{{Ref: step, Status: change.StepStarted, UpdateID: "8f2c1a9e-1111-4222-8333-444455556666", StartedAt: now.Add(-2 * time.Minute)}},
		Phases: []change.PhaseState{{Ref: phase, Status: change.PhaseRunning, StartedAt: now.Add(-2 * time.Minute)}},
		Progress: map[change.StepRef]change.StepProgress{step: {
			Update: &change.UpdateProgress{UpdateID: "8f2c1a9e-1111-4222-8333-444455556666", Status: "InProgress"},
			Nodes:  &change.NodeView{Nodegroup: "web", Snapshot: snap, Watching: true, StartedAt: now.Add(-2 * time.Minute)},
		}},
		Controls: change.Controls{},
		LastSeq:  5,
	}}
	c.Log = []change.LogEntry{
		{Seq: 1, Event: change.Event{At: now.Add(-3 * time.Minute), Kind: change.PlanReady, Plan: p}},
		{Seq: 2, Event: change.Event{At: now.Add(-3 * time.Minute), Kind: change.GateReport, Verdict: &p.Clusters[0].Gate}},
		{Seq: 3, Event: change.Event{At: now.Add(-2 * time.Minute), Kind: change.PhaseStarted, Phase: phase}},
		{Seq: 4, Event: change.Event{At: now.Add(-2 * time.Minute), Kind: change.StepChanged, Step: step, Result: &c.Steps[0]}},
		{Seq: 5, Event: change.Event{At: now.Add(-time.Minute), Kind: change.Notice, Text: "Auto Scaling group eks-web: launched i-0abc"}},
	}
	c.Nodes = map[change.StepRef][]noderoll.Event{step: {
		{Node: "ip-10-0-2-20.ec2.internal", Kind: noderoll.EvtJoining, At: now.Add(-90 * time.Second)},
		{Node: "ip-10-0-2-21.ec2.internal", Kind: noderoll.EvtOnline, At: now.Add(-60 * time.Second)},
		{Node: "ip-10-0-1-10.ec2.internal", Kind: noderoll.EvtDraining, At: now.Add(-30 * time.Second)},
	}}
	if asking {
		c.State = change.Asking
		c.Question = &change.PendingQuestion{ID: 3, AskedAt: now, Question: change.Question{Kind: change.GateWarnings, Text: "The health gate warns. Go on?", Reasons: []string{"PDBs: the budget web lets no pod go"}}}
		c.Controls = change.Controls{Answer: true}
	}
	return c
}

// upgradeChange is an upgrade of c-000 in its nodegroup phase, with the
// controls of an own change, or none when elsewhere.
func upgradeChange(id state.ChangeID, elsewhere bool) state.Change {
	p := upgradePlan()
	phases := p.Clusters[0].Phases
	c := state.Change{ID: id, View: change.View{
		Version: 12, State: change.Running, Kind: change.ClusterUpgrade, Target: prodTarget, Elsewhere: elsewhere, StartedAt: now.Add(-40 * time.Minute), Plan: p,
		Steps: []change.StepResult{
			{Ref: phases[0].Steps[0].Ref, Status: change.StepSucceeded, StartedAt: now.Add(-40 * time.Minute), FinishedAt: now.Add(-10 * time.Minute)},
			{Ref: phases[1].Steps[0].Ref, Status: change.StepStarted, StartedAt: now.Add(-9 * time.Minute)},
			{Ref: phases[1].Steps[1].Ref, Status: change.StepStarted, StartedAt: now.Add(-9 * time.Minute)},
			{Ref: phases[2].Steps[0].Ref, Status: change.StepNotAttempted},
		},
		Phases: []change.PhaseState{
			{Ref: phases[0].Ref, Status: change.PhaseDone, StartedAt: now.Add(-40 * time.Minute), EndedAt: now.Add(-10 * time.Minute)},
			{Ref: phases[1].Ref, Status: change.PhaseRunning, StartedAt: now.Add(-9 * time.Minute)},
			{Ref: phases[2].Ref, Status: change.PhasePending},
		},
		Progress: map[change.StepRef]change.StepProgress{},
		Controls: change.Controls{Pause: !elsewhere, Stop: !elsewhere},
		LastSeq:  9,
	}}
	c.Log = []change.LogEntry{
		{Seq: 1, Event: change.Event{At: now.Add(-40 * time.Minute), Kind: change.PlanReady, Plan: p}},
		{Seq: 2, Event: change.Event{At: now.Add(-40 * time.Minute), Kind: change.PhaseStarted, Phase: phases[0].Ref}},
		{Seq: 3, Event: change.Event{At: now.Add(-10 * time.Minute), Kind: change.StepChanged, Step: phases[0].Steps[0].Ref, Result: &c.Steps[0]}},
		{Seq: 4, Event: change.Event{At: now.Add(-10 * time.Minute), Kind: change.PhaseEnded, Phase: phases[0].Ref}},
		{Seq: 5, Event: change.Event{At: now.Add(-9 * time.Minute), Kind: change.PhaseStarted, Phase: phases[1].Ref}},
		{Seq: 6, Event: change.Event{At: now.Add(-9 * time.Minute), Kind: change.Notice, Text: "no Kubernetes access to c-000 (no kubeconfig context matches); the nodes of the roll are not shown"}},
	}
	return c
}

// finishedChange is an add-on update that failed on its last step.
func finishedChange(id state.ChangeID) state.Change {
	ref := change.PhaseRef{Target: prodTarget, Kind: change.PhaseAddons}
	step := change.StepRef{Phase: ref, Kind: change.StepAddon, Name: "coredns"}
	p := &change.Plan{Kind: change.AddonUpdate, At: now, Clusters: []change.ClusterPlan{{Target: prodTarget, From: v("1.33"),
		Phases: []change.Phase{{Ref: ref, Steps: []change.Step{{Ref: step, Action: apidoc.ActionSubmit, From: "v1.11.1-eksbuild.1", To: "v1.11.3-eksbuild.2"}}}}}}}
	c := state.Change{ID: id, View: change.View{
		Version: 4, State: change.Finished, Kind: change.AddonUpdate, Target: prodTarget, StartedAt: now.Add(-6 * time.Minute), Plan: p,
		Steps:    []change.StepResult{{Ref: step, Status: change.StepFailed, Failure: &apidoc.Failure{Kind: apidoc.FailureAddon, Name: "coredns", Error: "the update failed: InsufficientNumberOfReplicas"}}},
		Phases:   []change.PhaseState{{Ref: ref, Status: change.PhaseFailed, StartedAt: now.Add(-6 * time.Minute), EndedAt: now.Add(-time.Minute)}},
		Progress: map[change.StepRef]change.StepProgress{},
		Result:   &change.Result{Status: change.Failed, Err: errors.New("add-on coredns: the update failed")},
		LastSeq:  3,
	}}
	c.Log = []change.LogEntry{
		{Seq: 1, Event: change.Event{At: now.Add(-6 * time.Minute), Kind: change.PlanReady, Plan: p}},
		{Seq: 2, Event: change.Event{At: now.Add(-6 * time.Minute), Kind: change.PhaseStarted, Phase: ref}},
		{Seq: 3, Event: change.Event{At: now.Add(-time.Minute), Kind: change.StepChanged, Step: step, Result: &c.Steps[0]}},
	}
	return c
}

// writable is a state of a UI with --allow-changes.
func writable(st state.State) state.State {
	st.ReadOnly = false
	return st
}

// withChanges attaches changes to a fleet state: the first cluster
// carries the first running one.
func withChanges(st state.State, cs ...state.Change) state.State {
	st.Changes = cs
	for _, c := range cs {
		if c.Running() {
			st.Clusters[0].Change = c.ID
			break
		}
	}
	return st
}

func TestChangeScreensFillTheTerminalExactly(t *testing.T) {
	for _, th := range []view.Theme{colorTheme, asciiTheme} {
		for _, size := range [][2]int{{minWidth, minHeight}, {120, 40}, {160, 42}, {231, 64}} {
			h := newHarness(t, size[0], size[1], th)
			name := func(step string) string {
				return fmt.Sprintf("%s at %dx%d, unicode %v", step, size[0], size[1], th.Unicode)
			}
			h.state(fleetState(4))
			h.keys("3")
			checkFrame(t, name("no change yet"), h.m)
			st := withChanges(fleetState(4), rollChange(1, true), upgradeChange(2, true), finishedChange(3))
			h.state(st)
			for _, k := range []string{"3", "]", "]", "[", "[", "space", "w", "w", "space", "?", "esc"} {
				h.keys(k)
				checkFrame(t, name("change screen after "+k), h.m)
			}
			h.tick(now.Add(time.Second))
			checkFrame(t, name("change tick"), h.m)
			// The dry-run dialogs: a picker and a proposal, scrolled.
			h.keys("1")
			h.b.proposal = proposalOf(1, state.Action{Kind: state.ActionUpgrade, Cluster: h.m.cluster().Key, To: v("1.34")}, upgradePlan())
			for _, k := range []string{"U", "down", "G", "enter", "down", "pgdown", "G", "esc"} {
				h.keys(k)
				checkFrame(t, name("dialog after "+k), h.m)
			}
			h.b.proposal = proposalOf(2, state.Action{Kind: state.ActionRoll, Cluster: h.m.cluster().Key, Nodegroup: "web"}, rollPlan())
			st.Clusters[0].Change = 0
			st.Clusters[0] = stale(st.Clusters[0])
			st.Clusters[0].Snapshot.Nodegroups[1].AMI.Current = "ami-0"
			st.Clusters[0].Stale.AMIs = 2
			h.state(st)
			for _, k := range []string{"p", "down", "enter", "y"} {
				h.keys(k)
				checkFrame(t, name("roll dialog after "+k), h.m)
			}
			// A dry run on its way shows in the log strip.
			h.b.planErr = errors.New("the cluster could not be read")
			h.keys("esc", "a")
			checkFrame(t, name("failed dry run"), h.m)
		}
	}
}

// y in the dry run sends Start once; a second y while the start is on
// its way sends nothing, and esc does not close the dialog then.
func TestStartIsSentOncePerConfirmation(t *testing.T) {
	h := newHarness(t, 160, 42, colorTheme)
	h.state(writable(fleetState(3)))
	h.b.proposal = proposalOf(5, state.Action{Kind: state.ActionAddons, Cluster: h.m.cluster().Key}, finishedChange(0).Plan)
	h.keys("a")
	if len(h.b.plans) != 1 || h.b.plans[0].Kind != state.ActionAddons || h.m.confirm == nil {
		t.Fatalf("plans %v, dialog %v", h.b.plans, h.m.confirm != nil)
	}
	h.contains("Add-on update c-000", "dry run · nothing changed yet", "coredns", "refresh --profile prod-admin", "start the change")
	// The key handler alone, without the command: the start is on its way.
	m, cmd := h.m.key("y")
	if cmd == nil || !m.starting {
		t.Fatal("y did not start the change")
	}
	if _, again := m.key("y"); again != nil {
		t.Fatal("a second y while starting sent a second start")
	}
	if closed, _ := m.key("esc"); closed.confirm == nil {
		t.Fatal("esc closed the dialog while the change was starting")
	}
	if closed, _ := m.key("n"); closed.confirm == nil {
		t.Fatal("n closed the dialog while the change was starting")
	}
	h.m = m
	h.b.started = 9
	h.update(cmd())
	if len(h.b.starts) != 1 || h.b.starts[0] != 5 {
		t.Fatalf("starts = %v, want [5]", h.b.starts)
	}
	if h.m.confirm != nil || h.m.focusAfter != 9 {
		t.Fatalf("after the start: dialog %v, focus %d", h.m.confirm != nil, h.m.focusAfter)
	}
	// The next state that carries the change opens the Change screen on it.
	h.state(withChanges(writable(fleetState(3)), finishedChange(8), rollChange(9, false)))
	if h.m.screen != screenChange || h.m.focusAfter != 0 {
		t.Fatalf("screen %d, focus %d", h.m.screen, h.m.focusAfter)
	}
	if c, ok := h.m.change(); !ok || c.ID != 9 {
		t.Fatalf("the change screen shows %v", c.ID)
	}
	h.contains("Roll c-000", "running", "Nodes of web", "ip-10-0-1-10", "draining", "3/5 pods evicted", "joining (NotReady)", "MemoryPressure")
}

// The backend refuses a start: the dialog says so and stays open, and
// nothing else is sent. Read-only is the backend's call, in one place.
func TestRefusedStartStaysInTheDialog(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	st := fleetState(3)
	st.ReadOnly = true
	h.state(st)
	h.b.proposal = proposalOf(1, state.Action{Kind: state.ActionAddons, Cluster: h.m.cluster().Key}, finishedChange(0).Plan)
	h.b.startErr = errors.New("the UI is read-only; start it with --allow-changes")
	h.keys("a")
	h.contains("read-only: run the command above")
	h.keys("y")
	if len(h.b.starts) != 1 {
		t.Fatalf("starts = %v", h.b.starts)
	}
	if h.m.confirm == nil || h.m.starting {
		t.Fatal("a refused start closed the dialog or stayed starting")
	}
	h.contains("the UI is read-only; start it with --allow-changes")
	h.keys("esc")
	if h.m.confirm != nil {
		t.Fatal("esc did not close the dialog")
	}
}

// A blocked dry run has no start key, and y sends nothing.
func TestBlockedProposalCannotStart(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(writable(fleetState(3)))
	p := upgradePlan()
	p.Clusters[0].Blockers = []change.Blocker{{Kind: apidoc.BlockerBusy, Message: "nodegroup web is UPDATING"}}
	prop := proposalOf(1, state.Action{Kind: state.ActionUpgrade, Cluster: h.m.cluster().Key, To: v("1.34")}, p)
	prop.Blocked = true
	h.b.proposal = prop
	h.keys("U", "enter")
	h.contains("BLOCKERS", "nodegroup web is UPDATING", "a real run would be blocked")
	h.lacks("start the change")
	h.keys("y")
	if len(h.b.starts) != 0 {
		t.Fatal("y started a blocked proposal")
	}
}

// The action keys need a cluster that has no change: a busy cluster
// gets a notice and no dry run.
func TestActionKeysNeedAFreeCluster(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(withChanges(fleetState(4), rollChange(1, false)))
	h.keys("a")
	if len(h.b.plans) != 0 {
		t.Fatal("a cluster with a change got a dry run")
	}
	h.contains("has a change in this UI")
	h.keys("down", "down", "a") // c-002 is changing in AWS
	if len(h.b.plans) != 0 {
		t.Fatal("a cluster that changes got a dry run")
	}
	h.contains("is changing")
	h.keys("down", "p")
	h.contains("every nodegroup in c-003 is current")
	if len(h.b.plans) != 0 {
		t.Fatal("a current cluster got a roll")
	}
	// B needs a rollback window.
	h.keys("B")
	if len(h.b.plans) != 0 {
		t.Fatal("B without a window ran a dry run")
	}
	st := fleetState(4)
	st.Clusters[0].Rollback = &fleet.Rollback{PreviousVersion: v("1.32"), UpgradedAt: now.Add(-time.Hour), AvailableUntil: now.Add(71 * time.Hour)}
	h.state(st)
	h.keys("g", "B")
	if len(h.b.plans) != 1 || h.b.plans[0].Kind != state.ActionRollback {
		t.Fatalf("plans = %v", h.b.plans)
	}
}

// p with one stale nodegroup dry-runs it; with several it asks which.
func TestPatchPicksTheNodegroup(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	st := fleetState(3)
	h.state(st)
	h.keys("down", "p") // c-001 is stale: api on an old AMI
	if len(h.b.plans) != 1 || h.b.plans[0].Nodegroup != "api" || h.b.plans[0].Kind != state.ActionRoll {
		t.Fatalf("plans = %v", h.b.plans)
	}
	h.keys("esc")
	st.Clusters[1].Snapshot.Nodegroups[1].Version = v("1.32")
	st.Clusters[1].Stale.Behind = 1
	h.state(st)
	h.keys("p")
	if h.m.pick == nil {
		t.Fatal("two stale nodegroups did not open the picker")
	}
	h.contains("Roll which nodegroup?", "api", "web", "1.32 → 1.33")
	h.keys("down", "enter")
	if len(h.b.plans) != 2 || h.b.plans[1].Nodegroup != "web" || h.b.plans[1].Kind != state.ActionUpgrade || h.b.plans[1].To != v("1.33") {
		t.Fatalf("plans = %v", h.b.plans)
	}
	if h.m.pick != nil {
		t.Fatal("enter left the picker open")
	}
	// The upgrade picker offers the parts, and catch-ups for what lags.
	h.keys("esc", "U")
	h.contains("Control plane only", "Control plane and add-ons", "Everything", "Catch up the nodegroups", "Catch up the add-ons")
	h.keys("G", "enter")
	last := h.b.plans[len(h.b.plans)-1]
	if last.Kind != state.ActionUpgrade || last.To != v("1.33") || len(last.Only) != 1 || last.Only[0] != change.PartAddons {
		t.Fatalf("catch-up action = %+v", last)
	}
}

// The steering keys call the backend only while the handle offers them.
func TestChangeKeysFollowTheControls(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(withChanges(fleetState(3), upgradeChange(4, false), rollChange(5, true)))
	h.keys("3")
	bar := h.m.keyBar(120).Plain()
	for _, want := range []string{" P ", " S ", " space ", " [ ] "} {
		if !strings.Contains(bar, want) {
			t.Errorf("key bar lacks %q: %q", want, bar)
		}
	}
	h.keys("P", "S")
	if want := []string{"pause 4", "stop 4"}; !slices.Equal(h.b.calls, want) {
		t.Fatalf("calls = %v, want %v", h.b.calls, want)
	}
	h.contains("stop requested")
	// The question of the roll: y and n answer it; P and S are off.
	h.keys("]", "P", "S", "y")
	if want := []string{"pause 4", "stop 4", "answer 5 3 true"}; !slices.Equal(h.b.calls, want) {
		t.Fatalf("calls = %v, want %v", h.b.calls, want)
	}
	h.keys("n")
	if last := h.b.calls[len(h.b.calls)-1]; last != "answer 5 3 false" {
		t.Fatalf("n sent %q", last)
	}
	// A paused change: P resumes.
	paused := upgradeChange(4, false)
	paused.State = change.Paused
	h.state(withChanges(fleetState(3), paused))
	h.keys("P")
	if last := h.b.calls[len(h.b.calls)-1]; last != "resume 4" {
		t.Fatalf("P while paused sent %q", last)
	}
	h.contains("paused")
	// A change started elsewhere has no steering key at all.
	h.b.calls = nil
	h.state(withChanges(fleetState(3), upgradeChange(6, true)))
	h.keys("P", "S", "y", "n")
	if len(h.b.calls) != 0 {
		t.Fatalf("a change started elsewhere got %v", h.b.calls)
	}
	h.contains("started elsewhere · watch only")
	bar = h.m.keyBar(120).Plain()
	if strings.Contains(bar, " P ") || strings.Contains(bar, " S ") {
		t.Errorf("key bar offers steering for a change started elsewhere: %q", bar)
	}
	// A refused call is a notice.
	h.b.steerErr = errors.New("the change has ended")
	h.state(withChanges(fleetState(3), upgradeChange(7, false)))
	h.keys("S")
	h.contains("stop: the change has ended")
}

// space freezes the log at its sequence number: later entries stay out
// until space again.
func TestSpaceFreezesTheLog(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	c := upgradeChange(1, false)
	h.state(withChanges(fleetState(3), c))
	h.keys("3", "space")
	if h.m.frozen != c.LastSeq {
		t.Fatalf("frozen at %d, want %d", h.m.frozen, c.LastSeq)
	}
	h.contains("frozen")
	later := upgradeChange(1, false)
	later.LastSeq = 10
	later.Log = append(later.Log, change.LogEntry{Seq: 10, Event: change.Event{At: now, Kind: change.Notice, Text: "a line after the freeze"}})
	h.state(withChanges(fleetState(3), later))
	h.lacks("a line after the freeze")
	h.keys("space")
	h.contains("a line after the freeze", "following")
	h.keys("w")
	h.lacks("Control plane started")
	h.contains("warnings only", "a line after the freeze")
}

// The Change screen follows its change across versions, and the clock
// ticks while a change runs.
func TestChangeScreenFollowsItsChange(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(withChanges(fleetState(3), finishedChange(1), upgradeChange(2, false)))
	if !h.m.ticking {
		t.Fatal("a running change scheduled no tick")
	}
	h.keys("3", "]")
	if c, _ := h.m.change(); c.ID != 2 {
		t.Fatalf("] moved to %d", c.ID)
	}
	// The running change moves first in the next version.
	h.state(withChanges(fleetState(3), upgradeChange(2, false), finishedChange(1)))
	if c, _ := h.m.change(); c.ID != 2 {
		t.Fatalf("the screen lost its change: %d", c.ID)
	}
	h.contains("Upgrade c-000", "1.33 → 1.34", "Control plane 1.34", "done", "Nodegroups 1.34", "running", "api", "web", "Add-ons after the nodegroups 1.34", "pending", "change 1/2")
	done := finishedChange(2)
	h.m.ticking = false
	h.state(withChanges(fleetState(3), done))
	if h.m.ticking {
		t.Fatal("a finished change scheduled a tick")
	}
	h.contains("failed", "coredns failed: the update failed")
}

func TestHelpListsTheChangeKeys(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(fleetState(3))
	h.keys("?")
	h.contains("dry-run a roll", "dry-run the update of the stale add-ons", "dry-run an upgrade", "IN A DRY RUN", "start the change", "copy the command")
	h.lacks("dry-run the rollback")
	h.keys("esc")
	h.state(withChanges(fleetState(3), upgradeChange(1, true)))
	h.keys("3", "?")
	h.contains("freeze the log", "warnings and failures only")
	h.lacks("pause before the next phase")
}

// c copies the command through the terminal.
func TestCopyCommand(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(fleetState(3))
	h.b.proposal = proposalOf(1, state.Action{Kind: state.ActionAddons, Cluster: h.m.cluster().Key}, finishedChange(0).Plan)
	h.keys("a")
	m, cmd := h.m.key("c")
	if cmd == nil || !strings.Contains(m.notice, "clipboard") {
		t.Fatal("c did not send the command to the clipboard")
	}
	// The message type is Bubble Tea's own; its text is the command.
	if got := fmt.Sprint(cmd()); !strings.Contains(got, "refresh --profile prod-admin") {
		t.Fatalf("c sent %T %q", cmd(), got)
	}
}

// A dry run that ends after esc, or after a newer one, never opens; esc
// cancels the context of the dry run on its way, and a newer dry run is
// the only one whose reply opens.
func TestOnlyTheNewestDryRunOpens(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	h.state(writable(fleetState(3)))
	// The key handler alone: the command of the dry run is held, so its
	// reply can land later.
	m, cmd := h.m.key("a")
	if cmd == nil || m.planning == nil {
		t.Fatal("a did not start a dry run")
	}
	older := m.planning
	if _, again := m.key("a"); again != nil {
		t.Fatal("a second a while a dry run is on its way started another")
	}
	m, _ = m.key("esc")
	if m.planning != nil || older.cancel == nil || !strings.Contains(m.notice, "cancelled") {
		t.Fatalf("esc did not cancel the dry run: planning %v, notice %q", m.planning != nil, m.notice)
	}
	m, _ = m.key("a")
	newer := m.planning
	if newer == nil || newer.seq == older.seq {
		t.Fatal("a after esc did not start a newer dry run")
	}
	h.m = m
	h.b.proposal = proposalOf(7, state.Action{Kind: state.ActionAddons, Cluster: h.m.cluster().Key}, finishedChange(0).Plan)
	h.update(proposalMsg{seq: older.seq, p: h.b.proposal})
	if h.m.confirm != nil {
		t.Fatal("the reply of a cancelled dry run opened the dialog")
	}
	h.update(proposalMsg{seq: newer.seq, p: h.b.proposal})
	if h.m.confirm == nil || h.m.planning != nil {
		t.Fatal("the newest dry run did not open the dialog")
	}
	h.keys("esc")
	// A picker takes the keys; esc closes it and nothing opens behind it.
	h.keys("U", "down", "esc")
	if h.m.pick != nil || h.m.confirm != nil || h.m.planning != nil {
		t.Fatal("esc did not close the picker")
	}
}

// The node table names the pods left on a draining node, keeps a gap
// after a long node name, and keeps a long warning on one row at the
// minimum width.
func TestNodeTableNamesPodsAndKeepsItsGaps(t *testing.T) {
	h := newHarness(t, 160, 42, colorTheme)
	c := rollChange(1, false)
	step := c.Steps[0].Ref
	nodes := c.Progress[step].Nodes
	nodes.Snapshot.Nodes[0].Name = "ip-10-0-1-10.a-very-long-subdomain-of-the-vpc.us-east-1.compute.internal"
	nodes.Snapshot.Warnings[0].Message = strings.Repeat("Cannot evict pod as it would violate the pod's disruption budget. ", 4)
	h.state(withChanges(fleetState(3), c))
	h.keys("3")
	h.contains("left: default/web-7f9c-abcde, default/web-7f9c-fghij", "3/5 pods evicted")
	for _, l := range h.m.frame() {
		text := l.Plain()
		if strings.Contains(text, "ip-10-0-1-10.a-very-long") && !strings.Contains(text, "…  old ") {
			t.Fatalf("a long node name has no gap before its age: %q", text)
		}
		if strings.Contains(text, "Cannot evict pod") && strings.Count(text, "Cannot evict") > 1 {
			t.Fatalf("a long warning took more than its row: %q", text)
		}
	}
	checkFrame(t, "long names", h.m)
	h.update(tea.WindowSizeMsg{Width: minWidth, Height: minHeight})
	checkFrame(t, "long names at the minimum width", h.m)
	rows := 0
	for _, l := range h.m.frame() {
		if strings.Contains(l.Plain(), "Cannot evict") {
			rows++
		}
	}
	if rows != 1 {
		t.Fatalf("the warning takes %d rows at the minimum width, want 1", rows)
	}
}

// A watched control-plane update is titled by its kind: an upgrade, or a
// rollback.
func TestWatchedControlPlaneTitle(t *testing.T) {
	h := newHarness(t, 120, 40, colorTheme)
	up := upgradeChange(1, true)
	back := upgradeChange(2, true)
	back.Kind = change.ClusterRollback
	h.state(withChanges(fleetState(3), up, back))
	h.keys("3")
	h.contains("Upgrade c-000", "started elsewhere · watch only")
	h.keys("]")
	h.contains("Rollback c-000")
	h.lacks("Roll c-000")
}
