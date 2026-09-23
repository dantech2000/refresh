package rollview

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

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
	for _, want := range []string{
		"rolling spot-burst",
		"ami-old → ami-new",
		"1/3 replaced",
		"1 new ready",
		"old", "new",
		"draining · evicting",
		"3/4 pods",
		"joining (NotReady)",
		"events",
		"ip-3", "ip-1",
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

// With surge, the new nodes can all be Ready while every old node still
// serves. That is not a finished roll: old (baseline) nodes must be gone.
func TestRollComplete_SurgeWithOldNodesRemaining(t *testing.T) {
	done := rollComplete(2)
	surge := noderoll.Snapshot{
		Total: 4, ReadyTarget: 2,
		Nodes: []noderoll.NodeView{
			{Name: "old-1", OnTarget: false, Ready: true, Phase: noderoll.PhaseReady},
			{Name: "old-2", OnTarget: false, Ready: true, Phase: noderoll.PhaseReady},
			{Name: "new-1", OnTarget: true, Ready: true, Phase: noderoll.PhaseReady},
			{Name: "new-2", OnTarget: true, Ready: true, Phase: noderoll.PhaseReady},
		},
	}
	if done(surge) {
		t.Fatal("surge snapshot with 2 old Ready nodes reported complete")
	}
	surge.Nodes = surge.Nodes[2:]
	surge.Total = 2
	if !done(surge) {
		t.Fatal("expected complete once the old nodes are gone")
	}
}

// The scripted surge roll is only complete on its final frame, after the last
// old node has drained away.
func TestRollComplete_DemoTimelineOnlyAtEnd(t *testing.T) {
	frames := noderoll.DemoTimeline()
	done := rollComplete(3)
	for i, f := range frames {
		want := i == len(frames)-1
		if got := done(f); got != want {
			t.Errorf("frame %d: complete = %v, want %v", i, got, want)
		}
	}
}

// Appending snapshots (piped/CI) must never repaint faster than
// liveRollAppendRepaint; in place, the existing cadence rules hold.
func TestRollRepaintInterval(t *testing.T) {
	cases := []struct {
		name              string
		poll              time.Duration
		watching, inPlace bool
		want              time.Duration
	}{
		{"in place, default poll, polling", 15 * time.Second, false, true, liveRollPoll},
		{"in place, default poll, watching", 15 * time.Second, true, true, liveRollWatchRepaint},
		{"in place, fast poll kept", 500 * time.Millisecond, false, true, 500 * time.Millisecond},
		{"in place, zero poll", 0, true, true, liveRollWatchRepaint},
		{"append, watching", 15 * time.Second, true, false, liveRollAppendRepaint},
		{"append, fast poll throttled", 10 * time.Millisecond, false, false, liveRollAppendRepaint},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rollRepaintInterval(tc.poll, tc.watching, tc.inPlace); got != tc.want {
				t.Errorf("rollRepaintInterval = %v, want %v", got, tc.want)
			}
		})
	}
}

// Appended snapshots of an unchanged roll must be identical (no spinner
// animation) so the live region prints them once.
func TestRunRoll_AppendModeSkipsUnchangedFrames(t *testing.T) {
	var buf bytes.Buffer
	th := render.New(render.ColorNone, true)
	snap := noderoll.Snapshot{Total: 1, Nodes: []noderoll.NodeView{{Name: "ip-1", Phase: noderoll.PhaseReady}}}
	obs := &staticObserver{snap: snap}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_ = runRoll(ctx, th, &buf, obs, rollMeta{Nodegroup: "ng", Desired: 1}, time.Millisecond, func(noderoll.Snapshot) bool {
		calls++
		return calls >= 5
	})
	if n := strings.Count(buf.String(), "rolling ng"); n != 1 {
		t.Fatalf("unchanged roll printed %d frames, want 1:\n%s", n, buf.String())
	}
}

type staticObserver struct{ snap noderoll.Snapshot }

func (o *staticObserver) Snapshot(context.Context) (noderoll.Snapshot, error) { return o.snap, nil }
