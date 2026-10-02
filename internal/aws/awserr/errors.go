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
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/common"
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

	// ssoLoginFallbackPatterns is the string fallback for an expired
	// sso-session login: the SDK's SSO token provider returns plain fmt
	// errors with these SDK-authored prefixes, and no typed error.
	ssoLoginFallbackPatterns = []string{
		"cached sso token is expired, or not present, and cannot be refreshed",
	}

	// ssoLoginRejections are the SSO OIDC CreateToken codes that mean the
	// saved login can no longer be renewed: log in again.
	ssoLoginRejections = map[string]bool{
		"InvalidGrantException":       true,
		"ExpiredTokenException":       true,
		"AccessDeniedException":       true,
		"UnauthorizedClientException": true,
		"InvalidClientException":      true,
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

// IsNoCredentials reports whether the credential chain found no
// credentials: it ran down to its last resort, the EC2 instance metadata
// service, and that call failed. Off EC2 that most often means no profile
// was chosen (`aws sso login --profile X` caches a login, but refresh uses
// profile X only when told to); on EC2, a missing instance role or an
// unreachable metadata service. The SDK gives IMDS a short timeout of its
// own, so its "deadline exceeded" is this, not the --timeout.
func IsNoCredentials(err error) bool {
	// An IMDS failure can sit inside another operation error (an
	// AssumeRole profile whose credential_source is Ec2InstanceMetadata):
	// look inside each operation error errors.As finds.
	for e := err; e != nil; {
		var op *smithy.OperationError
		if !errors.As(e, &op) {
			return false
		}
		if op.ServiceID == "ec2imds" {
			return true
		}
		e = op.Err
	}
	return false
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
	// Before the timeout and network checks: the chain found nothing and
	// asked IMDS last, so its timeout or dial failure is the symptom, not
	// the cause.
	if IsNoCredentials(err) {
		return FormatNoCredentials(err, nil)
	}
	// Before the API error switch: an expired SSO session comes back as an
	// SSO OIDC API error (InvalidGrantException) that says nothing useful.
	if isSSOEndpointNotFound(err) {
		return formatSSORegionError(err)
	}
	if IsSSONotLoggedIn(err) {
		return FormatSSONotLoggedIn(err, "")
	}
	// A connect timeout also reports itself as context.DeadlineExceeded,
	// but a longer --timeout does not help an endpoint that cannot be
	// reached (#418). A DNS failure keeps its own path below (an invalid
	// region's endpoint does not resolve).
	var dns *net.DNSError
	if common.IsDialFailure(err) && !errors.As(err, &dns) && !errors.Is(err, context.Canceled) {
		return formatNetworkError(err, operation)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &formattedError{msg: fmt.Sprintf("timed out while %s %s", operation, deadlineHint(DefaultTimeoutFlag)), err: err}
	}

	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch {
		case permissionErrorCodes[ae.ErrorCode()] && isAssumeRole(err):
			return formatAssumeRoleError(err)
		case isSSORoleCall(err) && ae.ErrorCode() == "ForbiddenException":
			return formatSSORoleError(err)
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

	// A missing region first: an AssumeRole profile with no region fails
	// inside the credential chain, and its text matches the credential
	// fallback patterns too, but the region is what to fix.
	if isMissingRegion(err) {
		return formatRegionError(err, operation)
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
	if isUnreadable403(err) {
		return formatUnreadable403(err, operation)
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
	// A network failure below the SDK: the dial error says what failed
	// ("dial tcp …: connection refused") without the SDK's retry chain.
	// An error FormatAWSError already formatted keeps its own headline.
	var fe *formattedError
	var ne *net.OpError
	if !errors.As(err, &fe) && errors.As(err, &ne) {
		return oneLine(ne.Error())
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

// The help texts share one layout, which render.ErrorLines styles: a
// headline, the cause on a "Cause:" (or "AWS:") line, then a heading and
// indented rows whose second column starts after two spaces.

func formatRegionError(err error, operation string) error {
	return formatted(err, `AWS region configuration issue while %s
Cause: %s

Set a valid region:
  -r <region>              for this command (status and the fleet commands take several)
  AWS_REGION=<region>      for this shell
  region = <region>        in the profile in ~/.aws/config
  common regions           us-east-1, us-west-2, eu-west-1, ap-southeast-1`, operation, Summary(err))
}

func formatCredentialError(err error) error {
	msg := fmt.Sprintf(`AWS credentials not configured or invalid
Cause: %s

Set up credentials in one of these ways:
  aws sso login            an SSO profile (IAM Identity Center); pick it with --profile or AWS_PROFILE
  aws configure            an access key in a profile
  AWS_ACCESS_KEY_ID        with AWS_SECRET_ACCESS_KEY, as environment variables
  an IAM role              when refresh runs on EC2, EKS, or Lambda`, Summary(err))
	return &formattedError{msg: msg, err: err}
}

// KeysShadowProfileNote is the note a credential error gets when the keys
// came from the environment while AWS_PROFILE is set: the SDK uses the keys,
// as the AWS CLI does, so AWS_PROFILE has no effect. The caller decides
// that from the resolved credentials' source.
const KeysShadowProfileNote = `

Note:
  AWS_ACCESS_KEY_ID        is set, and the SDK uses it before AWS_PROFILE: unset it, or pass --profile`

// WithNote appends note to a formatted error's message, keeping the chain.
func WithNote(err error, note string) error {
	return &formattedError{msg: err.Error() + note, err: err}
}

// findOperation returns the first operation error in err's chain that match
// accepts, looking inside each one errors.As finds: an STS or SSO call can
// sit inside an EKS call that needed the credentials.
func findOperation(err error, match func(*smithy.OperationError) bool) *smithy.OperationError {
	for e := err; e != nil; {
		var op *smithy.OperationError
		if !errors.As(e, &op) {
			return nil
		}
		if match(op) {
			return op
		}
		e = op.Err
	}
	return nil
}

// isTransportFailure reports an error that never got an answer from AWS: a
// DNS failure, a dial failure, or a timeout.
func isTransportFailure(err error) bool {
	var dns *net.DNSError
	return errors.As(err, &dns) || common.IsDialFailure(err) || errors.Is(err, context.DeadlineExceeded)
}

// isMissingRegion reports a call made with no region at all (not a region
// whose endpoint does not resolve).
func isMissingRegion(err error) bool {
	var missing *aws.MissingRegionError
	return errors.As(err, &missing) || containsAny(err.Error(), regionFallbackPatterns)
}

// isSSOEndpointNotFound reports an IAM Identity Center endpoint that does
// not resolve: a wrong sso_region in the profile or sso-session.
func isSSOEndpointNotFound(err error) bool {
	var dns *net.DNSError
	if !errors.As(err, &dns) || !dns.IsNotFound {
		return false
	}
	h := strings.ToLower(dns.Name)
	return strings.HasPrefix(h, "portal.sso.") || strings.HasPrefix(h, "oidc.")
}

func formatSSORegionError(err error) error {
	return formatted(err, `the IAM Identity Center endpoint does not resolve
Cause: %s

Check the AWS config:
  sso_region               the Region of your IAM Identity Center, in the profile or its sso-session`, Summary(err))
}

// isAssumeRole reports an error from an STS AssumeRole call: the credential
// chain of a profile with role_arn.
func isAssumeRole(err error) bool {
	return findOperation(err, func(op *smithy.OperationError) bool {
		return op.ServiceID == "STS" && strings.HasPrefix(op.OperationName, "AssumeRole")
	}) != nil
}

func formatAssumeRoleError(err error) error {
	return formatted(err, `cannot assume the role of the AWS profile
AWS: %s

Check:
  the role's trust policy  it must let the source identity (source_profile or credential_source) assume it
  the source identity      its policy must allow sts:AssumeRole on the role
  role_arn                 the role ARN in the profile`, Summary(err))
}

// isSSORoleCall reports an error from IAM Identity Center's
// GetRoleCredentials: an SSO profile's role lookup.
func isSSORoleCall(err error) bool {
	return findOperation(err, func(op *smithy.OperationError) bool {
		return op.ServiceID == "SSO" && op.OperationName == "GetRoleCredentials"
	}) != nil
}

func formatSSORoleError(err error) error {
	return formatted(err, `IAM Identity Center gives this user no access to the profile's role
AWS: %s

Check the profile in the AWS config:
  sso_account_id           the account the role is in
  sso_role_name            a permission set assigned to you in that account; the AWS access portal lists yours`, Summary(err))
}

// IsSSONotLoggedIn reports an SSO profile with no usable login: no cached
// token (never logged in, or logged in with another profile style), or an
// expired one that could not be refreshed.
func IsSSONotLoggedIn(err error) bool {
	var tokenErr *ssocreds.InvalidTokenError
	if errors.As(err, &tokenErr) {
		return true
	}
	// A refresh that never reached IAM Identity Center (DNS, a dial, a
	// timeout) or that it failed on its side is not an expired login: its
	// own error says what to fix.
	if isTransportFailure(err) {
		return false
	}
	// A refresh IAM Identity Center refused (the session ended), or a role
	// lookup with a token it no longer takes.
	if op := findOperation(err, func(op *smithy.OperationError) bool {
		return op.ServiceID == "SSO OIDC" && op.OperationName == "CreateToken"
	}); op != nil {
		var ae smithy.APIError
		return errors.As(op, &ae) && ssoLoginRejections[ae.ErrorCode()]
	}
	var ae smithy.APIError
	if errors.As(err, &ae) && ae.ErrorCode() == "UnauthorizedException" && isSSORoleCall(err) {
		return true
	}
	// Each error in the chain: a formatted error's own text drops the SDK's.
	for e := err; e != nil; e = errors.Unwrap(e) {
		if containsAny(e.Error(), ssoLoginFallbackPatterns) {
			return true
		}
	}
	var pathErr *fs.PathError
	return errors.As(err, &pathErr) && errors.Is(pathErr.Err, fs.ErrNotExist) &&
		strings.Contains(filepath.ToSlash(pathErr.Path), "/.aws/sso/cache/")
}

// FormatSSONotLoggedIn says to log in to IAM Identity Center for profile
// ("" when it is not known). err may already be formatted: the cause comes
// from the SSO error inside it.
func FormatSSONotLoggedIn(err error, profile string) error {
	name := profile
	if name == "" {
		name = "<name>"
	}
	cause := "no SSO login is saved for this profile"
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		cause = "the SSO login expired and could not be renewed"
	}
	return &formattedError{msg: fmt.Sprintf(`not logged in to IAM Identity Center (SSO), or the login expired
Cause: %s

Log in, then run refresh again:
  aws sso login --profile %s`, cause, name), err: err}
}

// isUnreadable403 reports an HTTP 403 whose body the SDK could not decode:
// AWS refused the request, but the reply does not say whether the keys or a
// permission is wrong. A wrong secret access key gets this from EKS.
func isUnreadable403(err error) bool {
	var de *smithy.DeserializationError
	return httpStatus(err) == http.StatusForbidden && errors.As(err, &de)
}

func formatUnreadable403(err error, operation string) error {
	return formatted(err, `AWS refused the request (HTTP 403) while %s, and its reply could not be read
Cause: %s

Check:
  the secret access key    a wrong secret is refused this way; aws sts get-caller-identity tells
  the permissions          refresh's IAM actions are listed at %s`, operation, Summary(err), PermissionsDocURL)
}

// FormatNoCredentials explains a credential chain that found nothing
// (IsNoCredentials). ssoProfiles, when given, are the SSO profiles in the
// AWS config file, to pick one from.
func FormatNoCredentials(err error, ssoProfiles []string) error {
	msg := fmt.Sprintf(`no AWS credentials found
Cause: the credential chain found no keys or profile with credentials, and ended at the EC2 instance metadata service, which gave none (%s)

After aws sso login --profile <name>, tell refresh to use that profile:
  --profile <name>                for one command
  export AWS_PROFILE=<name>       for this shell
  refresh context add <ctx> --profile <name> --cluster <cluster> --region <region>  saved, then refresh use <ctx>

On EC2, check:
  the instance role               an instance profile is attached
  the metadata service            IMDS is enabled, and its hop limit is 2 or more in a container`, Summary(err))
	if len(ssoProfiles) > 0 {
		msg += "\n\nSSO profiles in your AWS config:"
		for _, p := range ssoProfiles {
			msg += "\n  " + p
		}
	}
	return &formattedError{msg: msg, err: err}
}

func formatNetworkError(err error, operation string) error {
	return formatted(err, `network connectivity issue while %s
Cause: %s

Check:
  the internet connection
  that the AWS endpoint for the region is reachable (a proxy, VPN, or firewall can block it)
  VPC endpoints and security groups, when refresh runs in a private network
  the AWS Health Dashboard, for an outage in the region`, operation, Summary(err))
}

func formatPermissionError(err error, operation string) error {
	// AWS's own message comes first: it names the denied action and says
	// whether a policy denies it explicitly. The full list follows.
	return formatted(err, `insufficient AWS permissions while %s
AWS: %s

Permissions refresh uses (grant the ones for the commands you run):
%s
See %s`, operation, Summary(err), permissionHint(), PermissionsDocURL)
}
