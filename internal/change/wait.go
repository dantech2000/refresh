package change

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/awsx"
)

// DefaultPollInterval is the pause between two polls when the caller
// gives none.
const DefaultPollInterval = 15 * time.Second

// WaitOptions say how a wait polls.
type WaitOptions struct {
	// Interval is the pause before each poll. Zero means
	// DefaultPollInterval.
	Interval time.Duration
	// After returns a channel that delivers after a duration. Nil means
	// time.After. A test passes its clock.
	After func(time.Duration) <-chan time.Time
	// Progress is called after each poll. It may be nil.
	Progress func(UpdateProgress)

	// budget is the wait budget of a run, or nil. See pause and over.
	budget *budget
}

func (o WaitOptions) interval() time.Duration {
	if o.Interval > 0 {
		return o.Interval
	}
	return DefaultPollInterval
}

// pause waits one interval before a poll. It returns false when ctx ended
// first.
//
// Under a budget the pause is never longer than the time that is left, so
// the last poll is due at the deadline and none comes after it. With no
// time left there is no pause: the caller makes one poll, and over ends
// the wait unless that poll ended it.
func (o WaitOptions) pause(ctx context.Context) bool {
	d := o.interval()
	if left, ok := o.budget.left(); ok {
		d = min(d, left)
	}
	if d <= 0 {
		return ctx.Err() == nil
	}
	after := time.After
	if o.After != nil {
		after = o.After
	}
	select {
	case <-after(d):
		// Both can be ready, and select then picks either one.
		return ctx.Err() == nil
	case <-ctx.Done():
		return false
	}
}

// over reports whether a wait must end after an answer that did not end
// it: the budget has run out on the clock of the run, or ctx ended. A
// wait calls it after every such answer. An answer that ends the wait is
// returned whatever the clock reads: the result of the change is known.
func (o WaitOptions) over(ctx context.Context) bool {
	o.budget.check()
	return ctx.Err() != nil
}

func (o WaitOptions) progress(p UpdateProgress) {
	if o.Progress != nil {
		o.Progress(p)
	}
}

// Resource names what a wait follows: a control plane, an add-on, or a
// nodegroup of a cluster.
type Resource struct {
	Target
	Kind StepKind
	// Name is the add-on or the nodegroup. It is empty for the control
	// plane.
	Name string
}

func (r Resource) String() string {
	switch r.Kind {
	case StepAddon:
		return "add-on " + r.Name
	case StepNodegroup, StepScale:
		return "nodegroup " + r.Name
	}
	return "cluster " + r.Cluster
}

// UpdateProgress is the answer of one poll.
type UpdateProgress struct {
	Resource Resource
	// UpdateID is empty for a wait that follows the resource.
	UpdateID string
	// Status is the status of the update, or of the resource. It is the
	// last one seen when the poll failed.
	Status string
	// PollError is the error of a poll that failed in a way that the wait
	// goes on through, or "".
	PollError string
	// Final says that this poll ended the wait.
	Final bool
}

// UpdateIssue is one error that EKS reports for an update that ended
// badly.
type UpdateIssue struct {
	Code, Message string
	ResourceIDs   []string
}

// UpdateError is an EKS update that ended Failed or Cancelled.
type UpdateError struct {
	Resource Resource
	UpdateID string
	// Status is Failed or Cancelled.
	Status string
	Issues []UpdateIssue
}

func (e *UpdateError) Error() string {
	msg := fmt.Sprintf("update %s of %s ended %s", e.UpdateID, e.Resource, e.Status)
	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		part := issue.Code
		if issue.Message != "" {
			part += ": " + issue.Message
		}
		if len(issue.ResourceIDs) > 0 {
			part += " (" + strings.Join(issue.ResourceIDs, ", ") + ")"
		}
		parts = append(parts, part)
	}
	if len(parts) > 0 {
		msg += ": " + strings.Join(parts, "; ")
	}
	return msg
}

// Cancelled reports whether the update ended Cancelled.
func (e *UpdateError) Cancelled() bool {
	return e.Status == string(ekstypes.UpdateStatusCancelled)
}

// UnobservedError says that a wait stopped because it could not follow
// the change: a poll failed in a way that another poll will not fix. The
// change may still be running.
type UnobservedError struct {
	Resource Resource
	UpdateID string
	// LastStatus is the last status that a poll saw, or "".
	LastStatus string
	Err        error
}

func (e *UnobservedError) Error() string {
	what := e.Resource.String()
	if e.UpdateID != "" {
		what = "update " + e.UpdateID + " of " + what
	}
	return fmt.Sprintf("could not follow %s; it may still be running: %s", what, awsx.Summary(e.Err))
}

func (e *UnobservedError) Unwrap() error { return e.Err }

// errNoAnswer is the poll error of an answer with no update or resource
// in it.
var errNoAnswer = errors.New("the answer was empty")

// errNoSizes is the read error of a nodegroup answer that lacks the
// scaling config, or one of its three sizes. A size that is not in the
// answer is not known, and it is never taken as 0.
var errNoSizes = errors.New("the answer did not have every scaling size of the nodegroup")

// ended returns the error of a wait whose context ended.
func ended(ctx context.Context, what, last string) error {
	if last != "" {
		what += " (last status " + last + ")"
	}
	return fmt.Errorf("waiting for %s: %w", what, ctx.Err())
}

// WaitUpdate polls DescribeUpdate for one update until it ends. It makes
// the first poll after one interval, because a fresh update ID can answer
// not-found for a moment.
//
// It returns nil for a Successful update and an *UpdateError for a Failed
// or a Cancelled one, also when that answer comes at the deadline of the
// run: a change that has finished is reported as finished. A poll error that another poll will not fix ends it
// with an *UnobservedError; a not-found before the update was seen once
// is not such an error. It polls through every other error, and through
// an answer with no update, and reports each through Progress. When ctx
// ends, it returns the error of ctx, wrapped. It calls no other operation
// and prints nothing.
func WaitUpdate(ctx context.Context, c awsx.PollClients, r Resource, updateID string, o WaitOptions) error {
	if updateID == "" {
		return &UnobservedError{Resource: r, Err: errors.New("EKS returned no update ID")}
	}
	in := &eks.DescribeUpdateInput{Name: aws.String(r.Cluster), UpdateId: aws.String(updateID)}
	switch r.Kind {
	case StepAddon:
		in.AddonName = aws.String(r.Name)
	case StepNodegroup, StepScale:
		in.NodegroupName = aws.String(r.Name)
	}
	what := "update " + updateID + " of " + r.String()
	seen, last := false, ""
	for {
		if !o.pause(ctx) {
			return ended(ctx, what, last)
		}
		out, err := c.EKS.DescribeUpdate(ctx, in)
		if err == nil && out.Update == nil {
			err = errNoAnswer
		}
		if err != nil {
			if ctx.Err() != nil {
				return ended(ctx, what, last)
			}
			if awsx.IsPermanentAPIError(err) && (seen || !awsx.IsNotFound(err)) {
				return &UnobservedError{Resource: r, UpdateID: updateID, LastStatus: last, Err: err}
			}
			o.progress(UpdateProgress{Resource: r, UpdateID: updateID, Status: last, PollError: awsx.Summary(err)})
			if o.over(ctx) {
				return ended(ctx, what, last)
			}
			continue
		}
		seen, last = true, string(out.Update.Status)
		status := out.Update.Status
		final := status == ekstypes.UpdateStatusSuccessful || status == ekstypes.UpdateStatusFailed || status == ekstypes.UpdateStatusCancelled
		o.progress(UpdateProgress{Resource: r, UpdateID: updateID, Status: last, Final: final})
		switch status {
		case ekstypes.UpdateStatusSuccessful:
			return nil
		case ekstypes.UpdateStatusFailed, ekstypes.UpdateStatusCancelled:
			e := &UpdateError{Resource: r, UpdateID: updateID, Status: last}
			for _, d := range out.Update.Errors {
				e.Issues = append(e.Issues, UpdateIssue{Code: string(d.ErrorCode), Message: aws.ToString(d.ErrorMessage), ResourceIDs: d.ResourceIds})
			}
			return e
		}
		if o.over(ctx) {
			return ended(ctx, what, last)
		}
	}
}

// Settled is the state that a poll of a resource saw.
type Settled struct {
	// Status is the EKS status of the resource.
	Status string
	// Version is the Kubernetes version, or the add-on version.
	Version string
	// Sizes are the scaling sizes of a nodegroup, each one as EKS returned
	// it.
	Sizes Sizes
	// Issues are the codes and messages of the health issues that EKS
	// reports for the resource.
	Issues []string
}

// WaitSettled polls the resource until done reports true, and returns the
// state that done accepted. done sees every answer and decides what it
// means: it returns false to go on, and an error to end the wait as
// failed. The first poll comes after one interval.
//
// The rules for a poll that fails, an empty answer, and a context that
// ends are the ones of WaitUpdate. A resource that does not exist is an
// error that another poll will not fix. WaitSettled calls the describe
// operation of its resource and nothing else, and prints nothing.
func WaitSettled(ctx context.Context, c awsx.PollClients, r Resource, done func(Settled) (bool, error), o WaitOptions) (Settled, error) {
	var state Settled
	for {
		if !o.pause(ctx) {
			return state, ended(ctx, r.String(), state.Status)
		}
		got, err := describe(ctx, c.Clients, r)
		if err != nil {
			if ctx.Err() != nil {
				return state, ended(ctx, r.String(), state.Status)
			}
			if awsx.IsPermanentAPIError(err) {
				return state, &UnobservedError{Resource: r, LastStatus: state.Status, Err: err}
			}
			o.progress(UpdateProgress{Resource: r, Status: state.Status, PollError: awsx.Summary(err)})
			if o.over(ctx) {
				return state, ended(ctx, r.String(), state.Status)
			}
			continue
		}
		state = got
		ok, err := done(state)
		o.progress(UpdateProgress{Resource: r, Status: state.Status, Final: ok || err != nil})
		if err != nil || ok {
			return state, err
		}
		if o.over(ctx) {
			return state, ended(ctx, r.String(), state.Status)
		}
	}
}

// describe reads the state of a resource with one describe call.
//
// The read of a scale needs every size of the nodegroup. An answer with
// no scaling config, or with a size missing, is an error and not a
// nodegroup of size 0: a wait polls through it like through any answer
// that says nothing, and the fresh read before a submit fails on it, so
// nothing is sent. A roll reads no size, so StepNodegroup takes the sizes
// that are there.
func describe(ctx context.Context, c *awsx.Clients, r Resource) (Settled, error) {
	switch r.Kind {
	case StepAddon:
		out, err := c.EKS.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String(r.Cluster), AddonName: aws.String(r.Name)})
		if err != nil {
			return Settled{}, err
		}
		if out.Addon == nil {
			return Settled{}, errNoAnswer
		}
		s := Settled{Status: string(out.Addon.Status), Version: aws.ToString(out.Addon.AddonVersion)}
		if out.Addon.Health != nil {
			for _, issue := range out.Addon.Health.Issues {
				s.Issues = append(s.Issues, string(issue.Code)+": "+aws.ToString(issue.Message))
			}
		}
		return s, nil
	case StepNodegroup, StepScale:
		out, err := c.EKS.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(r.Cluster), NodegroupName: aws.String(r.Name)})
		if err != nil {
			return Settled{}, err
		}
		if out.Nodegroup == nil {
			return Settled{}, errNoAnswer
		}
		s := Settled{Status: string(out.Nodegroup.Status), Version: aws.ToString(out.Nodegroup.Version)}
		sc := out.Nodegroup.ScalingConfig
		known := sc != nil && sc.DesiredSize != nil && sc.MinSize != nil && sc.MaxSize != nil
		switch {
		case known:
			s.Sizes = Sizes{Desired: *sc.DesiredSize, Min: *sc.MinSize, Max: *sc.MaxSize}
		case r.Kind == StepScale:
			return Settled{}, errNoSizes
		}
		return s, nil
	}
	out, err := c.EKS.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(r.Cluster)})
	if err != nil {
		return Settled{}, err
	}
	if out.Cluster == nil {
		return Settled{}, errNoAnswer
	}
	return Settled{Status: string(out.Cluster.Status), Version: aws.ToString(out.Cluster.Version)}, nil
}
