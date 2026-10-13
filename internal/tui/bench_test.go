package tui

import (
	"context"
	"testing"
)

// maxFrameAllocs is the allocation gate of one frame of the Fleet screen
// at 200 clusters. The first measurement was 2,551 on a 231 by 64
// terminal (0.57 ms), and 945 for the Cluster screen; the gate leaves
// room for a column, not for a copy of the fleet.
const maxFrameAllocs = 4000

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

// A frame of the Fleet screen at 200 clusters renders the rows that fit,
// never the fleet, so its allocations do not grow with the fleet.
func TestFrameAllocations(t *testing.T) {
	m := fleetModel(231, 64)
	if got := testing.AllocsPerRun(20, func() { _ = m.render() }); got > maxFrameAllocs {
		t.Fatalf("a Fleet frame at 200 clusters makes %.0f allocations, want at most %d", got, maxFrameAllocs)
	}
	m.screen, m.sel = screenCluster, 5
	if got := testing.AllocsPerRun(20, func() { _ = m.render() }); got > maxFrameAllocs {
		t.Fatalf("a Cluster frame makes %.0f allocations, want at most %d", got, maxFrameAllocs)
	}
}
