package nodegroup

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/view"
)

type describeOptions struct {
	f *cmdutil.Factory

	cluster       string
	nodegroup     string
	showInstances bool
	output        cmdutil.Format

	// target is the cluster, resolved by complete.
	target cmdutil.Target
}

func newCmdDescribe(f *cmdutil.Factory) *cobra.Command {
	o := &describeOptions{f: f}
	cmd := &cobra.Command{
		Use:   "describe <nodegroup>",
		Short: "Describe one nodegroup of a cluster",
		Long: `Describe a managed nodegroup: its status, version, scaling, and the AMI
that its nodes run next to the latest recommended one. With --show-instances
the command also lists the EC2 instances of the nodegroup.

The name is exact. For a name that does not exist, the command lists the
nodegroups whose names are close.

When the AMI or the instances cannot be read, the command prints what it
has, names the failure, and exits 4.`,
		Example: `  refresh nodegroup describe web -c prod

  # With the EC2 instances
  refresh nodegroup describe web -c prod --show-instances`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: fleetcmd.CompleteNodegroups(f, &o.cluster),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.complete(args); err != nil {
				return err
			}
			return o.run(cmd.Context())
		},
	}
	cmdutil.AddClusterFlag(cmd, &o.cluster)
	fleetcmd.RegisterClusterFlagCompletion(cmd, f)
	cmd.Flags().BoolVar(&o.showInstances, "show-instances", false, "List the EC2 instances of the nodegroup")
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *describeOptions) complete(args []string) error {
	o.nodegroup = args[0]
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
	snap, err := fetcher.Fetch(ctx, clients, o.target.Cluster, fleet.Need{
		OnlyNodegroups: []string{o.nodegroup},
		AMI:            true,
		Instances:      o.showInstances,
	})
	if err != nil {
		return fleetcmd.ClusterError(ctx, &fetcher, clients, o.target.Cluster, err)
	}
	nodegroup := snap.Nodegroups[0]
	if nodegroup.State != fleet.Collected {
		return o.nodegroupError(ctx, &fetcher, snap)
	}

	doc := apidoc.NodegroupDescription{
		Cluster:   snap.Cluster.Name,
		Region:    snap.Cluster.Region,
		Nodegroup: snap.NodegroupDoc(nodegroup),
		Failures:  snap.Failures,
	}
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

// nodegroupError returns the error for a nodegroup that could not be
// described. One that does not exist gets the names that are close to it,
// which costs one list call.
func (o *describeOptions) nodegroupError(ctx context.Context, fetcher *fleet.Fetcher, snap *fleet.Snapshot) error {
	if len(snap.Failures) == 0 {
		return fmt.Errorf("could not read nodegroup %s of cluster %s", o.nodegroup, snap.Cluster.Name)
	}
	failure := snap.Failures[0]
	switch failure.Reason {
	case apidoc.ReasonInterrupted:
		return fmt.Errorf("interrupted: %w", context.Canceled)
	case apidoc.ReasonNotFound:
	default:
		return errors.New("could not read " + view.FailureLine(failure))
	}
	clients, err := cmdutil.ClusterClients(ctx, o.f)
	if err != nil {
		return err
	}
	// Without the list the error still says what is wrong.
	names, _ := fetcher.NodegroupNames(ctx, clients, snap.Cluster.Name)
	return cmdutil.NotFoundError("nodegroup", o.nodegroup, "cluster "+snap.Cluster.Name, names,
		"refresh nodegroup list -c "+snap.Cluster.Name)
}

// describeView returns the table and plain form of doc.
func describeView(doc apidoc.NodegroupDescription) view.Description {
	n := doc.Nodegroup
	d := view.Description{
		Title:    n.Name,
		Status:   view.StatusOf(n.Status),
		Label:    n.Status,
		Subtitle: doc.Cluster + " in " + doc.Region,
		Failures: doc.Failures,
	}
	version := view.Field{Name: "version", Value: n.Version}
	if n.VersionBehind != nil && *n.VersionBehind {
		version = view.Field{Name: "version", Value: n.Version + " (behind the control plane)", Status: view.Warn}
	}
	d.Sections = append(d.Sections, view.Section{Title: "OVERVIEW", Fields: []view.Field{
		{Name: "status", Value: n.Status, Status: view.StatusOf(n.Status)},
		version,
		{Name: "instance types", Value: strings.Join(n.InstanceTypes, ", ")},
		{Name: "capacity", Value: n.CapacityType},
		{Name: "created", Value: formatTime(n.CreatedAt)},
		{Name: "modified", Value: formatTime(n.ModifiedAt)},
	}})

	ami := view.Section{Title: "AMI", Fields: []view.Field{
		{Name: "type", Value: n.AMIType},
		{Name: "release", Value: n.ReleaseVersion},
	}}
	if n.AMI != nil {
		ami.Fields = append(ami.Fields,
			view.Field{Name: "status", Value: string(n.AMI.Status), Status: amiStatus(n)},
			view.Field{Name: "current", Value: n.AMI.Current},
			view.Field{Name: "latest", Value: n.AMI.Latest},
		)
	} else {
		ami.Fields = append(ami.Fields, view.Field{Name: "status", Value: unknownCell, Status: view.Unknown})
	}
	d.Sections = append(d.Sections, ami)

	if s := n.Scaling; s != nil {
		d.Sections = append(d.Sections, view.Section{Title: "SCALING", Fields: []view.Field{
			{Name: "desired", Value: strconv.Itoa(int(s.Desired))},
			{Name: "min", Value: strconv.Itoa(int(s.Min))},
			{Name: "max", Value: strconv.Itoa(int(s.Max))},
		}})
	}

	issues := view.Section{Title: "ISSUES"}
	for _, issue := range n.HealthIssues {
		issues.Items = append(issues.Items, view.Item{Name: issue.Code, Fields: []view.Field{{Name: "message", Value: issue.Message}}})
	}
	instances := view.Section{Title: "INSTANCES"}
	for _, inst := range n.Instances {
		instances.Items = append(instances.Items, view.Item{Name: inst.ID, Fields: []view.Field{
			{Name: "state", Value: inst.State, Status: view.StatusOf(inst.State)},
			{Name: "type", Value: inst.Type},
			{Name: "lifecycle", Value: inst.Lifecycle},
			{Name: "zone", Value: inst.AvailabilityZone},
			{Name: "ami", Value: inst.ImageID},
			{Name: "launched", Value: formatTime(inst.LaunchTime)},
		}})
	}
	d.Sections = append(d.Sections, issues, instances)
	return d
}
