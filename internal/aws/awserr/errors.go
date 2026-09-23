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

// apiErrorCode returns the typed AWS API error code, or "" when err did not
// come back from the API.
func apiErrorCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

// IsCredentialError reports whether err is an AWS credentials problem: a
// credential-class API error code, a SigV4 signing failure, an expired SSO
// session, empty static credentials, or (fallback) an SDK credential-chain
// failure that carries no type.
func IsCredentialError(err error) bool {
	if err == nil {
		return false
	}
	if code := apiErrorCode(err); code != "" {
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
	if permissionErrorCodes[apiErrorCode(err)] {
		return true
	}
	var status interface{ HTTPStatusCode() int }
	return errors.As(err, &status) && status.HTTPStatusCode() == 403
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
	return inaccessibleRegionCodes[apiErrorCode(err)]
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
		return &formattedError{msg: fmt.Sprintf("timed out while %s (increase --timeout to allow more time)", operation), err: err}
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
		return fmt.Sprintf("%s: %s", ae.ErrorCode(), ae.ErrorMessage())
	}
	msg, _, _ := strings.Cut(err.Error(), "\n")
	return strings.TrimSpace(msg)
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
- eks:ListClusters
- eks:DescribeCluster
- eks:ListNodegroups
- eks:DescribeNodegroup
- eks:UpdateNodegroupVersion
- eks:UpdateClusterVersion (for cluster upgrade)
- eks:DescribeUpdate (for cluster upgrade)
- eks:DescribeClusterVersions (for cluster upgrade and status support calendar)
- eks:ListInsights (for cluster upgrade readiness / upgrade-check)
- eks:DescribeInsight (for upgrade-check insight detail)
- eks:ListAddons / eks:DescribeAddon / eks:DescribeAddonVersions (for addon status / version-skew)
- ec2:DescribeImages / ec2:DescribeInstances (for AMI staleness and compute detection)
- ssm:GetParameter (for the latest recommended EKS AMI lookup)
- cloudwatch:GetMetricStatistics (for health checks)

Current error: %w`, operation, err)
}
