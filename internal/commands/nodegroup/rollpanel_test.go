package nodegroup

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/render"
)

// TestRollPanelLines_Warnings verifies scoped Warning events render in a
// warnings section with reason + node + (truncated) message.
func TestRollPanelLines_Warnings(t *testing.T) {
	th := render.New(render.ColorNone, true)
	snap := noderoll.Snapshot{
		Total: 1, Draining: 1,
		Nodes: []noderoll.NodeView{{Name: "ip-1", OnTarget: false, Ready: true, Phase: noderoll.PhaseDraining}},
		Warnings: []noderoll.WarnEvent{{
			Node: "ip-1", Object: "Node/ip-1", Reason: "FailedDraining",
			Message: "Cannot evict pod as it would violate the pod's disruption budget",
		}},
	}
	m := rollMeta{Nodegroup: "spot-burst", OldAMI: "ami-old", NewAMI: "ami-new", Desired: 1}
	joined := strings.Join(rollPanelLines(th, snap, nil, m), "\n")
	if strings.Contains(joined, "\x1b") {
		t.Fatalf("ColorNone panel contains ANSI escapes:\n%s", joined)
	}
	for _, want := range []string{"warnings", "FailedDraining", "ip-1", "violate the pod's disruption budget"} {
		if !strings.Contains(joined, want) {
			t.Errorf("panel missing %q in:\n%s", want, joined)
		}
	}
}

// TestRollPanelLines_PressureAdvisory verifies a Ready node under pressure still
// reads "Ready" but carries the pressure advisory in its state cell.
func TestRollPanelLines_PressureAdvisory(t *testing.T) {
	th := render.New(render.ColorNone, true)
	snap := noderoll.Snapshot{
		Total: 1, ReadyTarget: 1,
		Nodes: []noderoll.NodeView{
			{Name: "ip-hot", OnTarget: true, Ready: true, Phase: noderoll.PhaseReady, Pressure: []string{"MemoryPressure", "DiskPressure"}},
		},
	}
	m := rollMeta{Nodegroup: "spot-burst", OldAMI: "ami-old", NewAMI: "ami-new", Desired: 1}
	joined := strings.Join(rollPanelLines(th, snap, nil, m), "\n")
	if strings.Contains(joined, "\x1b") {
		t.Fatalf("ColorNone panel contains ANSI escapes:\n%s", joined)
	}
	for _, want := range []string{"Ready", "MemoryPressure+DiskPressure"} {
		if !strings.Contains(joined, want) {
			t.Errorf("panel missing %q in:\n%s", want, joined)
		}
	}
}

func TestRollPanelLines_MidRoll(t *testing.T) {
	th := render.New(render.ColorNone, true)
	snap := noderoll.Snapshot{
		Total: 4, ReadyTarget: 1, Draining: 1, Joining: 1,
		Nodes: []noderoll.NodeView{
			{Name: "ip-1", OnTarget: false, Ready: true, Phase: noderoll.PhaseDraining, Pods: 1, PodsTotal: 4},
			{Name: "ip-2", OnTarget: false, Ready: true, Phase: noderoll.PhaseReady},
			{Name: "ip-3", OnTarget: true, Ready: true, Phase: noderoll.PhaseReady},
			{Name: "ip-4", OnTarget: true, Ready: false, Phase: noderoll.PhaseJoining},
		},
	}
	events := []noderoll.Event{
		{Node: "ip-3", Kind: noderoll.EvtOnline},
		{Node: "ip-1", Kind: noderoll.EvtDraining},
	}
	m := rollMeta{Nodegroup: "spot-burst", OldAMI: "ami-old", NewAMI: "ami-new", Desired: 3, Frame: 2}
	joined := strings.Join(rollPanelLines(th, snap, events, m), "\n")

	if strings.Contains(joined, "\x1b") {
		t.Fatalf("ColorNone panel contains ANSI escapes:\n%s", joined)
	}
	// Assert the salient, stable bits.
	for _, want := range []string{
		"rolling spot-burst",
		"ami-old → ami-new",
		"1/3 replaced",
		"1 new ready",
		"old", "new", // AMI words
		"draining · evicting",
		"3/4 pods",
		"joining (NotReady)",
		"events",
		"ip-3", "ip-1", // event feed nodes
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("panel missing %q in:\n%s", want, joined)
		}
	}
}

func TestRollComplete(t *testing.T) {
	done := rollComplete(3)
	if done(noderoll.Snapshot{Total: 3, ReadyTarget: 3}) != true {
		t.Error("expected complete when 3/3 ready on target")
	}
	if done(noderoll.Snapshot{Total: 4, ReadyTarget: 2, Draining: 1}) != false {
		t.Error("expected not complete mid-roll")
	}
}
