package nodegroup

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/kube"
	"github.com/dantech2000/refresh/internal/view"
)

type listOptions struct {
	f   *cmdutil.Factory
	ios *cmdutil.IOStreams

	cluster        string
	filters        cmdutil.Filters
	sortKey        string
	desc           bool
	checkReadiness bool
	watch          bool
	interval       time.Duration
	output         cmdutil.Format

	// target is the cluster, resolved by complete.
	target cmdutil.Target
}

func newCmdList(f *cmdutil.Factory) *cobra.Command {
	o := &listOptions{f: f, ios: f.IOStreams}
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the nodegroups of a cluster",
		Long: `List the managed nodegroups of a cluster with their status, Kubernetes
version, and the state of their AMI.

The AMI column compares the AMI that the nodes run with the latest
recommended AMI for the version and the AMI type of the nodegroup: Latest,
Outdated, Updating, Custom (a launch template sets the AMI), or Unknown (a
nodegroup with no instance). A version with a warning mark is behind the
control plane.

A nodegroup that could not be read, or whose AMI could not be looked up, is
listed under INCOMPLETE DATA, or in "failures" with -o json or -o yaml.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context, then the current kubeconfig context.

Exit codes:
  0  Done, or a --watch that Ctrl+C stopped.
  1  An error, or an interrupt.
  4  Incomplete: a nodegroup could not be read, or its AMI could not be
     looked up. The command prints what it has.`,
		Example: `  refresh nodegroup list -c prod

  # The nodegroups with an outdated AMI
  refresh nodegroup list -c prod -f amiStatus=Outdated

  # The largest nodegroups first
  refresh nodegroup list -c prod --sort nodes --desc

  # The names of the outdated nodegroups for a script
  refresh nodegroup list -c prod -o json | jq -r '.nodegroups[] | select(.ami.status == "Outdated") | .name'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.complete(); err != nil {
				return err
			}
			if err := o.validate(); err != nil {
				return err
			}
			if o.watch {
				return cmdutil.Watch(cmd.Context(), o.ios, o.interval, f.After, o.run)
			}
			return o.run(cmd.Context(), o.ios)
		},
	}
	cmdutil.AddClusterFlag(cmd, &o.cluster)
	fleetcmd.RegisterClusterFlagCompletion(cmd, f)
	cmdutil.AddFilterFlag(cmd, &o.filters, filterKeys...)
	cmdutil.AddSortFlags(cmd, &o.sortKey, &o.desc, keyName, keyStatus, keyInstanceType, keyNodes, keyVersion, keyAMIStatus)
	cmd.Flags().BoolVar(&o.checkReadiness, "check-readiness", false, "Read the Ready nodes of each nodegroup from the Kubernetes API; NODES then shows ready/desired")
	cmdutil.AddWatchFlags(cmd, &o.watch, &o.interval)
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *listOptions) complete() error {
	var err error
	o.target, err = cmdutil.ResolveCluster(o.f, o.cluster, true)
	return err
}

// validate checks the watch flags, and a --kube-context that the
// kubeconfig does not have, before any AWS call.
func (o *listOptions) validate() error {
	if err := cmdutil.ValidateWatch(o.ios, o.output, o.watch, o.interval); err != nil {
		return err
	}
	if !o.checkReadiness {
		return nil
	}
	return kube.CheckContext(o.f.Global.Kubeconfig, o.f.Global.KubeContext)
}

// run reads the nodegroups once and prints them to ios.
func (o *listOptions) run(ctx context.Context, ios *cmdutil.IOStreams) error {
	ctx, cancel := cmdutil.WithTimeout(ctx, o.f.Global.Timeout)
	defer cancel()
	read, err := fleetcmd.FetchCluster(ctx, o.f, o.target, fleet.Need{AMI: true})
	if err != nil {
		return err
	}
	snap := read.Snapshot
	if o.checkReadiness {
		access, err := fleetcmd.ConnectKube(o.f.Kube, snap)
		if err != nil {
			return err
		}
		fleetcmd.ReportKube(ios, access)
		if access.Client != nil {
			read.Fetcher.FetchKube(ctx, access.Client, snap, fleet.KubeNeed{Ready: true})
		}
	}

	doc := apidoc.NodegroupList{
		Cluster:    snap.Cluster.Name,
		Region:     snap.Cluster.Region,
		Nodegroups: o.rows(snap),
		Failures:   snap.Failures,
	}
	apidoc.SortFailures(doc.Failures)
	return cmdutil.PrintResult(ctx, ios, o.output, doc, listView(doc), doc.Failures)
}

// rows returns the rows for the nodegroups of snap: filtered, then sorted.
func (o *listOptions) rows(snap *fleet.Snapshot) []apidoc.Nodegroup {
	selected := make([]fleet.Nodegroup, 0, len(snap.Nodegroups))
	for _, n := range snap.Nodegroups {
		if o.filters.Match(filterValues(n)) {
			selected = append(selected, n)
		}
	}
	slices.SortStableFunc(selected, func(a, b fleet.Nodegroup) int {
		c := compareBy(o.sortKey, a, b)
		if o.desc {
			c = -c
		}
		// Ties keep the name order in both directions.
		return cmp.Or(c, cmp.Compare(a.Name, b.Name))
	})
	rows := make([]apidoc.Nodegroup, len(selected))
	for i, n := range selected {
		rows[i] = snap.NodegroupDoc(n)
	}
	return rows
}

// compareBy orders two nodegroups by a sort key. A nodegroup with several
// instance types sorts by the first.
func compareBy(key string, a, b fleet.Nodegroup) int {
	switch key {
	case keyStatus:
		return cmp.Compare(a.Status, b.Status)
	case keyInstanceType:
		return slices.Compare(a.InstanceTypes, b.InstanceTypes)
	case keyNodes:
		return cmp.Compare(a.Desired, b.Desired)
	case keyVersion:
		return a.Version.Compare(b.Version)
	case keyAMIStatus:
		sa, _ := a.AMIStatus()
		sb, _ := b.AMIStatus()
		return cmp.Compare(sa, sb)
	}
	return cmp.Compare(a.Name, b.Name)
}

// listView returns the table and plain form of doc.
func listView(doc apidoc.NodegroupList) view.List[apidoc.Nodegroup] {
	return view.List[apidoc.Nodegroup]{
		Title:    "NODEGROUPS",
		Subtitle: doc.Cluster + ", " + strconv.Itoa(len(doc.Nodegroups)),
		Empty:    "No nodegroups found",
		Rows:     doc.Nodegroups,
		Failures: doc.Failures,
		Columns: []view.Column[apidoc.Nodegroup]{
			{Title: "NAME", MinWidth: 8, Value: func(n apidoc.Nodegroup) string { return n.Name }},
			{
				Title: "STATUS", Truncate: view.TruncateNever,
				Value:  func(n apidoc.Nodegroup) string { return view.OrUnknown(n.Incomplete, n.Status) },
				Status: func(n apidoc.Nodegroup) view.Status { return view.StatusOf(n.Status) },
			},
			{Title: "INSTANCE", MinWidth: 10, MaxWidth: 30, Value: func(n apidoc.Nodegroup) string {
				return view.OrUnknown(n.Incomplete, strings.Join(n.InstanceTypes, ","))
			}},
			{
				Title: "VERSION", Truncate: view.TruncateNever,
				Value:  func(n apidoc.Nodegroup) string { return view.OrUnknown(n.Incomplete, n.Version) },
				Status: view.BehindStatus,
			},
			{Title: "AMI", Truncate: view.TruncateNever, Value: amiText, Status: amiStatus},
			{Title: "NODES", Align: view.AlignRight, Truncate: view.TruncateNever, Value: view.NodesCell},
		},
	}
}
