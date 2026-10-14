package cluster

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/kube"
	"github.com/dantech2000/refresh/internal/view"
)

type describeOptions struct {
	ios    *cmdutil.IOStreams
	global *cmdutil.GlobalOptions
	// now returns the time that a snapshot records.
	now func() time.Time
	// resolve returns the cluster of the run, from the flag or the
	// defaults, and clients the AWS clients of its region.
	resolve func(cluster string) (cmdutil.Target, error)
	clients func(ctx context.Context) (*awsx.Clients, error)
	// kube returns the Kubernetes client of a cluster, for
	// --check-readiness.
	kube func(target kube.Target) (*kube.Client, error)

	cluster        string
	checkReadiness bool
	output         cmdutil.Format

	// target is the cluster, resolved by complete.
	target cmdutil.Target
}

func newCmdDescribe(f *cmdutil.Factory) *cobra.Command {
	o := &describeOptions{
		ios: f.IOStreams, global: &f.Global, now: f.Now,
		resolve: func(cluster string) (cmdutil.Target, error) { return cmdutil.ResolveCluster(f, cluster, true) },
		clients: func(ctx context.Context) (*awsx.Clients, error) { return cmdutil.ClusterClients(ctx, f) },
		kube:    f.Kube,
	}
	cmd := &cobra.Command{
		Use:   "describe [cluster]",
		Short: "Describe one cluster with its nodegroups and add-ons",
		Long: `Describe an EKS cluster: its version and support window, its network and
security configuration, its nodegroups, its add-ons, and its health.

The HEALTH section runs the checks that a change runs before it starts.
The checks that read the Kubernetes API run with --check-readiness;
without it they are skipped. The section shows health and not upgrade
readiness: it reads no upgrade insight and no version skew, so an insight
in ERROR shows in "cluster upgrade-check" and not here. The summary does
not change the exit code: describe reads and reports, and "status" and
"cluster upgrade-check" are the commands that judge.

Without an argument the cluster comes from REFRESH_CLUSTER, then the active
refresh context, then the current kubeconfig context. The name is exact. For
a name that does not exist, the command lists the clusters whose names are
close.

When a part of the cluster cannot be read, the command prints what it has,
and names the part under INCOMPLETE DATA or in "failures". The support
dates come from EKS; when EKS does not report them, the command uses the
dates that refresh ships with and says so.

Exit codes:
  0  Done. The health summary does not change the code.
  1  An error, or an interrupt.
  4  Incomplete: a part of the cluster could not be read. The command
     prints what it has.`,
		Example: `  refresh cluster describe prod

  # The cluster of the active context
  refresh cluster describe

  # The add-on versions for a script
  refresh cluster describe prod -o json | jq -r '.cluster.addons[] | "\(.name) \(.version)"'`,
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
	cmd.Flags().BoolVar(&o.checkReadiness, "check-readiness", false, "Read the Kubernetes API too: the Ready nodes of each nodegroup, and the health checks that need it")
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *describeOptions) complete(args []string) error {
	if len(args) > 0 {
		o.cluster = args[0]
	}
	target, err := o.resolve(o.cluster)
	o.target = target
	return err
}

// validate rejects a --kube-context that the kubeconfig does not have,
// before any AWS call.
func (o *describeOptions) validate() error {
	if !o.checkReadiness {
		return nil
	}
	return kube.CheckContext(o.global.Kubeconfig, o.global.KubeContext)
}

func (o *describeOptions) run(ctx context.Context) error {
	ios := o.ios
	ctx, cancel := cmdutil.WithTimeout(ctx, o.global.Timeout)
	defer cancel()
	clients, err := o.clients(ctx)
	if err != nil {
		return err
	}
	fetcher := fleet.Fetcher{Now: o.now}
	snap, err := fetcher.Fetch(ctx, clients, o.target.Cluster, fleet.Need{Nodegroups: true, InstanceIDs: true, Addons: true, Support: true, VPC: true})
	if err != nil {
		return fleetcmd.ClusterError(ctx, &fetcher, clients, o.target.Cluster, err)
	}
	in := &health.Input{Snapshot: snap, Clients: clients, Now: snap.FetchedAt, NoKube: "pass --check-readiness to run the Kubernetes checks"}
	var wg sync.WaitGroup
	merge := func() {}
	if o.checkReadiness {
		access, err := fleetcmd.ConnectKube(o.kube, snap)
		if err != nil {
			return err
		}
		fleetcmd.ReportKube(ios, access)
		in.Kube, in.Metrics, in.NoKube = access.Client, access.Metrics, access.NoKube
		if access.Client != nil {
			// The Ready counts and the health reads need only the
			// endpoint and the nodegroups, so they run together: the
			// counts fill a copy of the snapshot while the checks read
			// the original, and the copy is merged back after both.
			kubeSnap := *snap
			kubeSnap.Nodegroups = slices.Clone(snap.Nodegroups)
			kubeSnap.Failures = nil
			wg.Go(func() { fetcher.FetchKube(ctx, access.Client, &kubeSnap, fleet.KubeNeed{Ready: true}) })
			merge = func() {
				snap.Nodegroups = kubeSnap.Nodegroups
				snap.Failures = append(snap.Failures, kubeSnap.Failures...)
			}
		}
	}
	results := health.Run(ctx, health.Preflight(), in)
	if !o.checkReadiness {
		// The Kubernetes checks were not asked for, so that they did not
		// run is not a warning. The summary is then the one that
		// "cluster list --show-health" gives the cluster.
		for i := range results {
			if results[i].Name == health.AccessCheck {
				results[i].Outcome, results[i].Message = health.Skipped, in.NoKube
			}
		}
	}
	wg.Wait()
	merge()
	hd := health.Doc(results)
	doc := apidoc.ClusterDescription{Cluster: snap.ClusterDoc(), Failures: append(snap.Failures, health.Failures(results)...)}
	doc.Cluster.Health = &hd
	doc.Cluster.Incomplete = len(doc.Failures) > 0
	apidoc.SortFailures(doc.Failures)
	if err := cmdutil.Print(ios, o.output, doc, describeView(doc)); err != nil {
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

// dateLayout is how a describe view prints a time, and dayLayout a date.
const (
	dateLayout = "2006-01-02 15:04 UTC"
	dayLayout  = "2006-01-02"
)

// describeView returns the table and plain form of doc.
func describeView(doc apidoc.ClusterDescription) view.Description {
	c := doc.Cluster
	d := view.Description{
		Title:    c.Name,
		Status:   view.StatusOf(c.Status),
		Label:    c.Status,
		Subtitle: c.Region,
		Failures: doc.Failures,
	}
	d.Sections = append(d.Sections, view.Section{Title: "OVERVIEW", Fields: []view.Field{
		{Name: "status", Value: c.Status, Status: view.StatusOf(c.Status)},
		{Name: "version", Value: c.Version},
		{Name: "platform version", Value: c.PlatformVersion},
		{Name: "support", Value: supportLine(c.Support), Status: supportMark(c.Support)},
		{Name: "upgrade policy", Value: c.UpgradePolicy},
		{Name: "endpoint", Value: c.Endpoint},
		{Name: "arn", Value: c.ARN},
		{Name: "created", Value: formatTime(c.CreatedAt, dateLayout)},
	}})
	if n := c.Network; n != nil {
		vpc := n.VPCID
		if n.VPCCIDR != "" {
			vpc += " (" + n.VPCCIDR + ")"
		}
		d.Sections = append(d.Sections, view.Section{Title: "NETWORK", Fields: []view.Field{
			{Name: "vpc", Value: vpc},
			{Name: "subnets", Value: strings.Join(n.SubnetIDs, ", ")},
			{Name: "security groups", Value: strings.Join(n.SecurityGroupIDs, ", ")},
			{Name: "endpoint access", Value: endpointAccess(n)},
			{Name: "public cidrs", Value: strings.Join(n.PublicAccessCIDRs, ", ")},
		}})
	}
	if s := c.Security; s != nil {
		d.Sections = append(d.Sections, view.Section{Title: "SECURITY", Fields: []view.Field{
			{Name: "role", Value: s.RoleARN},
			{Name: "secrets encryption", Value: orText(s.KMSKeyARN, "no KMS key")},
			{Name: "logging", Value: orText(strings.Join(s.LogTypes, ", "), "disabled")},
			{Name: "deletion protection", Value: enabled(s.DeletionProtection)},
		}})
	}

	issues := view.Section{Title: "ISSUES"}
	for _, issue := range c.HealthIssues {
		issues.Items = append(issues.Items, view.Item{Name: issue.Code, Fields: []view.Field{
			{Name: "message", Value: issue.Message},
			{Name: "resources", Value: strings.Join(issue.ResourceIDs, ", ")},
		}})
	}
	nodegroups := view.Section{Title: "NODEGROUPS"}
	for _, n := range c.Nodegroups {
		item := view.Item{Name: n.Name, Fields: []view.Field{
			{Name: "status", Value: orUnknownText(n.Incomplete, n.Status), Status: view.StatusOf(n.Status)},
			{Name: "version", Value: orUnknownText(n.Incomplete, n.Version), Status: behindMark(n)},
			{Name: "instance", Value: orUnknownText(n.Incomplete, strings.Join(n.InstanceTypes, ","))},
			{Name: "nodes", Value: nodesCell(n)},
		}}
		nodegroups.Items = append(nodegroups.Items, item)
	}
	addons := view.Section{Title: "ADD-ONS"}
	for _, a := range c.Addons {
		addons.Items = append(addons.Items, view.Item{Name: a.Name, Fields: []view.Field{
			{Name: "version", Value: orUnknownText(a.Incomplete, a.Version)},
			{Name: "status", Value: orUnknownText(a.Incomplete, a.Status), Status: view.StatusOf(a.Status)},
			{Name: "health", Value: orUnknownText(a.Incomplete, string(a.Health))},
		}})
	}
	tags := view.Section{Title: "TAGS"}
	for _, key := range slices.Sorted(maps.Keys(c.Tags)) {
		tags.Fields = append(tags.Fields, view.Field{Name: key, Value: c.Tags[key]})
	}
	d.Sections = append(d.Sections, issues, nodegroups, addons, healthSection(c.Health), tags)
	return d
}

// healthSection returns the HEALTH section: the summary, then one item per
// check with its outcome and message.
func healthSection(h *apidoc.Health) view.Section {
	s := view.Section{Title: "HEALTH"}
	if h == nil {
		return s
	}
	s.Fields = []view.Field{{Name: "summary", Value: string(h.Summary), Status: view.HealthStatus(h.Summary)}}
	for _, c := range h.Checks {
		s.Items = append(s.Items, view.Item{Name: c.Name, Fields: []view.Field{
			{Name: "outcome", Value: string(c.Outcome), Status: view.HealthStatus(c.Outcome)},
			{Name: "message", Value: c.Message},
		}})
	}
	return s
}

// supportLine returns the support of a cluster as one line of a describe
// view: the tier, the date that it ends, and what happens then.
func supportLine(s *apidoc.Support) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(strings.ToLower(string(s.Tier)))
	switch s.Tier {
	case apidoc.SupportStandard:
		fmt.Fprintf(&b, " until %s (%d days)", s.StandardUntil.Format(dayLayout), s.DaysRemaining)
		if s.AutoUpgrade {
			b.WriteString("; EKS upgrades the cluster then")
		}
	case apidoc.SupportExtended:
		fmt.Fprintf(&b, " until %s (%d days), at a higher price", s.ExtendedUntil.Format(dayLayout), s.DaysRemaining)
	}
	if s.BuiltIn {
		b.WriteString("; dates from refresh, not from EKS")
	}
	return b.String()
}

// supportMark returns the status that draws the support line, or none when
// the support was not read.
func supportMark(s *apidoc.Support) view.Status {
	if s == nil {
		return view.NoStatus
	}
	return supportStatus(s)
}

// endpointAccess says how the API server endpoint can be reached.
func endpointAccess(n *apidoc.Network) string {
	switch {
	case n.PublicAccess && n.PrivateAccess:
		return "public and private"
	case n.PublicAccess:
		return "public"
	case n.PrivateAccess:
		return "private"
	}
	return "none"
}

func enabled(on bool) string {
	if on {
		return "enabled"
	}
	return "disabled"
}

// orText returns value, or other when value is empty.
func orText(value, other string) string {
	if value == "" {
		return other
	}
	return value
}

// orUnknownText returns value, or "unknown" for a thing that was not read.
func orUnknownText(incomplete bool, value string) string {
	if value == "" && incomplete {
		return unknownCell
	}
	return value
}

// behindMark returns a warning for a nodegroup whose version is behind the
// control plane.
func behindMark(n apidoc.Nodegroup) view.Status {
	if n.VersionBehind != nil && *n.VersionBehind {
		return view.Warn
	}
	return view.NoStatus
}

// nodesCell returns the desired size of a nodegroup as a cell, with the
// Ready count before it when it was read: "5/6".
func nodesCell(n apidoc.Nodegroup) string {
	if n.Scaling == nil {
		return orUnknownText(n.Incomplete, "")
	}
	desired := strconv.Itoa(int(n.Scaling.Desired))
	if n.ReadyNodes != nil {
		return strconv.Itoa(*n.ReadyNodes) + "/" + desired
	}
	return desired
}

// formatTime returns t in a layout, or "" for the zero time.
func formatTime(t time.Time, layout string) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(layout)
}
