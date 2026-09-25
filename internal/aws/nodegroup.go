package aws

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/dantech2000/refresh/internal/ui"
)

// MatchingNodegroups returns nodegroup names that contain the given pattern.
// If a nodegroup is named exactly pattern, only that nodegroup is returned, so
// "ng-a" never also selects "ng-a-spot" or "ng-a2". If pattern is empty,
// returns all nodegroups.
func MatchingNodegroups(nodegroups []string, pattern string) []string {
	return matchPreferExact(nodegroups, pattern)
}

// NodegroupPatternNeedsConfirmation reports whether selecting matches for
// pattern needs a confirmation: the pattern is set and it is not the exact
// name of the one match (a single substring match, or several matches).
func NodegroupPatternNeedsConfirmation(matches []string, pattern string) bool {
	if pattern == "" || len(matches) == 0 {
		return false
	}
	return len(matches) > 1 || matches[0] != pattern
}

// ConfirmNodegroupSelection confirms the nodegroups that pattern selected.
// An exact name, or an empty pattern (every nodegroup), is returned as-is. A
// single non-exact (substring) match and multiple matches are confirmed on
// the terminal. The caller decides whether a prompt may run at all: without
// a TTY, or with -o json/yaml, it must require --yes instead of calling this.
// Returns the selected nodegroups, or an error if the user cancels.
func ConfirmNodegroupSelection(ctx context.Context, matches []string, pattern string) ([]string, error) {
	switch {
	case len(matches) == 0:
		return nil, fmt.Errorf("no nodegroups found matching pattern: %s", pattern)
	case !NodegroupPatternNeedsConfirmation(matches, pattern):
		return matches, nil
	case len(matches) == 1:
		return promptForSingleNodegroupMatch(ctx, matches[0], pattern)
	default:
		return promptForNodegroupConfirmation(ctx, matches, pattern)
	}
}

// nodegroupPromptOut receives the nodegroup-choice prompt. Prompts go to
// stderr, like the cluster prompts, so stdout carries only command output.
// A var so tests can capture it.
var nodegroupPromptOut io.Writer = ui.Stderr

// promptForSingleNodegroupMatch asks the user to confirm a non-exact match,
// so a pattern such as "web" never silently rolls "payments-web".
func promptForSingleNodegroupMatch(ctx context.Context, match, pattern string) ([]string, error) {
	_, _ = fmt.Fprintf(nodegroupPromptOut, "No nodegroup named %q. Update %q? [y/N]: ", pattern, match)
	response, err := promptLine(ctx)
	if err != nil {
		return nil, ui.PromptError(err)
	}
	switch strings.ToLower(response) {
	case "y", "yes":
		return []string{match}, nil
	default:
		return nil, fmt.Errorf("operation cancelled by user")
	}
}

// promptForNodegroupConfirmation displays matching nodegroups and prompts for confirmation.
func promptForNodegroupConfirmation(ctx context.Context, matches []string, pattern string) ([]string, error) {
	_, _ = fmt.Fprintf(nodegroupPromptOut, "Multiple nodegroups match pattern '%s':\n", pattern)
	for i, ng := range matches {
		_, _ = fmt.Fprintf(nodegroupPromptOut, "  %d) %s\n", i+1, ng)
	}

	_, _ = fmt.Fprintf(nodegroupPromptOut, "Update all %d matching nodegroups? (y/N): ", len(matches))

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
