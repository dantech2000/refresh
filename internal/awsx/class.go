package awsx

import (
	"context"
	"errors"
	"net"

	"github.com/aws/smithy-go"
)

// ErrorClass is why an AWS call failed, from a closed set. The values are
// the reasons that a document's failure list prints.
type ErrorClass string

// The error classes.
const (
	// ClassAccessDenied: IAM, or an SCP, denied the call.
	ClassAccessDenied ErrorClass = "AccessDenied"
	// ClassCredentialError: the credentials are missing, expired, or
	// invalid.
	ClassCredentialError ErrorClass = "CredentialError" //nolint:gosec // G101: a class name, not a credential
	// ClassThrottled: AWS rate-limited the call, after the retries.
	ClassThrottled ErrorClass = "Throttled"
	// ClassNotFound: the resource does not exist.
	ClassNotFound ErrorClass = "NotFound"
	// ClassRegionUnavailable: the region is not enabled for the account,
	// or it cannot be reached.
	ClassRegionUnavailable ErrorClass = "RegionUnavailable"
	// ClassInvalidRequest: AWS rejected the request as malformed.
	ClassInvalidRequest ErrorClass = "InvalidRequest"
	// ClassServiceError: AWS failed on its side.
	ClassServiceError ErrorClass = "ServiceError"
	// ClassNetworkError: the request got no response from AWS.
	ClassNetworkError ErrorClass = "NetworkError"
	// ClassTimeout: the deadline of the run passed.
	ClassTimeout ErrorClass = "Timeout"
	// ClassInterrupted: the user stopped the run.
	ClassInterrupted ErrorClass = "Interrupted"
	// ClassNotAttempted: the run ended before the request was sent.
	ClassNotAttempted ErrorClass = "NotAttempted"
	// ClassUnknown: any other error.
	ClassUnknown ErrorClass = "Unknown"
)

// ErrorClasses returns every class, in documentation order.
func ErrorClasses() []ErrorClass {
	return []ErrorClass{
		ClassAccessDenied, ClassCredentialError, ClassThrottled, ClassNotFound,
		ClassRegionUnavailable, ClassInvalidRequest, ClassServiceError, ClassNetworkError,
		ClassTimeout, ClassInterrupted, ClassNotAttempted, ClassUnknown,
	}
}

// Classify returns the class of err. It reads the types in the chain of
// err: context errors, API error codes, HTTP statuses, and net errors. So
// an error wrapped with %w, or by FormatAWSError, has the class of the
// original. Only the SDK failures that carry no type, a credential chain or
// an endpoint region error, are matched by the text the SDK wrote. A nil
// error is ClassUnknown.
func Classify(err error) ErrorClass {
	switch {
	case err == nil:
		return ClassUnknown
	case errors.Is(err, context.Canceled):
		return ClassInterrupted
	case errors.Is(err, ErrNotSent):
		return ClassNotAttempted
	// A connect timeout also matches context.DeadlineExceeded, but the
	// request never reached AWS: a network error, not the deadline of the
	// run. A DNS failure keeps its own path below (an invalid region).
	case IsDialFailure(err) && !isDNS(err):
		return ClassNetworkError
	case errors.Is(err, context.DeadlineExceeded):
		return ClassTimeout
	// A credential code must win over the HTTP 403 check of
	// IsPermissionError: STS returns ExpiredToken with status 403.
	case IsCredentialError(err):
		return ClassCredentialError
	case IsPermissionError(err):
		return ClassAccessDenied
	case IsRegionDisabled(err):
		return ClassRegionUnavailable
	case IsThrottling(err):
		return ClassThrottled
	case IsNotFound(err):
		return ClassNotFound
	case IsValidation(err):
		return ClassInvalidRequest
	case IsServerError(err):
		return ClassServiceError
	case ErrorCode(err) != "":
		// Any other answer of the API: the code says more than a class.
		return ClassUnknown
	// A missing region, or an AWS endpoint host that does not resolve,
	// before the general network check: NXDOMAIN is a net error too.
	case IsRegionError(err):
		return ClassRegionUnavailable
	case IsNetworkError(err):
		return ClassNetworkError
	}
	return ClassUnknown
}

func isDNS(err error) bool {
	var dns *net.DNSError
	return errors.As(err, &dns)
}

// iamPrefixes maps the service ID of an SDK client to the prefix of its IAM
// actions.
var iamPrefixes = map[string]string{
	"EKS":            "eks",
	"EC2":            "ec2",
	"Auto Scaling":   "autoscaling",
	"SSM":            "ssm",
	"CloudWatch":     "cloudwatch",
	"Service Quotas": "servicequotas",
	"STS":            "sts",
}

// Operation returns the IAM action of the call that failed with err, such
// as "eks:DescribeCluster", or "" when err did not come from a call of a
// service that refresh uses. The outermost call in the chain of err wins.
func Operation(err error) string {
	var op *smithy.OperationError
	if !errors.As(err, &op) {
		return ""
	}
	prefix, ok := iamPrefixes[op.ServiceID]
	if !ok {
		return ""
	}
	return prefix + ":" + op.OperationName
}
