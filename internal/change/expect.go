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
// change what is submitted must be equal. That includes --force,
// --skip-health-check, and --require-healthy: each changes what a run may
// do.
//
// Some fields are not compared. Limits, NoWait, SkipVerify, Nodes, and
// Expect itself cannot change what is submitted. Policy.Yes is how the consent is given, and no
// part of what is consented to: a plan that was reviewed without --yes and
// is run with it is the same plan, and so is the reverse.
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
// Limits, NoWait, Expect, and Policy.Yes are not compared: see
// sameRequest.
func (c Common) differs(o Common) string {
	return policyDiffers(c.Policy, o.Policy)
}

// policyDiffers compares every field of the policy but Yes.
func policyDiffers(p, o health.Policy) string {
	switch {
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

func (r RollRequest) differs(reviewed Request) string {
	o, ok := reviewed.(RollRequest)
	if !ok {
		return "the kind of change"
	}
	if field := r.Target.differs(o.Target); field != "" {
		return field
	}
	text := func(s *Selector) string {
		if s == nil {
			return ""
		}
		return s.Text
	}
	regions := func(f *FleetScope) []string {
		if f == nil {
			return nil
		}
		return f.Regions.Names
	}
	switch {
	case !slices.Equal(r.Nodegroups, o.Nodegroups):
		return "the nodegroups"
	case (r.Select == nil) != (o.Select == nil) || text(r.Select) != text(o.Select):
		return "the filter"
	case r.Reroll != o.Reroll:
		return "--reroll"
	case (r.Fleet == nil) != (o.Fleet == nil):
		return "--all-clusters"
	case !slices.Equal(regions(r.Fleet), regions(o.Fleet)):
		return "the regions"
	}
	return r.Common.differs(o.Common)
}
