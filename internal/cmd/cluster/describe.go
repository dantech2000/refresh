package cluster

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/kube"
	"github.com/dantech2000/refresh/internal/view"
)

type describeOptions struct {
	f   *cmdutil.Factory
	ios *cmdutil.IOStreams

	cluster        string
	checkReadiness bool
	output         cmdutil.Format

	// target is the cluster, resolved by complete.
	target cmdutil.Target
}

func newCmdDescribe(f *cmdutil.Factory) *cobra.Command {
	o := &describeOptions{f: f, ios: f.IOStreams}
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
		Args:              cobra.MatchAll(cobra.MaximumNArgs(1), cmdutil.NoEmptyArgs),
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
	target, err := cmdutil.ResolveCluster(o.f, o.cluster, true)
	o.target = target
	return err
}

// validate rejects a --kube-context that the kubeconfig does not have,
// before any AWS call.
func (o *describeOptions) validate() error {
	if !o.checkReadiness {
		return nil
	}
	return kube.CheckContext(o.f.Global.Kubeconfig, o.f.Global.KubeContext)
}

func (o *describeOptions) run(ctx context.Context) error {
	ios := o.ios
	ctx, cancel := cmdutil.WithTimeout(ctx, o.f.Global.Timeout)
	defer cancel()
	read, err := fleetcmd.FetchCluster(ctx, o.f, o.target, fleet.Need{Nodegroups: true, InstanceIDs: true, Addons: true, Support: true, VPC: true})
	if err != nil {
		return err
	}
	snap := read.Snapshot
	// The Kubernetes checks were not asked for without --check-readiness,
	// so that they do not run is not a warning. The summary is then the
	// one that "cluster list --show-health" gives the cluster.
	in := &health.Input{
		Snapshot: snap, Clients: read.Clients, Now: snap.FetchedAt,
		NoKube: "pass --check-readiness to run the Kubernetes checks", KubeNotRequested: !o.checkReadiness,
	}
	var wg sync.WaitGroup
	merge := func() {}
	if o.checkReadiness {
		access, err := fleetcmd.ConnectKube(o.f.Kube, snap)
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
			wg.Go(func() { read.Fetcher.FetchKube(ctx, access.Client, &kubeSnap, fleet.KubeNeed{Ready: true}) })
			merge = func() {
				snap.Nodegroups = kubeSnap.Nodegroups
				snap.Failures = append(snap.Failures, kubeSnap.Failures...)
			}
		}
	}
	results := health.Run(ctx, health.Preflight(), in)
	wg.Wait()
	merge()
	hd := health.Doc(results)
	doc := apidoc.ClusterDescription{Cluster: snap.ClusterDoc(), Failures: append(snap.Failures, health.Failures(results)...)}
	doc.Cluster.Health = &hd
	doc.Cluster.Incomplete = len(doc.Failures) > 0
	apidoc.SortFailures(doc.Failures)
	return cmdutil.PrintResult(ctx, ios, o.output, doc, describeView(doc), doc.Failures)
}

// dayLayout is how a describe view prints a date.
const dayLayout = "2006-01-02"

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
		{Name: "created", Value: view.Time(c.CreatedAt)},
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
			{Name: "status", Value: view.OrUnknown(n.Incomplete, n.Status), Status: view.StatusOf(n.Status)},
			{Name: "version", Value: view.OrUnknown(n.Incomplete, n.Version), Status: view.BehindStatus(n)},
			{Name: "instance", Value: view.OrUnknown(n.Incomplete, strings.Join(n.InstanceTypes, ","))},
			{Name: "nodes", Value: view.NodesCell(n)},
		}}
		nodegroups.Items = append(nodegroups.Items, item)
	}
	addons := view.Section{Title: "ADD-ONS"}
	for _, a := range c.Addons {
		addons.Items = append(addons.Items, view.Item{Name: a.Name, Fields: []view.Field{
			{Name: "version", Value: view.OrUnknown(a.Incomplete, a.Version)},
			{Name: "status", Value: view.OrUnknown(a.Incomplete, a.Status), Status: view.StatusOf(a.Status)},
			{Name: "health", Value: view.OrUnknown(a.Incomplete, string(a.Health))},
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
	s.Items = view.HealthItems(h.Checks)
	return s
}

// supportLine returns the support of a cluster as one line of a describe
// view: the tier, the date that it ends, and what happens then. In
// extended support that is the premium over standard support.
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
		fmt.Fprintf(&b, " until %s (%d days), ", s.ExtendedUntil.Format(dayLayout), s.DaysRemaining)
		if s.ExtendedSupportPremiumUSDPerHour > 0 {
			fmt.Fprintf(&b, "+$%.2f/hr over standard support", s.ExtendedSupportPremiumUSDPerHour)
		} else {
			b.WriteString("at a higher price")
		}
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
	return view.SupportStatus(s)
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
