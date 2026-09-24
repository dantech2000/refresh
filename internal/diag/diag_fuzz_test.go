package diag_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/diag"
)

// FuzzFromError checks that any API error code and message, bare or wrapped,
// gives a known reason, a retryable flag that matches it, the code back, and
// a one-line, non-empty Error.
func FuzzFromError(f *testing.F) {
	f.Add("ThrottlingException", "Rate exceeded", uint8(0))
	f.Add("AccessDeniedException", "line one\nline two", uint8(1))
	f.Add("", "plain error\r\nsecond", uint8(2))
	f.Add("InvalidInstanceID.NotFound", "", uint8(3))
	f.Fuzz(func(t *testing.T, code, msg string, fault uint8) {
		var err error = &smithy.GenericAPIError{Code: code, Message: msg, Fault: smithy.ErrorFault(fault % 3)}
		if code == "" {
			err = errors.New(msg)
		}
		for _, e := range []error{err, fmt.Errorf("x: %w", err), awserr.FormatAWSError(err, "listing")} {
			fl := diag.FromError(diag.KindCluster, "prod", diag.OpDescribeCluster, e)
			if !slices.Contains(diag.Reasons(), fl.Reason) {
				t.Fatalf("reason %q is not in Reasons()", fl.Reason)
			}
			if fl.Retryable != fl.Reason.Retryable() {
				t.Fatalf("retryable %v does not match %s", fl.Retryable, fl.Reason)
			}
			if fl.AWSErrorCode != code {
				t.Fatalf("awsErrorCode = %q, want %q", fl.AWSErrorCode, code)
			}
			if fl.Error == "" || strings.ContainsAny(fl.Error, "\r\n") {
				t.Fatalf("Error = %q, want one non-empty line", fl.Error)
			}
		}
	})
}
