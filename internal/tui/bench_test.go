package tui

import (
	"context"
	"fmt"
	"testing"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// maxFrameAllocs is the allocation gate of one frame of the Fleet screen
// at 200 clusters. The first measurement was 2,551 on a 231 by 64
// terminal (0.57 ms), and 945 for the Cluster screen; the gate leaves
// room for a column, not for a copy of the fleet.
const maxFrameAllocs = 4000

// maxChangeFrameAllocs is the gate of one frame of the Change screen with
// a 60-node roll and a log at the cap. The log lines are worded for every
// entry before the newest fit: the first measurement was about 11,000.
const maxChangeFrameAllocs = 16000

func fleetModel(w, h int) Model {
	m := New(context.Background(), &scripted{}, colorTheme)
	m.w, m.h = w, h
	m.st, m.loaded, m.now = fleetState(200), true, now
	m.sel = 100
	return m
}

func BenchmarkFleetFrame(b *testing.B) {
	m := fleetModel(231, 64)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.render()
	}
}

func BenchmarkClusterFrame(b *testing.B) {
	m := fleetModel(231, 64)
	m.screen, m.sel = screenCluster, 5
	b.ReportAllocs()
	for b.Loop() {
		_ = m.render()
	}
}

// changeModel is fleetModel with a roll of 60 nodes on the Change screen
// and a log at the cap.
func changeModel(w, h int) Model {
	m := fleetModel(w, h)
	c := rollChange(1, true)
	step := c.Steps[0].Ref
	nodes := c.Progress[step].Nodes
	for i := range 60 {
		nodes.Snapshot.Nodes = append(nodes.Snapshot.Nodes, noderoll.NodeView{Name: fmt.Sprintf("ip-10-0-%d-%d.ec2.internal", i/10, i), Phase: noderoll.PhaseDraining, Pods: 3, PodsTotal: 9})
	}
	for i := range 4096 {
		c.Log = append(c.Log, change.LogEntry{Seq: uint64(i + 10), Event: change.Event{At: now, Kind: change.Notice, Text: fmt.Sprintf("Auto Scaling group eks-web: launched i-%06d", i)}})
	}
	m.st.Changes = []state.Change{c}
	m.screen = screenChange
	return m
}

func BenchmarkChangeFrame(b *testing.B) {
	m := changeModel(231, 64)
	b.ReportAllocs()
	for b.Loop() {
		_ = m.render()
	}
}

// A frame of the Fleet screen at 200 clusters renders the rows that fit,
// never the fleet, so its allocations do not grow with the fleet. A
// Change frame renders the log lines that fit, never the log.
func TestFrameAllocations(t *testing.T) {
	m := fleetModel(231, 64)
	if got := testing.AllocsPerRun(20, func() { _ = m.render() }); got > maxFrameAllocs {
		t.Fatalf("a Fleet frame at 200 clusters makes %.0f allocations, want at most %d", got, maxFrameAllocs)
	}
	m.screen, m.sel = screenCluster, 5
	if got := testing.AllocsPerRun(20, func() { _ = m.render() }); got > maxFrameAllocs {
		t.Fatalf("a Cluster frame makes %.0f allocations, want at most %d", got, maxFrameAllocs)
	}
	m = changeModel(231, 64)
	if got := testing.AllocsPerRun(20, func() { _ = m.render() }); got > maxChangeFrameAllocs {
		t.Fatalf("a Change frame with a log at the cap makes %.0f allocations, want at most %d", got, maxChangeFrameAllocs)
	}
}
