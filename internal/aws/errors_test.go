package aws

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// The classification and formatting tests live in internal/aws/awserr; this
// only checks that the forwarder keeps the wrap chain.
func TestFormatAWSError_ForwardsAndWraps(t *testing.T) {
	if FormatAWSError(nil, "op") != nil {
		t.Error("nil error should return nil")
	}
	err := FormatAWSError(fmt.Errorf("operation error EKS: %w", context.Canceled), "listing clusters")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(context.Canceled) = false through FormatAWSError: %v", err)
	}
}

func TestPrintCredentialHelp_NoPanic(t *testing.T) {
	// PrintCredentialHelp writes to stdout; just verify it doesn't panic.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("PrintCredentialHelp panicked: %v", r)
		}
	}()
	PrintCredentialHelp()
}
