// Package ctxcmd implements refresh context management commands (use, current, context).
// The package is named ctxcmd to avoid shadowing the stdlib "context" package.
package ctxcmd

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/cliconfig"
)

// completeContextNames prints saved context names for shell completion.
// Reading the local context file is fast and never touches AWS.
func completeContextNames(_ context.Context, cmd *cli.Command) {
	if cmd.NArg() > 0 {
		return // context name already provided
	}
	f, err := cliconfig.Load()
	if err != nil {
		return
	}
	for _, name := range f.Names() {
		_, _ = fmt.Fprintln(cmd.Root().Writer, name)
	}
}

// UseCommand returns the `refresh use` command (kubectx-style context switch).
func UseCommand() *cli.Command {
	return &cli.Command{
		Name:          "use",
		Usage:         "Switch the active refresh context (kubectx-style)",
		ArgsUsage:     "[context-name|-]",
		Action:        runUse,
		ShellComplete: completeContextNames,
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
		Commands: []*cli.Command{
			contextListCommand(),
			contextAddCommand(),
			contextRemoveCommand(),
		},
	}
}
