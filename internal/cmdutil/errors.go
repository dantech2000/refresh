package cmdutil

import (
	"errors"

	"github.com/dantech2000/refresh/internal/exitcode"
)

// The exit codes of every command. The values are the ones of
// internal/exitcode, which the packages below the command layer use.
const (
	ExitOK             = exitcode.OK             // done, nothing to report
	ExitFailure        = exitcode.Failure        // an error or an interrupt
	ExitNeedsAttention = exitcode.NeedsAttention // warnings or stale items
	ExitBlocked        = exitcode.Blocked        // a gate stopped the change, or it is unsupported; nothing changed
	ExitIncomplete     = exitcode.Incomplete     // incomplete data or a partial failure
	ExitVerifyFailed   = exitcode.VerifyFailed   // the change was applied and the check after it found issues
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
// ExitError anywhere in the chain, and ExitFailure for any other error.
//
// ExitCode does not look for an interrupt. A cancelled context inside err
// can come from the run itself, such as a fan-out that stops its other
// calls, and then the code of the ExitError stands. main knows whether the
// signal context ended, and makes an interrupt exit 1.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return ExitFailure
}
