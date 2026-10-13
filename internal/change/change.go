// Package change plans and runs the changes that refresh makes to a
// cluster: an add-on update and a nodegroup scale now, and roll, upgrade,
// and rollback as they are built.
//
// One function builds the plan of a change. NewPlan returns it for a dry
// run, and Run calls the same function, so a dry run shows what a real run
// would do. Run executes on the goroutine of its caller and reports
// through two hooks: an event and a question. Nothing here writes to a
// terminal.
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

// The kinds of change that this package can plan and run. Each other
// kind arrives with its planner.
const (
	AddonUpdate    Kind = "AddonUpdate"
	NodegroupScale Kind = "NodegroupScale"
)

// independent reports whether the steps of a change of kind k are
// independent targets: one that fails beside one that changed something
// is a partial failure. Both kinds that are built are. A kind whose
// phases are a sequence is not: a failed phase has failed the whole
// operation.
func (k Kind) independent() bool {
	return k == AddonUpdate || k == NodegroupScale
}

// Request is one of the request types of this package: AddonUpdateRequest
// or ScaleRequest. The kinds that are not built yet add theirs.
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
	return plan(ctx, d, req)
}

// plan builds the plan of a request. NewPlan and Run both call it, so
// there is no dry-run code path. A new kind adds a case here and a file
// with its planner.
func plan(ctx context.Context, d Deps, req Request) (*Plan, error) {
	// Every plan reads anew. The fetcher of the command remembers the
	// newest version of an add-on, which may have changed since the plan
	// before this one.
	if d.Fetch == nil {
		d.Fetch = &fleet.Fetcher{Now: d.Now}
	}
	d.Fetch = d.Fetch.Fresh()
	switch r := req.(type) {
	case AddonUpdateRequest:
		return planAddons(ctx, d, r)
	case ScaleRequest:
		return planScale(ctx, d, r)
	}
	return nil, &UnsupportedError{Kind: req.kind()}
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
