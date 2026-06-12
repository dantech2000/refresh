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
		Name:      "use",
		Usage:     "Switch the active refresh context (kubectx-style)",
		ArgsUsage: "[context-name|-]",
		Description: `Switch the active context so subsequent commands inherit its cluster,
region, and profile instead of you repeating --cluster/--region/--profile on
every invocation (the kubectx workflow, applied to EKS).

Save contexts first with 'refresh context add', then switch between them:

  refresh context add prod  --cluster prod-eks  --region us-east-1 --profile prod
  refresh context add stage --cluster stage-eks --region us-west-2 --profile stage
  refresh use prod      # all later commands target prod-eks/us-east-1/prod
  refresh use -         # toggle back to the previously active context
  refresh use           # no name: pick interactively from the saved list

Per-invocation --region/--profile/--cluster flags still override the active
context. The REFRESH_CONTEXT env var overrides the saved current pointer for a
single shell.`,
		Action:        runUse,
		ShellComplete: completeContextNames,
	}
}

// CurrentCommand returns the `refresh current` command.
func CurrentCommand() *cli.Command {
	return &cli.Command{
		Name:  "current",
		Usage: "Print the active refresh context",
		Description: `Print the name and cluster/region/profile of the currently active context
(set with 'refresh use'). Honors the REFRESH_CONTEXT env override. Prints a hint
when no context is active.`,
		Action: runCurrent,
	}
}

// ContextCommand returns the `refresh context` command group.
func ContextCommand() *cli.Command {
	return &cli.Command{
		Name:    "context",
		Aliases: []string{"ctx"},
		Usage:   "Manage saved refresh contexts (list, add, remove)",
		Description: `Manage the named contexts that 'refresh use' switches between. Each context
binds a cluster to an optional region and AWS profile, so you can name your
environments once and select them by name (the kubectx model for EKS).

Contexts are stored as YAML under $XDG_CONFIG_HOME/refresh/context.yaml
(default ~/.config/refresh/context.yaml).

  refresh context add prod --cluster prod-eks --region us-east-1 --profile prod
  refresh context list             # show all saved contexts (* marks active)
  refresh context add prod --cluster prod-eks --use   # add and switch in one step
  refresh context remove prod      # delete a saved context`,
		Commands: []*cli.Command{
			contextListCommand(),
			contextAddCommand(),
			contextRemoveCommand(),
		},
	}
}
