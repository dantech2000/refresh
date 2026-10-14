//go:build unix

package parity

import (
	"cmp"
	"encoding/json"
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
	// RealTime is how long a run of the scenario may take on the real
	// clock, for either binary. Zero means two minutes. A scenario sets it
	// only when a binary waits on its own clock for something that the
	// world's clock cannot move: see "Time" in the package comment.
	RealTime time.Duration
	// Kube gives the scenario's cluster a Kubernetes API, serving the
	// nodes, pods, and PDBs of the seed, and gives the binary a kubeconfig
	// for it. Without it the binary has an empty kubeconfig and skips its
	// Kubernetes checks.
	Kube bool
	// Live marks a run that draws the live node view. The view polls the
	// nodes, their pods, the events, and the scaling activities on tickers
	// of its own, so those operations are kept out of the steps of the
	// trace, and so is the run's output.
	Live bool
	// Moves change the world while the binary runs, between two of its
	// requests.
	Moves []Move
	// Holds keep requests from being answered until something else has
	// happened, to fix the order of two things the binary does at once.
	Holds []Hold
	// V2Require are facts the v2 binary must have seen at some point of the
	// run, where the contract asks more of it than the old binary did.
	// V2Checks are further assertions on the v2 trace alone, such as a
	// notice the contract requires. The legacy trace is not held to either.
	V2Require []Fact
	V2Checks  []Check
	// LegacyPreview and V2Preview are a command line to run first on the
	// same world, such as the dry run of the change. AfterPreview then
	// changes the world, and the trace is that of the main run alone.
	LegacyPreview []string
	V2Preview     []string
	AfterPreview  func(w *fleettest.World) error
	// Cluster is a cluster whose final state the trace records even when
	// no request names it. Empty means "prod".
	Cluster string
	// UnstableReads are read operations whose request count depends on
	// timing in the binary, such as a watcher on its own ticker. The trace
	// records that they were called, and leaves them out of the steps.
	UnstableReads []fleettest.Operation
}

// limit returns how long a run of the scenario may take on the real clock.
func (sc Scenario) limit() time.Duration { return cmp.Or(sc.RealTime, 2*time.Minute) }

// Move changes the world once, at a point of the run that both binaries
// reach whatever they read on the way there.
type Move struct {
	When Anchor
	Do   func(w *fleettest.World) error
}

// Anchor is a point of a run. The points are things every correct binary
// does or sees, and never "the Nth read", which is how one binary happens to
// be written.
type Anchor struct {
	kind   anchorKind
	op     fleettest.Operation
	target string
	update int
	status fleettest.UpdateStatus
	prompt int
}

type anchorKind int

const (
	beforeRequest anchorKind = iota + 1
	afterAccepted
	updateStatus
	promptAnswered
)

// BeforeMutation is when the first mutating request of the operation for
// the target ("cluster", "cluster/nodegroup", "cluster/addon") has arrived
// and is not answered yet: the binary has decided, and the fake has not
// acted. The move runs before the fake does.
func BeforeMutation(op fleettest.Operation, target string) Anchor {
	return Anchor{kind: beforeRequest, op: op, target: target}
}

// AfterMutation is when the fake has accepted the first such request, and
// before the binary has the answer.
func AfterMutation(op fleettest.Operation, target string) Anchor {
	return Anchor{kind: afterAccepted, op: op, target: target}
}

// WhenUpdate is when the nth update the world started has the status, and
// before any request is answered that could show it.
func WhenUpdate(nth int, status fleettest.UpdateStatus) Anchor {
	return Anchor{kind: updateStatus, update: nth, status: status}
}

// AfterPrompt is when the nth prompt of a terminal run has appeared, and
// before the reply is typed.
func AfterPrompt(nth int) Anchor { return Anchor{kind: promptAnswered, prompt: nth} }

// matches reports whether the request is the one the anchor names.
func (a Anchor) matches(r awsxtest.Request) bool {
	target, _ := observe(r, awsxtest.Answer{Dropped: true})
	return r.Operation == a.op && target == a.target
}

func (a Anchor) String() string {
	switch a.kind {
	case beforeRequest:
		return fmt.Sprintf("before %s %s", a.op, a.target)
	case afterAccepted:
		return fmt.Sprintf("after %s %s was accepted", a.op, a.target)
	case updateStatus:
		return fmt.Sprintf("when update %d is %s", a.update, a.status)
	case promptAnswered:
		return fmt.Sprintf("after prompt %d", a.prompt)
	}
	return "nowhere"
}

// Hold keeps each request that Request accepts from being answered until
// Until accepts a request of the run. answered says whether that request
// has its answer.
type Hold struct {
	Request func(r awsxtest.Request) bool
	Until   func(r awsxtest.Request, answered bool) bool
}

// Signal says when to interrupt the binary.
type Signal struct {
	// Signal is sent when the Nth request that Match accepts arrives,
	// counted from 1.
	Signal os.Signal
	Match  func(r awsxtest.Request) bool
	Nth    int
	// AfterPrompt, when not 0, counts only the requests that arrive after
	// the prompt with that number was answered, from 1. The answer is a
	// point that both binaries reach, so the count does not depend on how
	// often a binary read before it asked.
	AfterPrompt int
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
	// DrainBlocked: a PodDisruptionBudget would refuse the eviction of a
	// pod on the nodes to drain. Nothing changes. DrainForced: the same,
	// with --force, and the change is made.
	DrainBlocked Behaviour = "drain-blocked"
	DrainForced  Behaviour = "drain-forced"
	// GateUnreadable: the Kubernetes API refuses to list the budgets, so
	// the gate cannot tell. Nothing changes. GateUnreadableForced: the
	// same, with --force.
	GateUnreadable       Behaviour = "gate-unreadable"
	GateUnreadableForced Behaviour = "gate-unreadable-forced"
)

// Check is one assertion on a trace.
type Check struct {
	// Field is the part of the trace the check is about. A paired run skips
	// the check when the allowlist lets that field of the scenario differ:
	// the check then describes the old binary only. A check with no field
	// always runs.
	Field Field
	// Run returns what is wrong, or nil.
	Run func(t Trace) error
}

// The checks that take no argument.
var (
	fails                    = Check{Field: FieldExitCode, Run: failsIn}
	noRequests               = Check{Field: FieldDecision, Run: noRequestsIn}
	noMutations              = Check{Field: FieldMutations, Run: noMutationsIn}
	noUpdates                = Check{Field: FieldMutations, Run: noUpdatesIn}
	changes                  = Check{Field: FieldMutations, Run: changesIn}
	allAccepted              = Check{Field: FieldMutations, Run: allAcceptedIn}
	refusedByEKS             = Check{Field: FieldMutations, Run: refusedByEKSIn}
	nothingAfterSignal       = Check{Field: FieldMutations, Run: nothingAfterSignalIn}
	oneUpdatePerLostResponse = Check{Field: FieldMutations, Run: oneUpdatePerLostResponseIn}
	sawThrottle              = Check{Run: sawThrottleIn}
	noRolls                  = Check{Field: FieldMutations, Run: noRollsIn}
	rolls                    = Check{Field: FieldMutations, Run: rollsIn}
	anUpdateFailed           = Check{Field: FieldWorld, Run: anUpdateFailedIn}
)

// behaviourChecks are what a scenario of each behaviour must show.
var behaviourChecks = map[Behaviour][]Check{
	Success:         {exits(0), changes, allAccepted},
	DryRun:          {noMutations},
	Unattended:      {fails, noRequests},
	PromptYes:       {changes},
	PromptNo:        {noUpdates},
	PromptInterrupt: {fails, noUpdates},
	Blocked:         {fails, noUpdates},
	Failed:          {fails, changes, anUpdateFailed},
	Busy:            {noUpdates},
	Attach:          {exits(0), noUpdates},
	AttachFailed:    {},
	Deadline:        {fails, changes, waited(deadlineArg)},
	Interrupt:       {exits(1), changes, nothingAfterSignal},
	LostResponse:    {exits(0), oneUpdatePerLostResponse},
	ThrottledRead:   {exits(0), changes, sawThrottle},

	DrainBlocked:         {fails, noRolls, sawKubeAnswer(fleettest.KubeListPDBs, "=allows0")},
	DrainForced:          {rolls, sawKubeAnswer(fleettest.KubeListPDBs, "=allows0")},
	GateUnreadable:       {fails, noRolls, sawKubeAnswer(fleettest.KubeListPDBs, "error=Forbidden")},
	GateUnreadableForced: {rolls, sawKubeAnswer(fleettest.KubeListPDBs, "error=Forbidden")},
}

// dryRunReads are the reads a dry run of each command must make: the state
// of what it would change, and what its gate and its plan rest on. A dry
// run that changes nothing because it looked at nothing is not a dry run.
var dryRunReads = map[Command][]fleettest.Operation{
	AddonUpdate:     {fleettest.EKSDescribeAddon, fleettest.EKSDescribeAddonVersions},
	NodegroupScale:  {fleettest.EKSDescribeNodegroup},
	NodegroupUpdate: {fleettest.EKSDescribeNodegroup, fleettest.SSMGetParameter},
	ClusterUpgrade:  {fleettest.EKSDescribeCluster, fleettest.EKSListInsights, fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeAddonVersions},
	ClusterRollback: {fleettest.EKSDescribeCluster, fleettest.EKSListUpdates, fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeAddonVersions},
}

// checks returns the scenario's own checks and those of its behaviour.
func (sc Scenario) checks() []Check {
	out := slices.Concat(behaviourChecks[sc.Behaviour], sc.Checks)
	if sc.Behaviour == DryRun {
		for _, op := range dryRunReads[sc.Command] {
			out = append(out, sawRead(op))
		}
	}
	return out
}

// check runs the scenario's checks, except those about a field that skip
// accepts. A nil skip runs them all.
func (sc Scenario) check(t Trace, skip func(Field) bool) error {
	var errs []error
	for _, c := range sc.checks() {
		if skip != nil && skip(c.Field) {
			continue
		}
		if err := c.Run(t); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// checkV2 runs what the scenario asks of the v2 binary alone.
func (sc Scenario) checkV2(t Trace) error {
	var errs []error
	for _, f := range sc.V2Require {
		errs = append(errs, saw(f).Run(t))
	}
	for _, c := range sc.V2Checks {
		errs = append(errs, c.Run(t))
	}
	return errors.Join(errs...)
}

// sawRead: the binary made the read at least once and got an answer.
func sawRead(op fleettest.Operation) Check {
	return Check{Run: func(t Trace) error {
		for _, r := range t.Requests {
			if r.Operation == op && r.Completed != 0 && !strings.HasPrefix(r.Observed, "error=") && r.Observed != "dropped" {
				return nil
			}
		}
		return fmt.Errorf("the binary never read %s", op)
	}}
}

// waited: the binary ran for at least this long on the real clock. A
// command that gives up before its deadline has not waited for it.
func waited(d time.Duration) Check {
	return Check{Run: func(t Trace) error {
		if t.Elapsed < d {
			return fmt.Errorf("the binary ran for %s, want at least the wait timeout of %s", t.Elapsed, d)
		}
		return nil
	}}
}

// worldMoved: the binary polled while it waited, so the world's clock
// moved. It is for the commands whose poll interval the scenario sets.
var worldMoved = Check{Run: func(t Trace) error {
	if t.WorldElapsed <= 0 {
		return errors.New("the world's clock did not move: the binary did not poll while it waited")
	}
	return nil
}}

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
	return Check{Field: FieldExitCode, Run: func(t Trace) error {
		if t.ExitCode != code {
			return fmt.Errorf("exit code %d, want %d", t.ExitCode, code)
		}
		return nil
	}}
}

func failsIn(t Trace) error {
	if t.ExitCode == 0 {
		return errors.New("exit code 0, want a failure")
	}
	return nil
}

// noRequests: the binary decided before it called AWS at all.
func noRequestsIn(t Trace) error {
	if len(t.Requests) != 0 {
		return fmt.Errorf("the binary sent %d request(s), first %s", len(t.Requests), t.Requests[0].Operation)
	}
	return nil
}

func noMutationsIn(t Trace) error {
	if m := t.Mutations(); len(m) != 0 {
		return fmt.Errorf("%d mutating request(s), want none: first %s %s", len(m), m[0].Operation, m[0].Target)
	}
	return nil
}

// noUpdates: no Update request. It allows StartInsightsRefresh, which
// changes no resource.
func noUpdatesIn(t Trace) error {
	if u := updates(t); len(u) != 0 {
		return fmt.Errorf("update requests %v, want none", u)
	}
	return nil
}

func changesIn(t Trace) error {
	if len(updates(t)) == 0 {
		return errors.New("no update request, want at least one")
	}
	return nil
}

func allAcceptedIn(t Trace) error {
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
	return Check{Field: FieldMutations, Run: func(t Trace) error {
		if got := updates(t); !slices.Equal(got, want) {
			return fmt.Errorf("update requests %v, want %v", got, want)
		}
		return nil
	}}
}

// refusedByEKS: the one update request was sent and EKS refused it.
func refusedByEKSIn(t Trace) error {
	m := t.Mutations()
	if len(m) != 1 || !strings.HasPrefix(m[0].Outcome, "error=") {
		return fmt.Errorf("mutations %+v, want one that EKS refused", m)
	}
	return nil
}

// nothingAfterSignal: no mutating request arrived after the signal.
func nothingAfterSignalIn(t Trace) error {
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
func oneUpdatePerLostResponseIn(t Trace) error {
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

func sawThrottleIn(t Trace) error {
	for _, r := range t.Requests {
		if r.Mutation == nil && strings.HasPrefix(r.Observed, "error=") && strings.Contains(r.Observed, "hrottl") {
			return nil
		}
	}
	return errors.New("no read was throttled")
}

// sawAnswer: at some point of the run, an answer to the read contained the
// text.
func sawAnswer(op fleettest.Operation, text string) Check {
	return Check{Run: func(t Trace) error {
		for _, r := range t.Requests {
			if r.Operation == op && r.Completed != 0 && strings.Contains(r.Observed, text) {
				return nil
			}
		}
		return fmt.Errorf("no %s answer with %q", op, text)
	}}
}

// sawKube: the binary made the Kubernetes request at least once.
func sawKube(op fleettest.Operation) Check { return sawKubeAnswer(op, "") }

// isRoll reports whether a mutation changes the nodes of a nodegroup.
func isRoll(m *Mutation) bool {
	return m != nil && (m.Operation == fleettest.EKSUpdateNodegroupVersion || m.Operation == fleettest.EKSUpdateNodegroupConfig)
}

// noRolls: no request that changes the nodes of a nodegroup. A cluster
// upgrade may have updated the control plane and the add-ons before its
// gate for the first nodegroup.
func noRollsIn(t Trace) error {
	for _, m := range t.Mutations() {
		if isRoll(&m) {
			return fmt.Errorf("%s %s was sent, want no nodegroup change", m.Operation, m.Target)
		}
	}
	return nil
}

// rolls: at least one request that changes the nodes of a nodegroup.
func rollsIn(t Trace) error {
	if noRollsIn(t) == nil {
		return errors.New("no nodegroup was changed, want at least one")
	}
	return nil
}

// worldSays: the final state of prod, as the trace records it, contains
// the text.
func worldSays(text string) Check {
	return Check{Field: FieldWorld, Run: func(t Trace) error {
		if state := fmt.Sprintf("%+v", t.World[Region+"/prod"]); !strings.Contains(state, text) {
			return fmt.Errorf("the final state of prod does not contain %q: %s", text, state)
		}
		return nil
	}}
}

// sawKubeAnswer: the binary had an answer to the Kubernetes request that
// contained the text before it first sent a change to a nodegroup, or
// without sending one. It is how a scenario shows that a gate read the state
// it then decided on.
func sawKubeAnswer(op fleettest.Operation, text string) Check {
	return Check{Field: FieldEvidence, Run: func(t Trace) error {
		roll := 0
		for _, r := range t.Requests {
			if isRoll(r.Mutation) {
				roll = r.Arrived
				break
			}
		}
		for _, r := range t.Requests {
			if r.Operation == op && r.Completed != 0 && (roll == 0 || r.Completed < roll) && strings.Contains(r.Observed, text) {
				return nil
			}
		}
		return fmt.Errorf("no %s answer with %q before the first nodegroup change", op, text)
	}}
}

// called: the binary made the request at least once, at any point.
func called(op fleettest.Operation) Check {
	return Check{Run: func(t Trace) error {
		if !slices.Contains(t.Operations, op) {
			return fmt.Errorf("%s was not called (called: %v)", op, t.Operations)
		}
		return nil
	}}
}

// saw: the binary saw the fact at some point of the run.
func saw(f Fact) Check {
	return Check{Run: func(t Trace) error {
		for _, r := range t.Requests {
			if r.Completed != 0 && slices.Contains(r.Facts, f) {
				return nil
			}
		}
		return fmt.Errorf("the binary never saw %s %s=%s", f.Subject, f.Attribute, f.Value)
	}}
}

// sawAfterPrompt: the binary saw the fact in an answer to a request it sent
// after the user answered its last prompt.
func sawAfterPrompt(f Fact) Check {
	return Check{Run: func(t Trace) error {
		if len(t.Confirmations) == 0 {
			return errors.New("no prompt was answered")
		}
		for _, r := range t.Requests {
			if r.Completed != 0 && r.Arrived > t.Confirmations[len(t.Confirmations)-1] && slices.Contains(r.Facts, f) {
				return nil
			}
		}
		return fmt.Errorf("the binary did not see %s %s=%s after the prompt was answered", f.Subject, f.Attribute, f.Value)
	}}
}

// gateAccepted: the document of the run records, among the reasons that
// --yes went past in the gate of its first cluster, one with the text.
func gateAccepted(text string) Check {
	return Check{Run: func(t Trace) error {
		var doc struct {
			Clusters []struct {
				Gate struct {
					Accepted []string `json:"accepted"`
				} `json:"gate"`
			} `json:"clusters"`
		}
		if err := json.Unmarshal([]byte(t.Stdout), &doc); err != nil {
			return fmt.Errorf("stdout is not one JSON document: %w", err)
		}
		if len(doc.Clusters) == 0 {
			return errors.New("the document has no cluster")
		}
		accepted := doc.Clusters[0].Gate.Accepted
		if !slices.ContainsFunc(accepted, func(reason string) bool { return strings.Contains(reason, text) }) {
			return fmt.Errorf("the gate records %q as accepted, want a reason with %q", accepted, text)
		}
		return nil
	}}
}

// says: the binary printed the text, on stdout, on stderr, or on its
// terminal.
func says(text string) Check {
	return Check{Run: func(t Trace) error {
		if !strings.Contains(t.Stdout+t.Stderr+t.Terminal, text) {
			return fmt.Errorf("the binary did not print %q", text)
		}
		return nil
	}}
}

// anUpdateFailed: some update in the final world is Failed.
func anUpdateFailedIn(t Trace) error {
	for _, c := range t.World {
		for _, u := range c.Updates {
			if strings.Contains(u, " Failed ") {
				return nil
			}
		}
	}
	return errors.New("no update in the final world is Failed")
}

// resourceIs: the final line of a nodegroup or an add-on of prod contains
// the text.
func resourceIs(name, text string) Check {
	return Check{Field: FieldWorld, Run: func(t Trace) error {
		c := t.World[Region+"/prod"]
		line, ok := c.Nodegroups[name]
		if !ok {
			line = c.Addons[name]
		}
		if !strings.Contains(line, text) {
			return fmt.Errorf("%s is %q in the end, want it to contain %q", name, line, text)
		}
		return nil
	}}
}

// cell is one cell of the behaviour matrix: the scenario that covers it, or
// why none does.
type cell struct {
	scenario string
	why      string
}

func by(scenario string) cell { return cell{scenario: scenario} }
func none(why string) cell    { return cell{why: why} }
