// Package status implements "refresh status".
package status

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

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
	keyVersion = "version"
	keySupport = "support"
	keyStale   = "stale"
)

type options struct {
	ios    *cmdutil.IOStreams
	aws    func(ctx context.Context) (*awsx.Provider, error)
	global *cmdutil.GlobalOptions
	// now returns the time for the age of a stale AMI. Nil means
	// time.Now.
	now func() time.Time

	allRegions bool
	rawFilters []string
	sortKey    string
	desc       bool
	output     cmdutil.Format

	// filters is rawFilters, parsed by complete.
	filters cmdutil.Filters
}

// NewCmdStatus returns the status command.
func NewCmdStatus(f *cmdutil.Factory) *cobra.Command {
	o := &options{ios: f.IOStreams, aws: f.AWS, global: &f.Global, now: f.Now}
	filterKeys := []string{keyName, keyRegion, keyVersion, keySupport}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what is out of date in the fleet",
		Long: `Show every cluster with what is out of date: the Kubernetes version and its
EKS support window, the nodegroups whose AMI is not the latest, and the
add-ons that have a newer version.

Without a region flag the command reads the configured region. -r names one
or more regions, and -A reads every region: the list in REFRESH_EKS_REGIONS,
or else the regions that every account has enabled.

Columns:
  SUPPORT    standard, extended, or unsupported, with the days left. A "*"
             marks dates that refresh ships with, because EKS did not
             report them.
  COMPUTE    the managed nodegroups, Auto Mode, Karpenter, or none
  STALE AMI  the nodegroups that do not run the latest AMI for their own
             Kubernetes version, of all nodegroups, with the age of the
             oldest such AMI; and the nodegroups on an older version than
             the control plane
  ADDONS     the add-ons with a newer compatible version
  HEALTH     the control-plane issues that EKS reports

When several exit codes apply, the highest wins.

Exit codes:
  0  Every cluster is current and in standard support.
  1  An error, an interrupt, or no region answered.
  2  Something is out of date: an AMI, an add-on, a nodegroup behind the
     control plane, or a control-plane issue.
  3  A cluster is in extended support, or unsupported.
  4  Incomplete: a part of the fleet could not be read.`,
		Example: `  # The configured region
  refresh status

  # Every region, the clusters with the most to do first
  refresh status -A --sort stale --desc

  # The clusters that left standard support
  refresh status -A -f support=extended

  # The names of the clusters with a stale AMI, for a script
  refresh status -o json | jq -r '.clusters[] | select(any(.nodegroups[]?; .ami.status == "Outdated")) | .name'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.complete(filterKeys); err != nil {
				return err
			}
			if err := o.validate(); err != nil {
				return err
			}
			return o.run(cmd.Context())
		},
	}
	fleetcmd.AddAllRegionsFlag(cmd, &o.allRegions)
	cmdutil.AddFilterFlag(cmd, &o.rawFilters, filterKeys...)
	cmdutil.AddSortFlags(cmd, &o.sortKey, &o.desc, keyName, keyRegion, keyVersion, keySupport, keyStale)
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *options) complete(filterKeys []string) error {
	filters, err := cmdutil.ParseFilters(o.rawFilters, filterKeys...)
	o.filters = filters
	return err
}

// need is what the command reads of each cluster.
var need = fleet.Need{AMI: true, AMIAge: true, LatestAddons: true, Support: true, Compute: true}

func (o *options) validate() error {
	return fleetcmd.ValidateSweepFlags(o.global, o.allRegions)
}

func (o *options) run(ctx context.Context) error {
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
	progress := fleetcmd.NewProgress(o.ios, o.output == cmdutil.FormatTable, len(regions.Names),
		func(snaps []*fleet.Snapshot, answered int) []string {
			width, _ := o.ios.Size()
			partial := o.document(&fleet.SweepResult{Snapshots: snaps})
			// The failures of the rows are listed once, under the final
			// table.
			partial.Failures = nil
			return o.view(partial, answered).Render(o.ios.Theme(), width)
		})
	fetcher := fleet.Fetcher{Now: o.now}
	res := fetcher.Sweep(ctx, provider, regions, fleet.SweepOptions{
		Need:     need,
		OnRegion: progress.Add,
		Match: func(region, name string) bool {
			return early.Match(func(key string) []string {
				if key == keyRegion {
					return []string{region}
				}
				return []string{name}
			})
		},
	})
	progress.Clear()
	if err := fleetcmd.SweepError(ctx, provider, res); err != nil {
		return err
	}
	fleetcmd.ReportSkippedRegions(o.ios, res.Skipped)

	doc := o.document(res)
	if err := cmdutil.Print(o.ios, o.output, doc, o.view(doc, len(res.Answered))); err != nil {
		return err
	}
	if o.output != cmdutil.FormatTable {
		cmdutil.ReportFailures(o.ios, doc.Failures)
	}
	// An interrupt is not a partial result.
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("interrupted: %w", context.Canceled)
	}
	return exitError(doc)
}

// document builds the document from the result of a sweep: the rows that
// the filters select, sorted, and every failure of the run.
func (o *options) document(res *fleet.SweepResult) apidoc.FleetStatus {
	late := o.filters.Only(keyVersion, keySupport)
	doc := apidoc.FleetStatus{Clusters: []apidoc.Cluster{}, Failures: slices.Clone(res.Failures)}
	if res.Regions.Default {
		doc.SkippedRegions = append([]string{}, res.Skipped...)
	}
	for _, snap := range res.Snapshots {
		doc.Failures = append(doc.Failures, snap.Failures...)
		matches := late.Match(func(key string) []string {
			if key == keySupport {
				return []string{string(snap.Cluster.Support.Tier)}
			}
			return []string{snap.Cluster.Version.String()}
		})
		if matches {
			doc.Clusters = append(doc.Clusters, snap.ClusterDoc())
		}
	}
	slices.SortStableFunc(doc.Clusters, func(a, b apidoc.Cluster) int {
		c := compareBy(o.sortKey, a, b)
		if o.desc {
			c = -c
		}
		// Ties keep one order in both directions: name, then region.
		return cmp.Or(c, cmp.Compare(a.Name, b.Name), cmp.Compare(a.Region, b.Region))
	})
	apidoc.SortFailures(doc.Failures)
	return doc
}

// compareBy orders two clusters by a sort key.
func compareBy(key string, a, b apidoc.Cluster) int {
	switch key {
	case keyRegion:
		return cmp.Compare(a.Region, b.Region)
	case keyVersion:
		// As numbers: 1.9 is older than 1.10. A version that was not
		// read is the zero value, which is older than every version.
		va, _ := fleet.ParseVersion(a.Version)
		vb, _ := fleet.ParseVersion(b.Version)
		return va.Compare(vb)
	case keySupport:
		return cmp.Compare(supportRank(a.Support), supportRank(b.Support))
	case keyStale:
		return cmp.Compare(findingsOf(a).total(), findingsOf(b).total())
	}
	return cmp.Compare(a.Name, b.Name)
}

// supportRank orders the support tiers from the least to the most urgent.
func supportRank(s *apidoc.Support) int {
	if s == nil {
		return 1
	}
	switch s.Tier {
	case apidoc.SupportStandard:
		return 0
	case apidoc.SupportExtended:
		return 2
	case apidoc.SupportUnsupported:
		return 3
	}
	return 1
}

// findings counts what is out of date in one cluster.
type findings struct {
	nodegroups       int       // the nodegroups whose AMI was compared
	staleAMIs        int       // the nodegroups with an outdated AMI
	oldestAMI        time.Time // when the oldest outdated AMI was created
	behindNodegroups int       // the nodegroups on an older version than the control plane
	behindAddons     []string  // the add-ons with a newer compatible version
	issues           int       // the control-plane issues that EKS reports
}

func findingsOf(c apidoc.Cluster) findings {
	f := findings{issues: len(c.HealthIssues)}
	for _, n := range c.Nodegroups {
		if n.VersionBehind != nil && *n.VersionBehind {
			f.behindNodegroups++
		}
		if n.AMI == nil {
			continue
		}
		f.nodegroups++
		if n.AMI.Status == apidoc.AMIOutdated {
			f.staleAMIs++
			if t := n.AMI.CurrentCreatedAt; !t.IsZero() && (f.oldestAMI.IsZero() || t.Before(f.oldestAMI)) {
				f.oldestAMI = t
			}
		}
	}
	for _, a := range c.Addons {
		if a.Latest != nil && a.Latest.Behind != nil && *a.Latest.Behind {
			f.behindAddons = append(f.behindAddons, a.Name)
		}
	}
	return f
}

// total is the number of things to bring up to date, for --sort stale.
func (f findings) total() int {
	return f.staleAMIs + f.behindNodegroups + len(f.behindAddons)
}

// attention reports whether something in the cluster is out of date.
func (f findings) attention() bool {
	return f.total() > 0 || f.issues > 0
}

// supportRisk reports whether the cluster left standard support.
func supportRisk(c apidoc.Cluster) bool {
	return c.Support != nil && (c.Support.Tier == apidoc.SupportExtended || c.Support.Tier == apidoc.SupportUnsupported)
}

// exitError returns the error that sets the exit code of the run, after
// the document is printed. The highest code wins: incomplete data (4), a
// cluster out of standard support (3), something out of date (2).
func exitError(doc apidoc.FleetStatus) error {
	if err := cmdutil.IncompleteError(doc.Failures); err != nil {
		return err
	}
	stale := false
	for _, c := range doc.Clusters {
		if supportRisk(c) {
			return &cmdutil.ExitError{Code: cmdutil.ExitBlocked}
		}
		stale = stale || findingsOf(c).attention()
	}
	if stale {
		return &cmdutil.ExitError{Code: cmdutil.ExitNeedsAttention}
	}
	return nil
}

// overall returns the status of a row: the glyph before the cluster name.
func overall(c apidoc.Cluster) view.Status {
	switch {
	case c.Support != nil && c.Support.Tier == apidoc.SupportUnsupported:
		return view.Fail
	case supportRisk(c) || findingsOf(c).attention():
		return view.Warn
	case c.Incomplete:
		return view.Unknown
	}
	return view.Healthy
}

// unknownCell is the text of a cell whose data could not be read.
const unknownCell = "unknown"

// view returns the table and plain form of doc. regions is the number of
// regions that answered.
func (o *options) view(doc apidoc.FleetStatus, regions int) view.List[apidoc.Cluster] {
	subtitle := strconv.Itoa(len(doc.Clusters))
	if regions > 1 {
		subtitle += " in " + strconv.Itoa(regions) + " regions"
	}
	now := time.Now()
	if o.now != nil {
		now = o.now()
	}
	// A value that was not read shows as unknown, not as empty.
	orUnknown := func(c apidoc.Cluster, value string) string {
		if value == "" && c.Incomplete {
			return unknownCell
		}
		return value
	}
	return view.List[apidoc.Cluster]{
		Title:    "FLEET",
		Subtitle: subtitle,
		Empty:    "No clusters found",
		Rows:     doc.Clusters,
		Notes:    notes(o.ios.Theme(), doc.Clusters),
		Failures: doc.Failures,
		Columns: []view.Column[apidoc.Cluster]{
			{Title: "CLUSTER", MinWidth: 12, Value: func(c apidoc.Cluster) string { return c.Name }, Status: overall},
			{Title: "REGION", Truncate: view.TruncateNever, Value: func(c apidoc.Cluster) string { return c.Region }},
			{Title: "VERSION", Truncate: view.TruncateNever, Value: func(c apidoc.Cluster) string { return orUnknown(c, c.Version) }},
			{Title: "SUPPORT", Truncate: view.TruncateNever, Value: func(c apidoc.Cluster) string { return orUnknown(c, supportText(c.Support)) }},
			{Title: "COMPUTE", Truncate: view.TruncateNever, Value: func(c apidoc.Cluster) string { return orUnknown(c, computeText(c)) }},
			{Title: "STALE AMI", MinWidth: 9, Value: func(c apidoc.Cluster) string { return orUnknown(c, staleText(c, now)) }},
			{Title: "ADDONS", MinWidth: 6, MaxWidth: 32, Value: func(c apidoc.Cluster) string { return orUnknown(c, addonsText(c)) }},
			{Title: "HEALTH", Truncate: view.TruncateNever, Value: func(c apidoc.Cluster) string { return orUnknown(c, healthText(c)) }},
		},
	}
}

// computeText says how a cluster runs its nodes, with the number of its
// managed nodegroups.
func computeText(c apidoc.Cluster) string {
	n := len(c.Nodegroups)
	switch c.Compute {
	case apidoc.ComputeManagedNodegroups:
		return plural(n, "nodegroup")
	case apidoc.ComputeAutoMode:
		if n > 0 {
			return "Auto Mode, " + plural(n, "nodegroup")
		}
		return "Auto Mode"
	case apidoc.ComputeKarpenter:
		return "Karpenter"
	case apidoc.ComputeNone:
		return "none"
	}
	// The compute type could not be found out, and that is not a failure.
	if c.Nodegroups != nil || c.Status != "" {
		return unknownCell
	}
	return ""
}

// staleText returns the stale AMIs of a cluster: how many of the
// nodegroups, the age of the oldest, the nodegroups behind the control
// plane, and the nodegroups with no AMI to compare. A cluster with no managed nodegroup has none to be stale.
func staleText(c apidoc.Cluster, now time.Time) string {
	if c.Nodegroups == nil {
		return ""
	}
	f := findingsOf(c)
	if len(c.Nodegroups) == 0 {
		return "n/a"
	}
	// The nodegroups whose AMI was not read, and those with nothing to
	// compare: no instance, or an AMI type with no recommended AMI.
	unread, notCompared := 0, 0
	for _, n := range c.Nodegroups {
		switch {
		case n.AMI == nil && n.Incomplete:
			unread++
		case n.AMI != nil && n.AMI.Status == apidoc.AMIUnknown:
			notCompared++
		}
	}
	if unread == len(c.Nodegroups) {
		return unknownCell
	}
	var parts []string
	switch {
	case f.staleAMIs > 0:
		text := strconv.Itoa(f.staleAMIs) + "/" + strconv.Itoa(f.nodegroups)
		if !f.oldestAMI.IsZero() {
			text += " (" + strconv.Itoa(int(now.Sub(f.oldestAMI).Hours()/24)) + "d)"
		}
		parts = append(parts, text)
	case notCompared == 0 || notCompared < f.nodegroups:
		// "0" is for a cluster where an AMI was compared.
		parts = append(parts, "0")
	}
	if f.behindNodegroups > 0 {
		parts = append(parts, strconv.Itoa(f.behindNodegroups)+" behind CP")
	}
	if unread > 0 {
		parts = append(parts, strconv.Itoa(unread)+" unknown")
	}
	if notCompared > 0 {
		parts = append(parts, strconv.Itoa(notCompared)+" not compared")
	}
	return strings.Join(parts, ", ")
}

// addonsText returns the add-ons of a cluster that are behind: the count
// and the first names.
func addonsText(c apidoc.Cluster) string {
	if c.Addons == nil {
		return ""
	}
	// The add-ons whose newest version could not be read.
	unread := 0
	for _, a := range c.Addons {
		if a.Latest == nil && a.Incomplete {
			unread++
		}
	}
	behind := findingsOf(c).behindAddons
	switch {
	case unread > 0 && unread == len(c.Addons):
		return unknownCell
	case unread > 0:
		return strconv.Itoa(len(behind)) + ", " + strconv.Itoa(unread) + " unknown"
	case len(behind) == 0:
		return "0"
	}
	const most = 2
	names := behind
	more := ""
	if len(names) > most {
		names, more = names[:most], " +"+strconv.Itoa(len(behind)-most)
	}
	return strconv.Itoa(len(behind)) + " (" + strings.Join(names, ",") + more + ")"
}

// healthText returns the number of control-plane issues of a cluster.
func healthText(c apidoc.Cluster) string {
	if c.HealthIssues == nil {
		return ""
	}
	if n := len(c.HealthIssues); n > 0 {
		return plural(n, "issue")
	}
	return "ok"
}

// notes returns the line under the table that counts the clusters by what
// they need.
func notes(th view.Theme, clusters []apidoc.Cluster) []string {
	if len(clusters) == 0 {
		return nil
	}
	counts := map[view.Status]int{}
	for _, c := range clusters {
		counts[overall(c)]++
		// A row with something that was not read is incomplete, whatever
		// else it shows: a row that needs attention can be both.
		if c.Incomplete && overall(c) != view.Unknown {
			counts[view.Unknown]++
		}
	}
	parts := []string{th.Token(view.Healthy, strconv.Itoa(counts[view.Healthy])+" current")}
	for _, p := range []struct {
		status view.Status
		label  string
	}{{view.Warn, "need attention"}, {view.Fail, "unsupported"}, {view.Unknown, "incomplete"}} {
		if n := counts[p.status]; n > 0 {
			parts = append(parts, th.Token(p.status, strconv.Itoa(n)+" "+p.label))
		}
	}
	return []string{strings.Join(parts, "   ")}
}

// plural returns n and noun, with an "s" unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// supportText returns the support of a cluster as a short cell: the tier,
// and the days left while a window is open, such as "standard (61d)". A
// "*" marks dates that refresh ships with, because EKS did not report
// them. It returns "" when the support was not read.
func supportText(s *apidoc.Support) string {
	if s == nil {
		return ""
	}
	text := strings.ToLower(string(s.Tier))
	if s.DaysRemaining > 0 {
		text += " (" + strconv.Itoa(s.DaysRemaining) + "d)"
	}
	if s.BuiltIn {
		text += "*"
	}
	return text
}
