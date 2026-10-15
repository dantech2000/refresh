// Package exitcode has the exit codes of every command. It imports
// nothing, so the packages below the command layer can give the code of a
// plan, a gate, or a run without knowing the command line.
package exitcode

// The exit codes of every command.
const (
	OK             = 0 // done, nothing to report
	Failure        = 1 // an error or an interrupt
	NeedsAttention = 2 // warnings or stale items
	Blocked        = 3 // a gate stopped the change, or it is unsupported; nothing changed
	Incomplete     = 4 // incomplete data or a partial failure
	VerifyFailed   = 5 // the change was applied and the check after it found issues
)
