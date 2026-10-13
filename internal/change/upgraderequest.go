package change

import (
	"fmt"
	"slices"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// The kinds of change whose phases are a sequence: a phase that fails has
// failed the whole operation.
const (
	ClusterUpgrade  Kind = "ClusterUpgrade"
	ClusterRollback Kind = "ClusterRollback"
)

// UpgradeRequest upgrades one cluster to a Kubernetes version: the
// control plane, the add-ons, and the managed nodegroups, one minor
// version at a time.
type UpgradeRequest struct {
	Common
	Target
	// To is the version to reach. The version that the control plane runs
	// is a catch-up: only the add-ons and the nodegroups move.
	To fleet.Version
	// Only limits the change to these parts. Empty means every part.
	Only []Part
	// Nodegroups limits the rolls to these nodegroups, by exact name.
	// Empty means every nodegroup.
	Nodegroups []string
	// SkipAddons and SkipNodegroups name what the run leaves alone, by
	// exact name.
	SkipAddons     []string
	SkipNodegroups []string
	// SkipInsights leaves the EKS cluster insights out of the readiness
	// of a hop.
	SkipInsights bool
}

func (UpgradeRequest) kind() Kind { return ClusterUpgrade }

func (r UpgradeRequest) differs(reviewed Request) string {
	o, ok := reviewed.(UpgradeRequest)
	if !ok {
		return "the kind of change"
	}
	if field := r.Target.differs(o.Target); field != "" {
		return field
	}
	switch {
	case r.To != o.To:
		return "--to"
	case !slices.Equal(r.Only, o.Only):
		return "--only"
	case !slices.Equal(r.Nodegroups, o.Nodegroups):
		return "--nodegroup"
	case !slices.Equal(r.SkipAddons, o.SkipAddons):
		return "--skip-addon"
	case !slices.Equal(r.SkipNodegroups, o.SkipNodegroups):
		return "--skip-nodegroup"
	case r.SkipInsights != o.SkipInsights:
		return "--skip-insights-check"
	}
	return r.Common.differs(o.Common)
}

// RollbackRequest rolls one cluster back to the minor version before the
// one that it runs: the nodegroups, then the add-ons that the older
// version cannot run, then the control plane.
type RollbackRequest struct {
	Common
	Target
	// SkipNodegroups names nodegroups that the run leaves alone, by exact
	// name. The control plane does not roll back while one of them is
	// above the target.
	SkipNodegroups []string
	// SkipInsights stops the rollback readiness insights from blocking,
	// and sets force on the control-plane request, so EKS skips its own
	// insight checks too.
	SkipInsights bool
	// RollbackTimeout is the time that EKS gives the control-plane
	// rollback. Zero leaves the default of EKS.
	RollbackTimeout time.Duration
}

func (RollbackRequest) kind() Kind { return ClusterRollback }

func (r RollbackRequest) differs(reviewed Request) string {
	o, ok := reviewed.(RollbackRequest)
	if !ok {
		return "the kind of change"
	}
	if field := r.Target.differs(o.Target); field != "" {
		return field
	}
	switch {
	case !slices.Equal(r.SkipNodegroups, o.SkipNodegroups):
		return "--skip-nodegroup"
	case r.SkipInsights != o.SkipInsights:
		return "--skip-insights-check"
	case r.RollbackTimeout != o.RollbackTimeout:
		return "--rollback-timeout"
	}
	return r.Common.differs(o.Common)
}

// The bounds of RollbackRequest.RollbackTimeout, which EKS sets for the
// rollback configuration of an update.
const (
	MinRollbackTimeout = 2 * time.Hour
	MaxRollbackTimeout = 7 * 24 * time.Hour
)

// ValidateRollbackTimeout checks a --rollback-timeout value: 0, which
// leaves the default of EKS, or a whole number of minutes from 2h to 7
// days. A command calls it before any request.
func ValidateRollbackTimeout(d time.Duration) error {
	if d == 0 {
		return nil
	}
	if d < MinRollbackTimeout || d > MaxRollbackTimeout || d%time.Minute != 0 {
		return &RefusalError{
			Reason:  RefusalRollbackTimeout,
			Message: fmt.Sprintf("--rollback-timeout must be whole minutes from 2h to 168h (7 days), got %s", d),
		}
	}
	return nil
}

// RefusalReason says why a planner returned no plan.
type RefusalReason string

// The reasons.
const (
	// RefusalVersionUnknown: the version of the control plane, or the
	// version to reach, is not known.
	RefusalVersionUnknown RefusalReason = "VersionUnknown"
	// RefusalOlderTarget: --to is older than the control plane.
	RefusalOlderTarget RefusalReason = "OlderTarget"
	// RefusalOtherMajor: --to is in another major version.
	RefusalOtherMajor RefusalReason = "OtherMajor"
	// RefusalTooManyHops: --to is further ahead than one plan crosses.
	RefusalTooManyHops RefusalReason = "TooManyHops"
	// RefusalNotOffered: EKS does not offer --to.
	RefusalNotOffered RefusalReason = "NotOffered"
	// RefusalNeedsControlPlane: --only leaves the control plane out, and
	// --to is not the version that it runs.
	RefusalNeedsControlPlane RefusalReason = "NeedsControlPlane"
	// RefusalNeedsAddons: --only leaves the add-ons out, and --to is more
	// than one minor version ahead.
	RefusalNeedsAddons RefusalReason = "NeedsAddons"
	// RefusalNoPreviousVersion: the cluster runs the first minor version,
	// so there is none to roll back to.
	RefusalNoPreviousVersion RefusalReason = "NoPreviousVersion"
	// RefusalRollbackTimeout: --rollback-timeout is out of bounds.
	RefusalRollbackTimeout RefusalReason = "RollbackTimeout"
	// RefusalTargetChanged: the plan computed now rolls back to another
	// version than the plan that a person reviewed.
	RefusalTargetChanged RefusalReason = "TargetChanged"
)

// RefusalError is the error of a request that a planner refuses before
// it derives a step: the request cannot be kept safe, or names a version
// that cannot be reached. A caller tells the cases apart by Reason.
type RefusalError struct {
	Reason  RefusalReason
	Message string
}

func (e *RefusalError) Error() string { return e.Message }

// VersionOffer is what EKS says about one Kubernetes version: the answer
// of DescribeClusterVersions for it.
type VersionOffer struct {
	// Offered is false when EKS does not list the version.
	Offered bool
	// Tier is the support tier of the version now. Only a rollback reads
	// it.
	Tier apidoc.SupportTier
	// Err is the error of the read. The other fields are then not set.
	Err error
}

// InsightsRead is the EKS cluster insights of one category as a planner
// reads them, or the error of reading them.
type InsightsRead struct {
	// Insights are the insights of the category, of every status.
	Insights []apidoc.Insight
	// Err is the error of ListInsights.
	Err error
	// Refreshed says that EKS evaluated the insights anew before the
	// read, which a real upgrade asks for. Without it the read is the
	// preview of a dry run: an insight that is missing or UNKNOWN is a
	// notice, because a real run refreshes first.
	Refreshed bool
	// RefreshErr is the error of that refresh. The insights were then not
	// read.
	RefreshErr error
}
