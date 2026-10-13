//go:build unix

package parity

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Scenario is one command line run on one world.
type Scenario struct {
	Name string
	// Command and Behaviour place the scenario in the behaviour matrix (see
	// matrix). A scenario with a Behaviour passes that behaviour's checks.
	// One without shows something particular to its command, and says what
	// in Checks.
	Command   Command
	Behaviour Behaviour
	// Checks are what the scenario means, as assertions on its trace,
	// besides the checks of its Behaviour.
	Checks []Check

	// Seed fills the world. Prepare, when set, runs after it, to start an
	// update or to set a fault.
	Seed    func(s *fleettest.State)
	Prepare func(w *fleettest.World) error
	// Legacy and V2 are the arguments of the legacy binary and of the v2
	// binary for the same intent. An empty one means that binary has no
	// such run yet.
	Legacy []string
	V2     []string
	// Stdin is what the binary reads when it has no Terminal.
	Stdin string
	// Terminal, when set, runs the binary on a pseudo-terminal and answers
	// its prompts.
	Terminal *Terminal
	// Step is how far the world's clock moves for each request of a clock
	// operation. Zero means one minute.
	Step time.Duration
	// Clock lists the operations that move the clock, in place of the
	// default: DescribeUpdate and DescribeInsightsRefresh, which only wait
	// loops call.
	Clock []fleettest.Operation
	// Signal, when set, is sent to the binary once.
	Signal *Signal
	// Cluster is a cluster whose final state the trace records even when
	// no request names it. Empty means "prod".
	Cluster string
	// UnstableReads are read operations whose request count depends on
	// timing in the binary, such as a watcher on its own ticker. The trace
	// records that they were called, and leaves them out of the steps.
	UnstableReads []fleettest.Operation
}

// Signal says when to interrupt the binary.
type Signal struct {
	// Signal is sent when the Nth request that Match accepts arrives,
	// counted from 1.
	Signal os.Signal
	Match  func(r awsxtest.Request) bool
	Nth    int
}

// Command is a legacy command that changes AWS state.
type Command string

// The commands of the matrix.
const (
	AddonUpdate     Command = "addon update"
	NodegroupScale  Command = "nodegroup scale"
	NodegroupUpdate Command = "nodegroup update"
	ClusterUpgrade  Command = "cluster upgrade"
	ClusterRollback Command = "cluster rollback"
)

// Behaviour is something every changing command must get right.
type Behaviour string

// The behaviours of the matrix.
const (
	// Success: --yes, a healthy fleet, and the change is made.
	Success Behaviour = "success"
	// DryRun: --dry-run changes nothing.
	DryRun Behaviour = "dry-run"
	// Unattended: no --yes and no terminal. The command refuses.
	Unattended Behaviour = "unattended"
	// PromptYes, PromptNo, PromptInterrupt: on a terminal, the prompt is
	// answered yes, answered no, or left with Ctrl+C.
	PromptYes       Behaviour = "prompt-yes"
	PromptNo        Behaviour = "prompt-no"
	PromptInterrupt Behaviour = "prompt-interrupt"
	// Blocked: a gate refuses, and nothing changes.
	Blocked Behaviour = "blocked"
	// Failed: the update starts and EKS reports that it failed.
	Failed Behaviour = "failed"
	// Busy: the resource already has a change in progress.
	Busy Behaviour = "busy"
	// Attach, AttachFailed: an update of the same change is in flight. The
	// command waits for it, and starts no second one.
	Attach       Behaviour = "attach"
	AttachFailed Behaviour = "attach-failed"
	// Deadline: the wait outlasts --wait-timeout.
	Deadline Behaviour = "deadline"
	// Interrupt: SIGINT during the wait.
	Interrupt Behaviour = "interrupt"
	// LostResponse: the answer to the mutating call is lost. The retry
	// makes no second update.
	LostResponse Behaviour = "lost-response"
	// ThrottledRead: a read is throttled once, and the run goes on.
	ThrottledRead Behaviour = "throttled-read"
)

// Check is one assertion on a trace. It returns what is wrong, or nil.
type Check func(t Trace) error

// behaviourChecks are what a scenario of each behaviour must show.
var behaviourChecks = map[Behaviour][]Check{
	Success:         {exits(0), changes, allAccepted},
	DryRun:          {noMutations},
	Unattended:      {fails, noRequests},
	PromptYes:       {changes},
	PromptNo:        {noUpdates},
	PromptInterrupt: {fails, noUpdates},
	Blocked:         {fails, noUpdates},
	Failed:          {fails, changes, worldHasUpdate("Failed")},
	Busy:            {noUpdates},
	Attach:          {exits(0), noUpdates},
	AttachFailed:    {},
	Deadline:        {fails, changes},
	Interrupt:       {exits(1), changes, nothingAfterSignal},
	LostResponse:    {exits(0), oneUpdatePerLostResponse},
	ThrottledRead:   {exits(0), changes, sawThrottle},
}

// check runs the scenario's own checks and those of its behaviour.
func (sc Scenario) check(t Trace) error {
	var errs []error
	for _, c := range slices.Concat(behaviourChecks[sc.Behaviour], sc.Checks) {
		if err := c(t); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// updates returns the Update requests of a trace, as "Operation target".
func updates(t Trace) []string {
	var out []string
	for _, m := range t.Mutations() {
		if op := string(m.Operation); strings.HasPrefix(op, "eks:Update") {
			out = append(out, strings.TrimPrefix(op, "eks:")+" "+m.Target)
		}
	}
	return out
}

func exits(code int) Check {
	return func(t Trace) error {
		if t.ExitCode != code {
			return fmt.Errorf("exit code %d, want %d", t.ExitCode, code)
		}
		return nil
	}
}

func fails(t Trace) error {
	if t.ExitCode == 0 {
		return errors.New("exit code 0, want a failure")
	}
	return nil
}

// noRequests: the binary decided before it called AWS at all.
func noRequests(t Trace) error {
	if len(t.Steps) != 0 {
		return fmt.Errorf("the binary sent requests: %d step(s)", len(t.Steps))
	}
	return nil
}

func noMutations(t Trace) error {
	if m := t.Mutations(); len(m) != 0 {
		return fmt.Errorf("%d mutating request(s), want none: first %s %s", len(m), m[0].Operation, m[0].Target)
	}
	return nil
}

// noUpdates: no Update request. It allows StartInsightsRefresh, which
// changes no resource.
func noUpdates(t Trace) error {
	if u := updates(t); len(u) != 0 {
		return fmt.Errorf("update requests %v, want none", u)
	}
	return nil
}

func changes(t Trace) error {
	if len(updates(t)) == 0 {
		return errors.New("no update request, want at least one")
	}
	return nil
}

func allAccepted(t Trace) error {
	for _, m := range t.Mutations() {
		if !strings.HasPrefix(m.Outcome, "accepted") {
			return fmt.Errorf("%s %s ended %q, want accepted", m.Operation, m.Target, m.Outcome)
		}
	}
	return nil
}

// updatesAre: the Update requests are exactly these, in this order, each as
// "Operation target".
func updatesAre(want ...string) Check {
	return func(t Trace) error {
		if got := updates(t); !slices.Equal(got, want) {
			return fmt.Errorf("update requests %v, want %v", got, want)
		}
		return nil
	}
}

// refusedByEKS: the one update request was sent and EKS refused it.
func refusedByEKS(t Trace) error {
	m := t.Mutations()
	if len(m) != 1 || !strings.HasPrefix(m[0].Outcome, "error=") {
		return fmt.Errorf("mutations %+v, want one that EKS refused", m)
	}
	return nil
}

// nothingAfterSignal: no mutating request arrived after the signal.
func nothingAfterSignal(t Trace) error {
	switch {
	case t.SignalAfterMutations == nil:
		return errors.New("no signal was sent")
	case len(t.Mutations()) != *t.SignalAfterMutations:
		return fmt.Errorf("%d mutating request(s) in all, %d before the signal: the binary went on after it", len(t.Mutations()), *t.SignalAfterMutations)
	}
	return nil
}

// oneUpdatePerLostResponse: a request whose answer was lost is followed by
// the same request with the same token, and the two started one update.
func oneUpdatePerLostResponse(t Trace) error {
	m := t.Mutations()
	lost := 0
	for i := range m {
		if m[i].Outcome != "lost" {
			continue
		}
		lost++
		if i+1 >= len(m) {
			return fmt.Errorf("%s %s lost its answer and was not retried", m[i].Operation, m[i].Target)
		}
		next := m[i+1]
		token := m[i].Payload["clientRequestToken"]
		if next.Operation != m[i].Operation || next.Target != m[i].Target || token == nil || next.Payload["clientRequestToken"] != token {
			return fmt.Errorf("the request after the lost %s %s is %s %s with token %v, want the same request with token %v",
				m[i].Operation, m[i].Target, next.Operation, next.Target, next.Payload["clientRequestToken"], token)
		}
	}
	if lost == 0 {
		return errors.New("no answer was lost")
	}
	// One update per distinct accepted answer: the lost request and its retry
	// count once. A scenario seeds at most one update of its own.
	ids := map[string]bool{}
	for _, x := range m {
		if strings.HasPrefix(x.Outcome, "accepted update") {
			ids[x.Outcome] = true
		}
	}
	accepted, started := len(ids), 0
	for _, c := range t.World {
		started += len(c.Updates)
	}
	if seeded := started - accepted; seeded < 0 || seeded > 1 {
		return fmt.Errorf("the world has %d update(s) for %d accepted request(s)", started, accepted)
	}
	return nil
}

func sawThrottle(t Trace) error {
	for _, s := range t.Steps {
		for _, r := range s.Reads {
			if strings.HasPrefix(r.Observed, "error=") && strings.Contains(r.Observed, "hrottl") {
				return nil
			}
		}
	}
	return errors.New("no read was throttled")
}

// worldHasUpdate: some update in the final world has the status.
func worldHasUpdate(status string) Check {
	return func(t Trace) error {
		for _, c := range t.World {
			for _, u := range c.Updates {
				if strings.Contains(u, " "+status+" ") {
					return nil
				}
			}
		}
		return fmt.Errorf("no update in the final world is %s", status)
	}
}

// resourceIs: the final line of a nodegroup or an add-on of prod contains
// the text.
func resourceIs(name, text string) Check {
	return func(t Trace) error {
		c := t.World[Region+"/prod"]
		line, ok := c.Nodegroups[name]
		if !ok {
			line = c.Addons[name]
		}
		if !strings.Contains(line, text) {
			return fmt.Errorf("%s is %q in the end, want it to contain %q", name, line, text)
		}
		return nil
	}
}

// cell is one cell of the behaviour matrix: the scenario that covers it, or
// why none does.
type cell struct {
	scenario string
	why      string
}

func by(scenario string) cell { return cell{scenario: scenario} }
func none(why string) cell    { return cell{why: why} }
