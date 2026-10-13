package cluster

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
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
	io     *cmdutil.IOStreams
	aws    func(ctx context.Context) (*awsx.Provider, error)
	global *cmdutil.GlobalOptions

	allRegions bool
	rawFilters []string
	sortKey    string
	desc       bool
	output     cmdutil.Format

	// filters is rawFilters, parsed by complete.
	filters cmdutil.Filters
}

func newCmdList(f *cmdutil.Factory) *cobra.Command {
	o := &listOptions{io: f.IOStreams, aws: f.AWS, global: &f.Global}
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

A cluster or a region that could not be read is listed under INCOMPLETE
DATA, or in "failures" with -o json or -o yaml, and the command exits 4.
When no region answers, the command fails with exit 1.`,
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
			if err := o.complete(); err != nil {
				return err
			}
			if err := o.validate(); err != nil {
				return err
			}
			return o.run(cmd.Context())
		},
	}
	fleetcmd.AddAllRegionsFlag(cmd, &o.allRegions)
	cmdutil.AddFilterFlag(cmd, &o.rawFilters, keyName, keyRegion, keyStatus, keyVersion)
	cmdutil.AddSortFlags(cmd, &o.sortKey, &o.desc, keyName, keyRegion, keyStatus, keyVersion)
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *listOptions) complete() error {
	filters, err := cmdutil.ParseFilters(o.rawFilters, keyName, keyRegion, keyStatus, keyVersion)
	o.filters = filters
	return err
}

func (o *listOptions) validate() error {
	return fleetcmd.ValidateSweepFlags(o.global, o.allRegions)
}

func (o *listOptions) run(ctx context.Context) error {
	ctx, cancel := cmdutil.WithTimeout(ctx, o.global.Timeout)
	defer cancel()
	provider, err := o.aws(ctx)
	if err != nil {
		return err
	}
	regions := fleetcmd.SweepRegions(o.global, provider.Region(), o.allRegions)

	// The name and the region are known before any describe call, so
	// those filters save the calls for the clusters they drop.
	early := o.filters.Only(keyName, keyRegion)
	progress := o.newProgress(len(regions.Names))
	var fetcher fleet.Fetcher
	res := fetcher.Sweep(ctx, provider, regions, fleet.SweepOptions{
		Match: func(region, name string) bool {
			return early.Match(func(key string) []string {
				if key == keyRegion {
					return []string{region}
				}
				return []string{name}
			})
		},
		OnRegion: progress.add,
	})
	progress.clear()
	if err := fleetcmd.SweepError(ctx, provider, res); err != nil {
		return err
	}
	fleetcmd.ReportSkippedRegions(o.io, res.Skipped)

	doc := o.document(res)
	if err := cmdutil.Print(o.io, o.output, doc, o.view(doc, len(res.Answered))); err != nil {
		return err
	}
	if o.output != cmdutil.FormatTable {
		cmdutil.ReportFailures(o.io, doc.Failures)
	}
	// An interrupt is not a partial result: the document above shows what
	// was read, and the run fails.
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("interrupted: %w", context.Canceled)
	}
	return cmdutil.IncompleteError(doc.Failures)
}

// document builds the document from the result of a sweep: the rows that
// the filters select, sorted, and every failure of the run.
func (o *listOptions) document(res *fleet.SweepResult) apidoc.ClusterList {
	doc := apidoc.ClusterList{
		Clusters: o.rows(res.Snapshots),
		Failures: slices.Clone(res.Failures),
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

// rows returns the rows for snaps: filtered, then sorted.
func (o *listOptions) rows(snaps []*fleet.Snapshot) []apidoc.Cluster {
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

// unknownCell is the text of a cell whose data could not be read.
const unknownCell = "unknown"

// view returns the table and plain form of doc. regions is the number of
// regions that answered.
func (o *listOptions) view(doc apidoc.ClusterList, regions int) view.List[apidoc.Cluster] {
	subtitle := strconv.Itoa(len(doc.Clusters))
	if regions > 1 {
		subtitle += " in " + strconv.Itoa(regions) + " regions"
	}
	// A value that was not read shows as unknown, not as empty.
	orUnknown := func(c apidoc.Cluster, value string) string {
		if value == "" && c.Incomplete {
			return unknownCell
		}
		return value
	}
	return view.List[apidoc.Cluster]{
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
}

// progress shows the rows of a sweep as its regions answer. It draws only
// for a table on a terminal; every other output prints once, at the end.
type progress struct {
	o       *listOptions
	regions int // the regions of the sweep
	table   *view.Live
	status  *view.Live

	mu       sync.Mutex
	answered int
	snaps    []*fleet.Snapshot
}

func (o *listOptions) newProgress(regions int) *progress {
	p := &progress{o: o, regions: regions}
	if o.output != cmdutil.FormatTable || !o.io.OutTTY {
		return p
	}
	p.table = view.NewLive(o.io.Out, o.io.Size)
	if o.io.ErrTTY {
		// The terminal behind stderr is the one behind stdout here.
		p.status = view.NewLive(o.io.ErrOut, o.io.Size)
	}
	return p
}

// add takes the snapshots of one region and redraws the table with every
// row so far, in the final order. A table taller than the terminal cannot
// be redrawn in place: one status line on stderr stands in for it.
func (p *progress) add(_ string, snaps []*fleet.Snapshot) {
	if p.table == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answered++
	p.snaps = append(p.snaps, snaps...)
	width, _ := p.o.io.Size()
	doc := apidoc.ClusterList{Clusters: p.o.rows(p.snaps)}
	if p.table.Draw(p.o.view(doc, p.answered).Render(p.o.io.Theme(), width)) || p.status == nil {
		return
	}
	p.status.Draw([]string{p.o.io.ErrTheme().Dim(fmt.Sprintf("%d of %d regions, %d clusters", p.answered, p.regions, len(p.snaps)))})
}

// clear removes what add drew, so that the final table prints once.
func (p *progress) clear() {
	if p.table != nil {
		p.table.Clear()
	}
	if p.status != nil {
		p.status.Clear()
	}
}
