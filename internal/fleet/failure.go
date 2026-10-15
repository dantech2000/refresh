package fleet

import (
	"context"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
)

// FailureFromError returns the Failure for an AWS error about the item that
// kind and name identify. The reason comes from awsx.Classify, so it follows
// the type of the error and never its text. The caller sets Cluster and
// Region; for a Region failure, Region is set to name.
//
// For a Region failure, the error codes of a region that is not enabled for
// the account (UnrecognizedClientException, InvalidClientTokenId,
// AuthFailure) give RegionUnavailable. Invalid credentials return the same
// codes, but they fail every region, and a sweep in which no region
// answered checks the credentials before it reports. An access denial stays
// AccessDenied: a missing IAM action is the more useful diagnosis.
func FailureFromError(kind apidoc.FailureKind, name string, err error) apidoc.Failure {
	// Every class is also a reason; a test checks the two lists.
	reason := apidoc.Reason(awsx.Classify(err))
	if kind == apidoc.FailureRegion && reason != apidoc.ReasonAccessDenied && awsx.IsRegionInaccessible(err) {
		reason = apidoc.ReasonRegionUnavailable
	}
	msg := awsx.Summary(err)
	if host, ok := awsx.UnknownEndpoint(err); ok {
		msg = awsx.UnknownRegionMessage(host)
	}
	if msg == "" {
		msg = "unknown error"
	}
	f := apidoc.NewFailure(kind, name, reason, msg)
	f.Operation = awsx.Operation(err)
	f.AWSErrorCode = awsx.ErrorCode(err)
	if kind == apidoc.FailureRegion {
		f.Region = name
	}
	return f
}

// notAttempted returns the Failure of an item that the run never started,
// because ctx ended first.
func notAttempted(ctx context.Context, kind apidoc.FailureKind, name string) apidoc.Failure {
	why := "the run stopped early"
	if cause := ctx.Err(); cause != nil {
		why = cause.Error()
	}
	f := apidoc.NewFailure(kind, name, apidoc.ReasonNotAttempted, "not read: "+why)
	if kind == apidoc.FailureRegion {
		f.Region = name
	}
	return f
}
