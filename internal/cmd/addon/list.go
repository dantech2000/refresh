package addon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/view"
)

type listOptions struct {
	ios    *cmdutil.IOStreams
	global *cmdutil.GlobalOptions
	// now returns the time that a snapshot records.
	now func() time.Time
	// resolve returns the cluster of the run, from the flag or the
	// defaults, and clients the AWS clients of its region.
	resolve func(cluster string) (cmdutil.Target, error)
	clients func(ctx context.Context) (*awsx.Clients, error)
	// after is the pause between two reads of a watch.
	after func(time.Duration) <-chan time.Time

	cluster  string
	watch    bool
	interval time.Duration
	output   cmdutil.Format

	// target is the cluster, resolved by complete.
	target cmdutil.Target
}

func newCmdList(f *cmdutil.Factory) *cobra.Command {
	o := &listOptions{
		ios: f.IOStreams, global: &f.Global, now: f.Now,
		resolve: func(cluster string) (cmdutil.Target, error) { return cmdutil.ResolveCluster(f, cluster, true) },
		clients: func(ctx context.Context) (*awsx.Clients, error) { return cmdutil.ClusterClients(ctx, f) },
		after:   f.After,
	}
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the add-ons of a cluster",
		Long: `List the EKS add-ons of a cluster with their version, status, and health.

The health is the status as one of four values: Healthy, Failed, InProgress,
or Unknown. A DEGRADED add-on is Failed.

An add-on that could not be read is listed under INCOMPLETE DATA, or in
"failures" with -o json or -o yaml, and the command exits 4.`,
		Example: `  # The add-ons of a cluster
  refresh addon list -c prod

  # The add-ons of the cluster of the active context
  refresh addon list

  # The names and versions for a script
  refresh addon list -c prod -o json | jq -r '.addons[] | "\(.name) \(.version)"'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.complete(); err != nil {
				return err
			}
			if err := o.validate(); err != nil {
				return err
			}
			if o.watch {
				return cmdutil.Watch(cmd.Context(), o.ios, o.interval, o.after, o.run)
			}
			return o.run(cmd.Context())
		},
	}
	cmdutil.AddClusterFlag(cmd, &o.cluster)
	fleetcmd.RegisterClusterFlagCompletion(cmd, f)
	cmdutil.AddWatchFlags(cmd, &o.watch, &o.interval)
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *listOptions) complete() error {
	target, err := o.resolve(o.cluster)
	o.target = target
	return err
}

func (o *listOptions) validate() error {
	return cmdutil.ValidateWatch(o.ios, o.output, o.watch, o.interval)
}

func (o *listOptions) run(ctx context.Context) error {
	ios := o.ios
	ctx, cancel := cmdutil.WithTimeout(ctx, o.global.Timeout)
	defer cancel()
	clients, err := o.clients(ctx)
	if err != nil {
		return err
	}
	fetcher := fleet.Fetcher{Now: o.now}
	snap, err := fetcher.Fetch(ctx, clients, o.target.Cluster, fleet.Need{Addons: true})
	if err != nil {
		return fleetcmd.ClusterError(ctx, &fetcher, clients, o.target.Cluster, err)
	}

	cluster := snap.ClusterDoc()
	doc := apidoc.AddonList{
		Cluster:  cluster.Name,
		Region:   cluster.Region,
		Addons:   cluster.Addons,
		Failures: snap.Failures,
	}
	if doc.Addons == nil {
		doc.Addons = []apidoc.Addon{}
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

// listView returns the table and plain form of doc.
func listView(doc apidoc.AddonList) view.List[apidoc.Addon] {
	return view.List[apidoc.Addon]{
		Title:    "ADD-ONS",
		Subtitle: doc.Cluster + ", " + strconv.Itoa(len(doc.Addons)),
		Empty:    "No add-ons found",
		Rows:     doc.Addons,
		Failures: doc.Failures,
		Columns: []view.Column[apidoc.Addon]{
			{Title: "NAME", MinWidth: 8, Value: func(a apidoc.Addon) string { return a.Name }},
			{Title: "VERSION", MinWidth: 8, Value: func(a apidoc.Addon) string { return orUnknown(a, a.Version) }},
			{
				Title: "STATUS", Truncate: view.TruncateNever,
				Value:  func(a apidoc.Addon) string { return orUnknown(a, a.Status) },
				Status: func(a apidoc.Addon) view.Status { return view.StatusOf(a.Status) },
			},
			{
				Title: "HEALTH", Truncate: view.TruncateNever,
				Value:  func(a apidoc.Addon) string { return orUnknown(a, string(a.Health)) },
				Status: func(a apidoc.Addon) view.Status { return healthStatus(a.Health) },
			},
		},
	}
}
