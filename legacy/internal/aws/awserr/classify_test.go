package awserr

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"syscall"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func httpStatusErr(code int) error {
	return sdkOpErr(&awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: code}},
		Err:      errors.New("http error"),
	}})
}

func TestTypedClassifiers(t *testing.T) {
	type want struct{ throttle, notFound, validation, server, regionDisabled bool }
	cases := []struct {
		name string
		err  error
		want want
	}{
		{"nil", nil, want{}},
		{"plain text", errors.New("ThrottlingException: ResourceNotFoundException InternalFailure"), want{}},
		{"context deadline", context.DeadlineExceeded, want{}},
		{"network", dialErr(syscall.ECONNREFUSED), want{}},

		{"ThrottlingException", apiErr("ThrottlingException", "x"), want{throttle: true}},
		{"Throttling", apiErr("Throttling", "x"), want{throttle: true}},
		{"TooManyRequestsException", apiErr("TooManyRequestsException", "x"), want{throttle: true}},
		{"RequestLimitExceeded", apiErr("RequestLimitExceeded", "x"), want{throttle: true}},
		{"SlowDown", apiErr("SlowDown", "x"), want{throttle: true}},
		{"http 429", httpStatusErr(http.StatusTooManyRequests), want{throttle: true}},

		{"ResourceNotFoundException", apiErr("ResourceNotFoundException", "x"), want{notFound: true}},
		{"NotFoundException", apiErr("NotFoundException", "x"), want{notFound: true}},
		{"ParameterNotFound", apiErr("ParameterNotFound", "x"), want{notFound: true}},
		{"ec2 dotted not found", apiErr("InvalidInstanceID.NotFound", "x"), want{notFound: true}},

		{"InvalidParameterException", apiErr("InvalidParameterException", "x"), want{validation: true}},
		{"InvalidParameterValue", apiErr("InvalidParameterValue", "x"), want{validation: true}},
		{"InvalidParameterCombination", apiErr("InvalidParameterCombination", "x"), want{validation: true}},
		{"InvalidRequestException", apiErr("InvalidRequestException", "x"), want{validation: true}},
		{"ValidationException", apiErr("ValidationException", "x"), want{validation: true}},
		{"MissingParameter", apiErr("MissingParameter", "x"), want{validation: true}},

		{"InternalFailure", apiErr("InternalFailure", "x"), want{server: true}},
		{"ServerException", apiErr("ServerException", "x"), want{server: true}},
		{"ServiceUnavailableException", apiErr("ServiceUnavailableException", "x"), want{server: true}},
		{"server fault", sdkOpErr(&smithy.GenericAPIError{Code: "Odd", Fault: smithy.FaultServer}), want{server: true}},
		{"http 503", httpStatusErr(http.StatusServiceUnavailable), want{server: true}},
		{"http 500", httpStatusErr(http.StatusInternalServerError), want{server: true}},
		{"http 404 is not server", httpStatusErr(http.StatusNotFound), want{}},

		{"OptInRequired", apiErr("OptInRequired", "x"), want{regionDisabled: true}},
		{"RegionDisabledException", apiErr("RegionDisabledException", "x"), want{regionDisabled: true}},

		{"AccessDenied is none of these", apiErr("AccessDeniedException", "x"), want{}},
		{"unknown code", apiErr("SomethingElse", "x"), want{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, err := range []error{tc.err, fmt.Errorf("wrapped: %w", tc.err), FormatAWSError(tc.err, "listing clusters")} {
				if tc.err == nil && err != nil {
					continue // FormatAWSError(nil) is nil; fmt.Errorf(nil) is not an AWS error
				}
				got := want{
					throttle:       IsThrottling(err),
					notFound:       IsNotFound(err),
					validation:     IsValidation(err),
					server:         IsServerError(err),
					regionDisabled: IsRegionDisabled(err),
				}
				if got != tc.want {
					t.Errorf("%v: classification = %+v, want %+v", err, got, tc.want)
				}
			}
		})
	}
}

func TestErrorCode(t *testing.T) {
	if got := ErrorCode(fmt.Errorf("x: %w", FormatAWSError(apiErr("ThrottlingException", "slow"), "listing"))); got != "ThrottlingException" {
		t.Errorf("ErrorCode = %q, want ThrottlingException", got)
	}
	for _, err := range []error{nil, errors.New("ThrottlingException"), context.Canceled} {
		if got := ErrorCode(err); got != "" {
			t.Errorf("ErrorCode(%v) = %q, want empty", err, got)
		}
	}
}

func TestSummary_MultiLineAPIMessageIsOneLine(t *testing.T) {
	got := Summary(apiErr("ValidationException", "first line\nsecond  line\r\n"))
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("Summary = %q, want one line", got)
	}
	if want := "ValidationException: first line second line"; got != want {
		t.Errorf("Summary = %q, want %q", got, want)
	}
	// A plain error keeps its first line; a bare \r also ends it.
	if got := Summary(errors.New("x: \r0")); got != "x:" {
		t.Errorf("Summary = %q, want %q", got, "x:")
	}
}
