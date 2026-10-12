package contextcmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/contexts"
)

type addOptions struct {
	io    *cmdutil.IOStreams
	store func() (*contexts.Store, error)

	name    string
	context contexts.Context
	use     bool
}

func newCmdAdd(f *cmdutil.Factory) *cobra.Command {
	o := &addOptions{io: f.IOStreams, store: f.Contexts}
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or update a saved context",
		Long: `Save a context that gives a name to a cluster, with an optional region and
AWS profile. A context with the same name is replaced.`,
		Example: `  refresh context add prod --cluster prod-eks --region us-east-1 --profile prod

  # Add the context and switch to it
  refresh context add stage --cluster stage-eks --use`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var err error
			if o.name, err = contextName(args[0]); err != nil {
				return err
			}
			return o.run()
		},
	}
	// These flags are local: --region and --profile here are the values to
	// save, not the region and profile of this run.
	cmd.Flags().StringVarP(&o.context.Cluster, "cluster", "c", "", "EKS cluster name (required)")
	cmd.Flags().StringVarP(&o.context.Region, "region", "r", "", "AWS region to save with the context")
	cmd.Flags().StringVar(&o.context.Profile, "profile", "", "AWS shared-config profile to save with the context")
	cmd.Flags().BoolVar(&o.use, "use", false, "Switch to the context after it is saved")
	// The flag was added above, so this cannot fail.
	_ = cmd.MarkFlagRequired("cluster")
	return cmd
}

func (o *addOptions) run() error {
	store, file, err := loadFile(o.store)
	if err != nil {
		return err
	}
	if err := file.Set(o.name, o.context); err != nil {
		return err
	}
	if o.use {
		if err := file.Use(o.name); err != nil {
			return err
		}
	}
	if err := store.Save(file); err != nil {
		return err
	}
	_, err = fmt.Fprintf(o.io.Out, "Saved context %q\n", o.name)
	return err
}
