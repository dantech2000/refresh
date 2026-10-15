package cmdutil

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// RequireYes returns the error of a mutation that would need a prompt it
// cannot show: a machine format, or no terminal on stdin. --yes and
// --dry-run are exempt, because neither prompts. It reads no input.
func RequireYes(ios *IOStreams, format Format, yes, dryRun bool) error {
	switch {
	case yes || dryRun:
		return nil
	case format.IsMachine():
		return fmt.Errorf("-o %s prints one document and cannot prompt; pass --yes to confirm the change", format)
	case !ios.InTTY:
		return errors.New("no terminal to confirm the change; pass --yes")
	}
	return nil
}

// Confirm asks question on stderr, as "question [y/N]: ", and reads one
// line. It answers true for "y" or "yes", in any case; anything else, the
// end of the input included, is no. It honours the cancellation of ctx,
// and under a context from WithTimeout the deadline stops while it waits.
func Confirm(ctx context.Context, ios *IOStreams, question string) (bool, error) {
	// Nothing can report a failed write to stderr.
	_, _ = fmt.Fprint(ios.ErrOut, question+" [y/N]: ")
	line, err := ReadLine(ctx, ios)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
