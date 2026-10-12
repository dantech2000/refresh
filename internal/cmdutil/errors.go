package cmdutil

import (
	"context"
	"errors"
)

// The exit codes of every command.
const (
	ExitOK             = 0 // done, nothing to report
	ExitFailure        = 1 // an error or an interrupt
	ExitNeedsAttention = 2 // warnings or stale items
	ExitBlocked        = 3 // a gate stopped the change, or it is unsupported; nothing changed
	ExitIncomplete     = 4 // incomplete data or a partial failure
	ExitVerifyFailed   = 5 // the change was applied and the check after it found issues
)

// ExitError is an error that sets the exit code of the process. main prints
// Err, when there is one, and exits with Code. A command that already
// printed its result returns an ExitError with no Err.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode returns the exit code for err: ExitOK for nil, the code of an
// ExitError anywhere in the chain, and ExitFailure for any other error. A
// cancelled context is an interrupt, which is ExitFailure even inside an
// ExitError. A deadline is not: an ExitError keeps its code then.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, context.Canceled) {
		return ExitFailure
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return ExitFailure
}
