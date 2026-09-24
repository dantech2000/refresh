package diag

import (
	"context"
	"errors"

	"github.com/dantech2000/refresh/internal/aws/awserr"
)

// Reason is why a Failure happened, from a closed set. Scripts branch on it.
// Later versions may add reasons; consumers must accept unknown values.
type Reason string

// The failure reasons. docs/concepts/output.md documents each one.
const (
	// ReasonAccessDenied: IAM (or an SCP) denied the call.
	ReasonAccessDenied Reason = "AccessDenied"
	// ReasonCredentialError: the credentials are missing, expired, or
	// invalid.
	ReasonCredentialError Reason = "CredentialError" //nolint:gosec // G101: a reason name, not a credential
	// ReasonThrottled: AWS rate-limited the call, after the retries.
	ReasonThrottled Reason = "Throttled"
	// ReasonNotFound: the resource does not exist.
	ReasonNotFound Reason = "NotFound"
	// ReasonRegionUnavailable: the region is not enabled for the account,
	// or it cannot be reached.
	ReasonRegionUnavailable Reason = "RegionUnavailable"
	// ReasonInvalidRequest: AWS rejected the request as malformed.
	ReasonInvalidRequest Reason = "InvalidRequest"
	// ReasonServiceError: AWS failed on its side (5xx).
	ReasonServiceError Reason = "ServiceError"
	// ReasonNetworkError: the request got no response from AWS.
	ReasonNetworkError Reason = "NetworkError"
	// ReasonTimeout: the run's deadline (--timeout or --wait-timeout)
	// passed.
	ReasonTimeout Reason = "Timeout"
	// ReasonInterrupted: the user stopped the run (Ctrl+C or SIGTERM).
	ReasonInterrupted Reason = "Interrupted"
	// ReasonUpdateFailed: an EKS update ended with status Failed.
	ReasonUpdateFailed Reason = "UpdateFailed"
	// ReasonUpdateCancelled: an EKS update ended with status Cancelled.
	ReasonUpdateCancelled Reason = "UpdateCancelled"
	// ReasonNotMonitored: status polling stopped; the EKS update may still
	// be running.
	ReasonNotMonitored Reason = "NotMonitored"
	// ReasonNotAttempted: the run stopped before this item started.
	ReasonNotAttempted Reason = "NotAttempted"
	// ReasonUnknown: any other error.
	ReasonUnknown Reason = "Unknown"
)

// retryable is the Retryable value of each reason.
var retryable = map[Reason]bool{
	ReasonAccessDenied:      false,
	ReasonCredentialError:   false,
	ReasonThrottled:         true,
	ReasonNotFound:          false,
	ReasonRegionUnavailable: false,
	ReasonInvalidRequest:    false,
	ReasonServiceError:      true,
	ReasonNetworkError:      true,
	ReasonTimeout:           true,
	ReasonInterrupted:       true,
	ReasonUpdateFailed:      false,
	ReasonUpdateCancelled:   false,
	ReasonNotMonitored:      true,
	ReasonNotAttempted:      true,
	ReasonUnknown:           false,
}

// Reasons returns every Reason, in documentation order.
func Reasons() []Reason {
	return []Reason{
		ReasonAccessDenied,
		ReasonCredentialError,
		ReasonThrottled,
		ReasonNotFound,
		ReasonRegionUnavailable,
		ReasonInvalidRequest,
		ReasonServiceError,
		ReasonNetworkError,
		ReasonTimeout,
		ReasonInterrupted,
		ReasonUpdateFailed,
		ReasonUpdateCancelled,
		ReasonNotMonitored,
		ReasonNotAttempted,
		ReasonUnknown,
	}
}

// Retryable reports whether running the same command again, with no other
// change, may succeed after a failure for reason r. An unknown reason is not
// retryable.
func (r Reason) Retryable() bool {
	return retryable[r]
}

// Classify returns the Reason for err, whether that reason is retryable, and
// the AWS API error code in err's chain ("" when there is none). It checks
// types (errors.Is and errors.As on context errors, AWS API error codes,
// HTTP status codes, and net errors), so errors wrapped with %w or by
// awserr.FormatAWSError classify the same as the originals. Only the SDK
// failures that carry no type (a credential chain or endpoint region error)
// fall back to awserr's match on SDK-authored message prefixes. A nil err is
// ReasonUnknown.
func Classify(err error) (reason Reason, isRetryable bool, awsCode string) {
	reason = classify(err)
	return reason, reason.Retryable(), awserr.ErrorCode(err)
}

func classify(err error) Reason {
	switch {
	case err == nil:
		return ReasonUnknown
	case errors.Is(err, context.Canceled):
		return ReasonInterrupted
	case errors.Is(err, context.DeadlineExceeded):
		return ReasonTimeout
	// A credential code must win over IsPermissionError's HTTP 403 check:
	// STS returns ExpiredToken with status 403.
	case awserr.IsCredentialError(err):
		return ReasonCredentialError
	case awserr.IsPermissionError(err):
		return ReasonAccessDenied
	case awserr.IsRegionDisabled(err):
		return ReasonRegionUnavailable
	case awserr.IsThrottling(err):
		return ReasonThrottled
	case awserr.IsNotFound(err):
		return ReasonNotFound
	case awserr.IsValidation(err):
		return ReasonInvalidRequest
	case awserr.IsServerError(err):
		return ReasonServiceError
	case awserr.ErrorCode(err) != "":
		// Any other API response: the code is in the Failure, the
		// reason stays Unknown.
		return ReasonUnknown
	// A missing region, or an AWS endpoint host that does not resolve,
	// before the general network check (NXDOMAIN is also a net error).
	case awserr.IsRegionError(err):
		return ReasonRegionUnavailable
	case awserr.IsNetworkError(err):
		return ReasonNetworkError
	}
	return ReasonUnknown
}
