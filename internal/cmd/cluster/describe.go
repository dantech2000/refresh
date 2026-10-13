package cluster

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/view"
)

type describeOptions struct {
	f *cmdutil.Factory

	cluster string
	output  cmdutil.Format

	// target is the cluster, resolved by complete.
	target cmdutil.Target
}

func newCmdDescribe(f *cmdutil.Factory) *cobra.Command {
	o := &describeOptions{f: f}
	cmd := &cobra.Command{
		Use:   "describe [cluster]",
		Short: "Describe one cluster with its nodegroups and add-ons",
		Long: `Describe an EKS cluster: its version and support window, its network and
security configuration, its nodegroups, and its add-ons.

Without an argument the cluster comes from REFRESH_CLUSTER, then the active
refresh context, then the current kubeconfig context. The name is exact. For
a name that does not exist, the command lists the clusters whose names are
close.

When a part of the cluster cannot be read, the command prints what it has,
names the part under INCOMPLETE DATA or in "failures", and exits 4. The
support dates come from EKS; when EKS does not report them, the command
uses the dates that refresh ships with and says so.`,
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
			return o.run(cmd.Context())
		},
	}
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

func (o *describeOptions) run(ctx context.Context) error {
	ios := o.f.IOStreams
	ctx, cancel := cmdutil.WithTimeout(ctx, o.f.Global.Timeout)
	defer cancel()
	clients, err := cmdutil.ClusterClients(ctx, o.f)
	if err != nil {
		return err
	}
	fetcher := fleet.Fetcher{Now: o.f.Now}
	snap, err := fetcher.Fetch(ctx, clients, o.target.Cluster, fleet.Need{Nodegroups: true, Addons: true, Support: true, VPC: true})
	if err != nil {
		return fleetcmd.ClusterError(ctx, &fetcher, clients, o.target.Cluster, err)
	}

	doc := apidoc.ClusterDescription{Cluster: snap.ClusterDoc(), Failures: snap.Failures}
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
			{Name: "instance", Value: strings.Join(n.InstanceTypes, ",")},
			{Name: "nodes", Value: desired(n)},
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
	d.Sections = append(d.Sections, issues, nodegroups, addons, tags)
	return d
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
	return fleetcmd.SupportStatus(s)
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

// desired returns the desired size of a nodegroup as a cell.
func desired(n apidoc.Nodegroup) string {
	if n.Scaling == nil {
		return orUnknownText(n.Incomplete, "")
	}
	return strconv.Itoa(int(n.Scaling.Desired))
}

// formatTime returns t in a layout, or "" for the zero time.
func formatTime(t time.Time, layout string) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(layout)
}
