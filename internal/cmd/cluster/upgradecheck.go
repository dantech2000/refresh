package cluster

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/view"
)

// The insight categories and statuses that the flags accept.
var (
	categories      = []string{health.CategoryUpgradeReadiness, string(ekstypes.CategoryMisconfiguration), string(ekstypes.CategoryRollbackReadiness)}
	insightStatuses = []string{"PASSING", "WARNING", "ERROR", "UNKNOWN"}
)

// shortInsightID is how many characters of an insight ID the table shows,
// and the least that --insight takes as the start of one.
const shortInsightID = 8

type upgradeCheckOptions struct {
	ios     *cmdutil.IOStreams
	clients func(ctx context.Context) (*awsx.Clients, error)
	target  func(explicit string) (cmdutil.Target, error)
	timeout time.Duration
	now     func() time.Time

	cluster     string
	category    string
	statuses    []string
	showPassing bool
	insight     string
	exitZero    bool
	output      cmdutil.Format

	// resolved is the cluster, set by complete.
	resolved cmdutil.Target
}

func newCmdUpgradeCheck(f *cmdutil.Factory) *cobra.Command {
	o := &upgradeCheckOptions{
		ios:     f.IOStreams,
		clients: func(ctx context.Context) (*awsx.Clients, error) { return cmdutil.ClusterClients(ctx, f) },
		target:  func(explicit string) (cmdutil.Target, error) { return cmdutil.ResolveCluster(f, explicit, true) },
		timeout: f.Global.Timeout,
		now:     f.Now,
	}
	cmd := &cobra.Command{
		Use:   "upgrade-check [cluster]",
		Short: "Report whether a cluster is ready for its next upgrade",
		Long: `Report whether a cluster is ready for its next Kubernetes upgrade: the EKS
cluster insights, the version skew of its nodegroups and add-ons, the
control-plane metrics, the support window, and the rollback window after a
recent upgrade. Nothing is changed.

The exit code is the verdict, for a CI gate: 0 ready, 2 review (warnings),
3 blocked, 4 incomplete (a check could not read what it needs, or a
nodegroup or an add-on could not be read). --exit-zero reports and exits 0.

--status narrows the insights that are shown, not the ones that are
judged: an ERROR insight blocks whatever is shown. --insight shows one
insight in full, by its ID or by the short ID of the table, and exits with
that insight's status.`,
		Example: `  refresh cluster upgrade-check prod

  # Every insight, passing ones too, as JSON for a gate
  refresh cluster upgrade-check prod --show-passing -o json

  # The report without a failing exit code
  refresh cluster upgrade-check prod --exit-zero

  # One insight in full
  refresh cluster upgrade-check prod --insight bc8b2f86`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: fleetcmd.CompleteClusters(f),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.complete(args); err != nil {
				return err
			}
			if err := o.validate(); err != nil {
				return err
			}
			return o.run(cmd.Context())
		},
	}
	cmd.Flags().StringVar(&o.category, "category", health.CategoryUpgradeReadiness, "Insight category: "+strings.Join(categories, ", "))
	cmd.Flags().StringSliceVar(&o.statuses, "status", nil, "Show only the insights with these statuses: "+strings.Join(insightStatuses, ", "))
	cmd.Flags().BoolVar(&o.showPassing, "show-passing", false, "Show the PASSING insights too")
	cmd.Flags().StringVar(&o.insight, "insight", "", "Show one insight in full, by its ID or the short ID of the table")
	cmd.Flags().BoolVar(&o.exitZero, "exit-zero", false, "Exit 0 whatever the verdict (report mode)")
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *upgradeCheckOptions) complete(args []string) error {
	if len(args) > 0 {
		o.cluster = args[0]
	}
	o.category = strings.ToUpper(strings.TrimSpace(o.category))
	for i, s := range o.statuses {
		o.statuses[i] = strings.ToUpper(strings.TrimSpace(s))
	}
	if slices.Contains(o.statuses, "PASSING") {
		o.showPassing = true
	}
	o.insight = strings.TrimSpace(o.insight)
	target, err := o.target(o.cluster)
	o.resolved = target
	return err
}

func (o *upgradeCheckOptions) validate() error {
	if !slices.Contains(categories, o.category) {
		return fmt.Errorf("invalid --category %q: one of %s", o.category, strings.Join(categories, ", "))
	}
	for _, s := range o.statuses {
		if !slices.Contains(insightStatuses, s) {
			return fmt.Errorf("invalid --status %q: one of %s", s, strings.Join(insightStatuses, ", "))
		}
	}
	return nil
}

func (o *upgradeCheckOptions) run(ctx context.Context) error {
	ctx, cancel := cmdutil.WithTimeout(ctx, o.timeout)
	defer cancel()
	clients, err := o.clients(ctx)
	if err != nil {
		return err
	}
	if o.insight != "" {
		return o.describeInsight(ctx, clients)
	}
	fetcher := fleet.Fetcher{Now: o.now}

	// Wave 1: the cluster and its lists, the insights, and the
	// control-plane metrics, which need only the name of the cluster.
	var (
		wg       sync.WaitGroup
		snap     *fleet.Snapshot
		fetchErr error
		insights []apidoc.Insight
		listErr  error
		cp       []health.Result
	)
	stub := &fleet.Snapshot{Cluster: fleet.Cluster{Name: o.resolved.Cluster, Region: clients.Region}}
	wg.Go(func() {
		snap, fetchErr = fetcher.Fetch(ctx, clients, o.resolved.Cluster, fleet.Need{Nodegroups: true, Addons: true, LatestAddons: true, Support: true, Updates: true})
	})
	wg.Go(func() { insights, listErr = health.ListInsights(ctx, clients, o.resolved.Cluster, o.category) })
	wg.Go(func() {
		cp = health.Run(ctx, []health.Check{health.ControlPlane()}, &health.Input{Snapshot: stub, Clients: clients, Now: fetcher.Time()})
	})
	wg.Wait()
	if fetchErr != nil {
		return fleetcmd.ClusterError(ctx, &fetcher, clients, o.resolved.Cluster, fetchErr)
	}

	// Wave 3: the rollback insights, only inside a rollback window.
	var rollback *apidoc.Rollback
	if window, ok := fleet.RollbackWindowOf(snap, snap.FetchedAt); ok {
		rollback = &apidoc.Rollback{PreviousVersion: window.PreviousVersion.String(), UpgradedAt: window.UpgradedAt, AvailableUntil: window.AvailableUntil}
		if list, err := health.ListInsights(ctx, clients, o.resolved.Cluster, string(ekstypes.CategoryRollbackReadiness)); err == nil {
			rollback.Insights = countInsights(list)
		}
	}

	checks := []health.Check{health.Insights(health.InsightsInput{Insights: insights, Err: listErr, Category: o.category}), health.VersionSkew()}
	results := append(health.Run(ctx, checks, &health.Input{Snapshot: snap, Clients: clients, Now: snap.FetchedAt}), cp...)
	doc := o.document(snap, results, insights, listErr == nil, rollback)
	if err := cmdutil.Print(o.ios, o.output, doc, upgradeCheckView(doc)); err != nil {
		return err
	}
	if o.output != cmdutil.FormatTable {
		cmdutil.ReportFailures(o.ios, doc.Failures)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("interrupted: %w", context.Canceled)
	}
	return o.exit(doc)
}

// document builds the document from what the waves read.
func (o *upgradeCheckOptions) document(snap *fleet.Snapshot, results []health.Result, insights []apidoc.Insight, evaluated bool, rollback *apidoc.Rollback) apidoc.UpgradeCheck {
	c := snap.ClusterDoc()
	doc := apidoc.UpgradeCheck{
		Cluster: c.Name, Region: c.Region, Version: c.Version, Support: c.Support,
		Insights: []apidoc.Insight{},
		Checks:   health.Documents(results),
		Rollback: rollback,
		Failures: append(slices.Clone(snap.Failures), health.Failures(results)...),
	}
	if evaluated {
		doc.InsightsEvaluated = new(len(insights) > 0)
	}
	for _, in := range insights {
		if len(o.statuses) > 0 && !slices.Contains(o.statuses, in.Status) {
			continue
		}
		if !o.showPassing && in.Status == string(ekstypes.InsightStatusValuePassing) {
			continue
		}
		doc.Insights = append(doc.Insights, in)
	}
	apidoc.SortFailures(doc.Failures)
	if snap.Have.Cluster == fleet.Collected {
		doc.Skew = skewOf(snap)
	}
	doc.Readiness, doc.Reasons = readinessOf(results, c.Support)
	return doc
}

// readinessOf folds the results into the verdict: the gate with an empty
// policy, where a block from unread data alone is Incomplete. A support
// window that is not standard is a reason to review.
func readinessOf(results []health.Result, support *apidoc.Support) (apidoc.Readiness, []string) {
	v := health.Gate(results, health.Policy{})
	reasons := make([]string, 0, len(v.Reasons)+1)
	for _, r := range v.Reasons {
		reasons = append(reasons, r.String())
	}
	readiness := apidoc.ReadinessReady
	switch v.Decision {
	case health.Block:
		readiness = apidoc.ReadinessIncomplete
		for _, b := range v.Blockers {
			if b.Outcome == health.Fail {
				readiness = apidoc.ReadinessBlocked
			}
		}
	case health.Ask:
		readiness = apidoc.ReadinessReview
	}
	if support != nil && support.Tier != apidoc.SupportStandard && support.Tier != apidoc.SupportUnknown {
		reasons = append(reasons, "Support: the cluster is in "+strings.ToLower(string(support.Tier))+" support")
		if readiness == apidoc.ReadinessReady {
			readiness = apidoc.ReadinessReview
		}
	}
	return readiness, reasons
}

// skewOf returns the version skew of the snapshot for the document.
func skewOf(snap *fleet.Snapshot) *apidoc.Skew {
	skew := &apidoc.Skew{ControlPlaneVersion: snap.Cluster.Version.String(), Nodegroups: []apidoc.NodegroupSkew{}, Addons: []apidoc.AddonSkew{}}
	for _, n := range snap.Nodegroups {
		row := apidoc.NodegroupSkew{Name: n.Name, Incomplete: n.State != fleet.Collected}
		if !row.Incomplete {
			row.Version = n.Version.String()
			if minors := snap.Cluster.Version.Minor - n.Version.Minor; !n.Version.IsZero() && minors > 0 {
				row.MinorsBehind = minors
				row.AtSkewLimit = minors >= 3
			}
		}
		skew.Nodegroups = append(skew.Nodegroups, row)
	}
	for _, a := range snap.Addons {
		row := apidoc.AddonSkew{Name: a.Name, Incomplete: a.State != fleet.Collected || a.LatestState != fleet.Collected}
		if a.State == fleet.Collected {
			row.Version = a.Version
		}
		if a.LatestState == fleet.Collected {
			row.Latest = a.Latest
			row.Behind, _ = a.Behind()
		}
		skew.Addons = append(skew.Addons, row)
	}
	return skew
}

// countInsights counts insights by status. A status this build does not
// know counts as unknown, as EKS treats it.
func countInsights(insights []apidoc.Insight) *apidoc.InsightCounts {
	c := &apidoc.InsightCounts{}
	for _, in := range insights {
		switch in.Status {
		case "PASSING":
			c.Passing++
		case "WARNING":
			c.Warning++
		case "ERROR":
			c.Error++
		default:
			c.Unknown++
		}
	}
	return c
}

// exit returns the exit error of the report: the verdict, or 4 for a
// document with failures, whichever is higher. The message names the
// verdict and the failures both. --exit-zero drops it.
func (o *upgradeCheckOptions) exit(doc apidoc.UpgradeCheck) error {
	if o.exitZero {
		return nil
	}
	reasons := strings.Join(doc.Reasons, "; ")
	var verdict string
	code := cmdutil.ExitOK
	switch doc.Readiness {
	case apidoc.ReadinessBlocked:
		verdict, code = "upgrade blocked: "+reasons, cmdutil.ExitBlocked
	case apidoc.ReadinessReview:
		verdict, code = "upgrade needs review: "+reasons, cmdutil.ExitNeedsAttention
	case apidoc.ReadinessIncomplete:
		verdict, code = "upgrade check incomplete: "+reasons, cmdutil.ExitIncomplete
	}
	if incomplete := cmdutil.IncompleteError(doc.Failures); incomplete != nil {
		msg := incomplete.Error()
		if verdict != "" {
			msg = verdict + "; " + msg
		}
		return &cmdutil.ExitError{Code: cmdutil.ExitIncomplete, Err: errors.New(msg)}
	}
	if code == cmdutil.ExitOK {
		return nil
	}
	return &cmdutil.ExitError{Code: code, Err: errors.New(verdict + " (pass --exit-zero to report only)")}
}

// describeInsight runs the --insight form: one insight in full.
func (o *upgradeCheckOptions) describeInsight(ctx context.Context, clients *awsx.Clients) error {
	id, err := o.resolveInsightID(ctx, clients)
	if err != nil {
		return err
	}
	out, err := clients.EKS.DescribeInsight(ctx, &eks.DescribeInsightInput{ClusterName: aws.String(o.resolved.Cluster), Id: aws.String(id)})
	if err != nil {
		if awsx.IsNotFound(err) {
			return fmt.Errorf("insight %q not found on cluster %s; list them with: refresh cluster upgrade-check %s --show-passing", o.insight, o.resolved.Cluster, o.resolved.Cluster)
		}
		return awsx.FormatAWSError(err, "describing insight "+id)
	}
	if out.Insight == nil {
		return fmt.Errorf("insight %q not found on cluster %s", id, o.resolved.Cluster)
	}
	doc := insightDescription(o.resolved.Cluster, clients.Region, out.Insight)
	if err := cmdutil.Print(o.ios, o.output, doc, insightView(doc)); err != nil {
		return err
	}
	if o.exitZero {
		return nil
	}
	switch doc.Insight.Status {
	case "PASSING":
		return nil
	case "WARNING":
		return &cmdutil.ExitError{Code: cmdutil.ExitNeedsAttention, Err: fmt.Errorf("insight %q is WARNING (pass --exit-zero to report only)", doc.Insight.Name)}
	}
	return &cmdutil.ExitError{Code: cmdutil.ExitBlocked, Err: fmt.Errorf("insight %q is %s, which blocks the upgrade (pass --exit-zero to report only)", doc.Insight.Name, doc.Insight.Status)}
}

// resolveInsightID returns the full ID for --insight: the value itself,
// or the one insight of the category whose ID starts with it, when the
// value is at least as long as the short ID of the table.
func (o *upgradeCheckOptions) resolveInsightID(ctx context.Context, clients *awsx.Clients) (string, error) {
	if len(o.insight) < shortInsightID {
		return "", fmt.Errorf("--insight %q is too short: pass the ID, or the first %d characters of it", o.insight, shortInsightID)
	}
	insights, err := health.ListInsights(ctx, clients, o.resolved.Cluster, o.category)
	if err != nil {
		return "", awsx.FormatAWSError(err, "listing the insights of cluster "+o.resolved.Cluster)
	}
	var matches []string
	for _, in := range insights {
		if in.ID == o.insight {
			return in.ID, nil
		}
		if strings.HasPrefix(in.ID, o.insight) {
			matches = append(matches, in.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no %s insight of cluster %s has the ID %q; list them with: refresh cluster upgrade-check %s --show-passing",
			o.category, o.resolved.Cluster, o.insight, o.resolved.Cluster)
	}
	return "", fmt.Errorf("%d insights start with %q: %s", len(matches), o.insight, strings.Join(matches, ", "))
}

// insightDescription returns the document of one insight.
func insightDescription(cluster, region string, in *ekstypes.Insight) apidoc.InsightDescription {
	doc := apidoc.InsightDescription{
		Cluster: cluster, Region: region,
		Insight:        health.InsightDoc(in.Id, in.Name, in.Category, in.InsightStatus, in.KubernetesVersion, in.LastRefreshTime, in.Description),
		Recommendation: aws.ToString(in.Recommendation),
		AdditionalInfo: in.AdditionalInfo,
		Resources:      []string{},
		Deprecations:   []apidoc.Deprecation{},
		Failures:       apidoc.Failures{},
	}
	for _, r := range in.Resources {
		switch {
		case r.Arn != nil:
			doc.Resources = append(doc.Resources, aws.ToString(r.Arn))
		case r.KubernetesResourceUri != nil:
			doc.Resources = append(doc.Resources, aws.ToString(r.KubernetesResourceUri))
		}
	}
	if in.CategorySpecificSummary == nil {
		return doc
	}
	for _, d := range in.CategorySpecificSummary.DeprecationDetails {
		dep := apidoc.Deprecation{
			Usage: aws.ToString(d.Usage), ReplacedWith: aws.ToString(d.ReplacedWith),
			StopServingVersion: aws.ToString(d.StopServingVersion), StartServingReplacementVersion: aws.ToString(d.StartServingReplacementVersion),
			ClientStats: []apidoc.ClientStat{},
		}
		for _, c := range d.ClientStats {
			dep.ClientStats = append(dep.ClientStats, apidoc.ClientStat{UserAgent: aws.ToString(c.UserAgent), LastRequestTime: aws.ToTime(c.LastRequestTime), NumberOfRequestsLast30Days: c.NumberOfRequestsLast30Days})
		}
		// The most active callers first: they are the ones to chase.
		slices.SortStableFunc(dep.ClientStats, func(a, b apidoc.ClientStat) int {
			return int(b.NumberOfRequestsLast30Days - a.NumberOfRequestsLast30Days)
		})
		doc.Deprecations = append(doc.Deprecations, dep)
	}
	return doc
}

// readinessLabel returns the word of the header for a readiness.
func readinessLabel(r apidoc.Readiness) (string, view.Status) {
	switch r {
	case apidoc.ReadinessReady:
		return "READY", view.Healthy
	case apidoc.ReadinessReview:
		return "REVIEW", view.Warn
	case apidoc.ReadinessBlocked:
		return "NOT READY", view.Fail
	}
	return "INCOMPLETE", view.Unknown
}

// upgradeCheckView returns the table and plain form of doc.
func upgradeCheckView(doc apidoc.UpgradeCheck) view.Description {
	label, status := readinessLabel(doc.Readiness)
	subtitle := doc.Region
	if doc.Version != "" {
		subtitle += " · " + doc.Version
	}
	if doc.Support != nil {
		subtitle += " · " + fleetcmd.SupportText(doc.Support)
	}
	d := view.Description{Title: doc.Cluster, Status: status, Label: label, Subtitle: subtitle, Failures: doc.Failures}

	readiness := view.Section{Title: "READINESS"}
	if len(doc.Reasons) == 0 {
		readiness.Fields = []view.Field{{Name: "verdict", Value: "no finding", Status: view.Healthy}}
	}
	for i, r := range doc.Reasons {
		readiness.Fields = append(readiness.Fields, view.Field{Name: "reason " + strconv.Itoa(i+1), Value: r, Status: reasonStatus(doc.Checks, r)})
	}
	insights := view.Section{Title: "INSIGHTS"}
	for _, in := range doc.Insights {
		insights.Items = append(insights.Items, view.Item{Name: shortID(in.ID), Fields: []view.Field{
			{Name: "insight", Value: in.Name},
			{Name: "status", Value: in.Status, Status: insightStatus(in.Status)},
			{Name: "version", Value: in.KubernetesVersion},
		}})
	}
	switch {
	case doc.InsightsEvaluated == nil:
		insights.Fields = []view.Field{{Name: "evaluated", Value: "unreadable; the insights could not be read", Status: view.Unknown}}
	case !*doc.InsightsEvaluated:
		insights.Fields = []view.Field{{Name: "evaluated", Value: "no; EKS has not evaluated the cluster yet", Status: view.Warn}}
	case len(insights.Items) == 0:
		insights.Fields = []view.Field{{Name: "shown", Value: "none; pass --show-passing for the passing insights"}}
	}
	nodegroups := view.Section{Title: "NODEGROUP SKEW"}
	addons := view.Section{Title: "ADD-ON SKEW"}
	if s := doc.Skew; s != nil {
		for _, n := range s.Nodegroups {
			behind, st := strconv.Itoa(n.MinorsBehind), view.NoStatus
			switch {
			case n.Incomplete:
				behind, st = unknownCell, view.Unknown
			case n.AtSkewLimit:
				behind, st = behind+" (at the limit)", view.Fail
			case n.MinorsBehind > 0:
				st = view.Warn
			}
			nodegroups.Items = append(nodegroups.Items, view.Item{Name: n.Name, Fields: []view.Field{
				{Name: "version", Value: orUnknownText(n.Incomplete, n.Version)},
				{Name: "behind", Value: behind, Status: st},
			}})
		}
		for _, a := range s.Addons {
			state, st := "current", view.NoStatus
			switch {
			case a.Incomplete:
				state, st = unknownCell, view.Unknown
			case a.Behind:
				state, st = "behind", view.Warn
			}
			addons.Items = append(addons.Items, view.Item{Name: a.Name, Fields: []view.Field{
				{Name: "installed", Value: orUnknownText(a.Incomplete, a.Version)},
				{Name: "latest", Value: orText(a.Latest, "none published")},
				{Name: "state", Value: state, Status: st},
			}})
		}
	}
	checks := view.Section{Title: "CHECKS"}
	for _, c := range doc.Checks {
		checks.Items = append(checks.Items, view.Item{Name: c.Name, Fields: []view.Field{
			{Name: "outcome", Value: string(c.Outcome), Status: view.HealthStatus(c.Outcome)},
			{Name: "message", Value: c.Message},
		}})
	}
	rollback := view.Section{Title: "ROLLBACK"}
	if r := doc.Rollback; r != nil {
		rollback.Fields = []view.Field{
			{Name: "to", Value: r.PreviousVersion},
			{Name: "until", Value: formatTime(r.AvailableUntil, dateLayout)},
		}
		if c := r.Insights; c != nil {
			rollback.Fields = append(rollback.Fields, view.Field{Name: "insights", Value: fmt.Sprintf("%d passing, %d warning, %d error, %d unknown", c.Passing, c.Warning, c.Error, c.Unknown)})
		}
	}
	d.Sections = append(d.Sections, readiness, insights, nodegroups, addons, checks, rollback)
	return d
}

// reasonStatus returns the status of a reason line from the check that it
// names.
func reasonStatus(checks []apidoc.HealthResult, reason string) view.Status {
	name, _, _ := strings.Cut(reason, ":")
	for _, c := range checks {
		if c.Name == name {
			return view.HealthStatus(c.Outcome)
		}
	}
	return view.Warn
}

func insightStatus(status string) view.Status {
	switch status {
	case "PASSING":
		return view.Healthy
	case "WARNING":
		return view.Warn
	case "ERROR":
		return view.Fail
	}
	return view.Unknown
}

// shortID returns the first characters of an insight ID, enough to name
// it to --insight.
func shortID(id string) string {
	if len(id) > shortInsightID {
		return id[:shortInsightID]
	}
	return id
}

// insightView returns the table and plain form of one insight.
func insightView(doc apidoc.InsightDescription) view.Description {
	in := doc.Insight
	d := view.Description{Title: in.Name, Status: insightStatus(in.Status), Label: in.Status, Subtitle: doc.Cluster + " in " + doc.Region, Failures: doc.Failures}
	d.Sections = append(d.Sections, view.Section{Title: "INSIGHT", Fields: []view.Field{
		{Name: "id", Value: in.ID},
		{Name: "category", Value: in.Category},
		{Name: "version", Value: in.KubernetesVersion},
		{Name: "reason", Value: in.StatusReason},
		{Name: "description", Value: in.Description},
		{Name: "recommendation", Value: doc.Recommendation},
		{Name: "refreshed", Value: formatTime(in.LastRefreshTime, dateLayout)},
	}})
	resources := view.Section{Title: "RESOURCES"}
	for i, r := range doc.Resources {
		resources.Fields = append(resources.Fields, view.Field{Name: strconv.Itoa(i + 1), Value: r})
	}
	deprecations := view.Section{Title: "DEPRECATED APIS"}
	for _, dep := range doc.Deprecations {
		clients := make([]string, 0, len(dep.ClientStats))
		for _, c := range dep.ClientStats {
			clients = append(clients, fmt.Sprintf("%s (%d requests in 30 days)", c.UserAgent, c.NumberOfRequestsLast30Days))
		}
		deprecations.Items = append(deprecations.Items, view.Item{Name: dep.Usage, Fields: []view.Field{
			{Name: "replaced with", Value: dep.ReplacedWith},
			{Name: "removed in", Value: dep.StopServingVersion},
			{Name: "clients", Value: strings.Join(clients, "; ")},
		}})
	}
	d.Sections = append(d.Sections, resources, deprecations)
	return d
}
