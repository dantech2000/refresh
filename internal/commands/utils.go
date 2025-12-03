package commands

// truncateString truncates a string to maxLen characters, adding "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// Note: padColoredString and stripAnsiCodes functions have been removed.
// Use ui.DynamicTable for all key-value table displays which handles
// ANSI color alignment automatically.
