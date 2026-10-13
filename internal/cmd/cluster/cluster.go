// Package cluster implements "refresh cluster" and its subcommands.
package cluster

import (
	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmdutil"
)

// NewCmdCluster returns the cluster command with its subcommands.
func NewCmdCluster(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Work with EKS clusters",
		Long: `Work with EKS clusters.

"refresh cluster list" reads one region, the regions you name with -r, or
every region with -A. "refresh cluster describe" reads one cluster, and
"refresh cluster upgrade-check" reports whether it is ready to upgrade.`,
		Example: `  # The clusters of the configured region
  refresh cluster list

  # Every region, as JSON
  refresh cluster list -A -o json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newCmdList(f), newCmdDescribe(f), newCmdUpgradeCheck(f))
	return cmd
}
