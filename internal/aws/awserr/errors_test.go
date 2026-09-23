package awserr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// sdkOpErr wraps err the way the SDK surfaces every client call failure.
func sdkOpErr(err error) error {
	return &smithy.OperationError{ServiceID: "EKS", OperationName: "ListClusters", Err: err}
}

func apiErr(code, msg string) error {
	return sdkOpErr(&smithy.GenericAPIError{Code: code, Message: msg})
}

// dialErr is what net/http returns when nothing listens on the endpoint.
func dialErr(sysErr error) error {
	return sdkOpErr(&url.Error{Op: "Post", URL: "https://eks.us-west-2.amazonaws.com/clusters", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", sysErr),
	}})
}

func dnsErr(host string) error {
	return sdkOpErr(&url.Error{Op: "Post", URL: "https://" + host, Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: host, IsNotFound: true},
	}})
}

func http403() error {
	return sdkOpErr(&awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: http.StatusForbidden}},
		Err:      errors.New("forbidden"),
	}})
}

type timeoutErr struct{}

func (timeoutErr) Error() string { return "i/o timeout" }
func (timeoutErr) Timeout() bool { return true }

func TestClassification(t *testing.T) {
	type want struct{ cred, region, network, perm bool }
	cases := []struct {
		name string
		err  error
		want want
	}{
		{"nil", nil, want{}},
		{"plain unrelated", errors.New("cluster not found"), want{}},
		// Substrings the old matcher caught must no longer misclassify.
		{"text mentioning timeout", errors.New("waiting for timeout budget"), want{}},
		{"text mentioning dial", errors.New("dial plan invalid"), want{}},
		{"text mentioning forbidden", errors.New("403 Forbidden"), want{}},
		{"text mentioning region", errors.New("not authorized in region us-east-1"), want{}},

		{"access denied code", apiErr("AccessDeniedException", "not authorized in region us-east-1"), want{perm: true}},
		{"unauthorized operation code", apiErr("UnauthorizedOperation", "no"), want{perm: true}},
		{"http 403", http403(), want{perm: true}},
		{"expired token code", apiErr("ExpiredTokenException", "expired"), want{cred: true}},
		{"unrecognized client code", apiErr("UnrecognizedClientException", "bad"), want{cred: true}},
		{"not found code", apiErr("ResourceNotFoundException", "no cluster"), want{}},

		{"signing error", sdkOpErr(&v4.SigningError{Err: errors.New("no creds")}), want{cred: true}},
		{"sso token expired", sdkOpErr(&ssocreds.InvalidTokenError{}), want{cred: true}},
		{"identity chain fallback", sdkOpErr(errors.New("get identity: get credentials: failed to refresh cached credentials, no EC2 IMDS role found")), want{cred: true}},

		{"missing region", sdkOpErr(&aws.MissingRegionError{}), want{region: true}},
		{"endpoint resolver region fallback", sdkOpErr(errors.New("resolve endpoint: invalid input region us-bogus")), want{region: true}},
		{"aws host nxdomain", dnsErr("eks.us-bogus-1.amazonaws.com"), want{region: true, network: true}},
		{"non-aws host nxdomain", dnsErr("proxy.corp.example"), want{network: true}},

		{"connection refused", dialErr(syscall.ECONNREFUSED), want{network: true}},
		{"connection reset", sdkOpErr(os.NewSyscallError("read", syscall.ECONNRESET)), want{network: true}},
		{"network unreachable", sdkOpErr(os.NewSyscallError("connect", syscall.ENETUNREACH)), want{network: true}},
		{"timeout interface", sdkOpErr(timeoutErr{}), want{network: true}},
		{"response timeout", sdkOpErr(&awshttp.ResponseTimeoutError{TimeoutDur: 1}), want{network: true}},
		{"unexpected eof", sdkOpErr(fmt.Errorf("read body: %w", io.ErrUnexpectedEOF)), want{network: true}},
		{"context deadline is not network", sdkOpErr(&url.Error{Op: "Post", URL: "x", Err: context.DeadlineExceeded}), want{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := want{
				cred:    IsCredentialError(tc.err),
				region:  IsRegionError(tc.err),
				network: IsNetworkError(tc.err),
				perm:    IsPermissionError(tc.err),
			}
			if got != tc.want {
				t.Errorf("classification = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Every FormatAWSError branch must keep the original error reachable through
// errors.Is / errors.As.
func TestFormatAWSError_WrapsEveryBranch(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		contains string
		check    func(error) bool
	}{
		{"cancelled", sdkOpErr(context.Canceled), "operation cancelled while listing clusters",
			func(e error) bool { return errors.Is(e, context.Canceled) }},
		{"deadline", sdkOpErr(context.DeadlineExceeded), "timed out while listing clusters (increase --timeout to allow more time)",
			func(e error) bool { return errors.Is(e, context.DeadlineExceeded) }},
		{"permission", apiErr("AccessDeniedException", "denied in region us-east-1"), "insufficient AWS permissions",
			func(e error) bool { var ae smithy.APIError; return errors.As(e, &ae) }},
		{"credential code", apiErr("ExpiredTokenException", "expired"), "AWS credentials not configured",
			func(e error) bool { var ae smithy.APIError; return errors.As(e, &ae) }},
		{"generic api", apiErr("ResourceNotFoundException", "No cluster found"), "AWS API error while listing clusters: No cluster found (ResourceNotFoundException)",
			func(e error) bool {
				var ae smithy.APIError
				return errors.As(e, &ae) && ae.ErrorCode() == "ResourceNotFoundException"
			}},
		{"credential chain", sdkOpErr(&v4.SigningError{Err: errors.New("x")}), "AWS credentials not configured",
			func(e error) bool { var se *v4.SigningError; return errors.As(e, &se) }},
		{"region", sdkOpErr(&aws.MissingRegionError{}), "AWS region configuration issue",
			func(e error) bool { var me *aws.MissingRegionError; return errors.As(e, &me) }},
		{"network", dialErr(syscall.ECONNREFUSED), "network connectivity issue",
			func(e error) bool { return errors.Is(e, syscall.ECONNREFUSED) }},
		{"http 403", http403(), "insufficient AWS permissions",
			func(e error) bool { var re *awshttp.ResponseError; return errors.As(e, &re) }},
		{"generic", errBoom, "error while listing clusters: boom",
			func(e error) bool { return errors.Is(e, errBoom) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatAWSError(tc.err, "listing clusters")
			if got == nil {
				t.Fatal("FormatAWSError returned nil")
			}
			if !strings.Contains(got.Error(), tc.contains) {
				t.Errorf("message %q does not contain %q", got.Error(), tc.contains)
			}
			if !tc.check(got) {
				t.Errorf("wrap chain lost through FormatAWSError: %v", got)
			}
		})
	}
}

var errBoom = errors.New("boom")

// The cancel, deadline, and generic-API branches keep their exact text: the
// wrapped cause must not leak into the message.
func TestFormatAWSError_MessagesUnchanged(t *testing.T) {
	cases := map[string]error{
		"operation cancelled while listing clusters":                                         sdkOpErr(context.Canceled),
		"timed out while listing clusters (increase --timeout to allow more time)":           sdkOpErr(context.DeadlineExceeded),
		"AWS API error while listing clusters: No cluster found (ResourceNotFoundException)": apiErr("ResourceNotFoundException", "No cluster found"),
	}
	for want, in := range cases {
		if got := FormatAWSError(in, "listing clusters").Error(); got != want {
			t.Errorf("message = %q, want %q", got, want)
		}
	}
}

func TestFormatAWSError_NilReturnsNil(t *testing.T) {
	if FormatAWSError(nil, "test op") != nil {
		t.Error("nil error should return nil")
	}
}

func TestFormatAWSError_TypedAccessDeniedIsNotRegion(t *testing.T) {
	err := FormatAWSError(apiErr("AccessDeniedException", "not authorized to perform eks:ListClusters in region us-east-1"), "listing clusters")
	if strings.Contains(err.Error(), "AWS_DEFAULT_REGION") {
		t.Errorf("typed AccessDenied must not get region guidance, got: %s", err.Error())
	}
}

func TestFormatAWSError_CancelGetsNoNetworkHelp(t *testing.T) {
	err := FormatAWSError(sdkOpErr(&url.Error{Op: "Post", URL: "x", Err: context.Canceled}), "listing clusters")
	if strings.Contains(err.Error(), "Internet connection") {
		t.Errorf("cancellation must not get network remediation help, got: %s", err.Error())
	}
}

func TestIsRegionInaccessible(t *testing.T) {
	for _, code := range []string{"AccessDenied", "AccessDeniedException", "UnrecognizedClientException", "InvalidClientTokenId", "AuthFailure", "OptInRequired", "RegionDisabledException"} {
		if !IsRegionInaccessible(apiErr(code, "x")) {
			t.Errorf("%s: want inaccessible", code)
		}
		// Still classified after FormatAWSError and a caller's %w wrap.
		if !IsRegionInaccessible(fmt.Errorf("region x: %w", FormatAWSError(apiErr(code, "x"), "listing clusters"))) {
			t.Errorf("%s: want inaccessible through FormatAWSError", code)
		}
	}
	for _, err := range []error{
		nil,
		apiErr("ThrottlingException", "x"),
		apiErr("ServerException", "x"),
		context.DeadlineExceeded,
		errors.New("AccessDeniedException: plain string, not an API error"),
	} {
		if IsRegionInaccessible(err) {
			t.Errorf("%v: want not inaccessible", err)
		}
	}
}

func TestSummary(t *testing.T) {
	cases := map[string]error{
		"":                                    nil,
		"AccessDeniedException: no eks:ListX": FormatAWSError(apiErr("AccessDeniedException", "no eks:ListX"), "listing"),
		"network connectivity issue while listing.": FormatAWSError(dialErr(syscall.ECONNREFUSED), "listing"),
		"plain": errors.New("plain"),
	}
	for want, in := range cases {
		if got := Summary(in); got != want {
			t.Errorf("Summary(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatPermissionError_ListsPermissions(t *testing.T) {
	err := formatPermissionError(errors.New("denied"), "listing clusters")
	if !strings.Contains(err.Error(), "listing clusters") || !strings.Contains(err.Error(), "eks:ListClusters") {
		t.Errorf("expected operation and permissions in message, got: %s", err.Error())
	}
}
