package runner

import (
	"github.com/urfave/cli/v3"
)

// The exit-code contract every command follows (REF-165). The full table,
// with the codes each command can return, is docs/concepts/exit-codes.md.
const (
	// ExitOK: the command did what was asked and found nothing to report.
	ExitOK = 0
	// ExitError: an error (bad flags, an AWS error, not found, a missing
	// --yes) or an interrupt (Ctrl+C / SIGTERM).
	ExitError = 1
	// ExitNeedsAttention: the command finished, but it found warnings or
	// stale items.
	ExitNeedsAttention = 2
	// ExitBlocked: a gate blocked the operation, or the target is
	// unsupported. Nothing was changed.
	ExitBlocked = 3
	// ExitIncomplete: incomplete data or a partial failure. The command
	// printed what it gathered; some of it is missing or failed.
	ExitIncomplete = 4
	// ExitVerifyFailed: the change was applied, but the post-action
	// verification found issues.
	ExitVerifyFailed = 5
)

// ExitCodesURL is the published exit-code contract.
const ExitCodesURL = "https://drod.dev/refresh/concepts/exit-codes/"

// ExitCodeOf returns the process exit code err maps to: 0 for nil, the
// code of an unwrapped cli.ExitCoder, else ExitError. Like
// cli.HandleExitCoder, it does not unwrap: a wrapped exit error exits 1.
func ExitCodeOf(err error) int {
	if err == nil {
		return ExitOK
	}
	if ec, ok := err.(cli.ExitCoder); ok { //nolint:errorlint // mirrors cli.HandleExitCoder's unwrapped check
		return ec.ExitCode()
	}
	return ExitError
}

// IsIncomplete reports whether err is an ExitIncomplete exit: the command
// printed a partial result. --watch keeps polling after one.
func IsIncomplete(err error) bool {
	return err != nil && ExitCodeOf(err) == ExitIncomplete
}
