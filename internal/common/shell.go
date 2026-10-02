package common

import "strings"

// ShellQuote returns s as one POSIX shell word: unchanged when it holds only
// safe characters, else single-quoted. Commands refresh prints for the user
// to run quote their values with it.
func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := true
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("-_./:=,@%+", r) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
