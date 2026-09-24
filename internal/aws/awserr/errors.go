// Package awserr classifies and formats AWS SDK errors, and pages through AWS
// list APIs with the shared retry and formatting policy.
//
// It is a leaf package: it imports neither internal/ui nor internal/health, so
// every layer (including the health checks, which internal/ui imports) can
// format AWS errors without an import cycle. internal/aws re-exports
// FormatAWSError and ListAllPages so existing callers keep their imports.
package awserr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/smithy-go"
)

var (
	// API error codes that indicate a credentials problem (vs. missing IAM
	// permissions, which is AccessDenied*).
	credentialErrorCodes = map[string]bool{
		"UnrecognizedClientException": true,
		"InvalidClientTokenId":        true,
		"ExpiredToken":                true,
		"ExpiredTokenException":       true,
		"InvalidSignatureException":   true,
		"SignatureDoesNotMatch":       true,
	}

	permissionErrorCodes = map[string]bool{
		"AccessDeniedException": true,
		"AccessDenied":          true,
		"UnauthorizedOperation": true,
		"Forbidden":             true,
	}

	// credentialFallbackPatterns is the string fallback for credential-chain
	// failures. The SDK's generated auth middleware wraps identity resolution
	// failures in plain fmt errors ("get identity: get credentials: ...") and
	// the credential cache does the same ("failed to refresh cached
	// credentials, ..."), so no typed error reaches the caller. Keep this list
	// to SDK-authored prefixes only.
	credentialFallbackPatterns = []string{
		"get identity: ",
		"failed to refresh cached credentials",
		"failed to retrieve credentials",
		"no ec2 imds role found",
	}

	// regionFallbackPatterns is the string fallback for endpoint resolution
	// failures. The generated endpoint resolvers return plain fmt errors for
	// a malformed or missing region.
	regionFallbackPatterns = []string{
		"invalid input region",
		"invalid configuration: missing region",
	}
)

// IsCredentialError reports whether err is an AWS credentials problem: a
// credential-class API error code, a SigV4 signing failure, an expired SSO
// session, empty static credentials, or (fallback) an SDK credential-chain
// failure that carries no type.
func IsCredentialError(err error) bool {
	if err == nil {
		return false
	}
	if code := ErrorCode(err); code != "" {
		return credentialErrorCodes[code]
	}
	var signErr *v4.SigningError
	var ssoErr *ssocreds.InvalidTokenError
	var staticErr *credentials.StaticCredentialsEmptyError
	if errors.As(err, &signErr) || errors.As(err, &ssoErr) || errors.As(err, &staticErr) {
		return true
	}
	return containsAny(err.Error(), credentialFallbackPatterns)
}

// IsPermissionError reports whether err is an IAM denial: a permission-class
// API error code, or an HTTP 403 response.
func IsPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if permissionErrorCodes[ErrorCode(err)] {
		return true
	}
	return httpStatus(err) == http.StatusForbidden
}

// IsRegionError reports whether err points at AWS region misconfiguration: a
// missing region, an AWS endpoint host that does not resolve (NXDOMAIN), or
// (fallback) an endpoint resolver's region error. Matching is deliberately
// narrow: many unrelated AWS errors merely mention a region name ("user is not
// authorized ... in region us-east-1"), and those must not be diagnosed as
// region misconfiguration.
func IsRegionError(err error) bool {
	if err == nil {
		return false
	}
	var missing *aws.MissingRegionError
	if errors.As(err, &missing) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound && isAWSHost(dnsErr.Name) {
		return true
	}
	return containsAny(err.Error(), regionFallbackPatterns)
}

// isAWSHost reports whether host is an AWS service endpoint.
func isAWSHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return strings.HasSuffix(host, ".amazonaws.com") || strings.HasSuffix(host, ".amazonaws.com.cn")
}

// IsNetworkError reports whether err is a transport failure that never got an
// API response: a failed HTTP round trip (*url.Error), a dial/read/write
// failure (*net.OpError), a DNS failure, a timeout, a refused or reset
// connection, or a response cut short. Context cancellation and deadlines are
// not network errors; callers check those first.
func IsNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var urlErr *url.Error
	var opErr *net.OpError
	var dnsErr *net.DNSError
	if errors.As(err, &urlErr) || errors.As(err, &opErr) || errors.As(err, &dnsErr) {
		return true
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return true
	}
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// inaccessibleRegionCodes are API error codes from a region the caller's
// credentials can't use: an SCP region restriction (AccessDenied*), a region
// that isn't enabled for the account (UnrecognizedClientException,
// InvalidClientTokenId, AuthFailure, OptInRequired), or a disabled region.
var inaccessibleRegionCodes = map[string]bool{
	"AccessDenied":                true,
	"AccessDeniedException":       true,
	"UnrecognizedClientException": true,
	"InvalidClientTokenId":        true,
	"AuthFailure":                 true,
	"OptInRequired":               true,
	"RegionDisabledException":     true,
}

// IsRegionInaccessible reports whether err says a region is closed to these
// credentials, as opposed to a transient failure (throttling after retries,
// 5xx, timeouts) that must still fail a run. Multi-region sweeps use it to
// skip such regions when the user did not ask for them by name.
func IsRegionInaccessible(err error) bool {
	return inaccessibleRegionCodes[ErrorCode(err)]
}

// containsAny reports whether s contains any pattern, case-insensitively.
// Patterns must be lower case.
func containsAny(s string, patterns []string) bool {
	s = strings.ToLower(s)
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// formattedError carries a user-facing message and still unwraps to the
// original error, so errors.Is(err, context.Canceled) and
// errors.As(err, &smithy.APIError) keep working after formatting without the
// cause's text changing the message.
type formattedError struct {
	msg string
	err error
}

func (e *formattedError) Error() string { return e.msg }
func (e *formattedError) Unwrap() error { return e.err }

// formatted builds a formattedError wrapping err. format is a fmt format in
// which %w stands for err's text.
func formatted(err error, format string, args ...any) error {
	return &formattedError{msg: fmt.Sprintf(strings.ReplaceAll(format, "%w", "%v"), args...), err: err}
}

// DefaultTimeoutFlag is the flag FormatAWSError's timeout hint names: the
// global API timeout.
const DefaultTimeoutFlag = "--timeout"

// deadlineHint is the remediation text of a timed-out operation.
func deadlineHint(flag string) string {
	return fmt.Sprintf("(increase %s to allow more time)", flag)
}

// TimeoutHint returns the remediation text FormatAWSError adds to a
// timed-out operation, "(increase --timeout to allow more time)". Code that
// reports a deadline itself uses it, so RetargetDeadlineHint can name the
// flag that set the deadline.
func TimeoutHint() string {
	return deadlineHint(DefaultTimeoutFlag)
}

// retargetedError is err with its timeout hint naming another flag.
type retargetedError struct {
	msg string
	err error
}

func (e *retargetedError) Error() string { return e.msg }
func (e *retargetedError) Unwrap() error { return e.err }

// RetargetDeadlineHint returns err with FormatAWSError's "increase
// --timeout" hint naming flag instead, for a command whose run deadline is
// another flag (such as --wait-timeout). An err without the hint is returned
// as is. The result wraps err, so errors.Is/As still work.
func RetargetDeadlineHint(err error, flag string) error {
	if err == nil || flag == DefaultTimeoutFlag {
		return err
	}
	msg := err.Error()
	old := deadlineHint(DefaultTimeoutFlag)
	if !strings.Contains(msg, old) {
		return err
	}
	return &retargetedError{msg: strings.ReplaceAll(msg, old, deadlineHint(flag)), err: err}
}

// FormatAWSError provides user-friendly error messages for AWS errors. Every
// returned error wraps err.
//
// Classification order matters: context cancellation is user-initiated and
// needs no remediation help; typed API errors carry an authoritative error
// code and must win over any other check (an IAM denial that mentions
// "region" in its message is not a region misconfiguration); the transport
// and credential-chain checks cover failures that never reached the API.
func FormatAWSError(err error, operation string) error {
	if err == nil {
		return nil
	}
	// Already formatted (e.g. by ListAllPages): formatting it again would
	// repeat the remediation text, such as the IAM permission list.
	var already *formattedError
	if errors.As(err, &already) {
		return err
	}

	if errors.Is(err, context.Canceled) {
		return &formattedError{msg: fmt.Sprintf("operation cancelled while %s", operation), err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &formattedError{msg: fmt.Sprintf("timed out while %s %s", operation, deadlineHint(DefaultTimeoutFlag)), err: err}
	}

	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch {
		case permissionErrorCodes[ae.ErrorCode()]:
			return formatPermissionError(err, operation)
		case credentialErrorCodes[ae.ErrorCode()]:
			return formatCredentialError(err)
		default:
			return &formattedError{
				msg: fmt.Sprintf("AWS API error while %s: %s (%s)", operation, ae.ErrorMessage(), ae.ErrorCode()),
				err: err,
			}
		}
	}

	if IsCredentialError(err) {
		return formatCredentialError(err)
	}
	if IsRegionError(err) {
		return formatRegionError(err, operation)
	}
	if IsNetworkError(err) {
		return formatNetworkError(err, operation)
	}
	if IsPermissionError(err) {
		return formatPermissionError(err, operation)
	}

	// Default case - return the error with context
	return formatted(err, "error while %s: %w", operation, err)
}

// Summary renders err on one line, for a table cell or a status field where
// FormatAWSError's multi-line remediation text would break the layout. An AWS
// API error shows its code and message; any other error shows its first line.
func Summary(err error) string {
	if err == nil {
		return ""
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return oneLine(fmt.Sprintf("%s: %s", ae.ErrorCode(), ae.ErrorMessage()))
	}
	msg := err.Error()
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = msg[:i]
	}
	return strings.TrimSpace(msg)
}

// oneLine collapses each run of white space in s, line breaks included, to
// one space. An AWS error message can span lines; a summary must not.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func formatRegionError(err error, operation string) error {
	return formatted(err, `AWS region configuration issue while %s.

Please verify:
- AWS_DEFAULT_REGION environment variable is set to a valid region
- Region in ~/.aws/config matches an AWS region
- Common regions: us-east-1, us-west-2, eu-west-1, ap-southeast-1

Current error indicates an invalid or unsupported region.

Current error: %w`, operation, err)
}

func formatCredentialError(err error) error {
	return formatted(err, `AWS credentials not configured or invalid.

Please set up your AWS credentials using one of these methods:

1. AWS CLI configuration:
   aws configure

2. Environment variables:
   export AWS_ACCESS_KEY_ID="your-access-key"
   export AWS_SECRET_ACCESS_KEY="your-secret-key"
   export AWS_DEFAULT_REGION="us-west-2"

3. IAM role (if running on EC2/EKS/Lambda)

4. AWS SSO:
   aws sso login

Current error: %w`, err)
}

func formatNetworkError(err error, operation string) error {
	return formatted(err, `network connectivity issue while %s.

Please check:
- Internet connection
- AWS service endpoints are accessible
- VPC/Security group settings (if running in private network)
- Regional service availability

Current error: %w`, operation, err)
}

func formatPermissionError(err error, operation string) error {
	return formatted(err, `insufficient AWS permissions while %s.

Required permissions for refresh tool:
%s
See %s

Current error: %w`, operation, permissionHint(), PermissionsDocURL, err)
}
