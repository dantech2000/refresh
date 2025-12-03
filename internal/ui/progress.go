package ui

import (
	"context"
	"math/rand"
	"sync"
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
		WithSequence("⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"). // yacspin CharSet 14 equivalent
		WithDelay(100 * time.Millisecond).
		WithRemoveWhenDone(false) // Ensure success messages replace the spinner line

	return &ProgressSpinner{
		spinner: spinner,
		message: message,
	}
}

// Start begins the spinner animation
func (ps *ProgressSpinner) Start(ctx context.Context) func() {
	started, _ := ps.spinner.Start(ps.message)
	ps.spinner = started

	// Return a cancellation function
	return func() {
		_ = ps.spinner.Stop()
	}
}

// UpdateText changes the spinner message
func (ps *ProgressSpinner) UpdateText(message string) {
	ps.spinner.UpdateText(message)
}

// Success completes the spinner with a success message
func (ps *ProgressSpinner) Success(message string) {
	ps.spinner.Success(message)
}

// Fail completes the spinner with a failure message
func (ps *ProgressSpinner) Fail(message string) {
	ps.spinner.Fail(message)
}

// Warning completes the spinner with a warning message
func (ps *ProgressSpinner) Warning(message string) {
	ps.spinner.Warning(message)
}

// Stop stops the spinner with an optional success message
func (ps *ProgressSpinner) Stop(message string) {
	if message != "" {
		ps.spinner.Success(message)
	} else {
		_ = ps.spinner.Stop()
	}
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

	return &ProgressBar{
		bar: bar,
	}
}

// Start begins the progress bar display
func (pb *ProgressBar) Start() error {
	started, err := pb.bar.Start()
	pb.bar = started
	return err
}

// Increment advances the progress by 1
func (pb *ProgressBar) Increment() {
	pb.bar.Increment()
}

// Add advances the progress by n
func (pb *ProgressBar) Add(n int) {
	pb.bar.Add(n)
}

// UpdateTitle changes the progress bar title
func (pb *ProgressBar) UpdateTitle(title string) {
	pb.bar.UpdateTitle(title)
}

// Stop completes the progress bar
func (pb *ProgressBar) Stop() {
	_, _ = pb.bar.Stop()
}

// MultiProgressManager manages multiple concurrent progress indicators
type MultiProgressManager struct {
	multi    pterm.MultiPrinter
	spinners []*pterm.SpinnerPrinter
	bars     []*pterm.ProgressbarPrinter
}

// NewMultiProgressManager creates a new multi-progress manager
func NewMultiProgressManager() *MultiProgressManager {
	multi := pterm.DefaultMultiPrinter
	return &MultiProgressManager{
		multi:    multi,
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
	// Start all spinners
	for i, spinner := range mpm.spinners {
		started, err := spinner.Start()
		if err != nil {
			return err
		}
		mpm.spinners[i] = started
	}

	// Start all progress bars
	for i, bar := range mpm.bars {
		started, err := bar.Start()
		if err != nil {
			return err
		}
		mpm.bars[i] = started
	}

	// Start the multi-printer
	_, _ = mpm.multi.Start()
	return nil
}

// Stop ends all progress indicators
func (mpm *MultiProgressManager) Stop() {
	_, _ = mpm.multi.Stop()
}

// RegionProgressTracker tracks progress across multiple AWS regions
type RegionProgressTracker struct {
	manager      *MultiProgressManager
	regionBars   map[string]*pterm.ProgressbarPrinter
	totalRegions int
	completed    int
}

// NewRegionProgressTracker creates a tracker for multi-region operations
func NewRegionProgressTracker(regions []string, title string) *RegionProgressTracker {
	manager := NewMultiProgressManager()
	tracker := &RegionProgressTracker{
		manager:      manager,
		regionBars:   make(map[string]*pterm.ProgressbarPrinter),
		totalRegions: len(regions),
		completed:    0,
	}

	// Create progress bars for each region
	for _, region := range regions {
		bar := manager.AddProgressBar(1, "Querying "+region+"...")
		tracker.regionBars[region] = bar
	}

	return tracker
}

// Start begins tracking multi-region progress
func (rpt *RegionProgressTracker) Start() error {
	return rpt.manager.Start()
}

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
func (rpt *RegionProgressTracker) IsComplete() bool {
	return rpt.completed >= rpt.totalRegions
}

// Stop ends the multi-region progress tracking
func (rpt *RegionProgressTracker) Stop() {
	rpt.manager.Stop()
}

// PerformanceTimer tracks and displays operation timing
type PerformanceTimer struct {
	startTime time.Time
	operation string
}

// NewPerformanceTimer creates a new performance timer
func NewPerformanceTimer(operation string) *PerformanceTimer {
	return &PerformanceTimer{
		startTime: time.Now(),
		operation: operation,
	}
}

// Elapsed returns the time elapsed since creation
func (pt *PerformanceTimer) Elapsed() time.Duration {
	return time.Since(pt.startTime)
}

// PrintElapsed displays the elapsed time with consistent formatting
func (pt *PerformanceTimer) PrintElapsed() {
	elapsed := pt.Elapsed()
	pterm.Success.Printf("%s in %s\n", pt.operation, color.GreenString(elapsed.String()))
}

// NewPtermHealthSpinner creates a spinner for health checks (replaces yacspin version)
func NewPtermHealthSpinner(message string) *ProgressSpinner {
	return NewProgressSpinner(message)
}

// FunSpinner provides an entertaining spinner with rotating messages
type FunSpinner struct {
	spinner  *pterm.SpinnerPrinter
	messages []string
	current  int
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
	started  bool
	mu       sync.Mutex
}

// NewFunSpinner creates a spinner that rotates through fun messages
func NewFunSpinner(messages []string) *FunSpinner {
	if len(messages) == 0 {
		messages = []string{"Working on it..."}
	}

	spinner := pterm.DefaultSpinner.
		WithStyle(&pterm.Style{pterm.FgCyan}).
		WithSequence("⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏").
		WithDelay(100 * time.Millisecond).
		WithRemoveWhenDone(false)

	return &FunSpinner{
		spinner:  spinner,
		messages: messages,
		current:  0,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
		started:  false,
	}
}

// Start begins the fun spinner with rotating messages
func (fs *FunSpinner) Start() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if fs.started {
		return nil // Already started
	}

	started, err := fs.spinner.Start(fs.messages[0])
	if err != nil {
		return err
	}
	fs.spinner = started
	fs.started = true

	// Start message rotation in background
	go fs.rotateMessages()

	return nil
}

// rotateMessages cycles through the fun messages every 2 seconds
func (fs *FunSpinner) rotateMessages() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer close(fs.doneCh)

	for {
		select {
		case <-fs.stopCh:
			return
		case <-ticker.C:
			fs.current = (fs.current + 1) % len(fs.messages)
			fs.spinner.UpdateText(fs.messages[fs.current])
		}
	}
}

// Success completes the spinner with a success message
func (fs *FunSpinner) Success(message string) {
	fs.stop()
	fs.spinner.Success(message)
}

// Fail completes the spinner with a failure message
func (fs *FunSpinner) Fail(message string) {
	fs.stop()
	fs.spinner.Fail(message)
}

// Stop stops the spinner
func (fs *FunSpinner) Stop() {
	fs.stop()
	_ = fs.spinner.Stop()
}

func (fs *FunSpinner) stop() {
	fs.mu.Lock()
	wasStarted := fs.started
	fs.mu.Unlock()

	// Only wait for goroutine if spinner was started
	if !wasStarted {
		return
	}

	fs.stopOnce.Do(func() {
		close(fs.stopCh)
		<-fs.doneCh // wait for rotation goroutine to finish
	})
}

// FunMessages provides categorized entertaining messages for different operations
type FunMessages struct {
	Cluster   []string
	Nodegroup []string
	Addon     []string
	General   []string
	Health    []string
}

// GetMessages returns messages for the specified category, falling back to general if category is empty
func (fm *FunMessages) GetMessages(category string) []string {
	switch category {
	case "cluster":
		return fm.Cluster
	case "nodegroup":
		return fm.Nodegroup
	case "addon":
		return fm.Addon
	case "health":
		return fm.Health
	default:
		return fm.General
	}
}

// GetRandomMessage returns a random message from the specified category
func (fm *FunMessages) GetRandomMessage(category string) string {
	messages := fm.GetMessages(category)
	if len(messages) == 0 {
		return "Working on it..."
	}
	return messages[rand.Intn(len(messages))]
}

// DefaultFunMessages provides the default set of entertaining messages
var DefaultFunMessages = &FunMessages{
	Cluster: []string{
		"Exploring the AWS universe...",
		"Hunting for clusters across regions...",
		"Surfing the cloud waves...",
		"Mapping your EKS empire...",
		"Launching region scanners...",
		"Targeting your clusters...",
		"Supercharging the search...",
		"Analyzing cluster DNA...",
		"Collecting cloud treasures...",
		"Performing AWS magic tricks...",
		"Querying the cluster overlords...",
		"Decoding EKS hieroglyphics...",
		"Asking AWS nicely for cluster secrets...",
		"Convincing clusters to reveal themselves...",
	},
	Nodegroup: []string{
		"Interrogating EKS nodes... they're staying silent for now",
		"Asking AWS politely for nodegroup secrets...",
		"Counting how many nodes are having an existential crisis...",
		"Checking if the nodes have been doing their AMI homework...",
		"Waiting for AWS to stop procrastinating and return our data...",
		"Convincing stubborn nodes to reveal their status...",
		"Performing digital archaeology on your cluster...",
		"Teaching nodes to communicate in human language...",
		"Decoding the ancient art of EKS hieroglyphics...",
		"Bribing AWS APIs with virtual coffee for faster responses...",
		"Whispering sweet nothings to unresponsive nodegroups...",
		"Negotiating with nodes that refuse to scale...",
		"Analyzing node behavior patterns like a digital therapist...",
	},
	Addon: []string{
		"Hunting for add-ons in the AWS wilderness...",
		"Asking add-ons to introduce themselves politely...",
		"Checking if add-ons are playing hide and seek...",
		"Interrogating the add-on registry...",
		"Convincing shy add-ons to show their versions...",
		"Performing add-on archaeology...",
		"Deciphering add-on compatibility matrices...",
		"Bribing add-ons with virtual upgrades...",
	},
	Health: []string{
		"Checking cluster vital signs...",
		"Performing AWS wellness checkup...",
		"Taking cluster temperature...",
		"Analyzing cluster stress levels...",
		"Consulting the cloud doctor...",
		"Running diagnostic spells...",
		"Checking if your cluster needs vitamins...",
		"Performing digital CPR if needed...",
	},
	General: []string{
		"Working the AWS magic...",
		"Consulting the cloud spirits...",
		"Performing digital incantations...",
		"Asking AWS very nicely...",
		"Waiting for the cloud gods to respond...",
		"Processing with maximum efficiency...",
		"Doing the technical mumbo jumbo...",
		"Making API calls like a pro...",
	},
}

// NewFunSpinnerForCategory creates a fun spinner with messages for a specific category
func NewFunSpinnerForCategory(category string) *FunSpinner {
	messages := DefaultFunMessages.GetMessages(category)
	return NewFunSpinner(messages)
}

// NewEnhancedProgressSpinner creates a spinner that cycles through fun messages for the category
func NewEnhancedProgressSpinner(category string) *FunSpinner {
	return NewFunSpinnerForCategory(category)
}
