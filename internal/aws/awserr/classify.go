package awserr

import (
	"errors"
	"net/http"
	"strings"

	"github.com/aws/smithy-go"
)

var (
	// throttlingErrorCodes are the API error codes AWS services return when
	// they rate-limit a caller.
	throttlingErrorCodes = map[string]bool{
		"Throttling":                true,
		"ThrottlingException":       true,
		"ThrottledException":        true,
		"TooManyRequestsException":  true,
		"RequestLimitExceeded":      true,
		"RequestThrottled":          true,
		"RequestThrottledException": true,
		"EC2ThrottledException":     true,
		"BandwidthLimitExceeded":    true,
		"SlowDown":                  true,
		"PriorRequestNotComplete":   true,
	}

	// notFoundErrorCodes are the API error codes for a resource that does
	// not exist. EC2 also uses "<Resource>.NotFound" codes, which
	// IsNotFound matches by suffix.
	notFoundErrorCodes = map[string]bool{
		"ResourceNotFoundException": true,
		"NotFoundException":         true,
		"NotFound":                  true,
		"ParameterNotFound":         true,
		"NoSuchEntity":              true,
	}

	// validationErrorCodes are the API error codes for a request the
	// service rejected as malformed. Codes that start with InvalidParameter
	// or InvalidRequest also match.
	validationErrorCodes = map[string]bool{
		"ValidationException": true,
		"ValidationError":     true,
		"MissingParameter":    true,
		"InvalidInput":        true,
	}

	// serverErrorCodes are the API error codes for a failure on the AWS
	// side. An API error with a server fault or an HTTP 5xx response also
	// counts.
	serverErrorCodes = map[string]bool{
		"InternalFailure":             true,
		"InternalError":               true,
		"InternalServerError":         true,
		"InternalServerException":     true,
		"ServerException":             true,
		"ServiceException":            true,
		"ServiceUnavailable":          true,
		"ServiceUnavailableException": true,
	}

	// regionDisabledErrorCodes are the API error codes that say a region is
	// not enabled for the account.
	regionDisabledErrorCodes = map[string]bool{
		"OptInRequired":           true,
		"RegionDisabledException": true,
	}
)

// ErrorCode returns the code of the AWS API error in err's chain, such as
// "AccessDeniedException", or "" when err did not come back from the API.
func ErrorCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

// httpStatus returns the HTTP status code of the response in err's chain, or
// 0 when there is none.
func httpStatus(err error) int {
	var status interface{ HTTPStatusCode() int }
	if errors.As(err, &status) {
		return status.HTTPStatusCode()
	}
	return 0
}

// IsThrottling reports whether err is a rate-limit response: a throttling
// API error code or an HTTP 429 response.
func IsThrottling(err error) bool {
	if err == nil {
		return false
	}
	if code := ErrorCode(err); code != "" {
		return throttlingErrorCodes[code]
	}
	return httpStatus(err) == http.StatusTooManyRequests
}

// IsNotFound reports whether err is an API error for a resource that does not
// exist, such as ResourceNotFoundException or an EC2 "*.NotFound" code.
func IsNotFound(err error) bool {
	code := ErrorCode(err)
	return notFoundErrorCodes[code] || strings.HasSuffix(code, ".NotFound")
}

// IsValidation reports whether err is an API error for a malformed request:
// InvalidParameter*, InvalidRequest*, ValidationException, and similar codes.
// A not-found code is not a validation error, even when it starts with
// "Invalid" (InvalidInstanceID.NotFound).
func IsValidation(err error) bool {
	code := ErrorCode(err)
	if code == "" || IsNotFound(err) {
		return false
	}
	return validationErrorCodes[code] ||
		strings.HasPrefix(code, "InvalidParameter") ||
		strings.HasPrefix(code, "InvalidRequest")
}

// IsServerError reports whether err is a failure on the AWS side: a server
// error code, an API error with a server fault, or an HTTP 5xx response.
func IsServerError(err error) bool {
	if err == nil {
		return false
	}
	var ae smithy.APIError
	if errors.As(err, &ae) && (serverErrorCodes[ae.ErrorCode()] || ae.ErrorFault() == smithy.FaultServer) {
		return true
	}
	return httpStatus(err) >= 500 && httpStatus(err) <= 599
}

// IsRegionDisabled reports whether err is an API error that says the region
// is not enabled for the account (OptInRequired, RegionDisabledException).
func IsRegionDisabled(err error) bool {
	return regionDisabledErrorCodes[ErrorCode(err)]
}
