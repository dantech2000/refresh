// Package change plans and runs the changes that refresh makes to a
// cluster: an add-on update, a nodegroup scale, a nodegroup roll for one
// cluster or for a fleet, a cluster upgrade, and a cluster rollback.
//
// One function builds the plan of a change. NewPlan returns it for a dry
// run, and Run calls the same function, so a dry run shows what a real run
// would do. Run executes on the goroutine of its caller and reports
// through three hooks: an event, a question, and a checkpoint. Nothing
// here writes to a terminal.
package change

import (
	"context"
	"fmt"
	"time"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/kube"
)

// Kind names a change.
type Kind string

// The kinds of change whose targets are independent. ClusterUpgrade and
// ClusterRollback, whose phases are a sequence, are beside their requests.
const (
	AddonUpdate     Kind = "AddonUpdate"
	NodegroupScale  Kind = "NodegroupScale"
	NodegroupUpdate Kind = "NodegroupUpdate"
)

// independent reports whether the steps of a change of kind k are
// independent targets: one that fails beside one that changed something
// is a partial failure: the add-ons of an update, the nodegroups of a
// roll, and the clusters of a fleet. An upgrade and a rollback are not:
// their phases are a sequence, and a failed phase has failed the whole
// operation.
func (k Kind) independent() bool {
	return k == AddonUpdate || k == NodegroupScale || k == NodegroupUpdate
}

// Request is one of the request types of this package: AddonUpdateRequest,
// ScaleRequest, RollRequest, UpgradeRequest, or RollbackRequest.
type Request interface {
	kind() Kind
	base() Common
	cluster() Target
	// differs names the first field in which the request differs from
	// reviewed, the request of a plan that a person reviewed, or returns
	// "" when both ask for the same change. See sameRequest.
	differs(reviewed Request) string
}

// Target names one cluster. An empty Region means the home region of the
// run.
type Target struct {
	Region  string
	Cluster string
}

func (t Target) cluster() Target { return t }

// Common holds what every request carries.
type Common struct {
	// Policy is what --yes, --require-healthy, --force, and
	// --skip-health-check say to the health gate.
	Policy health.Policy
	// NoWait ends the run once AWS accepted the changes.
	NoWait bool
	// SkipVerify leaves out the verification after a roll, and the
	// baseline before it.
	SkipVerify bool
	// Nodes starts a node observer beside the wait of a roll, which
	// reports through NodeProgress events. It needs Kubernetes access,
	// and it runs only for a batch with exactly one roll in flight.
	Nodes  bool
	Limits Limits
	// Expect is a plan that a person reviewed before this call. It is
	// consent to that plan and to no other: Run returns an error before
	// it reads anything when Expect is not a plan of this request, and it
	// compares its first plan with Expect as it compares the plans around
	// a question. A first plan that matches starts with no question.
	Expect *Plan
}

func (c Common) base() Common { return c }

// AddonUpdateRequest updates add-ons of one cluster.
type AddonUpdateRequest struct {
	Common
	Target
	// Addons are exact names. It is empty with All.
	Addons []string
	// All updates every installed add-on, in dependency order.
	All bool
	// Version is the version to move to, with exactly one add-on. Empty
	// means the latest version that the cluster can run.
	Version string
	// Skip names add-ons that All leaves out.
	Skip []string
}

func (AddonUpdateRequest) kind() Kind { return AddonUpdate }

// ScaleRequest changes the sizes of one nodegroup. A nil size was not
// given and keeps its value.
type ScaleRequest struct {
	Common
	Target
	Nodegroup         string
	Desired, Min, Max *int32
}

func (ScaleRequest) kind() Kind { return NodegroupScale }

// RollRequest rolls nodegroups onto the latest AMI of their own
// Kubernetes version: the nodegroups of one cluster, or with Fleet the
// nodegroups of every cluster of a region sweep.
type RollRequest struct {
	Common
	Target
	// Nodegroups are exact names. Empty means every nodegroup.
	Nodegroups []string
	// Select keeps the nodegroups that it matches, of the named ones or
	// of all. Nil keeps every one.
	Select *Selector
	// Reroll also rolls a nodegroup that is on the latest AMI.
	Reroll bool
	// Fleet rolls every cluster of a sweep. Target is then empty.
	Fleet *FleetScope
}

func (RollRequest) kind() Kind { return NodegroupUpdate }

// Selector picks nodegroups by pattern: the -f flag of the command. The
// command parses the patterns; this package knows only the result.
type Selector struct {
	// Text is the selection as the user wrote it, such as "name=web-*".
	// A message names it, and two requests with the same Text ask for the
	// same selection.
	Text string
	// Match reports whether the selection takes the nodegroup.
	Match func(n fleet.Nodegroup) bool
}

// FleetScope names the regions of a fleet roll.
type FleetScope struct {
	Regions fleet.Regions
}

// Deps is what the package needs from the command.
type Deps struct {
	AWS   *awsx.Provider
	Fetch *fleet.Fetcher
	// Kube returns the Kubernetes client of a cluster. It makes no
	// request. An add-on update does not call it.
	Kube func(t kube.Target) (*kube.Client, error)
	// Now and After are the clock of the run: the time of an event, the
	// budgets of Limits, and the pause between two polls. Nil means
	// time.Now and time.After.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
	// AfterFunc starts the timer of a budget, which ends a call that
	// hangs. It is part of the clock: nil means time.AfterFunc. A test
	// with a clock of its own sets it, holds the timer, and fires it when
	// a call hangs.
	AfterFunc func(time.Duration, func()) (stop func() bool)
	// Tick is the clock of the node observer beside a wait: the pause
	// between two of its snapshots, and the limit of the first sync of
	// its watches. It is apart from After, because the
	// observer runs on a goroutine of its own, and a test clock that moves
	// with each pause of the wait must not move for the observer too. Nil
	// means time.After. Only a run with Common.Nodes calls it.
	Tick func(time.Duration) <-chan time.Time
	// FollowUp holds the words around a command that a notice of an
	// upgrade names, so that the command reaches the same account and
	// region as the run. The zero value gives "refresh".
	FollowUp FollowUp
}

// budget returns a context under parent and its budget of limit, on the
// clock of the run. See newBudget.
func (d Deps) budget(parent context.Context, limit time.Duration, cause error) (context.Context, *budget) {
	ctx, b := newBudget(parent, d.now, limit, cause)
	b.afterFunc = d.AfterFunc
	return ctx, b
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) after(dur time.Duration) <-chan time.Time {
	if d.After != nil {
		return d.After(dur)
	}
	return time.After(dur)
}

// NewPlan reads live state and returns the plan for req. It sends no
// mutating request. The error is non-nil when there is no plan to show:
// the request is invalid, a name does not exist, or the cluster could not
// be read at all.
func NewPlan(ctx context.Context, d Deps, req Request) (*Plan, error) {
	return plan(ctx, d, req, nil)
}

// plan builds the plan of a request. NewPlan and Run both call it, so
// there is no dry-run code path. A new kind adds a case here and a file
// with its planner.
//
// refresh is the one difference between the two callers. Run passes its
// insights refresher, and the plan of an upgrade then asks EKS to
// evaluate the insights anew before it reads them. NewPlan passes nil: a
// dry run sends no request that is not a read.
func plan(ctx context.Context, d Deps, req Request, refresh *insightsRefresher) (*Plan, error) {
	// Every plan reads anew. The fetcher of the command remembers the
	// newest version of an add-on, which may have changed since the plan
	// before this one.
	d.Fetch = d.fetcher().Fresh()
	switch r := req.(type) {
	case AddonUpdateRequest:
		return planAddons(ctx, d, r)
	case ScaleRequest:
		return planScale(ctx, d, r)
	case RollRequest:
		return planRoll(ctx, d, r)
	case UpgradeRequest:
		return planUpgrade(ctx, d, r, refresh)
	case RollbackRequest:
		return planRollback(ctx, d, r)
	}
	return nil, &UnsupportedError{Kind: req.kind()}
}

// fetcher returns the fetcher of the dependencies, or one on their clock
// when the caller set none.
func (d Deps) fetcher() *fleet.Fetcher {
	if d.Fetch != nil {
		return d.Fetch
	}
	return &fleet.Fetcher{Now: d.Now}
}

// UnsupportedError is the error for a kind of change that this build
// cannot plan or run.
type UnsupportedError struct {
	Kind Kind
}

func (e *UnsupportedError) Error() string {
	return "this build cannot run a " + string(e.Kind) + " change"
}

// NotFoundError is the error of a plan that names an add-on or a
// nodegroup that the cluster does not have. A front end suggests the
// close names from Names.
type NotFoundError struct {
	// Target is the cluster that was read.
	Target
	// Kind is StepAddon or StepNodegroup.
	Kind StepKind
	Name string
	// Names are the add-ons or the nodegroups that the cluster has.
	Names []string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("%s %q not found in cluster %s", resourceNoun(e.Kind), e.Name, e.Cluster)
}

// resourceNoun names the kind of resource that a step changes.
func resourceNoun(k StepKind) string {
	switch k {
	case StepAddon:
		return "add-on"
	case StepNodegroup, StepScale:
		return "nodegroup"
	}
	return "cluster"
}
