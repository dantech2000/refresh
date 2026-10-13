package addon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
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

	cluster string
	addon   string
	output  cmdutil.Format

	// target is the cluster, resolved by complete.
	target cmdutil.Target
}

func newCmdDescribe(f *cmdutil.Factory) *cobra.Command {
	o := &describeOptions{
		ios: f.IOStreams, global: &f.Global, now: f.Now,
		resolve: func(cluster string) (cmdutil.Target, error) { return cmdutil.ResolveCluster(f, cluster, true) },
		clients: func(ctx context.Context) (*awsx.Clients, error) { return cmdutil.ClusterClients(ctx, f) },
	}
	cmd := &cobra.Command{
		Use:   "describe <addon>",
		Short: "Describe one add-on of a cluster",
		Long: `Describe an EKS add-on: its version, status, health issues, and
configuration values.

The name is exact. For a name that does not exist, the command lists the
add-ons whose names are close.`,
		Example: `  refresh addon describe vpc-cni -c prod

  # The configuration values alone
  refresh addon describe coredns -c prod -o json | jq -r .addon.configuration`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: fleetcmd.CompleteAddons(f, &o.cluster),
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
	cmdutil.AddClusterFlag(cmd, &o.cluster)
	fleetcmd.RegisterClusterFlagCompletion(cmd, f)
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *describeOptions) complete(args []string) error {
	o.addon = args[0]
	target, err := o.resolve(o.cluster)
	o.target = target
	return err
}

// validate has nothing to reject: the name is exact, and the output flag
// is checked as it is parsed.
func (o *describeOptions) validate() error { return nil }

func (o *describeOptions) run(ctx context.Context) error {
	ctx, cancel := cmdutil.WithTimeout(ctx, o.global.Timeout)
	defer cancel()
	clients, err := o.clients(ctx)
	if err != nil {
		return err
	}
	fetcher := fleet.Fetcher{Now: o.now}
	snap, err := fetcher.Fetch(ctx, clients, o.target.Cluster, fleet.Need{OnlyAddons: []string{o.addon}})
	if err != nil {
		return fleetcmd.ClusterError(ctx, &fetcher, clients, o.target.Cluster, err)
	}
	addon := snap.Addons[0]
	if addon.State != fleet.Collected {
		return o.addonError(ctx, &fetcher, snap)
	}

	doc := apidoc.AddonDescription{
		Cluster: snap.Cluster.Name,
		Region:  snap.Cluster.Region,
		Addon:   fleet.AddonDoc(addon),
	}
	return cmdutil.Print(o.ios, o.output, doc, describeView(doc))
}

// addonError returns the error for an add-on that could not be read. One
// that does not exist gets the names that are close to it, which costs one
// list call.
func (o *describeOptions) addonError(ctx context.Context, fetcher *fleet.Fetcher, snap *fleet.Snapshot) error {
	if len(snap.Failures) == 0 {
		return fmt.Errorf("could not read add-on %s of cluster %s", o.addon, snap.Cluster.Name)
	}
	failure := snap.Failures[0]
	if failure.Reason != apidoc.ReasonNotFound {
		if failure.Reason == apidoc.ReasonInterrupted {
			return fmt.Errorf("interrupted: %w", context.Canceled)
		}
		return errors.New("could not read " + view.FailureLine(failure))
	}
	clients, err := o.clients(ctx)
	if err != nil {
		return err
	}
	// Without the list the error still says what is wrong.
	names, _ := fetcher.AddonNames(ctx, clients, snap.Cluster.Name)
	return cmdutil.NotFoundError("add-on", o.addon, "cluster "+snap.Cluster.Name, names,
		"refresh addon list -c "+snap.Cluster.Name)
}

// dateLayout is how a describe view prints a time.
const dateLayout = "2006-01-02 15:04 UTC"

// describeView returns the table and plain form of doc.
func describeView(doc apidoc.AddonDescription) view.Description {
	a := doc.Addon
	issues := make([]view.Item, len(a.Issues))
	for i, issue := range a.Issues {
		issues[i] = view.Item{Name: issue.Code, Fields: []view.Field{{Name: "message", Value: issue.Message}}}
	}
	return view.Description{
		Title:    a.Name,
		Status:   view.StatusOf(a.Status),
		Label:    a.Status,
		Subtitle: doc.Cluster + " in " + doc.Region,
		Sections: []view.Section{
			{Title: "OVERVIEW", Fields: []view.Field{
				{Name: "version", Value: a.Version},
				{Name: "status", Value: a.Status, Status: view.StatusOf(a.Status)},
				{Name: "health", Value: string(a.Health), Status: healthStatus(a.Health)},
				{Name: "arn", Value: a.ARN},
				{Name: "service account role", Value: a.ServiceAccountRoleARN},
				{Name: "created", Value: formatTime(a.CreatedAt)},
				{Name: "modified", Value: formatTime(a.ModifiedAt)},
			}},
			{Title: "ISSUES", Items: issues},
			{Title: "CONFIGURATION", Fields: []view.Field{{Name: "values", Value: a.Configuration}}},
		},
	}
}

// formatTime returns t for a describe view, or "" for the zero time.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(dateLayout)
}
