package aws

import (
	"fmt"
	"strings"

	"github.com/fatih/color"
)

// MatchingNodegroups returns nodegroup names that contain the given pattern.
// If pattern is empty, returns all nodegroups.
func MatchingNodegroups(nodegroups []string, pattern string) []string {
	if pattern == "" {
		return nodegroups
	}

	matches := make([]string, 0, len(nodegroups))
	for _, ng := range nodegroups {
		if strings.Contains(ng, pattern) {
			matches = append(matches, ng)
		}
	}

	return matches
}

// ConfirmNodegroupSelection prompts user to confirm when multiple nodegroups match.
// Returns the selected nodegroups or error if user cancels.
func ConfirmNodegroupSelection(matches []string, pattern string) ([]string, error) {
	switch {
	case len(matches) == 0:
		return nil, fmt.Errorf("no nodegroups found matching pattern: %s", pattern)
	case len(matches) == 1:
		return matches, nil
	case pattern == "":
		// No pattern specified - user wants to update all
		return matches, nil
	default:
		return promptForNodegroupConfirmation(matches, pattern)
	}
}

// promptForNodegroupConfirmation displays matching nodegroups and prompts for confirmation.
func promptForNodegroupConfirmation(matches []string, pattern string) ([]string, error) {
	color.Yellow("Multiple nodegroups match pattern '%s':", pattern)
	for i, ng := range matches {
		fmt.Printf("  %d) %s\n", i+1, ng)
	}

	color.Cyan("Update all %d matching nodegroups? (y/N): ", len(matches))

	response, err := readPromptLine()
	if err != nil {
		return nil, fmt.Errorf("operation cancelled: failed to read input")
	}

	// Default is No: bare Enter (or anything but yes) cancels.
	response = strings.ToLower(response)
	if response == "y" || response == "yes" {
		return matches, nil
	}

	return nil, fmt.Errorf("operation cancelled by user")
}
