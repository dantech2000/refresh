// Package flagcanon holds refresh's flag shorthand canon: the one meaning of
// each short flag letter across the CLI, the shorthands removed in 0.11.0
// with their replacements, and helpers for flags kept for one release as
// hidden, deprecated aliases. (REF-164)
//
// It depends only on urfave/cli and internal/ui so that main, the command
// packages, and the fakeaws test harness can all install the same handler.
package flagcanon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/ui"
)

// CanonVersion is the release that introduced the canon and removed the
// conflicting shorthands.
const CanonVersion = "0.11.0"

// RemovalVersion is the release that removes the deprecated aliases kept by
// CanonVersion.
const RemovalVersion = "0.12.0"

// Canon maps each canonical short flag letter to the long flag it stands for.
// A canon letter never means anything else, and a flag with one of these long
// names carries its letter.
var Canon = map[string]string{
	"c": "cluster",
	"n": "nodegroup",
	"a": "addon",
	"o": "format",
	"r": "region",
	"t": "timeout",
	"d": "dry-run",
	"y": "yes",
	"q": "quiet",
	"w": "watch",
	"f": "filter",
}

// Extra is one allowed shorthand outside the canon.
type Extra struct {
	Long   string // the only long flag the letter may stand for
	Reason string
}

// Allowed lists the shorthands outside the canon, each bound to one long
// name. Add a letter here only with a reason; TestFlagShorthandCanon fails on
// any other shorthand.
var Allowed = map[string]Extra{
	"A": {"all-regions", "multi-region sweep on status and cluster list; uppercase so it cannot clash with a canon letter"},
	"C": {"max-concurrency", "global concurrency cap; uppercase, used in scripts since 0.x"},
	"H": {"show-health", "health column on list commands; uppercase, one meaning everywhere"},
	"R": {"check-readiness", "node readiness from the cluster API on list/describe; uppercase"},
	"T": {"tree", "cluster list tree view; uppercase"},
	"I": {"show-instances", "nodegroup describe EC2 details; uppercase"},
	"W": {"show-workloads", "nodegroup describe workload placement; uppercase"},
}

// BuiltIn lists the shorthands urfave/cli adds itself when a command runs:
// -h/--help everywhere and -v/--version on the root. They are not in any
// command's Flags before Run, and no flag may reuse them.
var BuiltIn = map[string]string{"h": "help", "v": "version"}

// removed maps a command path (without the root name) to the shorthands
// removed from it in CanonVersion and what to use instead.
var removed = map[string]map[string]string{
	"cluster describe": {
		"d": "use --detailed",
		"s": "use --show-security",
		"a": "add-ons are shown by default; use --no-addons to hide them",
	},
	"cluster upgrade": {
		"s": "use --skip",
		"p": "use --poll-interval",
	},
	"nodegroup update": {
		"f": "use --force",
		"s": "use --skip-health-check",
		"p": "use --poll-interval",
	},
	"addon update": {
		"s": "use --skip",
		"p": "use --parallel",
	},
	"addon update-all": {
		"s": "use --skip",
		"p": "use --parallel",
	},
	"context add": {
		"p": "use --profile",
	},
}

// Removed returns a copy of the removed-shorthand table, keyed by command
// path then letter, for tests and docs.
func Removed() map[string]map[string]string {
	out := make(map[string]map[string]string, len(removed))
	for path, m := range removed {
		cp := make(map[string]string, len(m))
		for k, v := range m {
			cp[k] = v
		}
		out[path] = cp
	}
	return out
}

// notDefinedPrefix is urfave/cli's error text for an unknown flag. The flag
// name follows it without its leading dashes.
const notDefinedPrefix = "flag provided but not defined: -"

// commandPath is cmd's path without the root name ("cluster describe").
func commandPath(cmd *cli.Command) string {
	p := cmd.Path()
	if len(p) > 1 {
		p = p[1:]
	}
	return strings.Join(p, " ")
}

// RemovedShorthandError returns the helpful error for err when it reports an
// unknown flag that is a shorthand removed from cmd, or nil.
func RemovedShorthandError(cmd *cli.Command, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, notDefinedPrefix) {
		return nil
	}
	name := strings.TrimLeft(strings.TrimPrefix(msg, notDefinedPrefix), "-")
	path := commandPath(cmd)
	hint, ok := removed[path][name]
	if !ok {
		return nil
	}
	return fmt.Errorf("-%s was removed from '%s' in %s; %s", name, path, CanonVersion, hint)
}

// HandleUsageError is the OnUsageError handler for every command. A removed
// shorthand gets an error that names its replacement. Any other usage error
// keeps urfave/cli's default behavior: the error and the command help on
// stderr.
func HandleUsageError(_ context.Context, cmd *cli.Command, err error, isSubcommand bool) error {
	if rerr := RemovedShorthandError(cmd, err); rerr != nil {
		return rerr
	}
	_, _ = fmt.Fprintf(cmd.Root().ErrWriter, "Incorrect Usage: %s\n\n", err.Error())
	if isSubcommand {
		_ = cli.ShowSubcommandHelp(cmd)
	} else {
		_ = cli.ShowRootCommandHelp(cmd)
	}
	return err
}

// Install applies the CLI-wide parsing rules to root and every command
// below it:
//   - HandleUsageError on each command (urfave/cli does not inherit
//     OnUsageError).
//   - No help subcommand on a leaf command. urfave/cli gives every command a
//     hidden "help" subcommand (alias "h"), so on a leaf a first positional of
//     "h" or "help" printed help and exited 0 instead of running: a cluster
//     named "h" passed `cluster upgrade-check h` without a check. --help and
//     -h still work; group commands keep "help".
func Install(root *cli.Command) {
	_ = root.Walk(func(c *cli.Command) error {
		c.OnUsageError = HandleUsageError
		if len(c.Commands) == 0 && c != root {
			c.HideHelpCommand = true
		}
		return nil
	})
}

// Warn prints the one-line stderr notice for a deprecated flag set on cmd.
func Warn(cmd *cli.Command, flag, hint string) {
	th := render.Default(ui.Stderr)
	_, _ = fmt.Fprintln(ui.Stderr, th.Paint(th.Pal.Yellow, fmt.Sprintf(
		"warning: %s on '%s' is deprecated and will be removed in %s; %s",
		flag, commandPath(cmd), RemovalVersion, hint)))
}

// DeprecatedDuration returns a hidden --name duration flag kept for one
// release as an alias of --replacement. Setting it prints a deprecation
// warning; the caller reads it through runner.WaitTimeout. Setting both
// flags is an error, since one would silently override the other.
func DeprecatedDuration(name, replacement string, aliases ...string) *cli.DurationFlag {
	return &cli.DurationFlag{
		Name:    name,
		Aliases: aliases,
		Hidden:  true,
		Usage:   "Deprecated: use --" + replacement,
		Action: func(_ context.Context, cmd *cli.Command, _ time.Duration) error {
			if LocalIsSet(cmd, replacement) {
				return fmt.Errorf("--%s on '%s' is a deprecated alias of --%s; pass only --%s",
					name, commandPath(cmd), replacement, replacement)
			}
			Warn(cmd, "--"+name, "use --"+replacement)
			return nil
		},
	}
}

// DeprecatedSwitch returns a hidden --name bool flag, default true, kept for
// one release after the behavior it enabled became the default. Setting it
// prints a deprecation warning: --name alone is a no-op, and --name=false
// maps to the --negation flag the caller also checks.
func DeprecatedSwitch(name, negation string, aliases ...string) *cli.BoolFlag {
	return &cli.BoolFlag{
		Name:    name,
		Aliases: aliases,
		Hidden:  true,
		Value:   true,
		Usage:   "Deprecated: on by default; use --" + negation + " to turn it off",
		Action: func(_ context.Context, cmd *cli.Command, v bool) error {
			if v {
				Warn(cmd, "--"+name, "it is on by default (use --"+negation+" to turn it off)")
			} else {
				Warn(cmd, "--"+name+"=false", "use --"+negation)
			}
			return nil
		},
	}
}

// LocalIsSet reports whether cmd's own flag name (not an ancestor's) was set.
func LocalIsSet(cmd *cli.Command, name string) bool {
	for _, f := range cmd.Flags {
		if f.Names()[0] == name {
			return f.IsSet()
		}
	}
	return false
}
