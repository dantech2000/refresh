package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/aws/awserr"
)

// WaitDeadlineHint names --wait-timeout in a timeout error and keeps an
// exit code.
func TestWaitDeadlineHint(t *testing.T) {
	timedOut := awserr.FormatAWSError(fmt.Errorf("operation error EKS: %w", context.DeadlineExceeded), "waiting for the update")

	err := fmt.Errorf("upgrade failed: %w", timedOut)
	WaitDeadlineHint(&err)
	if !strings.Contains(err.Error(), "(increase --wait-timeout to allow more time)") || strings.Contains(err.Error(), "increase --timeout") {
		t.Errorf("err = %q, want the --wait-timeout hint", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("errors.Is(context.DeadlineExceeded) = false")
	}

	var exit error = cli.Exit(timedOut.Error(), 4)
	WaitDeadlineHint(&exit)
	ec, ok := exit.(cli.ExitCoder) //nolint:errorlint // urfave/cli checks the unwrapped ExitCoder
	if !ok || ec.ExitCode() != 4 || !strings.Contains(exit.Error(), "--wait-timeout") {
		t.Errorf("exit = %v, want exit code 4 with the --wait-timeout hint", exit)
	}

	var none error
	WaitDeadlineHint(&none)
	if none != nil {
		t.Errorf("nil became %v", none)
	}
	other := errors.New("boom")
	keep := other
	WaitDeadlineHint(&keep)
	if keep != other { //nolint:errorlint // identity: left unchanged
		t.Errorf("an error without the hint changed: %v", keep)
	}
}
