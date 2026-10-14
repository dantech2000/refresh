package contextcmd

import (
	"fmt"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/contexts"
)

type addOptions struct {
	ios   *cmdutil.IOStreams
	store func() (*contexts.Store, error)

	name    string
	context contexts.Context
	use     bool
}

func newCmdAdd(f *cmdutil.Factory) *cobra.Command {
	o := &addOptions{ios: f.IOStreams, store: f.Contexts}
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or update a saved context",
		Long: `Save a context that gives a name to a cluster, with an optional region and
AWS profile. A context with the same name is replaced, and the command says
"Updated" in place of "Saved".

The name has 1 to 63 letters, digits, hyphens, underscores, and dots. The
cluster name and the region are checked before the context is saved.

Exit codes:
  0  Done.
  1  An error.`,
		Example: `  refresh context add prod --cluster prod-eks --region us-east-1 --profile prod

  # Add the context and switch to it
  refresh context add stage --cluster stage-eks --use`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			o.complete(args)
			if err := o.validate(); err != nil {
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

func (o *addOptions) complete(args []string) {
	o.name = contextName(args[0])
}

// newNamePattern is the set of characters of a new context name: few
// enough that the name is safe in a shell, a prompt, and the YAML file.
var newNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,63}$`)

// validate checks what the context saves before it is saved: a context
// with a name, a cluster, or a region that cannot work would fail every
// command that uses it.
func (o *addOptions) validate() error {
	if err := validateName(o.name); err != nil {
		return err
	}
	switch {
	case o.name == "-":
		return fmt.Errorf(`invalid context name %q: "-" stands for the previous context in "refresh context use -"`, o.name)
	case !newNamePattern.MatchString(o.name):
		return fmt.Errorf("invalid context name %q: a context name has 1 to 63 letters, digits, hyphens, underscores, and dots", o.name)
	}
	if err := awsx.CheckClusterName(o.context.Cluster); err != nil {
		return err
	}
	if o.context.Region != "" {
		if err := awsx.CheckRegion(o.context.Region); err != nil {
			return fmt.Errorf("invalid --region: %w", err)
		}
	}
	return nil
}

func (o *addOptions) run() error {
	store, file, err := loadFile(o.store)
	if err != nil {
		return err
	}
	// Replacing a context is allowed, as kubectl config set-context
	// allows it, and the line says which one happened.
	verb := "Saved"
	if _, ok := file.Contexts[o.name]; ok {
		verb = "Updated"
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
	if _, err := fmt.Fprintf(o.ios.Out, "%s context %q\n", verb, o.name); err != nil {
		return err
	}
	if o.use {
		// The line of "context use", so that both say a switch the same way.
		_, err = fmt.Fprintf(o.ios.Out, "Switched to context %q (%s)\n", o.name, describe(file.Contexts[o.name]))
	}
	return err
}
