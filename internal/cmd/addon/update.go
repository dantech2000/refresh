package addon

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmd/changecmd"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
)

type updateOptions struct {
	f   *cmdutil.Factory
	ios *cmdutil.IOStreams

	cluster string
	version string
	all     bool
	skip    []string
	output  cmdutil.Format
	change  cmdutil.ChangeFlags

	// addons are the names from the arguments, and target the cluster,
	// both set by complete.
	addons []string
	target cmdutil.Target
}

func newCmdUpdate(f *cmdutil.Factory) *cobra.Command {
	o := &updateOptions{f: f, ios: f.IOStreams}
	cmd := &cobra.Command{
		Use:   "update [addon...]",
		Short: "Update add-ons of a cluster",
		Long: `Update EKS add-ons to the latest version that the cluster can run, or one
add-on to the version of --version. Names are exact.

With --all, every installed add-on is updated, one after another in
dependency order: vpc-cni before coredns and kube-proxy, and those before
the storage drivers. Named add-ons run in the order given. An add-on whose
dependency ran before it and did not end clean is not updated; the others
go on. Clean means skipped, or updated and verified healthy: a dependency
that failed, timed out, was already changing, is not healthy after its
update, or could not be read after it stops its dependents. --no-wait is
refused when an add-on to update depends on one that comes before it.

Before any change the command reads the whole cluster. It refuses while
the cluster or anything else in it is changing. An add-on that is DEGRADED,
or whose last change failed, is updated to repair it, after a question. An
add-on that is already at its target is left alone, and so is one that is
already changing, unless it is changing to the version this run wants: the
command then waits for that update and sends none.

The command asks once before it starts. --yes skips the question, and is
required with -o json|yaml and without a terminal. It waits for each
update, for --wait-timeout each; --no-wait returns once AWS accepted them.

The cluster comes from -c/--cluster, then REFRESH_CLUSTER, then the active
refresh context. It never comes from the kubeconfig.

` + changecmd.ExitCodeHelp(map[int]string{
			1: "Every update that was sent failed.",
			4: "An add-on failed and another was updated.",
			5: "An add-on was updated and is not healthy.",
		}),
		Example: `  # Update one add-on to the latest version
  refresh addon update vpc-cni -c prod

  # See what would change
  refresh addon update --all -c prod --dry-run

  # Update every add-on but one, without a prompt
  refresh addon update --all --skip-addon coredns -c prod --yes

  # Move one add-on to a version, and do not wait for it
  refresh addon update coredns --version v1.11.4-eksbuild.2 -c prod --yes --no-wait`,
		Args:              cobra.MatchAll(cobra.ArbitraryArgs, cmdutil.NoEmptyArgs),
		ValidArgsFunction: fleetcmd.CompleteAddons(f, &o.cluster, -1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.complete(args); err != nil {
				return err
			}
			if err := o.validate(); err != nil {
				return err
			}
			if err := cmdutil.RequireYes(o.ios, o.output, o.change.Yes, o.change.DryRun); err != nil {
				return err
			}
			return o.run(cmd.Context())
		},
	}
	cmdutil.AddChangeClusterFlag(cmd, &o.cluster)
	fleetcmd.RegisterClusterFlagCompletion(cmd, f)
	cmd.Flags().StringVar(&o.version, "version", "", "Add-on version to move to, with exactly one add-on (default: the latest that the cluster can run)")
	cmd.Flags().BoolVar(&o.all, "all", false, "Update every installed add-on, in dependency order")
	cmd.Flags().StringSliceVar(&o.skip, "skip-addon", nil, "Leave these add-ons out of --all, by exact name")
	cmdutil.AddChangeFlags(cmd, &o.change, cmdutil.ChangeFlagOptions{Wait: change.DefaultWait(change.AddonUpdate), WaitFor: "each add-on", NoWait: true})
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *updateOptions) complete(args []string) error {
	o.addons = args
	// A change never takes its cluster from the kubeconfig.
	target, err := cmdutil.ResolveCluster(o.f, o.cluster, false)
	o.target = target
	return err
}

func (o *updateOptions) validate() error {
	if err := cmdutil.CheckNames(awsx.CheckAddonName, slices.Concat(o.addons, o.skip)...); err != nil {
		return err
	}
	switch {
	case o.all && len(o.addons) > 0:
		return errors.New("name the add-ons or pass --all, not both")
	case !o.all && len(o.addons) == 0:
		return errors.New("name at least one add-on, or pass --all")
	case o.version != "" && (o.all || len(o.addons) != 1):
		return errors.New("--version needs exactly one add-on")
	case len(o.skip) > 0 && !o.all:
		return errors.New("--skip-addon needs --all")
	// A valid version that the add-on does not list is for the plan to
	// say; a string that is no version at all is a typing error.
	case o.version != "" && !fleet.ValidAddonVersion(o.version):
		return fmt.Errorf("invalid --version %q: not an add-on version, such as v1.11.4-eksbuild.2", o.version)
	}
	return o.change.Validate()
}

// request returns the change that the flags describe.
func (o *updateOptions) request() change.AddonUpdateRequest {
	return change.AddonUpdateRequest{
		// --wait-timeout bounds each add-on alone.
		Common:  changecmd.Common(&o.change, o.f.Global.Timeout),
		Target:  change.Target{Cluster: o.target.Cluster},
		Addons:  o.addons,
		All:     o.all,
		Version: o.version,
		Skip:    o.skip,
	}
}

func (o *updateOptions) run(ctx context.Context) error {
	d, err := changecmd.Deps(ctx, o.f)
	if err != nil {
		return err
	}
	req := o.request()
	if o.change.DryRun {
		return changecmd.PrintPlan(ctx, o.ios, o.output, d, req, o.f.Global.Timeout, nil)
	}
	return changecmd.RunChange(ctx, o.ios, o.output, d, req, changecmd.NewRenderer(o.ios, o.change.Quiet))
}
