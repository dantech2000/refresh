package ui

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/pterm/pterm"
)

var funSpinnerInterval = 2 * time.Second

// spinnerOutputIsTerminal reports whether spinner output (stderr) is an
// interactive terminal. Overridable in tests.
var spinnerOutputIsTerminal = func() bool {
	return isatty.IsTerminal(os.Stderr.Fd()) || isatty.IsCygwinTerminal(os.Stderr.Fd())
}

// FunSpinner provides an entertaining spinner with rotating messages
type FunSpinner struct {
	spinner   *pterm.SpinnerPrinter
	messages  []string
	current   int
	startedAt time.Time
	stopCh    chan struct{}
	doneCh    chan struct{}
	stopOnce  sync.Once
	started   bool
	animated  bool
	mu        sync.Mutex
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
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// NewFunSpinnerForCategory creates a fun spinner with messages for a specific category
func NewFunSpinnerForCategory(category string) *FunSpinner {
	return NewFunSpinner(DefaultFunMessages.GetMessages(category))
}

// Start begins the fun spinner with rotating messages. When output is not an
// interactive terminal (CI logs, redirected stderr), no animation is started:
// the \r and \033[K control sequences would just spam the log. Success
// still prints its final line.
func (fs *FunSpinner) Start() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if fs.started {
		return nil
	}

	fs.started = true
	fs.startedAt = time.Now()

	if !spinnerOutputIsTerminal() {
		return nil
	}

	fs.animated = true
	fs.renderFrame(fs.spinner.Sequence[0])

	go fs.render()
	return nil
}

func (fs *FunSpinner) render() {
	frameTicker := time.NewTicker(fs.spinner.Delay)
	messageTicker := time.NewTicker(funSpinnerInterval)
	defer frameTicker.Stop()
	defer messageTicker.Stop()
	defer close(fs.doneCh)

	frame := 1
	for {
		select {
		case <-fs.stopCh:
			fs.clearLine()
			return
		case <-messageTicker.C:
			fs.current = (fs.current + 1) % len(fs.messages)
		case <-frameTicker.C:
			sequence := fs.spinner.Sequence[frame%len(fs.spinner.Sequence)]
			frame++
			fs.renderFrame(sequence)
		}
	}
}

func (fs *FunSpinner) renderFrame(sequence string) {
	timer := ""
	if fs.spinner.ShowTimer {
		elapsed := time.Since(fs.startedAt).Round(fs.spinner.TimerRoundingFactor)
		timer = fmt.Sprintf(" (%s)", elapsed)
	}

	pterm.Fprinto(
		fs.spinner.Writer,
		"\033[K",
		fs.spinner.Style.Sprint(sequence),
		" ",
		fs.spinner.MessageStyle.Sprint(fs.messages[fs.current]),
		fs.spinner.TimerStyle.Sprint(timer),
	)
}

func (fs *FunSpinner) clearLine() {
	pterm.Fprinto(fs.spinner.Writer, "\033[K")
}

// Success completes the spinner with a success message
func (fs *FunSpinner) Success(message string) {
	fs.stop()
	pterm.Success.WithWriter(fs.spinner.Writer).Println(message)
}

// Stop stops the spinner
func (fs *FunSpinner) Stop() {
	fs.stop()
}

func (fs *FunSpinner) stop() {
	fs.mu.Lock()
	wasStarted := fs.started
	animated := fs.animated
	fs.mu.Unlock()

	if !wasStarted {
		return
	}

	fs.stopOnce.Do(func() {
		if animated {
			close(fs.stopCh)
			<-fs.doneCh
		}
	})
}

// FunMessages provides categorized entertaining messages for different operations
type FunMessages struct {
	Cluster   []string
	Nodegroup []string
	Addon     []string
	General   []string
	Health    []string
	Workload  []string
}

// GetMessages returns messages for the specified category, falling back to general.
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
	case "workload":
		return fm.Workload
	default:
		return fm.General
	}
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
	Workload: []string{
		"Checking workload disruption safety...",
		"Scanning namespaces for PDB coverage...",
		"Matching PDB selectors to deployments...",
		"Reviewing workload maintenance readiness...",
		"Looking for deployments without disruption budgets...",
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
