package cluster

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fanout"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/view"
)

// The keys of --filter and --sort. The first sort key is the default.
const (
	keyName    = "name"
	keyRegion  = "region"
	keyStatus  = "status"
	keyVersion = "version"
)

type listOptions struct {
	f *cmdutil.Factory

	allRegions bool
	filters    cmdutil.Filters
	sortKey    string
	desc       bool
	showHealth bool
	watch      bool
	interval   time.Duration
	output     cmdutil.Format

	// health holds the results of --show-health for each cluster, by
	// region and name, and the failures of its checks.
	health map[string]clusterHealth
}

// clusterHealth is what --show-health found for one cluster.
type clusterHealth struct {
	doc      apidoc.Health
	failures []apidoc.Failure
}

// checkHealth runs the checks that need no Kubernetes access on every
// cluster of snaps that was read, with the clients of its region, all at
// once. The caller passes the clusters that the filters select.
func (o *listOptions) checkHealth(ctx context.Context, provider *awsx.Provider, snaps []*fleet.Snapshot) map[string]clusterHealth {
	results := fanout.ForEach(ctx, snaps, max(len(snaps), 1), func(ctx context.Context, snap *fleet.Snapshot) clusterHealth {
		if snap.Have.Cluster != fleet.Collected {
			return clusterHealth{}
		}
		rs := health.Run(ctx, health.AWSOnly(), &health.Input{Snapshot: snap, Clients: provider.Clients(snap.Cluster.Region), Now: snap.FetchedAt, NoKube: "cluster list connects to no cluster"})
		return clusterHealth{doc: health.Doc(rs), failures: health.Failures(rs)}
	})
	out := make(map[string]clusterHealth, len(snaps))
	for i, snap := range snaps {
		if snap.Have.Cluster == fleet.Collected {
			out[snap.Cluster.Region+"/"+snap.Cluster.Name] = results[i]
		}
	}
	return out
}

func newCmdList(f *cmdutil.Factory) *cobra.Command {
	o := &listOptions{f: f}
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the EKS clusters of one or more regions",
		Long: `List EKS clusters with their status and Kubernetes version.

Without a region flag the command reads the configured region. -r names one
or more regions, and -A reads every region: the list in REFRESH_EKS_REGIONS,
or else the regions that every account has enabled. In that last case a
region that these credentials cannot use is skipped with one note on stderr.
A region that you name is never skipped.

With -w the command reads again every --watch-interval and redraws the
table, until Ctrl+C. With --show-health the command runs the health checks
that need no Kubernetes access on every cluster and adds a HEALTH column;
the column does not change the exit code.

A cluster or a region that could not be read is listed under INCOMPLETE
DATA, or in "failures" with -o json or -o yaml.

Exit codes:
  0  Done, or a --watch that Ctrl+C stopped.
  1  An error, an interrupt, or no region answered.
  4  Incomplete: a cluster or a region could not be read. The command
     prints what it has.`,
		Example: `  # The clusters of the configured region
  refresh cluster list

  # Two regions, the newest version first
  refresh cluster list -r us-east-1 -r eu-west-1 --sort version --desc

  # Every region, only the production clusters that are being updated
  refresh cluster list -A -f name=prod-* -f status=UPDATING

  # Names and versions for a script
  refresh cluster list -o json | jq -r '.clusters[] | "\(.name) \(.version)"'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.validate(); err != nil {
				return err
			}
			if o.watch {
				return cmdutil.Watch(cmd.Context(), f.IOStreams, o.interval, f.After, o.run)
			}
			return o.run(cmd.Context(), f.IOStreams)
		},
	}
	fleetcmd.AddAllRegionsFlag(cmd, &o.allRegions)
	cmd.Flags().BoolVar(&o.showHealth, "show-health", false, "Run the health checks that need no Kubernetes access on every cluster, and add a HEALTH column")
	cmdutil.AddFilterFlag(cmd, &o.filters, keyName, keyRegion, keyStatus, keyVersion)
	cmdutil.AddSortFlags(cmd, &o.sortKey, &o.desc, keyName, keyRegion, keyStatus, keyVersion)
	cmdutil.AddWatchFlags(cmd, &o.watch, &o.interval)
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *listOptions) validate() error {
	if err := fleetcmd.ValidateSweepFlags(&o.f.Global, o.allRegions); err != nil {
		return err
	}
	return cmdutil.ValidateWatch(o.f.IOStreams, o.output, o.watch, o.interval)
}

// run reads the clusters once and prints them to ios.
func (o *listOptions) run(ctx context.Context, ios *cmdutil.IOStreams) error {
	ctx, cancel := cmdutil.WithTimeout(ctx, o.f.Global.Timeout)
	defer cancel()
	provider, err := o.f.AWS(ctx)
	if err != nil {
		return err
	}
	regions := fleetcmd.SweepRegions(&o.f.Global, provider.Region(), o.allRegions)

	// The name and the region are known before any describe call, so
	// those filters save the calls for the clusters they drop.
	early := o.filters.Only(keyName, keyRegion)
	// A watch redraws the whole screen for each read, so it shows no rows
	// before the read is done.
	progress := fleetcmd.NewProgress(ios, o.output == cmdutil.FormatTable && !o.watch, len(regions.Names),
		func(snaps []*fleet.Snapshot, answered int) []string {
			width, _ := ios.Size()
			return o.view(apidoc.ClusterList{Clusters: o.rows(snaps)}, answered).Render(ios.Theme(), width)
		})
	fetcher := fleet.Fetcher{Now: o.f.Now}
	var need fleet.Need
	if o.showHealth {
		need = fleet.Need{Nodegroups: true, InstanceIDs: true}
	}
	res := fetcher.Sweep(ctx, provider, regions, fleet.SweepOptions{
		Need: need,
		Match: func(region, name string) bool {
			return early.Match(func(key string) []string {
				if key == keyRegion {
					return []string{region}
				}
				return []string{name}
			})
		},
		OnRegion: progress.Add,
	})
	progress.Clear()
	if err := fleetcmd.SweepError(ctx, provider, res); err != nil {
		return err
	}
	fleetcmd.ReportSkippedRegions(ios, res.Skipped)
	if o.showHealth {
		o.health = o.checkHealth(ctx, provider, o.selected(res.Snapshots))
	}

	doc := o.document(res)
	return cmdutil.PrintResult(ctx, ios, o.output, doc, o.view(doc, len(res.Answered)), doc.Failures)
}

// document builds the document from the result of a sweep: the rows that
// the filters select, sorted, and every failure of the run.
func (o *listOptions) document(res *fleet.SweepResult) apidoc.ClusterList {
	doc := apidoc.ClusterList{
		Clusters: o.rows(res.Snapshots),
		Failures: slices.Clone(res.Failures),
	}
	for _, h := range o.health {
		doc.Failures = append(doc.Failures, h.failures...)
	}
	if res.Regions.Default {
		doc.SkippedRegions = append([]string{}, res.Skipped...)
	}
	for _, snap := range res.Snapshots {
		doc.Failures = append(doc.Failures, snap.Failures...)
	}
	apidoc.SortFailures(doc.Failures)
	return doc
}

// selected returns the snapshots that the filters on the status and the
// version keep. The filters on the name and the region ran in the sweep.
func (o *listOptions) selected(snaps []*fleet.Snapshot) []*fleet.Snapshot {
	late := o.filters.Only(keyStatus, keyVersion)
	selected := make([]*fleet.Snapshot, 0, len(snaps))
	for _, snap := range snaps {
		matches := late.Match(func(key string) []string {
			if key == keyStatus {
				return []string{snap.Cluster.Status}
			}
			return []string{snap.Cluster.Version.String()}
		})
		if matches {
			selected = append(selected, snap)
		}
	}
	return selected
}

// rows returns the rows for snaps: filtered, then sorted.
func (o *listOptions) rows(snaps []*fleet.Snapshot) []apidoc.Cluster {
	selected := o.selected(snaps)
	slices.SortStableFunc(selected, func(a, b *fleet.Snapshot) int {
		c := compareBy(o.sortKey, a.Cluster, b.Cluster)
		if o.desc {
			c = -c
		}
		// Ties keep one order in both directions: name, then region.
		return cmp.Or(c, cmp.Compare(a.Cluster.Name, b.Cluster.Name), cmp.Compare(a.Cluster.Region, b.Cluster.Region))
	})
	rows := make([]apidoc.Cluster, len(selected))
	for i, snap := range selected {
		rows[i] = snap.ClusterDoc()
		if h, ok := o.health[snap.Cluster.Region+"/"+snap.Cluster.Name]; ok {
			doc := h.doc
			rows[i].Health = &doc
			rows[i].Incomplete = rows[i].Incomplete || len(h.failures) > 0
		}
	}
	return rows
}

// compareBy orders two clusters by a sort key. Versions compare as
// numbers, so 1.9 is older than 1.10.
func compareBy(key string, a, b fleet.Cluster) int {
	switch key {
	case keyRegion:
		return cmp.Compare(a.Region, b.Region)
	case keyStatus:
		return cmp.Compare(a.Status, b.Status)
	case keyVersion:
		return a.Version.Compare(b.Version)
	}
	return cmp.Compare(a.Name, b.Name)
}

// view returns the table and plain form of doc. regions is the number of
// regions that answered.
func (o *listOptions) view(doc apidoc.ClusterList, regions int) view.List[apidoc.Cluster] {
	subtitle := strconv.Itoa(len(doc.Clusters))
	if regions > 1 {
		subtitle += " in " + strconv.Itoa(regions) + " regions"
	}
	// A value that was not read shows as unknown, not as empty.
	orUnknown := func(c apidoc.Cluster, value string) string { return view.OrUnknown(c.Incomplete, value) }
	l := view.List[apidoc.Cluster]{
		Title:    "CLUSTERS",
		Subtitle: subtitle,
		Empty:    "No clusters found",
		Rows:     doc.Clusters,
		Failures: doc.Failures,
		Columns: []view.Column[apidoc.Cluster]{
			{Title: "CLUSTER", MinWidth: 12, Value: func(c apidoc.Cluster) string { return c.Name }},
			{Title: "REGION", MinWidth: 9, Truncate: view.TruncateNever, Value: func(c apidoc.Cluster) string { return c.Region }},
			{
				Title: "STATUS", MinWidth: 8, Truncate: view.TruncateNever,
				Value:  func(c apidoc.Cluster) string { return orUnknown(c, c.Status) },
				Status: func(c apidoc.Cluster) view.Status { return view.StatusOf(c.Status) },
			},
			{Title: "VERSION", Truncate: view.TruncateNever, Value: func(c apidoc.Cluster) string { return orUnknown(c, c.Version) }},
		},
	}
	if o.showHealth {
		l.Columns = append(l.Columns, view.Column[apidoc.Cluster]{
			Title: "HEALTH", Truncate: view.TruncateNever,
			Value: func(c apidoc.Cluster) string {
				if c.Health == nil {
					return orUnknown(c, "")
				}
				return string(c.Health.Summary)
			},
			Status: func(c apidoc.Cluster) view.Status {
				if c.Health == nil {
					return view.NoStatus
				}
				return view.HealthStatus(c.Health.Summary)
			},
		})
	}
	return l
}
