// Package ctxcmd implements refresh context management commands (use, current, context).
// The package is named ctxcmd to avoid shadowing the stdlib "context" package.
package ctxcmd

import (
	"github.com/urfave/cli/v2"
)

// UseCommand returns the `refresh use` command (kubectx-style context switch).
func UseCommand() *cli.Command {
	return &cli.Command{
		Name:      "use",
		Usage:     "Switch the active refresh context (kubectx-style)",
		ArgsUsage: "[context-name|-]",
		Action:    runUse,
	}
}

// CurrentCommand returns the `refresh current` command.
func CurrentCommand() *cli.Command {
	return &cli.Command{
		Name:   "current",
		Usage:  "Print the active refresh context",
		Action: runCurrent,
	}
}

// ContextCommand returns the `refresh context` command group.
func ContextCommand() *cli.Command {
	return &cli.Command{
		Name:    "context",
		Aliases: []string{"ctx"},
		Usage:   "Manage saved refresh contexts (list, add, remove)",
		Subcommands: []*cli.Command{
			contextListCommand(),
			contextAddCommand(),
			contextRemoveCommand(),
		},
	}
}
