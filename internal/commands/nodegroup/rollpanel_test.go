package nodegroup

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/render"
)

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
