package aws

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/ui"
)

// MatchingNodegroups returns nodegroup names that contain the given pattern.
// If a nodegroup is named exactly pattern, only that nodegroup is returned, so
// "ng-a" never also selects "ng-a-spot" or "ng-a2". If pattern is empty,
// returns all nodegroups.
func MatchingNodegroups(nodegroups []string, pattern string) []string {
	return matchPreferExact(nodegroups, pattern)
}

// ConfirmNodegroupSelection prompts user to confirm when multiple nodegroups match.
// Returns the selected nodegroups or error if user cancels.
func ConfirmNodegroupSelection(ctx context.Context, matches []string, pattern string) ([]string, error) {
	switch {
	case len(matches) == 0:
		return nil, fmt.Errorf("no nodegroups found matching pattern: %s", pattern)
	case len(matches) == 1:
		return matches, nil
	case pattern == "":
		// No pattern specified - user wants to update all
		return matches, nil
	default:
		return promptForNodegroupConfirmation(ctx, matches, pattern)
	}
}

// nodegroupPromptOut receives the nodegroup-choice prompt. Prompts go to
// stderr, like the cluster prompts, so stdout carries only command output.
// A var so tests can capture it.
var nodegroupPromptOut io.Writer = ui.Stderr

// promptForNodegroupConfirmation displays matching nodegroups and prompts for confirmation.
func promptForNodegroupConfirmation(ctx context.Context, matches []string, pattern string) ([]string, error) {
	_, _ = ui.ColorFor(nodegroupPromptOut, color.FgYellow).Fprintf(nodegroupPromptOut, "Multiple nodegroups match pattern '%s':\n", pattern)
	for i, ng := range matches {
		_, _ = fmt.Fprintf(nodegroupPromptOut, "  %d) %s\n", i+1, ng)
	}

	_, _ = ui.ColorFor(nodegroupPromptOut, color.FgCyan).Fprintf(nodegroupPromptOut, "Update all %d matching nodegroups? (y/N): ", len(matches))

	response, err := promptLine(ctx)
	if err != nil {
		return nil, ui.PromptError(err)
	}

	// Default is No: bare Enter (or anything but yes) cancels.
	response = strings.ToLower(response)
	if response == "y" || response == "yes" {
		return matches, nil
	}

	return nil, fmt.Errorf("operation cancelled by user")
}
