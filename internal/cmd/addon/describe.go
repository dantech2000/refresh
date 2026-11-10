package addon

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/view"
)

type describeOptions struct {
	f   *cmdutil.Factory
	ios *cmdutil.IOStreams

	cluster string
	addon   string
	output  cmdutil.Format

	// target is the cluster, resolved by complete.
	target cmdutil.Target
}

func newCmdDescribe(f *cmdutil.Factory) *cobra.Command {
	o := &describeOptions{f: f, ios: f.IOStreams}
	cmd := &cobra.Command{
		Use:   "describe <addon>",
		Short: "Describe one add-on of a cluster",
		Long: `Describe an EKS add-on: its version, status, health issues, and
configuration values.

The name is exact. For a name that does not exist, the command lists the
add-ons whose names are close.

The command reads one add-on, so it has no partial result: an add-on that
cannot be read is an error.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context, then the current kubeconfig context.

Exit codes:
  0  Done.
  1  An error, or an interrupt.`,
		Example: `  refresh addon describe vpc-cni -c prod

  # The configuration values alone
  refresh addon describe coredns -c prod -o json | jq -r .addon.configuration`,
		Args:              cobra.MatchAll(cobra.ExactArgs(1), cmdutil.NoEmptyArgs),
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
	target, err := cmdutil.ResolveCluster(o.f, o.cluster, true)
	o.target = target
	return err
}

// validate rejects a name that EKS cannot have, before any call. The
// output flag is checked as it is parsed.
func (o *describeOptions) validate() error { return awsx.CheckAddonName(o.addon) }

func (o *describeOptions) run(ctx context.Context) error {
	ctx, cancel := cmdutil.WithTimeout(ctx, o.f.Global.Timeout)
	defer cancel()
	read, err := fleetcmd.FetchCluster(ctx, o.f, o.target, fleet.Need{OnlyAddons: []string{o.addon}})
	if err != nil {
		return err
	}
	snap := read.Snapshot
	addon := snap.Addons[0]
	if addon.State != fleet.Collected {
		return o.addonError(ctx, read)
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
func (o *describeOptions) addonError(ctx context.Context, read *fleetcmd.Read) error {
	snap := read.Snapshot
	if len(snap.Failures) == 0 {
		return fmt.Errorf("could not read add-on %s of cluster %s", o.addon, snap.Cluster.Name)
	}
	failure := snap.Failures[0]
	if failure.Reason != apidoc.ReasonNotFound {
		if failure.Reason == apidoc.ReasonInterrupted {
			return cmdutil.ErrInterrupted
		}
		return errors.New("could not read " + view.FailureLine(failure))
	}
	// Without the list the error still says what is wrong.
	names, _ := read.Fetcher.AddonNames(ctx, read.Clients, snap.Cluster.Name)
	return cmdutil.NotFoundError("add-on", o.addon, "cluster "+snap.Cluster.Name, names,
		"refresh addon list -c "+snap.Cluster.Name)
}

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
				{Name: "health", Value: string(a.Health), Status: view.AddonHealthStatus(a.Health)},
				{Name: "arn", Value: a.ARN},
				{Name: "service account role", Value: a.ServiceAccountRoleARN},
				{Name: "created", Value: view.Time(a.CreatedAt)},
				{Name: "modified", Value: view.Time(a.ModifiedAt)},
			}},
			{Title: "ISSUES", Items: issues},
			{Title: "CONFIGURATION", Fields: []view.Field{{Name: "values", Value: a.Configuration}}},
		},
	}
}
