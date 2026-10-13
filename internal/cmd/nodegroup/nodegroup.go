// Package nodegroup implements "refresh nodegroup" and its subcommands.
package nodegroup

import (
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/view"
)

// NewCmdNodegroup returns the nodegroup command with its subcommands.
func NewCmdNodegroup(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nodegroup",
		Short: "Work with EKS managed nodegroups",
		Long: `Work with the managed nodegroups of one cluster.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context. A command that only reads also takes the cluster of the
current kubeconfig context; "nodegroup scale" never does.`,
		Example: `  # The nodegroups of a cluster, with the state of their AMIs
  refresh nodegroup list -c prod

  # One nodegroup, with its instances
  refresh nodegroup describe web -c prod --show-instances

  # Scale a nodegroup to four nodes
  refresh nodegroup scale web -c prod --desired 4`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newCmdList(f), newCmdDescribe(f), newCmdScale(f))
	return cmd
}

// unknownCell is the text of a cell whose data could not be read.
const unknownCell = "unknown"

// orUnknown returns value, or "unknown" for a nodegroup that was not read.
func orUnknown(n apidoc.Nodegroup, value string) string {
	if value == "" && n.Incomplete {
		return unknownCell
	}
	return value
}

// amiText returns the text of the AMI status of a nodegroup: "" when the
// AMIs were not asked for, and "unknown" when they could not be read.
func amiText(n apidoc.Nodegroup) string {
	if n.AMI == nil {
		return orUnknown(n, "")
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

// versionStatus returns the status that draws the version of a nodegroup:
// a warning when it is behind the control plane.
func versionStatus(n apidoc.Nodegroup) view.Status {
	if n.VersionBehind != nil && *n.VersionBehind {
		return view.Warn
	}
	return view.NoStatus
}

// instanceTypes returns the instance types of a nodegroup as one cell.
func instanceTypes(n apidoc.Nodegroup) string {
	return orUnknown(n, strings.Join(n.InstanceTypes, ","))
}

// nodes returns the desired size of a nodegroup as a cell, with the Ready
// count before it when it was read: "5/6".
func nodes(n apidoc.Nodegroup) string {
	if n.Scaling == nil {
		return orUnknown(n, "")
	}
	desired := strconv.Itoa(int(n.Scaling.Desired))
	if n.ReadyNodes != nil {
		return strconv.Itoa(*n.ReadyNodes) + "/" + desired
	}
	return desired
}

// dateLayout is how a describe view prints a time.
const dateLayout = "2006-01-02 15:04 UTC"

// formatTime returns t for a describe view, or "" for the zero time.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(dateLayout)
}
