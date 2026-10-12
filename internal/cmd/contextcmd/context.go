// Package contextcmd implements "refresh context" and its subcommands.
//
// The package is not named context, so that it does not hide the standard
// library package.
package contextcmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/contexts"
)

// NewCmdContext returns the "context" command group.
func NewCmdContext(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "context",
		Aliases: []string{"ctx"},
		Short:   "Manage saved contexts",
		Long: `Manage the named contexts and choose the active one.

A context gives a name to a cluster, with an optional region and AWS profile.
Commands use the active context when you do not name a cluster.

The contexts are in a YAML file: $REFRESH_CONFIG_HOME/context.yaml when that
variable is set, otherwise $XDG_CONFIG_HOME/refresh/context.yaml, which is
~/.config/refresh/context.yaml by default.`,
		Example: `  refresh context add prod --cluster prod-eks --region us-east-1 --profile prod
  refresh context list
  refresh context use prod
  refresh context current
  refresh context remove prod`,
		// Without Args and RunE, cobra prints help and exits 0 for
		// "refresh context nosuchcommand".
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newCmdList(f), newCmdAdd(f), newCmdRemove(f), newCmdUse(f), newCmdCurrent(f))
	return cmd
}

// loadFile reads the context file of the store that the factory gives.
func loadFile(f func() (*contexts.Store, error)) (*contexts.Store, *contexts.File, error) {
	store, err := f()
	if err != nil {
		return nil, nil, err
	}
	file, err := store.Load()
	if err != nil {
		return nil, nil, err
	}
	return store, file, nil
}

// contextName returns the context name in the argument arg, without the
// spaces around it. A name that is empty then is an error: no command could
// select that context again.
func contextName(arg string) (string, error) {
	name := strings.TrimSpace(arg)
	if name == "" {
		return "", errors.New("context name is required")
	}
	return name, nil
}

// envContext returns the value of REFRESH_CONTEXT.
func envContext() string {
	return os.Getenv(contexts.EnvContext)
}

// completeNames completes the first argument with the saved context names.
func completeNames(store func() (*contexts.Store, error)) cobra.CompletionFunc {
	return func(_ *cobra.Command, args []string, _ string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		_, file, err := loadFile(store)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var names []cobra.Completion
		for _, name := range file.Names() {
			names = append(names, cobra.CompletionWithDesc(name, describe(file.Contexts[name])))
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// describe returns the fields of c on one line, with "-" for an empty one.
func describe(c contexts.Context) string {
	return fmt.Sprintf("cluster=%s region=%s profile=%s", c.Cluster, orDash(c.Region), orDash(c.Profile))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
