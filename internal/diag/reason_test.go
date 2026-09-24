package diag_test

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/mocks"
)

// sdkOpErr wraps err the way the SDK wraps every client call failure.
func sdkOpErr(err error) error {
	return &smithy.OperationError{ServiceID: "EKS", OperationName: "DescribeNodegroup", Err: err}
}

func dialErr(sysErr error) error {
	return sdkOpErr(&url.Error{Op: "Post", URL: "https://eks.us-west-2.amazonaws.com", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", sysErr),
	}})
}

func dnsErr(host string) error {
	return sdkOpErr(&url.Error{Op: "Post", URL: "https://" + host, Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: host, IsNotFound: true},
	}})
}

func httpErr(status int) error {
	return sdkOpErr(&awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      errors.New("http error"),
	}})
}

// httpAPIErr is an API error code on an HTTP response, as the SDK returns it.
func httpAPIErr(status int, code string) error {
	return sdkOpErr(&awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      &smithy.GenericAPIError{Code: code, Message: "x"},
	}})
}

var classifyCases = []struct {
	name   string
	err    error
	reason diag.Reason
	code   string
}{
	{"nil", nil, diag.ReasonUnknown, ""},
	{"plain error", errors.New("boom"), diag.ReasonUnknown, ""},
	{"text that names a code is not typed", errors.New("ThrottlingException: AccessDenied"), diag.ReasonUnknown, ""},

	{"mocks.AccessDenied", mocks.AccessDenied(), diag.ReasonAccessDenied, "AccessDeniedException"},
	{"AccessDenied", mocks.APIError("AccessDenied", "no"), diag.ReasonAccessDenied, "AccessDenied"},
	{"UnauthorizedOperation", mocks.APIError("UnauthorizedOperation", "no"), diag.ReasonAccessDenied, "UnauthorizedOperation"},
	{"http 403", httpErr(http.StatusForbidden), diag.ReasonAccessDenied, ""},
	{"AccessDenied on 403", httpAPIErr(http.StatusForbidden, "AccessDeniedException"), diag.ReasonAccessDenied, "AccessDeniedException"},

	{"ExpiredToken", mocks.APIError("ExpiredToken", "expired"), diag.ReasonCredentialError, "ExpiredToken"},
	{"ExpiredToken on 403", httpAPIErr(http.StatusForbidden, "ExpiredToken"), diag.ReasonCredentialError, "ExpiredToken"},
	{"UnrecognizedClientException", mocks.APIError("UnrecognizedClientException", "bad"), diag.ReasonCredentialError, "UnrecognizedClientException"},
	{"signing error", sdkOpErr(&v4.SigningError{Err: errors.New("no creds")}), diag.ReasonCredentialError, ""},

	{"mocks.Throttling", mocks.Throttling(), diag.ReasonThrottled, "ThrottlingException"},
	{"TooManyRequestsException", mocks.APIError("TooManyRequestsException", "slow"), diag.ReasonThrottled, "TooManyRequestsException"},
	{"RequestLimitExceeded", mocks.APIError("RequestLimitExceeded", "slow"), diag.ReasonThrottled, "RequestLimitExceeded"},
	{"http 429", httpErr(http.StatusTooManyRequests), diag.ReasonThrottled, ""},

	{"mocks.NotFound", mocks.NotFound(), diag.ReasonNotFound, "ResourceNotFoundException"},
	{"ParameterNotFound", mocks.APIError("ParameterNotFound", "no param"), diag.ReasonNotFound, "ParameterNotFound"},
	{"ec2 dotted not found", mocks.APIError("InvalidInstanceID.NotFound", "no"), diag.ReasonNotFound, "InvalidInstanceID.NotFound"},

	{"OptInRequired", mocks.APIError("OptInRequired", "opt in"), diag.ReasonRegionUnavailable, "OptInRequired"},
	{"RegionDisabledException", mocks.APIError("RegionDisabledException", "off"), diag.ReasonRegionUnavailable, "RegionDisabledException"},
	{"missing region", sdkOpErr(&aws.MissingRegionError{}), diag.ReasonRegionUnavailable, ""},
	{"aws host nxdomain", dnsErr("eks.us-bogus-1.amazonaws.com"), diag.ReasonRegionUnavailable, ""},

	{"InvalidParameterException", mocks.APIError("InvalidParameterException", "bad"), diag.ReasonInvalidRequest, "InvalidParameterException"},
	{"InvalidRequestException", mocks.APIError("InvalidRequestException", "bad"), diag.ReasonInvalidRequest, "InvalidRequestException"},
	{"ValidationException", mocks.APIError("ValidationException", "bad"), diag.ReasonInvalidRequest, "ValidationException"},

	{"InternalFailure", mocks.APIError("InternalFailure", "oops"), diag.ReasonServiceError, "InternalFailure"},
	{"ServerException", mocks.APIError("ServerException", "oops"), diag.ReasonServiceError, "ServerException"},
	{"ServiceUnavailableException", mocks.APIError("ServiceUnavailableException", "down"), diag.ReasonServiceError, "ServiceUnavailableException"},
	{"http 502", httpErr(http.StatusBadGateway), diag.ReasonServiceError, ""},

	{"connection refused", dialErr(syscall.ECONNREFUSED), diag.ReasonNetworkError, ""},
	{"net.OpError", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, diag.ReasonNetworkError, ""},
	{"url.Error", &url.Error{Op: "Get", URL: "https://example.com", Err: errors.New("EOF")}, diag.ReasonNetworkError, ""},
	{"non-aws host nxdomain", dnsErr("proxy.corp.example"), diag.ReasonNetworkError, ""},

	{"context deadline", context.DeadlineExceeded, diag.ReasonTimeout, ""},
	{"sdk deadline", sdkOpErr(&url.Error{Op: "Post", URL: "x", Err: context.DeadlineExceeded}), diag.ReasonTimeout, ""},
	{"context canceled", context.Canceled, diag.ReasonInterrupted, ""},
	{"sdk canceled", sdkOpErr(&smithy.CanceledError{Err: context.Canceled}), diag.ReasonInterrupted, ""},

	{"other API code", mocks.APIError("ClientException", "in use"), diag.ReasonUnknown, "ClientException"},
	{"ResourceInUseException", mocks.APIError("ResourceInUseException", "busy"), diag.ReasonUnknown, "ResourceInUseException"},
}

func TestClassify(t *testing.T) {
	for _, tc := range classifyCases {
		t.Run(tc.name, func(t *testing.T) {
			variants := map[string]error{"bare": tc.err}
			if tc.err != nil {
				variants["%w"] = fmt.Errorf("describing nodegroup web: %w", tc.err)
				variants["FormatAWSError"] = awserr.FormatAWSError(tc.err, "describing nodegroup web")
				variants["FormatAWSError then %w"] = fmt.Errorf("cluster prod: %w", awserr.FormatAWSError(tc.err, "describing nodegroup web"))
			}
			for form, err := range variants {
				reason, retryable, code := diag.Classify(err)
				if reason != tc.reason || retryable != tc.reason.Retryable() || code != tc.code {
					t.Errorf("%s: Classify = (%s, %v, %q), want (%s, %v, %q)",
						form, reason, retryable, code, tc.reason, tc.reason.Retryable(), tc.code)
				}
			}
		})
	}
}

// TestClassify_CoversEveryErrorReason keeps the table honest: every reason
// that Classify can return from an error has at least one case.
func TestClassify_CoversEveryErrorReason(t *testing.T) {
	built := map[diag.Reason]bool{ // built directly with diag.New, never classified
		diag.ReasonUpdateFailed: true, diag.ReasonUpdateCancelled: true,
		diag.ReasonNotMonitored: true, diag.ReasonNotAttempted: true,
	}
	covered := map[diag.Reason]bool{}
	for _, tc := range classifyCases {
		covered[tc.reason] = true
	}
	for _, r := range diag.Reasons() {
		if !built[r] && !covered[r] {
			t.Errorf("no Classify case returns %s", r)
		}
	}
}

func TestReasonRetryable(t *testing.T) {
	want := map[diag.Reason]bool{
		diag.ReasonAccessDenied:      false,
		diag.ReasonCredentialError:   false,
		diag.ReasonThrottled:         true,
		diag.ReasonNotFound:          false,
		diag.ReasonRegionUnavailable: false,
		diag.ReasonInvalidRequest:    false,
		diag.ReasonServiceError:      true,
		diag.ReasonNetworkError:      true,
		diag.ReasonTimeout:           true,
		diag.ReasonInterrupted:       true,
		diag.ReasonUpdateFailed:      false,
		diag.ReasonUpdateCancelled:   false,
		diag.ReasonNotMonitored:      true,
		diag.ReasonNotAttempted:      true,
		diag.ReasonUnknown:           false,
	}
	if len(diag.Reasons()) != len(want) {
		t.Fatalf("Reasons() has %d values, the spec table has %d", len(diag.Reasons()), len(want))
	}
	for _, r := range diag.Reasons() {
		w, ok := want[r]
		if !ok {
			t.Errorf("Reasons() has %s, which the spec table does not", r)
			continue
		}
		if r.Retryable() != w {
			t.Errorf("%s.Retryable() = %v, want %v", r, r.Retryable(), w)
		}
	}
	if diag.Reason("NotAReason").Retryable() {
		t.Error("an unknown reason must not be retryable")
	}
}

// TestReasonsAreDocumented checks that docs/concepts/output.md has a row for
// each reason in its failure reason table.
func TestReasonsAreDocumented(t *testing.T) {
	data, err := os.ReadFile("../../docs/concepts/output.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	for _, r := range diag.Reasons() {
		if !strings.Contains(doc, "| `"+string(r)+"` |") {
			t.Errorf("docs/concepts/output.md has no table row for reason %s", r)
		}
	}
}
