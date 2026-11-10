// Package status implements "refresh status".
package status

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
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
	f   *cmdutil.Factory
	ios *cmdutil.IOStreams

	allRegions bool
	filters    cmdutil.Filters
	sortKey    string
	desc       bool
	output     cmdutil.Format
}

// NewCmdStatus returns the status command.
func NewCmdStatus(f *cmdutil.Factory) *cobra.Command {
	o := &options{f: f, ios: f.IOStreams}
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
             report them. A cluster in extended support shows what it
             costs per hour on top of standard support, such as
             +$0.50/hr, unless its upgrade policy is STANDARD.
  COMPUTE    the managed nodegroups, Auto Mode, Karpenter, or none
  STALE AMI  the nodegroups that do not run the latest AMI for their own
             Kubernetes version, of all nodegroups, with the age of the
             oldest such AMI; and the nodegroups on an older version than
             the control plane
  ADDONS     the add-ons with a newer compatible version
  HEALTH     the health issues that EKS reports for the control plane,
             the nodegroups, and the add-ons; an add-on that is DEGRADED
             with no issue listed counts as one

Under the table, the totals line gives the extended support premium for the
whole fleet per month, at 730 hours a month. The price is the one on the
EKS pricing page when refresh was released.

When several exit codes apply, the highest wins.

Exit codes:
  0  Every cluster is current and in standard support.
  1  An error, an interrupt, or no region answered.
  2  Something needs attention: an AMI, an add-on, or a nodegroup behind
     the control plane, or a health issue of the control plane, a
     nodegroup, or an add-on.
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
			if err := o.validate(); err != nil {
				return err
			}
			return o.run(cmd.Context())
		},
	}
	fleetcmd.AddAllRegionsFlag(cmd, &o.allRegions)
	cmdutil.AddFilterFlag(cmd, &o.filters, keyName, keyRegion, keyVersion, keySupport)
	cmdutil.AddSortFlags(cmd, &o.sortKey, &o.desc, keyName, keyRegion, keyVersion, keySupport, keyStale)
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

// need is what the command reads of each cluster.
var need = fleet.Need{AMI: true, AMIAge: true, LatestAddons: true, Support: true, Compute: true}

func (o *options) validate() error {
	return fleetcmd.ValidateSweepFlags(&o.f.Global, o.allRegions)
}

func (o *options) run(ctx context.Context) error {
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
	progress := fleetcmd.NewProgress(o.ios, o.output == cmdutil.FormatTable, len(regions.Names),
		func(snaps []*fleet.Snapshot, answered int) []string {
			width, _ := o.ios.Size()
			_, rows := o.document(&fleet.SweepResult{Snapshots: snaps})
			// The failures of the rows are listed once, under the final
			// table.
			return o.view(rows, nil, answered).Render(o.ios.Theme(), width)
		})
	fetcher := fleet.Fetcher{Now: o.f.Now}
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

	doc, rows := o.document(res)
	if err := cmdutil.PrintResult(ctx, o.ios, o.output, doc, o.view(rows, doc.Failures, len(res.Answered)), doc.Failures); err != nil {
		return err
	}
	return exitError(rows)
}

// row is one row of the table: a cluster as the document shows it, and
// the findings of the snapshot it comes from.
type row struct {
	apidoc.Cluster
	findings fleet.Findings
}

// document builds the document from the result of a sweep: the rows that
// the filters select, sorted, and every failure of the run. rows are the
// clusters of the document, in its order.
func (o *options) document(res *fleet.SweepResult) (apidoc.FleetStatus, []row) {
	late := o.filters.Only(keyVersion, keySupport)
	doc := apidoc.FleetStatus{Clusters: []apidoc.Cluster{}, Failures: slices.Clone(res.Failures)}
	if res.Regions.Default {
		doc.SkippedRegions = append([]string{}, res.Skipped...)
	}
	var rows []row
	for _, snap := range res.Snapshots {
		doc.Failures = append(doc.Failures, snap.Failures...)
		matches := late.Match(func(key string) []string {
			if key == keySupport {
				return []string{string(snap.Cluster.Support.Tier)}
			}
			return []string{snap.Cluster.Version.String()}
		})
		if matches {
			rows = append(rows, row{Cluster: snap.ClusterDoc(), findings: snap.Findings()})
		}
	}
	slices.SortStableFunc(rows, func(a, b row) int {
		c := compareBy(o.sortKey, a, b)
		if o.desc {
			c = -c
		}
		// Ties keep one order in both directions: name, then region.
		return cmp.Or(c, cmp.Compare(a.Name, b.Name), cmp.Compare(a.Region, b.Region))
	})
	for _, r := range rows {
		doc.Clusters = append(doc.Clusters, r.Cluster)
	}
	apidoc.SortFailures(doc.Failures)
	return doc, rows
}

// compareBy orders two clusters by a sort key.
func compareBy(key string, a, b row) int {
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
		return cmp.Compare(a.findings.Stale(), b.findings.Stale())
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

// exitError returns the error that sets the exit code of the run, after
// the document is printed: incomplete data (4), else the code of the fleet
// policy of health.FleetExit.
func exitError(rows []row) error {
	findings := make([]fleet.Findings, len(rows))
	for i, r := range rows {
		findings[i] = r.findings
	}
	if code := health.FleetExit(findings); code != cmdutil.ExitOK {
		return &cmdutil.ExitError{Code: code}
	}
	return nil
}

// overall returns the status of a row: the glyph before the cluster name.
func overall(r row) view.Status {
	return view.StandingStatus(r.findings.Standing())
}

// view returns the table and plain form of the rows. regions is the number
// of regions that answered.
func (o *options) view(rows []row, failures []apidoc.Failure, regions int) view.List[row] {
	subtitle := strconv.Itoa(len(rows))
	if regions > 1 {
		subtitle += " in " + strconv.Itoa(regions) + " regions"
	}
	now := time.Now()
	if o.f.Now != nil {
		now = o.f.Now()
	}
	// A value that was not read shows as unknown, not as empty.
	orUnknown := func(r row, value string) string { return view.OrUnknown(r.Incomplete, value) }
	return view.List[row]{
		Title:    "FLEET",
		Subtitle: subtitle,
		Empty:    "No clusters found",
		Rows:     rows,
		Notes:    notes(o.ios.Theme(), rows),
		Failures: failures,
		Columns: []view.Column[row]{
			{Title: "CLUSTER", MinWidth: 12, Value: func(r row) string { return r.Name }, Status: overall},
			{Title: "REGION", Truncate: view.TruncateNever, Value: func(r row) string { return r.Region }},
			{Title: "VERSION", Truncate: view.TruncateNever, Value: func(r row) string { return orUnknown(r, r.Version) }},
			{Title: "SUPPORT", Truncate: view.TruncateNever, Value: func(r row) string { return orUnknown(r, view.SupportCell(r.Support)) }},
			{Title: "COMPUTE", Truncate: view.TruncateNever, Value: func(r row) string { return orUnknown(r, computeText(r.Cluster)) }},
			{Title: "STALE AMI", MinWidth: 9, Value: func(r row) string { return orUnknown(r, staleText(r, now)) }},
			{Title: "ADDONS", MinWidth: 6, MaxWidth: 32, Value: func(r row) string { return orUnknown(r, addonsText(r)) }},
			{Title: "HEALTH", Truncate: view.TruncateNever, Value: func(r row) string { return orUnknown(r, healthText(r)) }},
		},
	}
}

// computeText says how a cluster runs its nodes, with the number of its
// managed nodegroups.
func computeText(c apidoc.Cluster) string {
	n := len(c.Nodegroups)
	switch c.Compute {
	case apidoc.ComputeManagedNodegroups:
		return view.Plural(n, "nodegroup")
	case apidoc.ComputeAutoMode:
		if n > 0 {
			return "Auto Mode, " + view.Plural(n, "nodegroup")
		}
		return "Auto Mode"
	case apidoc.ComputeKarpenter:
		return "Karpenter"
	case apidoc.ComputeNone:
		return "none"
	}
	// The compute type could not be found out, and that is not a failure.
	if c.Nodegroups != nil || c.Status != "" {
		return view.UnknownCell
	}
	return ""
}

// staleText returns the stale AMIs of a cluster: how many of the
// nodegroups, the age of the oldest, the nodegroups behind the control
// plane, and the nodegroups with no AMI to compare. A cluster with no managed nodegroup has none to be stale.
func staleText(r row, now time.Time) string {
	if r.Nodegroups == nil {
		return ""
	}
	f := r.findings
	if len(r.Nodegroups) == 0 {
		return "n/a"
	}
	if f.UnreadAMIs == len(r.Nodegroups) {
		return view.UnknownCell
	}
	var parts []string
	switch {
	case f.StaleAMIs > 0:
		text := strconv.Itoa(f.StaleAMIs) + "/" + strconv.Itoa(f.AMIs)
		if !f.OldestStaleAMI.IsZero() {
			text += " (" + strconv.Itoa(int(now.Sub(f.OldestStaleAMI).Hours()/24)) + "d)"
		}
		parts = append(parts, text)
	case f.UncomparedAMIs == 0 || f.UncomparedAMIs < f.AMIs:
		// "0" is for a cluster where an AMI was compared.
		parts = append(parts, "0")
	}
	if f.BehindNodegroups > 0 {
		parts = append(parts, strconv.Itoa(f.BehindNodegroups)+" behind CP")
	}
	if f.UnreadAMIs > 0 {
		parts = append(parts, strconv.Itoa(f.UnreadAMIs)+" unknown")
	}
	if f.UncomparedAMIs > 0 {
		parts = append(parts, strconv.Itoa(f.UncomparedAMIs)+" not compared")
	}
	return strings.Join(parts, ", ")
}

// addonsText returns the add-ons of a cluster that are behind: the count
// and the first names.
func addonsText(r row) string {
	if r.Addons == nil {
		return ""
	}
	unread, behind := r.findings.UnreadAddons, r.findings.BehindAddons
	switch {
	case unread > 0 && unread == len(r.Addons):
		return view.UnknownCell
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

// healthText returns the number of health issues of a cluster: of its
// control plane, its nodegroups, and its add-ons.
func healthText(r row) string {
	if n := r.findings.HealthIssues; n > 0 {
		return view.Plural(n, "issue")
	}
	if r.HealthIssues == nil {
		return ""
	}
	return "ok"
}

// notes returns the lines under the table: the clusters counted by what
// they need, the totals of what is out of date, and the command that
// looks closer at the most urgent cluster.
func notes(th view.Theme, clusters []row) []string {
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
	lines := []string{strings.Join(parts, "   ")}
	if t := totals(clusters); t != "" {
		lines = append(lines, th.Dim(t))
	}
	if h := hint(th, clusters); h != "" {
		lines = append(lines, "", h)
	}
	return lines
}

// hoursPerMonth is the hours of a month in the premium of the fleet
// totals: 365 days of 24 hours, over 12 months.
const hoursPerMonth = 730

// totals counts what is out of date in the whole fleet: stale AMIs,
// add-ons behind, nodegroups behind their control plane, and clusters out
// of standard support, with what extended support costs them a month on
// top of standard support. It is "" when nothing is.
func totals(clusters []row) string {
	var stale, addons, behind, support int
	var premium float64
	for _, c := range clusters {
		f := c.findings
		stale += f.StaleAMIs
		addons += len(f.BehindAddons)
		behind += f.BehindNodegroups
		if f.OutOfStandardSupport() {
			support++
		}
		if c.Support != nil {
			premium += c.Support.ExtendedSupportPremiumUSDPerHour * hoursPerMonth
		}
	}
	supportPart := view.Plural(support, "cluster") + " out of standard support"
	if premium > 0 {
		supportPart += fmt.Sprintf(" (+$%.0f/mo)", premium)
	}
	var parts []string
	for _, p := range []struct {
		n    int
		text string
	}{
		{stale, view.Plural(stale, "stale AMI")},
		{addons, view.Plural(addons, "add-on") + " behind"},
		{behind, view.Plural(behind, "nodegroup") + " behind the control plane"},
		{support, supportPart},
	} {
		if p.n > 0 {
			parts = append(parts, p.text)
		}
	}
	return strings.Join(parts, " · ")
}

// hint names the most urgent cluster, in table order, with the command
// that checks whether it is ready to upgrade: an unsupported cluster
// first, else the first one that needs attention. The command names the
// region of the cluster, which a sweep of several regions may not have
// as its default.
func hint(th view.Theme, clusters []row) string {
	i := slices.IndexFunc(clusters, func(c row) bool { return overall(c) == view.Fail })
	if i < 0 {
		i = slices.IndexFunc(clusters, func(c row) bool { return overall(c) == view.Warn })
	}
	if i < 0 {
		return ""
	}
	c := clusters[i]
	f := c.findings
	reason := "needs attention"
	switch {
	case overall(c) == view.Fail:
		reason = "is on a version that EKS no longer supports"
	case f.HealthIssues > 0:
		reason = "has " + view.Plural(f.HealthIssues, "health issue")
	case f.BehindNodegroups > 0:
		reason = "has " + view.Plural(f.BehindNodegroups, "nodegroup") + " behind the control plane"
	case f.OutOfStandardSupport():
		reason = "is out of standard support"
	case f.Stale() > 0:
		reason = "has stale AMIs or add-ons"
	}
	command := "refresh cluster upgrade-check " + c.Name
	if c.Region != "" {
		command += " -r " + c.Region
	}
	return th.Token(overall(c), c.Name) + " " + reason + ": " + th.Strong(command)
}
