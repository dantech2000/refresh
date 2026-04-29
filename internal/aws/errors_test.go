package aws

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// ──────────────────────────────────────────────────────────────────────────────
// IsCredentialError
// ──────────────────────────────────────────────────────────────────────────────

func TestIsCredentialError_NilIsFalse(t *testing.T) {
	if IsCredentialError(nil) {
		t.Error("nil error should not be a credential error")
	}
}

func TestIsCredentialError_AccessDenied(t *testing.T) {
	if !IsCredentialError(errors.New("access denied")) {
		t.Error("'access denied' should be a credential error")
	}
}

func TestIsCredentialError_UnableToLocate(t *testing.T) {
	if !IsCredentialError(errors.New("unable to locate credentials")) {
		t.Error("'unable to locate credentials' should be a credential error")
	}
}

func TestIsCredentialError_ExpiredToken(t *testing.T) {
	if !IsCredentialError(errors.New("The security token included in the request is expired")) {
		t.Error("expired token should be a credential error")
	}
}

func TestIsCredentialError_UnrelatedError(t *testing.T) {
	if IsCredentialError(errors.New("cluster not found")) {
		t.Error("unrelated error should not be a credential error")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// IsNetworkError
// ──────────────────────────────────────────────────────────────────────────────

func TestIsNetworkError_NilIsFalse(t *testing.T) {
	if IsNetworkError(nil) {
		t.Error("nil error should not be a network error")
	}
}

func TestIsNetworkError_NoSuchHost(t *testing.T) {
	if !IsNetworkError(errors.New("no such host")) {
		t.Error("'no such host' should be a network error")
	}
}

func TestIsNetworkError_ConnectionRefused(t *testing.T) {
	if !IsNetworkError(errors.New("connection refused")) {
		t.Error("'connection refused' should be a network error")
	}
}

func TestIsNetworkError_ContextDeadlineExceeded(t *testing.T) {
	if !IsNetworkError(errors.New("context deadline exceeded")) {
		t.Error("deadline exceeded should be a network error")
	}
}

func TestIsNetworkError_UnrelatedError(t *testing.T) {
	if IsNetworkError(errors.New("nodegroup not found")) {
		t.Error("unrelated error should not be a network error")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// IsPermissionError
// ──────────────────────────────────────────────────────────────────────────────

func TestIsPermissionError_NilIsFalse(t *testing.T) {
	if IsPermissionError(nil) {
		t.Error("nil error should not be a permission error")
	}
}

func TestIsPermissionError_AccessDenied(t *testing.T) {
	if !IsPermissionError(errors.New("AccessDenied: not allowed")) {
		t.Error("AccessDenied should be a permission error")
	}
}

func TestIsPermissionError_Forbidden(t *testing.T) {
	if !IsPermissionError(errors.New("403 Forbidden")) {
		t.Error("Forbidden should be a permission error")
	}
}

func TestIsPermissionError_UnrelatedError(t *testing.T) {
	if IsPermissionError(errors.New("cluster not found")) {
		t.Error("unrelated error should not be a permission error")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// IsRegionError
// ──────────────────────────────────────────────────────────────────────────────

func TestIsRegionError_NilIsFalse(t *testing.T) {
	if IsRegionError(nil) {
		t.Error("nil error should not be a region error")
	}
}

func TestIsRegionError_RegionInMessage(t *testing.T) {
	if !IsRegionError(errors.New("invalid region specified")) {
		t.Error("error mentioning 'region' should be a region error")
	}
}

func TestIsRegionError_NoSuchHostAmazonaws(t *testing.T) {
	if !IsRegionError(errors.New("no such host eks.us-invalid.amazonaws.com")) {
		t.Error("no such host for amazonaws.com should be a region error")
	}
}

func TestIsRegionError_UnrelatedError(t *testing.T) {
	if IsRegionError(errors.New("nodegroup not found")) {
		t.Error("unrelated error should not be a region error")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// FormatAWSError
// ──────────────────────────────────────────────────────────────────────────────

func TestFormatAWSError_NilReturnsNil(t *testing.T) {
	if FormatAWSError(nil, "test op") != nil {
		t.Error("nil error should return nil")
	}
}

func TestFormatAWSError_CredentialErrorHasGuidance(t *testing.T) {
	err := FormatAWSError(errors.New("unable to locate credentials"), "listing clusters")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "aws configure") && !strings.Contains(msg, "credential") {
		t.Errorf("credential error should include setup guidance, got: %s", msg)
	}
}

func TestFormatAWSError_NetworkErrorHasGuidance(t *testing.T) {
	err := FormatAWSError(errors.New("no such host"), "describing cluster")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "network") && !strings.Contains(err.Error(), "connectivity") {
		t.Errorf("network error should mention network issue, got: %s", err.Error())
	}
}

func TestFormatAWSError_RegionErrorHasGuidance(t *testing.T) {
	err := FormatAWSError(errors.New("invalid region specified"), "listing clusters")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "region") {
		t.Errorf("region error message should mention region, got: %s", err.Error())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// AWSError
// ──────────────────────────────────────────────────────────────────────────────

func TestAWSError_Error_ContainsOperationAndMessage(t *testing.T) {
	inner := errors.New("access denied")
	e := &AWSError{Operation: "listing clusters", Err: inner}
	msg := e.Error()
	if !strings.Contains(msg, "listing clusters") {
		t.Errorf("Error() should include operation, got %q", msg)
	}
	if !strings.Contains(msg, "access denied") {
		t.Errorf("Error() should include inner error, got %q", msg)
	}
}

func TestAWSError_Unwrap_ReturnsInner(t *testing.T) {
	inner := errors.New("inner error")
	e := &AWSError{Operation: "op", Err: inner}
	if e.Unwrap() != inner {
		t.Error("Unwrap should return the inner error")
	}
}

func TestAWSError_ErrorsIs_WorksViaUnwrap(t *testing.T) {
	sentinel := errors.New("sentinel")
	e := &AWSError{Operation: "op", Err: sentinel}
	if !errors.Is(e, sentinel) {
		t.Error("errors.Is should find sentinel through Unwrap")
	}
}

func TestFormatAWSError_GenericErrorHasOperation(t *testing.T) {
	err := FormatAWSError(errors.New("something went wrong"), "listing nodegroups")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "listing nodegroups") {
		t.Errorf("generic error should contain operation name, got: %s", err.Error())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// formatPermissionError
// ──────────────────────────────────────────────────────────────────────────────

func TestFormatPermissionError_ContainsOperation(t *testing.T) {
	err := formatPermissionError(errors.New("access denied"), "listing clusters")
	if err == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(err.Error(), "listing clusters") {
		t.Errorf("expected operation in message, got: %s", err.Error())
	}
}

func TestFormatPermissionError_ContainsPermissions(t *testing.T) {
	err := formatPermissionError(errors.New("denied"), "op")
	if !strings.Contains(err.Error(), "eks:ListClusters") {
		t.Errorf("expected required permissions in message, got: %s", err.Error())
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// PrintCredentialHelp
// ──────────────────────────────────────────────────────────────────────────────

func TestPrintCredentialHelp_NoPanic(t *testing.T) {
	// PrintCredentialHelp writes to stdout; just verify it doesn't panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PrintCredentialHelp panicked: %v", r)
		}
	}()
	PrintCredentialHelp()
}

// ──────────────────────────────────────────────────────────────────────────────
// NewAMIResolver / NewClusterResolver constructors
// ──────────────────────────────────────────────────────────────────────────────

func TestNewAMIResolver_NotNil(t *testing.T) {
	r := NewAMIResolver(nil, nil, nil)
	if r == nil {
		t.Error("NewAMIResolver should return non-nil resolver")
	}
}

func TestNewClusterResolver_NotNil(t *testing.T) {
	r := NewClusterResolver(aws.Config{})
	if r == nil {
		t.Error("NewClusterResolver should return non-nil resolver")
	}
}
