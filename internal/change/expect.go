package change

import (
	"errors"
	"fmt"
	"slices"

	"github.com/dantech2000/refresh/internal/health"
)

// ErrOtherRequest is the error of a run whose Common.Expect is not a plan
// of its request. Nothing was read and nothing was changed.
var ErrOtherRequest = errors.New("the reviewed plan is for another request")

// sameRequest checks that reviewed, the plan in Common.Expect, is a plan
// of req: the same kind of change, the same cluster and region, the same
// targets, and the same parameters. A reviewed plan is consent to what it
// shows and to nothing else, so every field of the request that can
// change what is submitted must be equal. That includes the whole policy:
// --yes, --force, --skip-health-check, and --require-healthy each change
// what a run may do.
//
// Three fields are not compared, because they cannot change what is
// submitted: Limits, NoWait, and Expect itself.
func sameRequest(reviewed *Plan, req Request) error {
	differs := ""
	switch {
	case reviewed.Request == nil:
		differs = "the reviewed plan has no request"
	case reviewed.Kind != req.kind() || reviewed.Request.kind() != req.kind():
		differs = fmt.Sprintf("it is a plan of %s, and this is %s", reviewed.Kind, req.kind())
	default:
		if field := req.differs(reviewed.Request); field != "" {
			differs = field + " differs"
		}
	}
	if differs == "" {
		return nil
	}
	return fmt.Errorf("%w (%s); nothing was changed", ErrOtherRequest, differs)
}

// differs names the first field of the target in which t differs from o.
func (t Target) differs(o Target) string {
	switch {
	case t.Cluster != o.Cluster:
		return "the cluster"
	case t.Region != o.Region:
		return "the region"
	}
	return ""
}

// differs names the first field of the policy in which c differs from o.
// Limits, NoWait, and Expect are not compared: see sameRequest.
func (c Common) differs(o Common) string {
	return policyDiffers(c.Policy, o.Policy)
}

func policyDiffers(p, o health.Policy) string {
	switch {
	case p.Yes != o.Yes:
		return "--yes"
	case p.RequireHealthy != o.RequireHealthy:
		return "--require-healthy"
	case p.Force != o.Force:
		return "--force"
	case p.Skip != o.Skip:
		return "--skip-health-check"
	}
	return ""
}

func (r AddonUpdateRequest) differs(reviewed Request) string {
	o, ok := reviewed.(AddonUpdateRequest)
	if !ok {
		return "the kind of change"
	}
	if field := r.Target.differs(o.Target); field != "" {
		return field
	}
	switch {
	case !slices.Equal(r.Addons, o.Addons):
		return "the add-ons"
	case r.All != o.All:
		return "--all"
	case r.Version != o.Version:
		return "--version"
	case !slices.Equal(r.Skip, o.Skip):
		return "--skip-addon"
	}
	return r.Common.differs(o.Common)
}

func (r ScaleRequest) differs(reviewed Request) string {
	o, ok := reviewed.(ScaleRequest)
	if !ok {
		return "the kind of change"
	}
	if field := r.Target.differs(o.Target); field != "" {
		return field
	}
	same := func(a, b *int32) bool {
		return (a == nil) == (b == nil) && (a == nil || *a == *b)
	}
	switch {
	case r.Nodegroup != o.Nodegroup:
		return "the nodegroup"
	case !same(r.Desired, o.Desired):
		return "--desired"
	case !same(r.Min, o.Min):
		return "--min"
	case !same(r.Max, o.Max):
		return "--max"
	}
	return r.Common.differs(o.Common)
}
