package ui

import "context"

// PromptContinueWithWarnings prompts the user to continue despite warnings.
// The default is No: this guards a rolling update on a cluster that just
// failed health warnings, so a bare Enter (or unreadable/closed stdin, as in
// CI) must not silently proceed. The report itself is rendered by
// internal/healthview.
func PromptContinueWithWarnings(ctx context.Context, warnings []string) bool {
	if len(warnings) > 0 {
		Outf("\n%d warning(s) reported above.", len(warnings))
	}
	Outf("\nProceed with update? (y/N): ")

	return Confirm(ctx)
}
