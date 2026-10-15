package fleetcmd

import (
	"fmt"
	"sync"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/view"
)

// Progress shows the rows of a sweep as its regions answer. It draws only
// when it is enabled, for a table on a terminal; every other output prints
// once, at the end.
type Progress struct {
	ios     *cmdutil.IOStreams
	regions int // the regions of the sweep
	// render returns the lines of the table for the snapshots so far.
	render func(snaps []*fleet.Snapshot, answered int) []string
	table  *view.Live
	status *view.Live

	mu       sync.Mutex
	answered int
	snaps    []*fleet.Snapshot
}

// NewProgress returns the progress of a sweep of some regions. render
// returns the table for the snapshots that have arrived, in the final
// order, and for the number of regions that have answered. With enabled
// false, or when stdout is not a terminal that can redraw, the progress
// draws nothing.
func NewProgress(ios *cmdutil.IOStreams, enabled bool, regions int, render func(snaps []*fleet.Snapshot, answered int) []string) *Progress {
	p := &Progress{ios: ios, regions: regions, render: render}
	if !enabled || !ios.OutTTY || ios.Dumb {
		return p
	}
	p.table = view.NewLive(ios.Out, ios.Size)
	if ios.ErrTTY {
		// The terminal behind stderr is the one behind stdout here.
		p.status = view.NewLive(ios.ErrOut, ios.Size)
	}
	return p
}

// Add takes the snapshots of one region and redraws the table with every
// row so far. A table taller than the terminal cannot be redrawn in place:
// one status line on stderr stands in for it. Add is the OnRegion function
// of the sweep.
func (p *Progress) Add(_ string, snaps []*fleet.Snapshot) {
	if p.table == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answered++
	p.snaps = append(p.snaps, snaps...)
	if p.table.Draw(p.render(p.snaps, p.answered)) || p.status == nil {
		return
	}
	p.status.Draw([]string{p.ios.ErrTheme().Dim(fmt.Sprintf("%d of %d regions, %d clusters", p.answered, p.regions, len(p.snaps)))})
}

// Clear removes what Add drew, so that the final table prints once.
func (p *Progress) Clear() {
	if p.table != nil {
		p.table.Clear()
	}
	if p.status != nil {
		p.status.Clear()
	}
}
