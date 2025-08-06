package commands

import "strings"

// truncateString truncates a string to maxLen characters, adding "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// padColoredString pads a colored string to a specific width
// This accounts for ANSI color codes that don't count toward visible width
func padColoredString(s string, width int) string {
	// Calculate visible length (excluding ANSI codes)
	visibleLen := len(stripAnsiCodes(s))
	if visibleLen >= width {
		return s
	}

	// Add padding spaces to reach desired width
	padding := strings.Repeat(" ", width-visibleLen)
	return s + padding
}

// stripAnsiCodes removes ANSI color codes from a string for length calculation
func stripAnsiCodes(s string) string {
	// Simple approach: remove sequences that start with ESC[ and end with a letter
	result := ""
	inEscape := false

	for i, r := range s {
		if r == '\033' && i+1 < len(s) && s[i+1] == '[' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		result += string(r)
	}

	return result
}
