package addon

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmd/changecmd"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
)

// addonWaitTimeout is the default of --wait-timeout: for each add-on.
const addonWaitTimeout = 5 * time.Minute

type updateOptions struct {
	ios *cmdutil.IOStreams
	// deps builds the dependencies of the change. Calling it is the first
	// use of the network.
	deps func(ctx context.Context) (change.Deps, error)
	// resolve returns the cluster of the run, the way a change does:
	// never from the kubeconfig.
	resolve func(explicit string) (cmdutil.Target, error)
	// timeout returns --timeout, which the root command sets before the
	// command runs.
	timeout func() time.Duration

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
	o := &updateOptions{
		ios:     f.IOStreams,
		deps:    changecmd.Deps(f),
		resolve: func(explicit string) (cmdutil.Target, error) { return cmdutil.ResolveCluster(f, explicit, false) },
		timeout: func() time.Duration { return f.Global.Timeout },
	}
	cmd := &cobra.Command{
		Use:   "update [addon...]",
		Short: "Update add-ons of a cluster",
		Long: `Update EKS add-ons to the latest version that the cluster can run, or one
add-on to the version of --version. Names are exact.

With --all, every installed add-on is updated, one after another in
dependency order: vpc-cni before coredns and kube-proxy, and those before
the storage drivers. A failed add-on does not stop the others.

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

Exit codes: 0 done or nothing to do. 1 an update failed, an error, or an
interrupt. 2 (--dry-run) a real run would ask about warnings. 3 blocked:
the cluster is busy, or --require-healthy and a warning. 4 incomplete: one
add-on failed and another was updated, a target was already changing, a
wait timed out, or something could not be read. 5 an add-on was updated
and is not healthy.`,
		Example: `  # Update one add-on to the latest version
  refresh addon update vpc-cni -c prod

  # See what would change
  refresh addon update --all -c prod --dry-run

  # Update every add-on but one, without a prompt
  refresh addon update --all --skip-addon coredns -c prod --yes

  # Move one add-on to a version, and do not wait for it
  refresh addon update coredns --version v1.11.4-eksbuild.2 -c prod --yes --no-wait`,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: fleetcmd.CompleteAddonNames(f, &o.cluster),
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
	cmdutil.AddClusterFlag(cmd, &o.cluster)
	fleetcmd.RegisterClusterFlagCompletion(cmd, f)
	cmd.Flags().StringVar(&o.version, "version", "", "Add-on version to move to, with exactly one add-on (default: the latest that the cluster can run)")
	cmd.Flags().BoolVar(&o.all, "all", false, "Update every installed add-on, in dependency order")
	cmd.Flags().StringSliceVar(&o.skip, "skip-addon", nil, "Add-on that --all leaves out; repeat it or separate the names with commas")
	cmdutil.AddChangeFlags(cmd, &o.change, addonWaitTimeout, "each add-on", true, false)
	cmdutil.AddOutputFlag(cmd, &o.output, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML, cmdutil.FormatPlain)
	return cmd
}

func (o *updateOptions) complete(args []string) error {
	o.addons = args
	target, err := o.resolve(o.cluster)
	o.target = target
	return err
}

func (o *updateOptions) validate() error {
	switch {
	case o.all && len(o.addons) > 0:
		return errors.New("name the add-ons or pass --all, not both")
	case !o.all && len(o.addons) == 0:
		return errors.New("name at least one add-on, or pass --all")
	case o.version != "" && (o.all || len(o.addons) != 1):
		return errors.New("--version needs exactly one add-on")
	case len(o.skip) > 0 && !o.all:
		return errors.New("--skip-addon needs --all")
	}
	return o.change.Validate()
}

// request returns the change that the flags describe.
func (o *updateOptions) request() change.AddonUpdateRequest {
	return change.AddonUpdateRequest{
		// --wait-timeout bounds each add-on alone.
		Common:  changecmd.Common(&o.change, o.timeout(), true),
		Target:  change.Target{Cluster: o.target.Cluster},
		Addons:  o.addons,
		All:     o.all,
		Version: o.version,
		Skip:    o.skip,
	}
}

func (o *updateOptions) run(ctx context.Context) error {
	d, err := o.deps(ctx)
	if err != nil {
		return err
	}
	req := o.request()
	if o.change.DryRun {
		return changecmd.PrintPlan(ctx, o.ios, o.output, d, req, o.timeout())
	}
	return changecmd.RunChange(ctx, o.ios, o.output, d, req, changecmd.NewRenderer(o.ios, o.change.Quiet))
}
