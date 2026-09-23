package runner

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/flagcanon"
	"github.com/dantech2000/refresh/internal/ui"
)

// Shared behavior of the mutating commands (addon update, nodegroup scale,
// nodegroup update, cluster upgrade): --dry-run/-d never prompts, --yes/-y
// skips the confirmation, and a run that cannot prompt (-o json/yaml, or no
// terminal on stdin) fails fast with an error that names --yes. (REF-164)

// StdinIsTerminal reports whether stdin is a terminal a prompt can read. It
// is a var so tests can simulate one.
var StdinIsTerminal = func() bool { return ui.IsTerminal(os.Stdin) }

// PromptLine reads one prompt answer. It is a var so tests can answer.
var PromptLine = ui.ReadLine

// DryRunFlag is the shared --dry-run/-d flag of a mutating command.
func DryRunFlag(usage string) *cli.BoolFlag {
	return &cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: usage}
}

// YesFlag is the shared --yes/-y flag of a mutating command.
func YesFlag(usage string) *cli.BoolFlag {
	return &cli.BoolFlag{Name: "yes", Aliases: []string{"y"}, Usage: usage}
}

// WaitTimeoutFlag is the shared --wait-timeout flag: how long a mutating
// command waits for its change to finish.
func WaitTimeoutFlag(usage string, value time.Duration) *cli.DurationFlag {
	return &cli.DurationFlag{Name: "wait-timeout", Usage: usage, Value: value}
}

// RequireYesUnattended returns an error naming --yes when a mutating run
// would need to prompt but cannot: with -o json/yaml (machine runs never
// prompt) or without a terminal on stdin. It returns nil with --yes or
// --dry-run. Call it before any AWS call.
func RequireYesUnattended(cmd *cli.Command) error {
	if cmd.Bool("yes") || cmd.Bool("dry-run") {
		return nil
	}
	name := commandName(cmd)
	format := strings.ToLower(cmd.String("format"))
	if IsMachineFormat(format) {
		return fmt.Errorf("%s -o %s does not prompt for confirmation; add --yes to proceed, or --dry-run to preview", name, format)
	}
	if !StdinIsTerminal() {
		return fmt.Errorf("%s needs confirmation but there is no interactive terminal; add --yes to proceed, or --dry-run to preview", name)
	}
	return nil
}

// ConfirmMutation asks question on stderr ("<question> [y/N]: ") and reads
// the answer with the shared, ctx-aware prompt reader. Only "y" or "yes"
// proceeds; anything else returns an "operation cancelled" error. Call
// RequireYesUnattended first; this never checks --yes itself.
func ConfirmMutation(ctx context.Context, question string) error {
	_, _ = fmt.Fprintf(ui.Stderr, "%s [y/N]: ", question)
	answer, err := PromptLine(ctx)
	if err != nil {
		return ui.PromptError(err)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	default:
		return fmt.Errorf("operation cancelled by user")
	}
}

// WaitTimeout returns the command's --wait-timeout, or the value of the
// deprecated local flag (e.g. --timeout on nodegroup update) when that was
// set on this command. flagcanon.DeprecatedDuration prints the warning.
func WaitTimeout(cmd *cli.Command, deprecated string) time.Duration {
	if deprecated != "" && flagcanon.LocalIsSet(cmd, deprecated) {
		return cmd.Duration(deprecated)
	}
	return cmd.Duration("wait-timeout")
}

// APITimeout returns the timeout for the command's API calls: the nearest
// visible --timeout in its lineage. A hidden local --timeout is a deprecated
// wait-timeout alias (nodegroup update, cluster upgrade), so it is skipped
// and the global --timeout applies.
func APITimeout(cmd *cli.Command) time.Duration {
	for _, c := range cmd.Lineage() {
		for _, f := range c.Flags {
			if !slices.Contains(f.Names(), "timeout") {
				continue
			}
			if vf, ok := f.(cli.VisibleFlag); ok && !vf.IsVisible() {
				continue
			}
			return c.Duration("timeout")
		}
	}
	return cmd.Duration("timeout")
}

// commandName is cmd's path without the root name ("addon update").
func commandName(cmd *cli.Command) string {
	p := cmd.Path()
	if len(p) > 1 {
		p = p[1:]
	}
	return strings.Join(p, " ")
}

// WaitDeadlineHint rewrites the "increase --timeout" hint in *errp to name
// --wait-timeout. Defer it right after setup in a command whose run deadline
// is --wait-timeout (nodegroup update, cluster upgrade, and the --wait runs
// of addon update and nodegroup scale), so a timeout names the flag that
// sets it. Setup errors happen before the defer and keep --timeout, which
// bounds the credential check. An exit code is kept.
func WaitDeadlineHint(errp *error) {
	err := *errp
	if err == nil {
		return
	}
	re := awserr.RetargetDeadlineHint(err, "--wait-timeout")
	if re.Error() == err.Error() {
		return
	}
	if ec, ok := err.(cli.ExitCoder); ok { //nolint:errorlint // urfave/cli checks the unwrapped ExitCoder
		*errp = cli.Exit(re.Error(), ec.ExitCode())
		return
	}
	*errp = re
}
