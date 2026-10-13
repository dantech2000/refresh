package changecmd

import (
	"fmt"
	"strings"
)

// sharedExitHelp says what an exit code means in every command that
// changes a cluster. The words are the same in each help text, so the
// same case reads the same everywhere. Code 5 has no shared case: what is
// checked after a change differs by command.
var sharedExitHelp = [...]string{
	0: "Done, or nothing to do.",
	1: "An error, a question answered no, or an interrupt.",
	2: "With --dry-run only: a real run would ask about warnings.",
	3: "Blocked, and nothing changed: the cluster is busy, a gate stops the\n     change, or --require-healthy and a warning.",
	4: "Incomplete: a target was already changing, a wait timed out, or\n     something could not be read.",
}

// ExitCodeHelp returns the last paragraph of the long help of a command
// that changes a cluster: one entry for each exit code that the command
// can return. An entry starts with the shared line of its code, and own
// adds the case that only this command has. A code above 4 is listed only
// when own has it.
func ExitCodeHelp(own map[int]string) string {
	var b strings.Builder
	b.WriteString("Exit codes:")
	for code := range 6 {
		var lines []string
		if code < len(sharedExitHelp) {
			lines = append(lines, sharedExitHelp[code])
		}
		if text := own[code]; text != "" {
			lines = append(lines, text)
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "\n  %d  %s", code, strings.Join(lines, "\n     "))
		}
	}
	return b.String()
}
