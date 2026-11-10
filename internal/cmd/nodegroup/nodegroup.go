// Package nodegroup implements "refresh nodegroup" and its subcommands.
package nodegroup

import (
	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/view"
)

// NewCmdNodegroup returns the nodegroup command with its subcommands.
func NewCmdNodegroup(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "nodegroup",
		Aliases: []string{"ng"},
		Short:   "Work with EKS managed nodegroups",
		Long: `Work with the managed nodegroups of one cluster.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context. A command that only reads also takes the cluster of the
current kubeconfig context; "nodegroup update" and "nodegroup scale" never
do.`,
		Example: `  # The nodegroups of a cluster, with the state of their AMIs
  refresh nodegroup list -c prod

  # One nodegroup, with its instances
  refresh nodegroup describe web -c prod --show-instances

  # Roll the nodegroups that are behind onto the latest AMI
  refresh nodegroup update -c prod

  # Scale a nodegroup to four nodes
  refresh nodegroup scale web -c prod --desired 4`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newCmdList(f), newCmdDescribe(f), newCmdUpdate(f), newCmdScale(f))
	return cmd
}

// amiText returns the text of the AMI status of a nodegroup: "" when the
// AMIs were not asked for, and "unknown" when they could not be read.
func amiText(n apidoc.Nodegroup) string {
	if n.AMI == nil {
		return view.OrUnknown(n.Incomplete, "")
	}
	return string(n.AMI.Status)
}

// amiStatus returns the status that draws the AMI of a nodegroup.
func amiStatus(n apidoc.Nodegroup) view.Status {
	if n.AMI == nil {
		return view.Unknown
	}
	switch n.AMI.Status {
	case apidoc.AMILatest:
		return view.Healthy
	case apidoc.AMIOutdated:
		return view.Warn
	case apidoc.AMIUpdating:
		return view.Progress
	case apidoc.AMICustom:
		return view.Neutral
	}
	return view.Unknown
}

// The keys of --filter and --sort. The first sort key is the default.
const (
	keyName         = "name"
	keyStatus       = "status"
	keyInstanceType = "instanceType"
	keyNodes        = "nodes"
	keyVersion      = "version"
	keyAMIStatus    = "amiStatus"
)

// filterKeys are the keys of -f: what one read of the nodegroups gives.
var filterKeys = []string{keyName, keyStatus, keyInstanceType, keyAMIStatus, keyVersion}

// filterValues returns the values of n for a key of -f.
func filterValues(n fleet.Nodegroup) func(key string) []string {
	return func(key string) []string {
		switch key {
		case keyStatus:
			return []string{n.Status}
		case keyInstanceType:
			return n.InstanceTypes
		case keyVersion:
			return []string{n.Version.String()}
		case keyAMIStatus:
			status, _ := n.AMIStatus()
			return []string{string(status)}
		}
		return []string{n.Name}
	}
}
