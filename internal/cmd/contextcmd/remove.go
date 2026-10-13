package contextcmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/contexts"
)

type removeOptions struct {
	ios   *cmdutil.IOStreams
	store func() (*contexts.Store, error)

	name string
}

func newCmdRemove(f *cmdutil.Factory) *cobra.Command {
	o := &removeOptions{ios: f.IOStreams, store: f.Contexts}
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove a saved context",
		Long: `Remove a saved context. When it is the active context, no context is active
afterwards.`,
		Example:           `  refresh context remove prod`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNames(f.Contexts),
		RunE: func(_ *cobra.Command, args []string) error {
			o.complete(args)
			if err := o.validate(); err != nil {
				return err
			}
			return o.run()
		},
	}
}

func (o *removeOptions) complete(args []string) {
	o.name = contextName(args[0])
}

func (o *removeOptions) validate() error {
	return validateName(o.name)
}

func (o *removeOptions) run() error {
	store, file, err := loadFile(o.store)
	if err != nil {
		return err
	}
	if err := file.Remove(o.name); err != nil {
		return err
	}
	if err := store.Save(file); err != nil {
		return err
	}
	_, err = fmt.Fprintf(o.ios.Out, "Removed context %q\n", o.name)
	return err
}
