package nodegroup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/kube"
	"github.com/dantech2000/refresh/internal/view"
)

// The keys of --filter and --sort. The first sort key is the default.
const (
	keyName         = "name"
	keyStatus       = "status"
	keyInstanceType = "instanceType"
	keyNodes        = "nodes"
	keyVersion      = "version"
	keyAMIStatus    = "amiStatus"
)

type listOptions struct {
	f *cmdutil.Factory

	cluster        string
	rawFilters     []string
	sortKey        string
	desc           bool
	checkReadiness bool
	watch          bool
	interval       time.Duration
	output         cmdutil.Format

	// filters and target are set by complete.
	filters cmdutil.Filters
	target  cmdutil.Target
}

func newCmdList(f *cmdutil.Factory) *cobra.Command {
	o := &listOptions{f: f}
	filterKeys := []string{keyName, keyStatus, keyInstanceType, keyAMIStatus, keyVersion}
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
listed under INCOMPLETE DATA, or in "failures" with -o json or -o yaml, and
the command exits 4.`,
		Example: `  refresh nodegroup list -c prod

  # The nodegroups with an outdated AMI
  refresh nodegroup list -c prod -f amiStatus=Outdated

  # The largest nodegroups first
  refresh nodegroup list -c prod --sort nodes --desc

  # The names of the outdated nodegroups for a script
  refresh nodegroup list -c prod -o json | jq -r '.nodegroups[] | select(.ami.status == "Outdated") | .name'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.complete(filterKeys); err != nil {
				return err
			}
			if err := o.validate(); err != nil {
				return err
			}
			if o.watch {
				return cmdutil.Watch(cmd.Context(), o.f.IOStreams, o.interval, o.f.After, o.run)
			}
			return o.run(cmd.Context())
		},
	}
	cmdutil.AddClusterFlag(cmd, &o.cluster)
	fleetcmd.RegisterClusterFlagCompletion(cmd, f)
	cmdutil.AddFilterFlag(cmd, &o.rawFilters, filterKeys...)
	cmdutil.AddSortFlags(cmd, &o.sortKey, &o.desc, keyName, keyStatus, keyInstanceType, keyNodes, keyVersion, keyAMIStatus)
	cmd.Flags().BoolVar(&o.checkReadiness, "check-readiness", false, "Read the Ready nodes of each nodegroup from the Kubernetes API; NODES then shows ready/desired")
	cmdutil.AddWatchFlags(cmd, &o.watch, &o.interval)
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *listOptions) complete(filterKeys []string) error {
	filters, err := cmdutil.ParseFilters(o.rawFilters, filterKeys...)
	if err != nil {
		return err
	}
	o.filters = filters
	o.target, err = cmdutil.ResolveCluster(o.f, o.cluster, true)
	return err
}

// validate checks the watch flags, and a --kube-context that the
// kubeconfig does not have, before any AWS call.
func (o *listOptions) validate() error {
	if err := cmdutil.ValidateWatch(o.f.IOStreams, o.output, o.watch, o.interval); err != nil {
		return err
	}
	if !o.checkReadiness {
		return nil
	}
	return kube.CheckContext(o.f.Global.Kubeconfig, o.f.Global.KubeContext)
}

func (o *listOptions) run(ctx context.Context) error {
	ios := o.f.IOStreams
	ctx, cancel := cmdutil.WithTimeout(ctx, o.f.Global.Timeout)
	defer cancel()
	clients, err := cmdutil.ClusterClients(ctx, o.f)
	if err != nil {
		return err
	}
	fetcher := fleet.Fetcher{Now: o.f.Now}
	snap, err := fetcher.Fetch(ctx, clients, o.target.Cluster, fleet.Need{AMI: true})
	if err != nil {
		return fleetcmd.ClusterError(ctx, &fetcher, clients, o.target.Cluster, err)
	}
	if o.checkReadiness {
		access, err := fleetcmd.ConnectKube(o.f.Kube, snap)
		if err != nil {
			return err
		}
		fleetcmd.ReportKube(ios, access)
		if access.Client != nil {
			fetcher.FetchKube(ctx, access.Client, snap, fleet.KubeNeed{Ready: true})
		}
	}

	doc := apidoc.NodegroupList{
		Cluster:    snap.Cluster.Name,
		Region:     snap.Cluster.Region,
		Nodegroups: o.rows(snap),
		Failures:   snap.Failures,
	}
	apidoc.SortFailures(doc.Failures)
	if err := cmdutil.Print(ios, o.output, doc, listView(doc)); err != nil {
		return err
	}
	if o.output != cmdutil.FormatTable {
		cmdutil.ReportFailures(ios, doc.Failures)
	}
	// An interrupt is not a partial result.
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("interrupted: %w", context.Canceled)
	}
	return cmdutil.IncompleteError(doc.Failures)
}

// rows returns the rows for the nodegroups of snap: filtered, then sorted.
func (o *listOptions) rows(snap *fleet.Snapshot) []apidoc.Nodegroup {
	selected := make([]fleet.Nodegroup, 0, len(snap.Nodegroups))
	for _, n := range snap.Nodegroups {
		matches := o.filters.Match(func(key string) []string {
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
		})
		if matches {
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
				Value:  func(n apidoc.Nodegroup) string { return orUnknown(n, n.Status) },
				Status: func(n apidoc.Nodegroup) view.Status { return view.StatusOf(n.Status) },
			},
			{Title: "INSTANCE", MinWidth: 10, MaxWidth: 30, Value: instanceTypes},
			{
				Title: "VERSION", Truncate: view.TruncateNever,
				Value:  func(n apidoc.Nodegroup) string { return orUnknown(n, n.Version) },
				Status: versionStatus,
			},
			{Title: "AMI", Truncate: view.TruncateNever, Value: amiText, Status: amiStatus},
			{Title: "NODES", Align: view.AlignRight, Truncate: view.TruncateNever, Value: nodes},
		},
	}
}
