package ui

import (
	"time"

	"github.com/fatih/color"
	"github.com/pterm/pterm"
)

// MultiProgressManager manages multiple concurrent progress indicators
type MultiProgressManager struct {
	multi    pterm.MultiPrinter
	spinners []*pterm.SpinnerPrinter
	bars     []*pterm.ProgressbarPrinter
}

// NewMultiProgressManager creates a new multi-progress manager
func NewMultiProgressManager() *MultiProgressManager {
	return &MultiProgressManager{
		multi:    pterm.DefaultMultiPrinter,
		spinners: make([]*pterm.SpinnerPrinter, 0),
		bars:     make([]*pterm.ProgressbarPrinter, 0),
	}
}

// AddSpinner adds a spinner to the multi-progress display
func (mpm *MultiProgressManager) AddSpinner(message string) *pterm.SpinnerPrinter {
	spinner := pterm.DefaultSpinner.
		WithStyle(&pterm.Style{pterm.FgCyan}).
		WithText(message).
		WithSequence("⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏").
		WithDelay(100 * time.Millisecond).
		WithWriter(mpm.multi.NewWriter())

	mpm.spinners = append(mpm.spinners, spinner)
	return spinner
}

// AddProgressBar adds a progress bar to the multi-progress display
func (mpm *MultiProgressManager) AddProgressBar(total int, title string) *pterm.ProgressbarPrinter {
	bar := pterm.DefaultProgressbar.
		WithTotal(total).
		WithTitle(title).
		WithBarStyle(&pterm.Style{pterm.FgCyan}).
		WithTitleStyle(&pterm.Style{pterm.FgYellow}).
		WithShowCount(true).
		WithShowPercentage(true).
		WithWriter(mpm.multi.NewWriter())

	mpm.bars = append(mpm.bars, bar)
	return bar
}

// Start begins all progress indicators
func (mpm *MultiProgressManager) Start() error {
	for i, spinner := range mpm.spinners {
		started, _ := spinner.Start()
		mpm.spinners[i] = started
	}
	for i, bar := range mpm.bars {
		started, _ := bar.Start()
		mpm.bars[i] = started
	}
	_, _ = mpm.multi.Start()
	return nil
}

// Stop ends all progress indicators
func (mpm *MultiProgressManager) Stop() { _, _ = mpm.multi.Stop() }

// RegionProgressTracker tracks progress across multiple AWS regions
type RegionProgressTracker struct {
	manager      *MultiProgressManager
	regionBars   map[string]*pterm.ProgressbarPrinter
	totalRegions int
	completed    int
}

// NewRegionProgressTracker creates a tracker for multi-region operations
func NewRegionProgressTracker(regions []string, _ string) *RegionProgressTracker {
	manager := NewMultiProgressManager()
	tracker := &RegionProgressTracker{
		manager:      manager,
		regionBars:   make(map[string]*pterm.ProgressbarPrinter),
		totalRegions: len(regions),
	}
	for _, region := range regions {
		tracker.regionBars[region] = manager.AddProgressBar(1, "Querying "+region+"...")
	}
	return tracker
}

// Start begins tracking multi-region progress
func (rpt *RegionProgressTracker) Start() error { return rpt.manager.Start() }

// CompleteRegion marks a region as completed
func (rpt *RegionProgressTracker) CompleteRegion(region string, clusterCount int) {
	if bar, exists := rpt.regionBars[region]; exists {
		if clusterCount > 0 {
			bar.UpdateTitle(color.GreenString("Found %d clusters in %s", clusterCount, region))
		} else {
			bar.UpdateTitle("No clusters in " + region)
		}
		bar.Increment()
		rpt.completed++
	}
}

// IsComplete returns true if all regions are complete
func (rpt *RegionProgressTracker) IsComplete() bool { return rpt.completed >= rpt.totalRegions }

// Stop ends the multi-region progress tracking
func (rpt *RegionProgressTracker) Stop() { rpt.manager.Stop() }
