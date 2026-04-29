package ui

import (
	"context"
	"time"

	"github.com/fatih/color"
	"github.com/pterm/pterm"
)

// ProgressSpinner wraps pterm.DefaultSpinner with refresh CLI styling
type ProgressSpinner struct {
	spinner *pterm.SpinnerPrinter
	message string
}

// NewProgressSpinner creates a new progress spinner with consistent styling
func NewProgressSpinner(message string) *ProgressSpinner {
	spinner := pterm.DefaultSpinner.
		WithStyle(&pterm.Style{pterm.FgCyan}).
		WithSequence("⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏").
		WithDelay(100 * time.Millisecond).
		WithRemoveWhenDone(false)

	return &ProgressSpinner{spinner: spinner, message: message}
}

// Start begins the spinner animation and returns a stop function.
func (ps *ProgressSpinner) Start(_ context.Context) func() {
	started, _ := ps.spinner.Start(ps.message)
	ps.spinner = started
	return func() { _ = ps.spinner.Stop() }
}

// UpdateText changes the spinner message
func (ps *ProgressSpinner) UpdateText(message string) { ps.spinner.UpdateText(message) }

// Success completes the spinner with a success message
func (ps *ProgressSpinner) Success(message string) { ps.spinner.Success(message) }

// Fail completes the spinner with a failure message
func (ps *ProgressSpinner) Fail(message string) { ps.spinner.Fail(message) }

// Warning completes the spinner with a warning message
func (ps *ProgressSpinner) Warning(message string) { ps.spinner.Warning(message) }

// Stop stops the spinner, optionally printing a success message.
func (ps *ProgressSpinner) Stop(message string) {
	if message != "" {
		ps.spinner.Success(message)
	} else {
		_ = ps.spinner.Stop()
	}
}

// NewPtermHealthSpinner creates a spinner for health checks.
func NewPtermHealthSpinner(message string) *ProgressSpinner {
	return NewProgressSpinner(message)
}

// ProgressBar wraps pterm.DefaultProgressbar with refresh CLI styling
type ProgressBar struct {
	bar *pterm.ProgressbarPrinter
}

// NewProgressBar creates a new progress bar with consistent styling
func NewProgressBar(total int, title string) *ProgressBar {
	bar := pterm.DefaultProgressbar.
		WithTotal(total).
		WithTitle(title).
		WithBarStyle(&pterm.Style{pterm.FgCyan}).
		WithTitleStyle(&pterm.Style{pterm.FgYellow}).
		WithShowCount(true).
		WithShowPercentage(true)

	return &ProgressBar{bar: bar}
}

// Start begins the progress bar display
func (pb *ProgressBar) Start() error {
	started, err := pb.bar.Start()
	pb.bar = started
	return err
}

// Increment advances the progress by 1
func (pb *ProgressBar) Increment() { pb.bar.Increment() }

// Add advances the progress by n
func (pb *ProgressBar) Add(n int) { pb.bar.Add(n) }

// UpdateTitle changes the progress bar title
func (pb *ProgressBar) UpdateTitle(title string) { pb.bar.UpdateTitle(title) }

// Stop completes the progress bar
func (pb *ProgressBar) Stop() { _, _ = pb.bar.Stop() }

// PerformanceTimer tracks and displays operation timing
type PerformanceTimer struct {
	startTime time.Time
	operation string
}

// NewPerformanceTimer creates a new performance timer
func NewPerformanceTimer(operation string) *PerformanceTimer {
	return &PerformanceTimer{startTime: time.Now(), operation: operation}
}

// Elapsed returns the time elapsed since creation
func (pt *PerformanceTimer) Elapsed() time.Duration { return time.Since(pt.startTime) }

// PrintElapsed displays the elapsed time with consistent formatting
func (pt *PerformanceTimer) PrintElapsed() {
	pterm.Success.Printf("%s in %s\n", pt.operation, color.GreenString(pt.Elapsed().String()))
}
