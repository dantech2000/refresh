package contextcmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/contexts"
)

type listOptions struct {
	ios   *cmdutil.IOStreams
	store func() (*contexts.Store, error)

	envContext string
}

func newCmdList(f *cmdutil.Factory) *cobra.Command {
	o := &listOptions{ios: f.IOStreams, store: f.Contexts}
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the saved contexts",
		Long: `List every saved context with its cluster, region, and profile. A "*" marks
the active context.`,
		Example: `  refresh context list`,
		Args:    cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			o.complete()
			return o.run()
		},
	}
}

func (o *listOptions) complete() {
	o.envContext = envContext()
}

func (o *listOptions) run() error {
	_, file, err := loadFile(o.store)
	if err != nil {
		return err
	}
	if len(file.Contexts) == 0 {
		// A hint, not data: it goes to stderr and stdout stays empty.
		_, err := fmt.Fprintln(o.ios.ErrOut, "No saved contexts. Add one with: refresh context add <name> --cluster <cluster>")
		return err
	}
	// The list is how a user finds the right name, so an unknown
	// REFRESH_CONTEXT is a warning here and not an error.
	active, _, _, err := file.Active(o.envContext)
	if err != nil {
		if _, err := fmt.Fprintf(o.ios.ErrOut, "warning: %v\n", err); err != nil {
			return err
		}
	}
	for _, name := range file.Names() {
		marker := " "
		if name == active {
			marker = "*"
		}
		if _, err := fmt.Fprintf(o.ios.Out, "%s %-20s %s\n", marker, name, describe(file.Contexts[name])); err != nil {
			return err
		}
	}
	return nil
}
