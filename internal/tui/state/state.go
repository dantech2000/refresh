// Package state is the data contract between the refresh TUI and whatever
// feeds it. A Backend owns the fleet, its checks, and the changes in flight,
// and hands the TUI an immutable State to draw on every frame. The TUI never
// talks to AWS or Kubernetes itself.
//
// Two backends are planned: internal/sim, a deterministic simulated fleet
// (refresh ui --simulate), and a live backend built on the real services.
package state

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/noderoll"
)

// Level is the severity of an event or a log line. It maps onto the status
// tokens of internal/render: each level has one glyph and one color.
type Level int

const (
	LevelInfo     Level = iota // •
	LevelOK                    // ●
	LevelWarn                  // ▲
	LevelError                 // ✗
	LevelProgress              // ◷
	LevelDone                  // ✓ finished, no longer interesting
)

// Source is where an event came from. The TUI log panes filter on it.
type Source int

const (
	SourceRoll    Source = iota // node lifecycle during a nodegroup roll
	SourceKube                  // Kubernetes events
	SourceAWS                   // AWS API calls
	SourceUpgrade               // cluster upgrade orchestrator
	SourceCheck                 // readiness checks
	SourceAddon                 // add-on updates
)

// Event is one line in a feed or a log.
type Event struct {
	// Seq orders events across every list in a State: a later event has a
	// larger Seq. The TUI freezes its live panes at a Seq, since several
	// events can share one timestamp.
	Seq     uint64
	At      time.Time
	Cluster string
	Source  Source
	Level   Level
	// Subject is what the event is about: a node, a pod, an add-on, a phase.
	Subject string
	Text    string
	// Detail is secondary text, drawn dim after Text.
	Detail string
}

// CheckStatus is the state of one readiness check or health gate.
type CheckStatus int

const (
	CheckPending CheckStatus = iota
	CheckRunning
	CheckPass
	CheckWarn
	CheckFail
)

// State is everything the TUI draws, captured at one instant. A Backend
// returns a fresh copy from every State call; the TUI may keep it.
type State struct {
	Now time.Time
	// Seq is the largest Event.Seq in the state.
	Seq uint64
	// Backend names the data source ("simulated", "live").
	Backend string
	// Badge, when set, is shown in the top bar ("SIMULATED", "READ-ONLY").
	Badge   string
	Context string
	Profile string
	// RegionsAnswered of RegionsTotal regions answered the last sweep.
	RegionsAnswered, RegionsTotal int
	SyncedAt                      time.Time
	// FleetProblem, when set, is why the last sweep read no region: bad or
	// expired credentials, or regions closed to the account. It may span
	// several lines.
	FleetProblem string

	Clusters []Cluster
	// Feed is the fleet-wide event feed, oldest first.
	Feed []Event
	// Log is the fleet-wide AWS API call log, oldest first.
	Log []Event
	// Readiness holds the last readiness run per cluster.
	Readiness map[string]Readiness
	// Rolls are the nodegroup rolls, running and finished, oldest first.
	Rolls []Roll
	// Upgrades are the cluster upgrades, running and finished, oldest first.
	Upgrades []Upgrade
}

// Cluster is one EKS cluster in the fleet.
type Cluster struct {
	Name    string
	Region  string
	ARN     string
	Version string
	// Latest is the newest Kubernetes version EKS offers.
	Latest string
	// ExtendedSupport is set when Version is past standard support.
	ExtendedSupport bool
	// SupportEnds is when standard support for Version ends.
	SupportEnds time.Time
	Nodegroups  []Nodegroup
	Addons      []Addon
	// Busy names the change in flight, if any ("upgrading", "rolling
	// ng-general", "updating add-ons").
	Busy string
	// Incomplete is set when part of the cluster could not be read, so its
	// counts may be missing something.
	Incomplete bool
}

// Nodegroup is a managed nodegroup.
type Nodegroup struct {
	Name    string
	Version string
	// AMI is the release version (or image ID) the nodes run; LatestAMI is
	// the newest one for Version, when the backend knows it.
	AMI, LatestAMI string
	// AMIStale is set when the backend knows the AMI is out of date without
	// knowing the newest release by name.
	AMIStale bool
	// AMIUnknown is set when the AMI's status could not be read (for
	// example, no SSM access to the recommended-AMI parameters).
	AMIUnknown bool
	Nodes      int
	Status     string
}

// Addon is an installed EKS add-on.
type Addon struct {
	Name    string
	Version string
	// Latest is the newest version compatible with the cluster's version.
	Latest string
	Status string
}

// Readiness is one run of the upgrade readiness checks.
type Readiness struct {
	Cluster   string
	From, To  string
	StartedAt time.Time
	Running   bool
	Checks    []Check
	// Log is the check log (AWS calls and results), oldest first.
	Log []Event
}

// Check is one readiness check.
type Check struct {
	Group   string
	Name    string
	Status  CheckStatus
	Summary string
	// Detail and Fix fill the detail pane when the check is selected.
	Detail []string
	Table  *Table
	Fix    []string
	Source string
}

// Table is a small table in a check's detail pane.
type Table struct {
	Header []string
	Rows   [][]string
}

// Pod is a pod on a draining node.
type Pod struct {
	Name   string
	State  PodState
	Reason string
}

// PodState is where a pod is in its eviction.
type PodState int

const (
	PodRunning PodState = iota
	PodTerminating
	PodBlocked
	PodDaemonSet
)

// Gate is one health gate watched during a change.
type Gate struct {
	Name   string
	Status CheckStatus
}

// Roll is one managed-nodegroup rolling update.
type Roll struct {
	Cluster, Nodegroup string
	FromVersion        string
	ToVersion          string
	FromAMI, ToAMI     string
	MaxUnavailable     int
	// MaxUnavailableText, when set, is shown instead of MaxUnavailable (a
	// percentage, or "unknown").
	MaxUnavailableText string
	StartedAt          time.Time
	// EndedAt is zero while the roll runs.
	EndedAt time.Time
	// ETA is the estimated time left; zero when unknown or done.
	ETA time.Duration
	// Planned is the number of old nodes the roll replaces.
	Planned  int
	Snapshot noderoll.Snapshot
	// NodePods is the pod count per node that is not draining.
	NodePods map[string]int
	// Pods lists the pods left on each draining node.
	Pods  map[string][]Pod
	Gates []Gate
	// Events is the roll's own feed: node lifecycle (from noderoll.Tracker),
	// Kubernetes events, and AWS calls, oldest first.
	Events []Event
	Failed string
	// UpgradeOf names the cluster upgrade that started this roll, if any.
	UpgradeOf string
}

// Running reports whether the roll is still in flight.
func (r Roll) Running() bool { return r.EndedAt.IsZero() }

// Replaced is the number of old nodes already gone.
func (r Roll) Replaced() int {
	old := 0
	for _, n := range r.Snapshot.Nodes {
		if !n.OnTarget {
			old++
		}
	}
	if d := r.Planned - old; d > 0 {
		return d
	}
	return 0
}

// PhaseStatus is where an upgrade phase is.
type PhaseStatus int

const (
	PhasePending PhaseStatus = iota
	PhaseRunning
	PhaseDone
	PhaseFailed
	PhaseSkipped
	// PhaseStopped is a phase a stop request ended early.
	PhaseStopped
)

// Upgrade is one cluster upgrade run by the orchestrator.
type Upgrade struct {
	Cluster   string
	From, To  string
	StartedAt time.Time
	EndedAt   time.Time
	Phases    []Phase
	// StopAfter stops the run once the current nodegroup finishes.
	StopAfter bool
	// Paused holds the run before its next phase.
	Paused  bool
	Stopped bool
	Failed  string
	Events  []Event
	// Question, when set, is a decision the run waits on (health warnings
	// before a roll, a plan that changed). Backend.Answer gives it.
	Question string
}

// Running reports whether the upgrade is still in flight.
func (u Upgrade) Running() bool { return u.EndedAt.IsZero() }

// Current returns the index of the running phase, or -1.
func (u Upgrade) Current() int {
	for i, p := range u.Phases {
		if p.Status == PhaseRunning {
			return i
		}
	}
	return -1
}

// Progress is the share of the whole upgrade that is done, 0..1.
func (u Upgrade) Progress() float64 {
	var total, done float64
	for _, p := range u.Phases {
		total += p.Weight
		switch p.Status {
		case PhaseDone, PhaseSkipped:
			done += p.Weight
		case PhaseRunning:
			done += p.Weight * p.Progress
		}
	}
	if total == 0 {
		return 0
	}
	return done / total
}

// Phase is one step of an upgrade: pre-flight, control plane, add-ons,
// nodegroups, verify.
type Phase struct {
	Name      string
	Status    PhaseStatus
	StartedAt time.Time
	EndedAt   time.Time
	Summary   string
	Items     []PhaseItem
	// Progress is 0..1 within the phase; Weight is its share of the run.
	Progress float64
	Weight   float64
}

// PhaseItem is one unit inside a phase: an add-on, a nodegroup.
type PhaseItem struct {
	Name     string
	Status   PhaseStatus
	Text     string
	Progress float64
}

// ActionKind is a change the TUI can ask a Backend for.
type ActionKind int

const (
	ActionRoll    ActionKind = iota // nodegroup update
	ActionAddons                    // update every stale add-on
	ActionUpgrade                   // cluster upgrade to the next minor
)

// Action is a change request.
type Action struct {
	Kind      ActionKind
	Cluster   string
	Nodegroup string
}

// Plan is the dry run of an Action, shown before the TUI asks to start it.
type Plan struct {
	Action Action
	Title  string
	// Changes are before/after pairs ("AMI release", from, to).
	Changes []Change
	// Facts are plain key/value lines (nodes, estimate).
	Facts []Fact
	Gates []PlanGate
	// Blocked is set when a gate stops the action.
	Blocked string
	// Command is the CLI equivalent.
	Command string
}

// Change is one before/after line of a Plan.
type Change struct{ Field, From, To string }

// Fact is one key/value line of a Plan.
type Fact struct{ Key, Value, Note string }

// PlanGate is one pre-flight gate of a Plan.
type PlanGate struct {
	Status CheckStatus
	Text   string
	Note   string
}

// Backend feeds the TUI. The TUI calls it from Bubble Tea commands, off the
// UI loop, so a call may block on the network; each one must honor ctx.
// Every method is safe for concurrent use.
type Backend interface {
	// State returns a snapshot of everything the TUI draws. The snapshot
	// shares no memory with the backend: the TUI may keep and change it.
	State(ctx context.Context) (State, error)
	// Plan dry-runs an action. It changes nothing.
	Plan(ctx context.Context, a Action) (Plan, error)
	// Start runs an action after checking its plan again, atomically: a
	// gate that closed since the dry run still blocks it.
	Start(ctx context.Context, a Action) error
	// RunReadiness starts (or restarts) the readiness checks for cluster.
	RunReadiness(ctx context.Context, cluster string) error
	// StopAfterCurrent toggles a request to stop a cluster upgrade once the
	// current step finishes. An EKS update in flight is never cancelled.
	StopAfterCurrent(ctx context.Context, cluster string) error
	// TogglePause holds or releases a cluster upgrade before its next phase.
	TogglePause(ctx context.Context, cluster string) error
	// Answer gives the decision a cluster upgrade waits on (its Question):
	// yes goes on, no stops the run after the current step.
	Answer(ctx context.Context, cluster string, yes bool) error
}

// Minor returns the minor number of a "1.NN" version, or -1.
func Minor(v string) int {
	_, after, ok := strings.Cut(v, ".")
	if !ok {
		return -1
	}
	if i := strings.IndexByte(after, '.'); i >= 0 {
		after = after[:i]
	}
	n, err := strconv.Atoi(after)
	if err != nil {
		return -1
	}
	return n
}

// NextMinor returns the version one minor after v ("1.31" → "1.32").
func NextMinor(v string) string {
	major, _, _ := strings.Cut(v, ".")
	m := Minor(v)
	if m < 0 {
		return ""
	}
	return major + "." + strconv.Itoa(m+1)
}

// Behind is how many minors the cluster is behind Latest.
func (c Cluster) Behind() int {
	a, b := Minor(c.Version), Minor(c.Latest)
	if a < 0 || b < 0 || b < a {
		return 0
	}
	return b - a
}

// StaleNodegroups lists nodegroups whose AMI or version is out of date.
func (c Cluster) StaleNodegroups() []Nodegroup {
	var out []Nodegroup
	for _, ng := range c.Nodegroups {
		if ng.NeedsPatch(c.Version) {
			out = append(out, ng)
		}
	}
	return out
}

// NeedsPatch reports whether the nodegroup trails the control plane or its
// AMI is not the newest.
func (ng Nodegroup) NeedsPatch(clusterVersion string) bool {
	return ng.Version != clusterVersion || ng.AMIStale || (ng.LatestAMI != "" && ng.AMI != ng.LatestAMI)
}

// StaleAddons lists add-ons that have a newer compatible version.
func (c Cluster) StaleAddons() []Addon {
	var out []Addon
	for _, a := range c.Addons {
		if a.Latest != "" && a.Version != a.Latest {
			out = append(out, a)
		}
	}
	return out
}

// NeedsAttention reports whether the cluster has anything to act on, whether
// or not a change is running on it.
func (c Cluster) NeedsAttention() bool {
	c.Busy = ""
	lvl, _ := c.Health()
	return lvl == LevelWarn || lvl == LevelError
}

// Health is the one-token verdict for a cluster row.
func (c Cluster) Health() (Level, string) {
	switch {
	case c.Busy != "":
		return LevelProgress, c.Busy
	case c.ExtendedSupport:
		return LevelError, "extended support"
	case c.Behind() == 1:
		return LevelWarn, "1 minor behind"
	case c.Behind() > 1:
		return LevelWarn, strconv.Itoa(c.Behind()) + " minors behind"
	case len(c.StaleNodegroups()) > 0:
		return LevelWarn, "AMI patches"
	case len(c.StaleAddons()) > 0:
		return LevelWarn, "add-on updates"
	default:
		return LevelOK, "current"
	}
}

// Blockers counts failed checks.
func (r Readiness) Blockers() int { return r.count(CheckFail) }

// Warnings counts warning checks.
func (r Readiness) Warnings() int { return r.count(CheckWarn) }

// Passed counts passed checks.
func (r Readiness) Passed() int { return r.count(CheckPass) }

// Pending counts checks not finished yet.
func (r Readiness) Pending() int { return r.count(CheckPending) + r.count(CheckRunning) }

func (r Readiness) count(s CheckStatus) int {
	n := 0
	for _, c := range r.Checks {
		if c.Status == s {
			n++
		}
	}
	return n
}
