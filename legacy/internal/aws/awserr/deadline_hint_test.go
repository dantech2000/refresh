package awserr

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// A command whose run deadline is --wait-timeout gets a hint that names it,
// and the original error stays reachable.
func TestRetargetDeadlineHint(t *testing.T) {
	timedOut := FormatAWSError(sdkOpErr(context.DeadlineExceeded), "describing update")
	wrapped := fmt.Errorf("rolling ng-a: %w", timedOut)

	got := RetargetDeadlineHint(wrapped, "--wait-timeout")
	want := "rolling ng-a: timed out while describing update (increase --wait-timeout to allow more time)"
	if got.Error() != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !errors.Is(got, context.DeadlineExceeded) {
		t.Error("errors.Is(context.DeadlineExceeded) = false after retargeting")
	}

	// No hint, or the default flag: returned as is.
	plain := errors.New("boom")
	if RetargetDeadlineHint(plain, "--wait-timeout") != plain { //nolint:errorlint // identity: returned as is
		t.Error("an error without the hint was rewritten")
	}
	if RetargetDeadlineHint(timedOut, DefaultTimeoutFlag) != timedOut { //nolint:errorlint // identity: returned as is
		t.Error("retargeting to --timeout rewrote the error")
	}
	if RetargetDeadlineHint(nil, "--wait-timeout") != nil {
		t.Error("nil became non-nil")
	}
}
