// Package addon implements "refresh addon" and its subcommands.
package addon

import (
	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmdutil"
)

// NewCmdAddon returns the addon command with its subcommands.
func NewCmdAddon(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "addon",
		Short: "Work with EKS add-ons",
		Long: `Work with the EKS add-ons of one cluster.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context. A command that only reads also takes the cluster of the
current kubeconfig context; "addon update" never does.`,
		Example: `  # The add-ons of a cluster
  refresh addon list -c prod

  # One add-on, with its configuration
  refresh addon describe vpc-cni -c prod

  # Update every add-on, in dependency order
  refresh addon update --all -c prod`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newCmdList(f), newCmdDescribe(f), newCmdUpdate(f))
	return cmd
}
